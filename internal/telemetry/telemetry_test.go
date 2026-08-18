package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func TestTracingIsOffUntilAnEndpointIsConfigured(t *testing.T) {
	// The default has to be free: an offline site with no collector should not
	// buffer spans nobody will read.
	provider, err := Setup(context.Background(), Defaults(), "api", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Enabled() {
		t.Fatal("tracing started without an endpoint")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutting down a disabled provider failed: %v", err)
	}
	// A nil provider is the same no-op, so a caller that skipped Install entirely
	// does not have to guard every call.
	var missing *Provider
	if missing.Enabled() {
		t.Fatal("a nil provider claims to be enabled")
	}
	if err := missing.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsWhatTheExporterWouldOnlyFailOnLater(t *testing.T) {
	for name, settings := range map[string]Settings{
		"no endpoint":     {Enabled: true, SampleRatio: 1},
		"not a url":       {Enabled: true, Endpoint: "collector:4318", SampleRatio: 1},
		"wrong scheme":    {Enabled: true, Endpoint: "grpc://collector:4317", SampleRatio: 1},
		"ratio above one": {Enabled: true, Endpoint: "http://c:4318", SampleRatio: 1.5},
		"negative ratio":  {Enabled: true, Endpoint: "http://c:4318", SampleRatio: -0.1},
		"empty header":    {Enabled: true, Endpoint: "http://c:4318", SampleRatio: 1, Headers: map[string]string{" ": "x"}},
	} {
		if err := settings.Validate(); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	for name, settings := range map[string]Settings{
		"disabled with nothing set": {},
		"http collector":            {Enabled: true, Endpoint: "http://otel-collector.observability.svc:4318", SampleRatio: 1},
		"https with a path":         {Enabled: true, Endpoint: "https://collector.example/v1/traces", SampleRatio: 0.25},
	} {
		if err := settings.Validate(); err != nil {
			t.Fatalf("%s was rejected: %v", name, err)
		}
	}
}

// settingsStub stands in for the store.
type settingsStub struct {
	value string
	err   error
}

func (s settingsStub) Setting(_ context.Context, _ string, dst any) error {
	if s.err != nil {
		return s.err
	}
	target, ok := dst.(*Settings)
	if !ok {
		return errors.New("unexpected destination")
	}
	target.Enabled = true
	target.Endpoint = s.value
	target.SampleRatio = 1
	return nil
}

func TestStoredSettingWinsOverTheEnvironment(t *testing.T) {
	t.Setenv(EnvEndpoint, "http://from-env:4318")
	settings := Load(context.Background(), settingsStub{value: "http://from-db:4318"}, slog.Default())
	if settings.Endpoint != "http://from-db:4318" {
		t.Fatalf("the stored endpoint should win: %q", settings.Endpoint)
	}
}

func TestEnvironmentFillsInWhenNothingIsStored(t *testing.T) {
	t.Setenv(EnvEndpoint, "http://from-env:4318")
	settings := Load(context.Background(), settingsStub{err: errors.New("setting not found")}, slog.Default())
	if !settings.Enabled || settings.Endpoint != "http://from-env:4318" {
		t.Fatalf("the environment fallback did not apply: %#v", settings)
	}
	if settings.SampleRatio != 1 || settings.ServiceName == "" {
		t.Fatalf("the fallback left unusable defaults: %#v", settings)
	}
}

func TestAMisconfiguredCollectorDoesNotStopTheProcess(t *testing.T) {
	// A collector that is wrong is a reason to run without traces, not a reason to
	// refuse to serve.
	logger := slog.New(slog.DiscardHandler)
	provider := Install(context.Background(), settingsStub{value: "not a url at all"}, logger, "api", "0.0.0-test")
	if provider.Enabled() {
		t.Fatal("a bad endpoint should leave tracing off")
	}
}

func TestTraceIDIsEmptyOutsideASpan(t *testing.T) {
	if id := TraceID(context.Background()); id != "" {
		t.Fatalf("unexpected trace id: %q", id)
	}
	ctx, span := Start(context.Background(), "test")
	defer span.End()
	// With no exporter the tracer is a no-op and there is no id to adopt, which is
	// exactly why the caller keeps its own trace id as a fallback.
	if id := TraceID(ctx); id != "" && len(id) != 32 {
		t.Fatalf("a trace id should be 32 hex characters: %q", id)
	}
	if strings.Contains(TraceID(ctx), " ") {
		t.Fatal("a trace id must not contain spaces")
	}
}

func TestTheCollectorsBaseAddressGetsTheSignalPath(t *testing.T) {
	// An administrator types the collector's address, not the OTLP path. Sending
	// to that address unchanged means a 404 that only shows up in the exporter's
	// own log — which is exactly how this was found.
	for endpoint, want := range map[string]string{
		"http://collector:4318":            "http://collector:4318/v1/traces",
		"http://collector:4318/":           "http://collector:4318/v1/traces",
		"https://collector.example":        "https://collector.example/v1/traces",
		"https://collector.example/traces": "https://collector.example/traces",
		"http://collector:4318/v1/traces":  "http://collector:4318/v1/traces",
	} {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if got := tracesURL(parsed); got != want {
			t.Fatalf("%s became %s, want %s", endpoint, got, want)
		}
	}
}
