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
