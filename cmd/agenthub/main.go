package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hkjang/AgentHub/internal/api"
	"github.com/hkjang/AgentHub/internal/buildinfo"
	"github.com/hkjang/AgentHub/internal/config"
	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
)

func main() {
	handled, err := runInfoCommand(os.Args[1:], os.Stdout)
	if handled {
		if err != nil {
			slog.Error("AgentHub command failed", "error", err)
			os.Exit(2)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("AgentHub stopped", "error", err)
		os.Exit(1)
	}
}

// runInfoCommand handles commands that must work without database credentials.
// In particular, a release can inspect the binary inside its freshly built
// container before any deployment configuration exists.
func runInfoCommand(args []string, output io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "version" {
		return false, nil
	}
	switch {
	case len(args) == 1:
		_, err := fmt.Fprintln(output, buildinfo.Version)
		return true, err
	case len(args) == 2 && args[1] == "--json":
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return true, encoder.Encode(buildinfo.Current())
	default:
		return true, fmt.Errorf("usage: agenthub version [--json]")
	}
}

type starterTemplateStore interface {
	StarterTemplateOwnerID(context.Context) (string, error)
	SeedTemplates(context.Context, string) error
}

func seedStarterTemplates(ctx context.Context, db starterTemplateStore) error {
	ownerID, err := db.StarterTemplateOwnerID(ctx)
	if err != nil {
		return err
	}
	if ownerID == "" {
		return errors.New("no administrator available for starter template attribution")
	}
	return db.SeedTemplates(ctx, ownerID)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ring := appLog.NewRing(10000)
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(appLog.Capture(baseHandler, ring))
	slog.SetDefault(logger)
	cipher, err := cryptox.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	db, err := store.Open(ctx, cfg.PostgresDSN, cipher)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	if err := db.BootstrapAdmin(ctx, cfg.BootstrapAdmin, cfg.BootstrapPassword); err != nil {
		return err
	}
	// Starter templates are system data, not a login. Authenticating with the
	// bootstrap password here meant an administrator who followed the guide and
	// rotated that password never received templates added by later releases.
	// BootstrapAdmin guarantees an administrator exists; use one only as the
	// created_by attribution and keep seeding idempotently by slug.
	if seedErr := seedStarterTemplates(ctx, db); seedErr != nil {
		// Not fatal — the platform runs without a catalog — but not silent either:
		// a refused template is a runtime nobody can find, and this failure went
		// unnoticed once because the error was discarded here.
		logger.Error("starter templates could not be published", "error", seedErr)
	}
	// Tracing, when an administrator configured a collector. With none configured
	// this installs the no-op tracer and costs nothing.
	tracing := telemetry.Install(ctx, db, logger, "api", buildinfo.Version)
	defer func() { _ = tracing.Shutdown(context.WithoutCancel(ctx)) }()

	spawner := appRuntime.NewKubernetesSpawner(db).WithLogger(logger)
	apiServer := api.New(db, cipher, logger, ring, spawner, os.DirFS("web/dist"))
	apiServer.RunBackground(ctx)
	handler := apiServer.Handler()
	server := &http.Server{Addr: cfg.ListenAddress, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("AgentHub started", "address", cfg.ListenAddress)
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
