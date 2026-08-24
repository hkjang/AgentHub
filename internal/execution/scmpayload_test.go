package execution

import "testing"

// The bodies below are the shapes the forges document, trimmed to the fields the
// platform reads. The nesting is the point: a flat reading cannot see any of it.

const githubPullRequest = `{
  "action": "opened",
  "number": 42,
  "pull_request": {
    "html_url": "https://github.com/acme/store/pull/42",
    "base": {"ref": "main", "sha": "0d33e1f1a1b2c3d4e5f60718293a4b5c6d7e8f90"},
    "head": {"ref": "feature/login", "sha": "9f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c6"}
  }
}`

const gitlabMergeRequest = `{
  "object_kind": "merge_request",
  "object_attributes": {
    "source_branch": "feature/login",
    "target_branch": "main",
    "last_commit": {"id": "9f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c6"}
  }
}`

const bitbucketPullRequest = `{
  "pullrequest": {
    "source": {"branch": {"name": "feature/login"}},
    "destination": {"branch": {"name": "main"}}
  }
}`

const githubPush = `{
  "ref": "refs/heads/main",
  "before": "0d33e1f1a1b2c3d4e5f60718293a4b5c6d7e8f90",
  "after": "9f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c6"
}`

func TestAForgesOwnPullRequestBodyNamesTheBranches(t *testing.T) {
	for _, forge := range []struct {
		name string
		body string
	}{
		{"GitHub", githubPullRequest},
		{"GitLab", gitlabMergeRequest},
		{"Bitbucket", bitbucketPullRequest},
	} {
		from, to, commit := reviewTargetsFromTask("리뷰해줘\n" + forge.body)
		if from != "main" || to != "feature/login" {
			t.Errorf("%s: a pull request from main to feature/login was read as from %q to %q",
				forge.name, from, to)
		}
		if commit != "" {
			t.Errorf("%s: a branch comparison also named a single commit %q", forge.name, commit)
		}
	}
}

// Reviewing feature → main instead of main → feature answers about the wrong
// diff and looks like a finished review, so the direction gets its own guard.
func TestTheDiffIsReadInTheDirectionTheForgeMeans(t *testing.T) {
	from, to, _ := scmTargets(gitlabMergeRequest)
	if from == "feature/login" || to == "main" {
		t.Fatalf("GitLab's source and target were read in the order they appear: from %q to %q", from, to)
	}
}

func TestAPushNamesTheCommitItArrivedAt(t *testing.T) {
	from, to, commit := reviewTargetsFromTask(githubPush)
	if commit != "9f8e7d6c5b4a39281706f5e4d3c2b1a0f9e8d7c6" {
		t.Fatalf("a push was read as commit %q", commit)
	}
	if from != "" || to != "" {
		t.Fatalf("a push was also read as a branch comparison: from %q to %q", from, to)
	}
}

// A first push sends forty zeros as `before`. Comparing against it fails in a way
// that reads like the review being broken rather than the request being empty.
func TestTheFirstPushDoesNotAskForAnAbsentCommit(t *testing.T) {
	_, _, commit := reviewTargetsFromTask(`{"before":"0000000000000000000000000000000000000000","after":"0000000000000000000000000000000000000000"}`)
	if commit != "" {
		t.Fatalf("a first push asked for commit %q", commit)
	}
}

// The hand-written body the platform accepted before still works: an adapter that
// takes something away is not an adapter.
func TestTheHandWrittenBodyStillWorks(t *testing.T) {
	from, to, _ := reviewTargetsFromTask(`{"from":"main","to":"feature/login"}`)
	if from != "main" || to != "feature/login" {
		t.Fatalf("the documented body was read as from %q to %q", from, to)
	}
}

// The failure that started this: a GitHub body used to fail to parse, so the task
// reported no target about a request that named one.
func TestANestedBodyIsNotSilentlySkipped(t *testing.T) {
	from, to, commit := reviewTargetsFromTask(githubPullRequest)
	if from == "" && to == "" && commit == "" {
		t.Fatal("a GitHub pull request body was read as naming nothing to review")
	}
}

// A finished review that names a branch and nothing else leaves the reader
// searching for the change by hand. Every forge sends the address; it was
// dropped.
func TestTheReviewKnowsWhichPullRequestItCameFrom(t *testing.T) {
	for name, want := range map[string]string{
		githubPullRequest:     "https://github.com/acme/store/pull/42",
		gitlabMergeRequestURL: "https://gitlab.com/acme/store/-/merge_requests/7",
		bitbucketWithLink:     "https://bitbucket.org/acme/store/pull-requests/3",
	} {
		if got := SourceURLFromPayload("리뷰해줘\n" + name); got != want {
			t.Errorf("the source page was read as %q, want %q", got, want)
		}
	}
}

const gitlabMergeRequestURL = `{
  "object_kind": "merge_request",
  "object_attributes": {
    "url": "https://gitlab.com/acme/store/-/merge_requests/7",
    "source_branch": "feature/login",
    "target_branch": "main"
  }
}`

const bitbucketWithLink = `{
  "pullrequest": {
    "links": {"html": {"href": "https://bitbucket.org/acme/store/pull-requests/3"}},
    "source": {"branch": {"name": "feature/login"}},
    "destination": {"branch": {"name": "main"}}
  }
}`

// The body is signed, not trusted. The address is rendered as a link, so a
// javascript: href in a payload would be a script running in the console of
// whoever opens the task.
func TestAnAddressThatIsNotAWebPageIsNotCarried(t *testing.T) {
	for _, hostile := range []string{
		`{"pull_request":{"html_url":"javascript:alert(1)"}}`,
		`{"pull_request":{"html_url":"data:text/html,<script>alert(1)</script>"}}`,
		`{"pull_request":{"html_url":"file:///etc/passwd"}}`,
		`{"pull_request":{"html_url":"https://"}}`,
	} {
		if got := SourceURLFromPayload(hostile); got != "" {
			t.Errorf("%s was carried as %q", hostile, got)
		}
	}
}

// A task the platform started itself has no source page, and must not borrow one.
func TestATaskWithNoPayloadHasNoSourcePage(t *testing.T) {
	if got := SourceURLFromPayload("리뷰해줘"); got != "" {
		t.Fatalf("a plain instruction produced a source page: %q", got)
	}
}
