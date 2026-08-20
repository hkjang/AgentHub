package api

import (
	"net/http"
	"strings"
	"testing"
)

// The runtime proxy forwards the runtime's own cookies — Langflow authenticates
// its editor with one — and drops this platform's by prefix. If a cookie the
// platform issues ever stopped carrying that prefix it would be handed to the
// application behind the proxy, which is a credential leak rather than a bug in
// rendering.
func TestPlatformCookiesAreRecognisableByPrefix(t *testing.T) {
	for _, name := range []string{sessionCookie, csrfCookie, runtimeAccessCookie, runtimePathCookieName("11111111-2222-3333-4444-555555555555")} {
		if !strings.HasPrefix(name, agentHubCookiePrefix) {
			t.Errorf("cookie %q does not carry the platform prefix, so the runtime proxy would forward it", name)
		}
	}
}

func TestForwardCookiesKeepsTheRuntimesOwn(t *testing.T) {
	request, _ := http.NewRequest(http.MethodGet, "http://runtime.example/api/v1/flows/", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "portal"})
	request.AddCookie(&http.Cookie{Name: runtimePathCookieName("abc"), Value: "ticket"})
	request.AddCookie(&http.Cookie{Name: "access_token_lf", Value: "langflow"})
	request.AddCookie(&http.Cookie{Name: "refresh_token_lf", Value: "langflow"})
	forwardCookies(request)

	got := request.Header.Get("Cookie")
	if strings.Contains(got, "portal") || strings.Contains(got, "ticket") {
		t.Fatalf("a platform cookie was forwarded: %q", got)
	}
	if !strings.Contains(got, "access_token_lf=langflow") || !strings.Contains(got, "refresh_token_lf=langflow") {
		t.Fatalf("the runtime's own session cookies were dropped: %q", got)
	}
}
