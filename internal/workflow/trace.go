package workflow

import "context"

// traceKey carries the run's correlation id. A workflow fans out across
// goroutines and outbound model calls, so the id has to travel on the context
// rather than being passed to every helper.
type traceKey struct{}

// WithTraceID attaches the correlation id used by the control-plane request that
// started the run, so a step's log line can be matched to the HTTP access log.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if traceID == "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, traceID)
}

func TraceIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(traceKey{}).(string)
	return value
}
