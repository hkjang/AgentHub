package execution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

func TestACommentGoesWhereTheForgeKeepsThem(t *testing.T) {
	for name, want := range map[string]struct {
		connection store.SCMConnection
		source     string
		endpoint   string
	}{
		"GitHub": {store.SCMConnection{Host: "github.com", Kind: "github"},
			"https://github.com/acme/store/pull/42",
			"https://api.github.com/repos/acme/store/issues/42/comments"},
		"GitHub Enterprise": {store.SCMConnection{Host: "git.acme.io", Kind: "github"},
			"https://git.acme.io/acme/store/pull/42",
			"https://git.acme.io/api/v3/repos/acme/store/issues/42/comments"},
		"Gitea": {store.SCMConnection{Host: "gitea.acme.io", Kind: "gitea"},
			"https://gitea.acme.io/acme/store/pulls/9",
			"https://gitea.acme.io/api/v1/repos/acme/store/issues/9/comments"},
		"GitLab": {store.SCMConnection{Host: "gitlab.com", Kind: "gitlab"},
			"https://gitlab.com/acme/store/-/merge_requests/7",
			"https://gitlab.com/api/v4/projects/acme%2Fstore/merge_requests/7/notes"},
		"GitLab nested group": {store.SCMConnection{Host: "gitlab.com", Kind: "gitlab"},
			"https://gitlab.com/acme/platform/store/-/merge_requests/7",
			"https://gitlab.com/api/v4/projects/acme%2Fplatform%2Fstore/merge_requests/7/notes"},
		"Bitbucket": {store.SCMConnection{Host: "bitbucket.org", Kind: "bitbucket"},
			"https://bitbucket.org/acme/store/pull-requests/3",
			"https://api.bitbucket.org/2.0/repositories/acme/store/pullrequests/3/comments"},
	} {
		request, err := commentRequest(want.connection, want.source, "리뷰 결과")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if request.endpoint != want.endpoint {
			t.Errorf("%s: comment goes to %s, want %s", name, request.endpoint, want.endpoint)
		}
	}
}

// The source page comes out of a webhook body, so whoever sends the body chooses
// it. A credential must never leave for a host it does not belong to.
func TestTheCredentialNeverLeavesItsOwnHost(t *testing.T) {
	connection := store.SCMConnection{Host: "github.com", Kind: "github"}
	for _, elsewhere := range []string{
		"https://github.com.evil.example/acme/store/pull/1",
		"https://evil.example/acme/store/pull/1",
		"https://evil.example/?x=https://github.com/acme/store/pull/1",
	} {
		if _, err := commentRequest(connection, elsewhere, "리뷰 결과"); err == nil {
			t.Errorf("a comment was addressed to %s", elsewhere)
		}
	}
}

func TestEachForgeIsGivenTheCredentialItAsksFor(t *testing.T) {
	for kind, check := range map[string]func(http.Header) string{
		"github":    func(h http.Header) string { return h.Get("Authorization") },
		"bitbucket": func(h http.Header) string { return h.Get("Authorization") },
		"gitea":     func(h http.Header) string { return h.Get("Authorization") },
		"gitlab":    func(h http.Header) string { return h.Get("PRIVATE-TOKEN") },
	} {
		header := http.Header{}
		authorize(header, kind, "s3cret")
		got := check(header)
		if !strings.Contains(got, "s3cret") {
			t.Errorf("%s was not given the credential: %q", kind, got)
		}
		if kind == "gitea" && got != "token s3cret" {
			t.Errorf("gitea expects `token <t>`, got %q", got)
		}
		if kind == "github" && got != "Bearer s3cret" {
			t.Errorf("github expects a bearer token, got %q", got)
		}
		// A token in the wrong header is a token sent to a forge that will
		// ignore it — which reads as a revoked credential.
		if kind == "gitlab" && header.Get("Authorization") != "" {
			t.Error("gitlab was also sent an Authorization header")
		}
	}
}

func TestTheReviewSaysWhatItFoundOnThePage(t *testing.T) {
	var seen struct {
		path, auth, body string
	}
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		seen.path, seen.auth, seen.body = r.URL.Path, r.Header.Get("Authorization"), string(payload)
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	err := PostReviewComment(context.Background(), forge.Client(), connection,
		"s3cret", forge.URL+"/acme/store/pulls/9", "빠뜨린 오류 처리 2건")
	if err != nil {
		t.Fatal(err)
	}
	if seen.path != "/api/v1/repos/acme/store/issues/9/comments" {
		t.Errorf("the comment went to %s", seen.path)
	}
	if seen.auth != "token s3cret" {
		t.Errorf("the forge was sent %q", seen.auth)
	}
	var posted struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(seen.body), &posted); err != nil || posted.Body != "빠뜨린 오류 처리 2건" {
		t.Errorf("the forge received %q", seen.body)
	}
}

// A revoked token and a review with nothing to say both post nothing. The
// difference has to reach the person who can fix it.
func TestARefusalIsReportedInTheForgesOwnWords(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	err := PostReviewComment(context.Background(), forge.Client(), connection, "stale",
		forge.URL+"/acme/store/pulls/9", "리뷰 결과")
	if err == nil {
		t.Fatal("a refused comment was reported as posted")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("the refusal does not say what the forge said: %v", err)
	}
}
