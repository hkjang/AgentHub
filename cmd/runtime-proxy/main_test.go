package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRequireToken(t *testing.T) {
	called := false
	handler := requireToken("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized || called {
		t.Fatalf("unauthorized request passed through: status=%d called=%v", unauthorized.Code, called)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetBasicAuth("agenthub", "secret")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusNoContent || !called {
		t.Fatalf("authorized request rejected: status=%d called=%v", authorized.Code, called)
	}
}

func TestUpstreamHealth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	response := httptest.NewRecorder()
	upstreamHealth(target).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("healthy upstream returned %d", response.Code)
	}
}
