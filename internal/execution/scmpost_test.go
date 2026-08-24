package execution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// A review runs again on every push. A comment per run buries the newest verdict
// under the older ones and teaches people to ignore the page.
func TestASecondReviewReplacesTheFirstComment(t *testing.T) {
	var method, path, query string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			query = r.URL.RawQuery
			_, _ = w.Write([]byte(`[{"id":11,"body":"사람이 쓴 댓글"},{"id":12,"body":"` + reviewMarker + `\n지난 리뷰"}]`))
			return
		}
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/pulls/9", ReviewComment("지적 없음", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch {
		t.Errorf("the second review was posted with %s, not an edit", method)
	}
	if path != "/api/v1/repos/acme/store/issues/comments/12" {
		t.Errorf("the edit went to %s", path)
	}
	// Asking for a default page means a comment past the thirtieth is invisible
	// and the platform adds another one beside it, for ever.
	if !strings.Contains(query, "per_page=100") {
		t.Errorf("the listing asked for %q", query)
	}
}

// The marker is the whole basis for touching a comment on a page the platform
// does not own. Without one, a new comment — never an edit to somebody else's.
func TestSomebodyElsesCommentIsNeverRewritten(t *testing.T) {
	var method string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":11,"body":"AgentHub 코드 리뷰가 이렇게 말했었죠"},{"id":12,"body":"LGTM"}]`))
			return
		}
		method = r.Method
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/pulls/9", ReviewComment("지적 1건", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Fatalf("a comment nobody marked was rewritten with %s", method)
	}
}

// A page that cannot be read is not a page whose comments are known. Guessing
// there is nothing there and posting is right; guessing an id is not.
func TestAnUnreadablePageStillGetsTheReview(t *testing.T) {
	var method string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Refused, and still carrying a body — a proxy's cached page, an
			// error envelope. What decides is the status, not what parses.
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`[{"id":99,"body":"` + reviewMarker + `\n오래된 캐시"}]`))
			return
		}
		method = r.Method
		w.WriteHeader(http.StatusCreated)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/pulls/9", ReviewComment("지적 1건", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Fatalf("a listing that failed produced %s", method)
	}
}

// Bitbucket answers with a page object and keeps the text one level down, and
// edits with PUT rather than PATCH — a wrong verb is a 405 that reads like a
// broken token.
func TestBitbucketsOwnShapeIsUnderstood(t *testing.T) {
	var method, path string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Bitbucket spells the page size its own way.
			if !strings.Contains(r.URL.RawQuery, "pagelen=100") {
				t.Errorf("Bitbucket was asked with %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"values":[{"id":31,"content":{"raw":"` + reviewMarker + `\n지난 리뷰"}}]}`))
			return
		}
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "bitbucket", APIBase: forge.URL + "/2.0"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/pull-requests/3", ReviewComment("지적 없음", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut {
		t.Errorf("Bitbucket was sent %s", method)
	}
	if path != "/2.0/repositories/acme/store/pullrequests/3/comments/31" {
		t.Errorf("the edit went to %s", path)
	}
}

// GitLab edits a note with PUT. PATCH is a 405 that reads like a broken token.
func TestGitLabEditsItsNoteTheWayGitLabDoes(t *testing.T) {
	var method, path string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":21,"body":"` + reviewMarker + `\n지난 리뷰"}]`))
			return
		}
		// EscapedPath, because a GitLab project id is a path with its slashes
		// escaped — decoded, it stops being one id.
		method, path = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitlab", APIBase: forge.URL + "/api/v4"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/-/merge_requests/7", ReviewComment("지적 없음", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut {
		t.Errorf("GitLab was sent %s", method)
	}
	if path != "/api/v4/projects/acme%2Fstore/merge_requests/7/notes/21" {
		t.Errorf("the edit went to %s", path)
	}
}

// The comment the platform writes has to be the comment the platform can find.
// Written and read by the same code, so a marker dropped from one side takes the
// other with it — and the only symptom is a comment per run, weeks later.
func TestTheCommentThePlatformWritesIsOneItCanFindAgain(t *testing.T) {
	first := ReviewComment("지적 1건", []store.ReviewFinding{
		{FilePath: "internal/api/review.go", StartLine: 42, Severity: "high", Message: "빠뜨린 오류 처리"},
	}, 10)
	var method string
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			listing, _ := json.Marshal([]map[string]any{{"id": 7, "body": first}})
			_, _ = w.Write(listing)
			return
		}
		method = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	if err := PostReviewComment(context.Background(), forge.Client(), connection, "s3cret",
		forge.URL+"/acme/store/pulls/9", ReviewComment("지적 없음", nil, 10)); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPatch {
		t.Fatalf("the platform could not recognise its own comment: %s", method)
	}
}

// A token is pasted once and used weeks later by a review at night. Whether it
// works has to be answerable now, while the person is still looking at the form.
func TestAStoredCredentialIsAskedAboutImmediately(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			t.Errorf("the check asked %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "token s3cret" {
			t.Errorf("the check was made with %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"login":"ci-bot"}`))
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	account, err := CheckSCMConnection(context.Background(), forge.Client(), connection, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if account != "ci-bot" {
		t.Fatalf("the forge said the token belongs to %q", account)
	}
}

// GitLab and Bitbucket name the account differently. Reading only GitHub's
// spelling would answer "it works, and I have no idea who as" — which reads as
// a check that did not happen.
func TestTheAccountIsNamedHoweverTheForgeSpellsIt(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"username":"ci-bot","id":9}`))
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitlab", APIBase: forge.URL + "/api/v4"}
	account, err := CheckSCMConnection(context.Background(), forge.Client(), connection, "s3cret")
	if err != nil || account != "ci-bot" {
		t.Fatalf("account %q, error %v", account, err)
	}
}

func TestARefusedCredentialSaysWhatTheForgeSaid(t *testing.T) {
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer forge.Close()
	connection := store.SCMConnection{Host: strings.TrimPrefix(forge.URL, "http://"), Kind: "gitea", APIBase: forge.URL + "/api/v1"}
	_, err := CheckSCMConnection(context.Background(), forge.Client(), connection, "wrong")
	if err == nil {
		t.Fatal("a refused token was reported as working")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("the refusal does not carry the forge's words: %v", err)
	}
}

// What a failed post-back says is read on the run's timeline, under a sentence
// this platform wrote: "리뷰를 <호스트>에 남기지 못했습니다: …". The HTTP half of
// this file already answered in kind; the half that reads the page's address
// answered in English, so the same line finished in a different language than
// it started.
func TestAPostBackFailureFinishesTheSentenceItStarted(t *testing.T) {
	github := store.SCMConnection{Host: "github.com", Kind: "github"}
	for _, item := range []struct {
		name, url string
		want      string
	}{
		{"another host", "https://gitlab.com/o/r/-/merge_requests/2", "원래 페이지가 이 연결의 호스트에 있지 않습니다"},
		{"no number", "https://github.com/o/r", "PR 번호를 찾지 못했습니다"},
	} {
		_, err := commentRequest(github, item.url, "리뷰")
		if err == nil {
			t.Errorf("%s: a page this platform cannot comment on was accepted", item.name)
			continue
		}
		if !strings.Contains(err.Error(), item.want) {
			t.Errorf("%s: the timeline would read %q", item.name, err.Error())
		}
	}
	gitlab := store.SCMConnection{Host: "gitlab.com", Kind: "gitlab"}
	if _, err := commentRequest(gitlab, "https://gitlab.com/o/r", "리뷰"); err == nil || !strings.Contains(err.Error(), "MR 번호를 찾지 못했습니다") {
		t.Errorf("a GitLab page with no merge request reads %v", err)
	}
	body, err := os.ReadFile("scmpost.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, english := range []string{"no pull request in", "no merge request in", "is not an address", "is not on the host this connection belongs to"} {
		if strings.Contains(string(body), english) {
			t.Errorf("a person is still shown %q at the end of a Korean sentence", english)
		}
	}
}
