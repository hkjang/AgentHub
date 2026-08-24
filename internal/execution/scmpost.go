package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// Saying what a review found where the change is being discussed.
//
// A review that came from a pull request wrote its findings into AgentHub and
// nowhere else, so the people reading the pull request never saw them unless
// somebody went and looked. The address is in the payload and the credential is
// the owner's; what was missing is the request.
//
// The address comes from a webhook body, so it is chosen by whoever sent it. The
// credential is only ever sent to the host it belongs to — the endpoint is built
// from the connection, and a source page on any other host is refused rather
// than followed.

// scmComment is where a comment goes and how the request is signed.
type scmComment struct {
	endpoint string
	header   http.Header
	body     []byte
}

var errForeignHost = errors.New("the pull request is not on the host this connection belongs to")

func apiRoot(connection store.SCMConnection) string {
	if base := strings.TrimSuffix(strings.TrimSpace(connection.APIBase), "/"); base != "" {
		return base
	}
	switch connection.Kind {
	case "github":
		if connection.Host == "github.com" {
			return "https://api.github.com"
		}
		// GitHub Enterprise serves the same API under /api/v3 on its own host.
		return "https://" + connection.Host + "/api/v3"
	case "bitbucket":
		if connection.Host == "bitbucket.org" {
			return "https://api.bitbucket.org/2.0"
		}
		return "https://" + connection.Host + "/2.0"
	case "gitlab":
		return "https://" + connection.Host + "/api/v4"
	default: // gitea, forgejo
		return "https://" + connection.Host + "/api/v1"
	}
}

// commentRequest turns a pull request page into the request that comments on it.
func commentRequest(connection store.SCMConnection, sourceURL, text string) (scmComment, error) {
	page, err := url.Parse(sourceURL)
	if err != nil || page.Host == "" {
		return scmComment{}, fmt.Errorf("the source page is not an address: %q", sourceURL)
	}
	// A self-hosted forge may live on a port, so the connection's host is allowed
	// to carry one. Compared against the address with and without it — never
	// against a suffix, which is how github.com.evil.example gets a token.
	if !strings.EqualFold(page.Hostname(), connection.Host) && !strings.EqualFold(page.Host, connection.Host) {
		return scmComment{}, errForeignHost
	}
	parts := strings.Split(strings.Trim(page.Path, "/"), "/")
	header := http.Header{"Content-Type": []string{"application/json"}}
	root := apiRoot(connection)

	switch connection.Kind {
	case "github", "gitea":
		// /{owner}/{repo}/pull/{number} on GitHub, /pulls/{number} on Gitea.
		if len(parts) < 4 {
			return scmComment{}, fmt.Errorf("no pull request in %q", page.Path)
		}
		owner, repo, number := parts[0], parts[1], parts[len(parts)-1]
		if connection.Kind == "github" {
			header.Set("Accept", "application/vnd.github+json")
			header.Set("X-GitHub-Api-Version", "2022-11-28")
		}
		body, _ := json.Marshal(map[string]string{"body": text})
		// Both forges comment on a pull request through the issue it also is.
		return scmComment{
			endpoint: fmt.Sprintf("%s/repos/%s/%s/issues/%s/comments", root, owner, repo, number),
			header:   header, body: body,
		}, nil

	case "gitlab":
		// /{group}/…/{project}/-/merge_requests/{iid} — the project path is
		// everything before the `-`, so nested groups survive.
		marker := strings.Index(page.Path, "/-/merge_requests/")
		if marker < 0 {
			return scmComment{}, fmt.Errorf("no merge request in %q", page.Path)
		}
		project := strings.Trim(page.Path[:marker], "/")
		iid := strings.Trim(page.Path[marker+len("/-/merge_requests/"):], "/")
		body, _ := json.Marshal(map[string]string{"body": text})
		return scmComment{
			endpoint: fmt.Sprintf("%s/projects/%s/merge_requests/%s/notes", root, url.PathEscape(project), iid),
			header:   header, body: body,
		}, nil

	default: // bitbucket
		if len(parts) < 4 {
			return scmComment{}, fmt.Errorf("no pull request in %q", page.Path)
		}
		workspace, repo, number := parts[0], parts[1], parts[len(parts)-1]
		body, _ := json.Marshal(map[string]any{"content": map[string]string{"raw": text}})
		return scmComment{
			endpoint: fmt.Sprintf("%s/repositories/%s/%s/pullrequests/%s/comments", root, workspace, repo, number),
			header:   header, body: body,
		}, nil
	}
}

// authorize puts the credential on the request the way each forge expects it.
func authorize(header http.Header, kind, token string) {
	switch kind {
	case "gitlab":
		header.Set("PRIVATE-TOKEN", token)
	case "gitea":
		header.Set("Authorization", "token "+token)
	default: // github, bitbucket
		header.Set("Authorization", "Bearer "+token)
	}
}

// PostReviewComment says what the review found, on the page it came from.
func PostReviewComment(ctx context.Context, client *http.Client, connection store.SCMConnection, token, sourceURL, text string) error {
	request, err := commentRequest(connection, sourceURL, text)
	if err != nil {
		return err
	}
	authorize(request.header, connection.Kind, token)
	call, err := http.NewRequestWithContext(ctx, http.MethodPost, request.endpoint, bytes.NewReader(request.body))
	if err != nil {
		return err
	}
	call.Header = request.header
	response, err := client.Do(call)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		// The forge's own words, capped: a 401 says the token is revoked and a
		// 404 says it cannot see the repository, and those are different repairs.
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 400))
		return fmt.Errorf("%s가 %d로 거절했습니다: %s", connection.Host, response.StatusCode,
			strings.TrimSpace(string(detail)))
	}
	return nil
}

// ReviewComment is what the pull request gets to read.
//
// The findings, not a link to them: somebody reading the change should be able
// to see what was said without an account on this platform. Capped, because a
// hundred-line comment is a wall nobody reads — and the cap is stated rather
// than silently applied.
func ReviewComment(summary string, findings []store.ReviewFinding, limit int) string {
	var text strings.Builder
	text.WriteString("**AgentHub 코드 리뷰**\n\n")
	text.WriteString(summary)
	if len(findings) == 0 {
		return text.String()
	}
	text.WriteString("\n\n")
	shown := findings
	if len(shown) > limit {
		shown = shown[:limit]
	}
	for _, finding := range shown {
		where := finding.FilePath
		if finding.StartLine > 0 {
			where = fmt.Sprintf("%s:%d", finding.FilePath, finding.StartLine)
		}
		text.WriteString(fmt.Sprintf("- **%s** `%s` — %s\n", finding.Severity, where,
			strings.TrimSpace(finding.Message)))
	}
	if len(findings) > len(shown) {
		text.WriteString(fmt.Sprintf("\n남은 %d건은 AgentHub에서 볼 수 있습니다.\n", len(findings)-len(shown)))
	}
	return text.String()
}

// announceReview says what the review found on the page the request came from.
//
// Silent when nobody has stored a credential for that host: posting back is
// something an owner opts into, and a review that came from a webhook is not a
// request to comment on it. Never silent when a credential exists and the
// attempt failed — a revoked token and a clean review both post nothing, so the
// difference is written on the connection and on the run.
func (o *Orchestrator) announceReview(ctx context.Context, run store.AgentRun, task store.AgentTask, ownerID, summary string, findings []store.ReviewFinding) {
	if task.SourceURL == "" {
		return
	}
	page, err := url.Parse(task.SourceURL)
	if err != nil || page.Hostname() == "" {
		return
	}
	// With the port first: a self-hosted forge on one is stored that way, and
	// looking it up without would find nothing and say nothing about it.
	connection, token, err := o.store.SCMTokenFor(ctx, ownerID, page.Host)
	if errors.Is(err, store.ErrNotFound) && page.Host != page.Hostname() {
		connection, token, err = o.store.SCMTokenFor(ctx, ownerID, page.Hostname())
	}
	if errors.Is(err, store.ErrNotFound) {
		return
	}
	if err != nil {
		o.logger.Error("the forge credential could not be read", "run", run.ID, "host", page.Host, "error", err)
		return
	}
	failure := ""
	if err := PostReviewComment(ctx, scmHTTPClient, connection, token, task.SourceURL,
		ReviewComment(summary, findings, 10)); err != nil {
		failure = err.Error()
		o.logger.Warn("the review could not be posted back", "run", run.ID, "host", connection.Host, "error", err)
		o.event(ctx, run, "review.post.failed", "리뷰를 "+connection.Host+"에 남기지 못했습니다: "+failure, nil)
	} else {
		o.event(ctx, run, "review.posted", connection.Host+"의 원래 페이지에 리뷰를 남겼습니다.",
			map[string]any{"url": task.SourceURL, "findings": len(findings)})
	}
	if err := o.store.RecordSCMUse(ctx, connection.ID, failure); err != nil {
		o.logger.Warn("the forge connection's last use could not be recorded", "connection", connection.ID, "error", err)
	}
}

// scmHTTPClient is deliberately short-tempered: a forge that will not answer must
// not hold a worker slot open behind it.
var scmHTTPClient = &http.Client{Timeout: 20 * time.Second}
