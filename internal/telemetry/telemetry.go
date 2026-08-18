// Package telemetry turns AgentHub's own trace ids into real OpenTelemetry
// traces.
//
// The platform already carried a trace id from the request that started a task
// through to every step's log line, which answers "what happened in this run" as
// long as somebody is reading logs. It does not answer the questions an operator
// actually has when a nightly agent is slow or expensive: where the time went,
// which model call cost what, whether the runtime acquisition or the model
// gateway is the bottleneck, and how one task relates to the workflow that
// spawned it.
//
// Tracing is off unless an endpoint is configured. An offline site with no
// collector must pay nothing for this: with no exporter installed the global
// tracer is the SDK's no-op, and every Start call in the codebase becomes an
// inexpensive nil-ish operation rather than a buffered span nobody will read.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// SettingKey is the system_settings row these settings live in.
const SettingKey = "observability"

// EnvEndpoint configures tracing for a process that starts before it can read
// the database, and for deployments that prefer to set it with the rest of their
// collector wiring. The stored setting wins when it is enabled.
const EnvEndpoint = "AGENTHUB_OTLP_ENDPOINT"

// Settings is what an administrator configures.
type Settings struct {
	Enabled bool `json:"enabled"`
	// Endpoint is an OTLP/HTTP collector, for example
	// http://otel-collector.observability.svc:4318. The path may be included; the
	// exporter appends /v1/traces when it is not.
	Endpoint string `json:"endpoint"`
	// SampleRatio is the fraction of traces to record, 0 to 1. A task that fails
	// is worth keeping whatever the ratio says, so sampling is parent-based and
	// the platform starts every task trace as a root — set this below 1 only when
	// the collector cannot keep up.
	SampleRatio float64 `json:"sampleRatio"`
	// ServiceName is what the collector groups spans under.
	ServiceName string `json:"serviceName"`
	// Headers are sent with every export, for a collector behind an
	// authenticating proxy.
	Headers map[string]string `json:"headers,omitempty"`
}

// Defaults are what an unconfigured deployment gets: nothing exported.
func Defaults() Settings {
	return Settings{Enabled: false, Endpoint: "", SampleRatio: 1, ServiceName: "agenthub"}
}

// Validate rejects what the exporter would only fail on later, at which point
// the operator would be reading a log line instead of a form error.
func (s Settings) Validate() error {
	if s.SampleRatio < 0 || s.SampleRatio > 1 {
		return errors.New("샘플링 비율은 0과 1 사이여야 합니다")
	}
	if len(s.ServiceName) > 120 {
		return errors.New("서비스 이름은 120자 이하여야 합니다")
	}
	if len(s.Headers) > 20 {
		return errors.New("헤더는 최대 20개까지 설정할 수 있습니다")
	}
	for name := range s.Headers {
		if strings.TrimSpace(name) == "" {
			return errors.New("헤더 이름을 입력해 주세요")
		}
	}
	if !s.Enabled {
		return nil
	}
	endpoint := strings.TrimSpace(s.Endpoint)
	if endpoint == "" {
		return errors.New("추적을 사용하려면 OTLP Collector 주소가 필요합니다")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("OTLP Collector 주소는 http 또는 https URL이어야 합니다")
	}
	return nil
}

// Provider is a running exporter. The zero value is a working no-op, so callers
// never have to check whether tracing is on.
type Provider struct {
	provider *sdktrace.TracerProvider
	// Endpoint is where spans are being sent, for the startup log line.
	Endpoint string
}

// Enabled reports whether spans are actually leaving the process.
func (p *Provider) Enabled() bool { return p != nil && p.provider != nil }

// Shutdown flushes what is buffered. It is given its own deadline: a collector
// that has gone away must not hold up a shutdown.
func (p *Provider) Shutdown(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return p.provider.Shutdown(ctx)
}

// Setup installs the global tracer for one process.
//
// component distinguishes the API from the worker in the collector, since both
// export under the same service name and a span from one is meaningless without
// knowing which produced it.
func Setup(ctx context.Context, settings Settings, component, version string) (*Provider, error) {
	// The propagator is installed either way: a process that does not export spans
	// still has to pass a trace id on to one that does.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	endpoint := strings.TrimSpace(settings.Endpoint)
	if !settings.Enabled || endpoint == "" {
		return &Provider{}, nil
	}
	if err := settings.Validate(); err != nil {
		return &Provider{}, err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return &Provider{}, err
	}
	options := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(tracesURL(parsed))}
	if parsed.Scheme == "http" {
		options = append(options, otlptracehttp.WithInsecure())
	}
	if len(settings.Headers) > 0 {
		options = append(options, otlptracehttp.WithHeaders(settings.Headers))
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return &Provider{}, fmt.Errorf("configure the OTLP exporter: %w", err)
	}

	service := strings.TrimSpace(settings.ServiceName)
	if service == "" {
		service = "agenthub"
	}
	ratio := settings.SampleRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 1
	}
	attributes := resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(service),
		semconv.ServiceVersion(version),
		attribute.String("agenthub.component", component),
	)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(attributes),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(provider)
	return &Provider{provider: provider, Endpoint: endpoint}, nil
}

// tracesURL fills in the signal path an operator does not type.
//
// WithEndpointURL sends to exactly the URL it is given, so a collector's base
// address — which is what an administrator has in front of them — would POST to
// "/" and be answered with a 404 that only appears in the exporter's own log.
func tracesURL(endpoint *url.URL) string {
	if endpoint.Path == "" || endpoint.Path == "/" {
		copied := *endpoint
		copied.Path = "/v1/traces"
		return copied.String()
	}
	return endpoint.String()
}

// Tracer is the one tracer the platform records under.
func Tracer() trace.Tracer { return otel.Tracer("github.com/hkjang/AgentHub") }

// Start opens a span. It is a thin wrapper so callers do not each reach for the
// global tracer, and so an un-instrumented process pays one function call.
func Start(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, trace.WithAttributes(attributes...))
}

// TraceID is the current trace's id, or "" outside a recording span.
//
// The platform's own trace id becomes this when tracing is on, so the id printed
// in a log line, stored on a run and shown in the console is the same string that
// finds the trace in the collector.
func TraceID(ctx context.Context) string {
	span := trace.SpanContextFromContext(ctx)
	if !span.IsValid() {
		return ""
	}
	return span.TraceID().String()
}

// Fail marks a span as failed with the error's message, which is what makes a
// failed task findable in a collector without reading every span.
func Fail(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
