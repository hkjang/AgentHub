package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/rest"

	"github.com/hkjang/AgentHub/internal/operator"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := rest.InClusterConfig()
	if err != nil {
		logger.Error("configure Kubernetes client", "error", err)
		os.Exit(1)
	}
	controller, err := operator.New(config, logger)
	if err != nil {
		logger.Error("create Agent Operator", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := controller.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("operator stopped", "error", err)
		os.Exit(1)
	}
}
