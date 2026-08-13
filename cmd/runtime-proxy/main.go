package main

import (
	"crypto/subtle"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

func main() {
	token := os.Getenv("AGENTHUB_RUNTIME_PROXY_TOKEN")
	if token == "" {
		log.Fatal("AGENTHUB_RUNTIME_PROXY_TOKEN is required")
	}
	listen := envOr("AGENTHUB_RUNTIME_PROXY_LISTEN", ":9119")
	target, err := url.Parse(envOr("AGENTHUB_RUNTIME_PROXY_TARGET", "http://127.0.0.1:9120"))
	if err != nil || target.Scheme != "http" || target.Host == "" {
		log.Fatal("AGENTHUB_RUNTIME_PROXY_TARGET must be a valid http URL")
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalDirector(request)
		request.Host = target.Host
		request.Header.Del("Authorization")
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		response.Header.Set("Cache-Control", "no-store")
		response.Header.Set("X-Content-Type-Options", "nosniff")
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /healthz", upstreamHealth(target))
	mux.Handle("/", requireToken(token, proxy))
	handler := mux
	server := &http.Server{Addr: listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	log.Printf("runtime proxy listening on %s", listen)
	log.Fatal(server.ListenAndServe())
}

func upstreamHealth(target *url.URL) http.HandlerFunc {
	client := &http.Client{Timeout: 2 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		response, err := client.Do(request)
		if err != nil {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		_ = response.Body.Close()
		if response.StatusCode >= http.StatusInternalServerError {
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func requireToken(expected string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(username), []byte("agenthub")) == 1
		passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(expected)) == 1
		if !ok || !userOK || !passwordOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="AgentHub Runtime"`)
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
