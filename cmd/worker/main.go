// Command agenthub-worker drains the Agent Task queue.
//
// It is deliberately a separate process from the API: an autonomous run can take
// minutes, and nothing that slow belongs in a request handler. Several workers
// can run against one database — tasks are claimed with FOR UPDATE SKIP LOCKED
// and held on a lease — so capacity scales by adding replicas.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/hkjang/AgentHub/internal/buildinfo"
	"github.com/hkjang/AgentHub/internal/config"
	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/execution"
	"github.com/hkjang/AgentHub/internal/guard"
	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimespec"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// envConcurrency bounds how many tasks one worker runs at once.
const envConcurrency = "AGENTHUB_WORKER_CONCURRENCY"

// envDisableScheduler lets an operator run several workers while keeping the
// cron scheduler on only some of them. Leaving it on everywhere is also safe:
// the trigger update decides who fires.
const envDisableScheduler = "AGENTHUB_WORKER_DISABLE_SCHEDULER"

// envMaxConcurrency is the ceiling the worker may scale its own concurrency up
// to when the queue backs up. Left unset it equals the floor, which keeps the
// fixed behaviour a deployment may already be tuned for.
const envMaxConcurrency = "AGENTHUB_WORKER_MAX_CONCURRENCY"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

// waitForSchema blocks until the execution tables exist.
func waitForSchema(ctx context.Context, db *store.Store, logger *slog.Logger) error {
	announced := false
	for {
		ready, err := db.ExecutionSchemaReady(ctx)
		if err == nil && ready {
			return nil
		}
		if !announced {
			logger.Info("waiting for the execution schema to be migrated")
			announced = true
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
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
	// Migrations are owned by the API process. On a fresh install the worker can
	// win the race, so wait for the schema rather than failing every poll until
	// the API catches up.
	if err := waitForSchema(ctx, db, logger); err != nil {
		return err
	}

	// The worker is where the interesting spans are: a task's steps, the model
	// calls and the runtime it had to wait for.
	tracing := telemetry.Install(ctx, db, logger, "worker", buildinfo.Version)
	defer func() { _ = tracing.Shutdown(context.WithoutCancel(ctx)) }()

	hostname, _ := os.Hostname()
	workerID := execution.NewWorkerID(hostname)
	spawner := appRuntime.NewKubernetesSpawner(db).WithLogger(logger)
	// Every model call the execution plane makes goes through this client, which
	// is why the data-loss check is attached here rather than in each caller.
	completion := workflow.NewModelCompletion().WithInspector(guard.NewModel(db, logger))
	// A flow runs inside the runtime, so its input and its answer are the only
	// two places the platform can inspect. Same detectors, same policy.
	orchestrator := execution.New(db, spawner, completion, logger, workerID).WithFlowInspector(guard.NewFlow(db, logger))

	worker := execution.NewWorker(db, orchestrator, logger, workerID)
	worker.Hostname, worker.Version = hostname, buildinfo.Version
	if value := os.Getenv(envConcurrency); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 && parsed <= 32 {
			worker.Concurrency = parsed
		} else {
			logger.Warn("ignoring invalid worker concurrency", envConcurrency, value)
		}
	}
	worker.MaxConcurrency = worker.Concurrency
	if value := os.Getenv(envMaxConcurrency); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= worker.Concurrency && parsed <= 32 {
			worker.MaxConcurrency = parsed
		} else {
			logger.Warn("ignoring invalid worker max concurrency", envMaxConcurrency, value)
		}
	}

	errs := make(chan error, 5)
	go func() { errs <- worker.Run(ctx) }()

	if os.Getenv(envDisableScheduler) != "true" {
		scheduler := execution.NewScheduler(db, logger)
		go func() { errs <- scheduler.Run(ctx) }()
	} else {
		logger.Info("cron scheduler disabled on this worker")
	}

	// The event dispatcher claims events on a lease and records each delivery, so
	// unlike the scheduler it is safe — and useful — to run on every worker.
	dispatcher := execution.NewDispatcher(db, logger).WithWorkerID(workerID)
	go func() { errs <- dispatcher.Run(ctx) }()

	// The caretaker reclaims tasks stranded by a worker that died and trims
	// history past its retention. Every sweep is idempotent, so running it on
	// each worker needs no coordination.
	caretaker := execution.NewCaretaker(db, logger)
	go func() { errs <- caretaker.Run(ctx) }()

	// The runtime warm pool claims each runtime before starting it, so several
	// workers running it cannot start the same Pod twice.
	pool := execution.NewPool(db, spawner, runtimespec.New(db, logger), logger)
	go func() { errs <- pool.Run(ctx) }()

	select {
	case <-ctx.Done():
		// Give in-flight tasks a moment to record their outcome.
		time.Sleep(2 * time.Second)
		return ctx.Err()
	case err := <-errs:
		return err
	}
}
