package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each answer is named for what an administrator would do about it. "실패했습니다"
// would be true of all of them and would send somebody to read logs instead of
// to the one field they got wrong.
func TestAModelEndpointCheckNamesTheMistake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		model   string
		verdict string
		says    string
	}{
		{"serving the model", listing(`{"data":[{"id":"qwen3"},{"id":"llama"}]}`), "qwen3", "ok", "정상"},
		{"model not there", listing(`{"data":[{"id":"llama"}]}`), "qwen3", "model_missing", "qwen3"},
		{"nothing served", listing(`{"data":[]}`), "", "reachable", "비어"},
		{"key refused", status(http.StatusUnauthorized), "", "unauthorised", "API 키"},
		{"wrong suffix", status(http.StatusNotFound), "", "wrong_path", "/v1"},
		{"provider error", status(http.StatusBadGateway), "", "error", "502"},
	} {
		server := httptest.NewServer(tc.handler)
		verdict, detail, _ := (&Server{}).askModelEndpoint(httptest.NewRequest(http.MethodPost, "/", nil), server.URL, "", tc.model)
		server.Close()
		if verdict != tc.verdict {
			t.Errorf("%s → %q, want %q (%s)", tc.name, verdict, tc.verdict, detail)
		}
		if !strings.Contains(detail, tc.says) {
			t.Errorf("%s does not mention %q: %q", tc.name, tc.says, detail)
		}
	}

	// An address nobody can reach, and an empty one, are different mistakes.
	if verdict, detail, _ := (&Server{}).askModelEndpoint(httptest.NewRequest(http.MethodPost, "/", nil), "http://127.0.0.1:9/v1", "", ""); verdict != "unreachable" {
		t.Errorf("a closed port → %q (%s)", verdict, detail)
	}
	if verdict, _, _ := (&Server{}).askModelEndpoint(httptest.NewRequest(http.MethodPost, "/", nil), "   ", "", ""); verdict != "unconfigured" {
		t.Errorf("an empty address → %q", verdict)
	}
}

// The trailing slash somebody pasted must not become a double slash in the URL
// the platform then reports as not found.
func TestATrailingSlashIsNotAMistake(t *testing.T) {
	var asked string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
	}))
	defer server.Close()
	if verdict, detail, _ := (&Server{}).askModelEndpoint(httptest.NewRequest(http.MethodPost, "/", nil), server.URL+"/v1/", "", ""); verdict != "ok" {
		t.Errorf("verdict = %q (%s)", verdict, detail)
	}
	if asked != "/v1/models" {
		t.Errorf("asked %q, want /v1/models", asked)
	}
}

func listing(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}
