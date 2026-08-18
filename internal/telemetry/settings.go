package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
)

// Reader is the part of the store this package needs, so a process can wire
// tracing without the package depending on the whole store.
type Reader interface {
	Setting(ctx context.Context, key string, dst any) error
}

// Load reads the stored settings, falling back to the environment.
//
// The environment exists for two cases: a process that starts before anyone has
// opened the admin screen, and a deployment that would rather configure its
// collector next to the collector. A stored setting that is switched on wins over
// it, because that is the one an operator can see.
func Load(ctx context.Context, reader Reader, logger *slog.Logger) Settings {
	settings := Defaults()
	if reader != nil {
		if err := reader.Setting(ctx, SettingKey, &settings); err != nil && logger != nil {
			// A missing row is the normal state of a deployment that never
			// configured tracing.
			if !isNotFound(err) {
				logger.Warn("observability setting is unreadable; tracing stays off", "error", err)
			}
			settings = Defaults()
		}
	}
	if !settings.Enabled || strings.TrimSpace(settings.Endpoint) == "" {
		if endpoint := strings.TrimSpace(os.Getenv(EnvEndpoint)); endpoint != "" {
			settings.Enabled, settings.Endpoint = true, endpoint
			if settings.ServiceName == "" {
				settings.ServiceName = Defaults().ServiceName
			}
			if settings.SampleRatio <= 0 {
				settings.SampleRatio = 1
			}
		}
	}
	return settings
}

// isNotFound keeps this package from importing the store for one sentinel.
func isNotFound(err error) bool {
	var notFound interface{ Error() string }
	return errors.As(err, &notFound) && strings.Contains(err.Error(), "not found")
}

// Install loads the settings and starts the exporter, logging what it decided.
// Tracing must never stop a process from starting: a collector that is wrong or
// unreachable is a reason to run without traces, not to refuse to serve.
func Install(ctx context.Context, reader Reader, logger *slog.Logger, component, version string) *Provider {
	settings := Load(ctx, reader, logger)
	provider, err := Setup(ctx, settings, component, version)
	switch {
	case err != nil:
		if logger != nil {
			logger.Error("tracing could not be started; continuing without it", "component", component, "error", err)
		}
	case provider.Enabled():
		if logger != nil {
			logger.Info("tracing enabled", "component", component, "endpoint", provider.Endpoint, "sampleRatio", settings.SampleRatio)
		}
	}
	return provider
}
