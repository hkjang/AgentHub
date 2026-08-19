package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/hkjang/AgentHub/internal/buildinfo"
	"github.com/hkjang/AgentHub/internal/cryptox"
	appLog "github.com/hkjang/AgentHub/internal/logging"
	"github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
)

const (
	sessionCookie = "agenthub_session"
	csrfCookie    = "agenthub_csrf"
	oidcCookie    = "agenthub_oidc"
)

type Server struct {
	store   *store.Store
	cipher  *cryptox.Cipher
	logger  *slog.Logger
	logs    *appLog.Ring
	spawner runtime.Spawner
	version buildinfo.Info
	static  fs.FS

	sessionSettingsMu    sync.RWMutex
	sessionSettings      sessionGatewaySettings
	sessionSettingsUntil time.Time
}

func New(db *store.Store, cipher *cryptox.Cipher, logger *slog.Logger, logs *appLog.Ring, spawner runtime.Spawner, static fs.FS) *Server {
	return &Server{store: db, cipher: cipher, logger: logger, logs: logs, spawner: spawner, version: buildinfo.Current(), static: static}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, s.tracing, s.accessLog, s.runtimeHostGateway, s.runtimePathGateway, s.securityHeaders)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			writeError(w, 503, "database_unavailable", "PostgreSQL 연결을 확인해 주세요.")
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	r.Get("/api/openapi.json", s.openAPI)
	r.Route("/api/v1", func(r chi.Router) {
		// Under /api/v1 an unknown path is a client's mistake, not a page. Falling
		// through to the single-page app answered it with 200 and a document,
		// which a client can only report as "the response was not JSON".
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "요청한 API 경로를 찾을 수 없습니다.")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "이 경로에서 지원하지 않는 메서드입니다.")
		})
		r.Get("/version", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.version) })
		r.Get("/auth/methods", s.authMethods)
		r.Post("/auth/login", s.login)
		r.Get("/auth/oidc/start", s.oidcStart)
		r.Get("/auth/oidc/callback", s.oidcCallback)
		// External systems fire triggers without a portal session; the handler
		// verifies an HMAC over the raw body instead, so it sits outside the
		// authentication group but is not otherwise open.
		r.Post("/triggers/{id}/webhook", s.triggerWebhook)
		// The MCP gateway inside a runtime Pod asks for a tool approval here and
		// polls for the decision, authenticating with the runtime's own token —
		// whose hash the control plane holds. Outside the browser session group for
		// the same reason the webhook route is, and no more open than it: without a
		// valid runtime token these answer 401.
		r.Post("/runtime-gateway/tool-approvals", s.requestToolApproval)
		r.Get("/runtime-gateway/tool-approvals/{id}", s.toolApprovalStatus)
		// The same gateway reports what its content scanner found. Tool calls never
		// pass through the control plane, so without this the scanning that happens
		// in the Pod would only ever appear in that Pod's log.
		r.Post("/runtime-gateway/dlp-events", s.reportDLPEvent)
		// And the initialisers report what configuration they actually wrote. The Pod
		// is the only thing that can answer that honestly, and the runtime token is
		// the only credential it has.
		r.Post("/runtime-gateway/config-report", s.reportRuntimeConfig)
		r.Group(func(r chi.Router) {
			r.Use(s.authentication)
			r.Get("/me", s.me)
			r.Group(func(r chi.Router) {
				r.Use(s.csrfProtection)
				// Every route below comes from the catalog, which is also what the
				// published OpenAPI description and the API-key scope check are built
				// from. Nothing is served that is not declared there.
				s.register(r, s.apiRoutes())
			})
		})
	})
	r.Post("/mcp", s.mcp)
	r.NotFound(s.spa)
	return r
}

func (s *Server) cachedSessionGatewaySettings(ctx context.Context) sessionGatewaySettings {
	s.sessionSettingsMu.RLock()
	if time.Now().Before(s.sessionSettingsUntil) {
		value := s.sessionSettings
		s.sessionSettingsMu.RUnlock()
		return value
	}
	s.sessionSettingsMu.RUnlock()

	value := sessionGatewaySettings{Scheme: "https", SessionHours: 8}
	_ = s.store.Setting(ctx, "sessionGateway", &value)
	s.sessionSettingsMu.Lock()
	s.sessionSettings, s.sessionSettingsUntil = value, time.Now().Add(5*time.Second)
	s.sessionSettingsMu.Unlock()
	return value
}

func (s *Server) invalidateSessionGatewaySettings() {
	s.sessionSettingsMu.Lock()
	s.sessionSettingsUntil = time.Time{}
	s.sessionSettingsMu.Unlock()
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// tracing records one span per request and, more importantly, adopts the trace a
// caller already started. A task that a workflow queued and a worker ran should
// be one trace end to end, not three unrelated ones.
//
// With no exporter installed this is the SDK's no-op tracer: a couple of function
// calls per request and nothing recorded.
func (s *Server) tracing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := telemetry.Start(ctx, r.Method+" "+r.URL.Path,
			semconv.HTTPRequestMethodKey.String(r.Method),
			semconv.URLPath(r.URL.Path),
			semconv.ServerAddress(r.Host),
		)
		defer span.End()
		wrapper := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapper, r.WithContext(ctx))
		// The route pattern is only known once chi has matched it, and it is what
		// makes spans groupable: /api/v1/agents/{id} rather than one name per id.
		if route := chi.RouteContext(ctx); route != nil && route.RoutePattern() != "" {
			span.SetName(r.Method + " " + route.RoutePattern())
			span.SetAttributes(semconv.HTTPRoute(route.RoutePattern()))
		}
		span.SetAttributes(semconv.HTTPResponseStatusCode(wrapper.Status()))
		if wrapper.Status() >= 500 {
			span.SetStatus(codes.Error, http.StatusText(wrapper.Status()))
		}
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapper := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapper, r)
		fields := []any{"method", r.Method, "path", r.URL.Path, "status", wrapper.Status(), "bytes", wrapper.BytesWritten(), "duration_ms", time.Since(start).Milliseconds(), "request_id", middleware.GetReqID(r.Context())}
		// The trace id goes in the log line so a log and a trace can be put side by
		// side without correlating by timestamp.
		if traceID := telemetry.TraceID(r.Context()); traceID != "" {
			fields = append(fields, "trace_id", traceID)
		}
		s.logger.Info("http request", fields...)
	})
}

func (s *Server) spa(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if requested == "." || requested == "" {
		requested = "index.html"
	}
	if info, err := fs.Stat(s.static, requested); err == nil && !info.IsDir() {
		http.ServeFileFS(w, r, s.static, requested)
		return
	}
	r.URL.Path = "/"
	http.ServeFileFS(w, r, s.static, "index.html")
}
