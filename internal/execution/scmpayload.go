package execution

import "encoding/json"

// What the forges actually send.
//
// A webhook trigger can tell a review what to look at, and until now the body
// had to be written by hand: {"from":"main","to":"feature"}. Every forge already
// sends a body saying exactly that, in its own shape — so a site had to put a
// translating job in between, and the platform's own documentation told them to.
//
// Worse than inconvenient: a GitHub body carries base and head as objects, so
// the flat reading failed to parse and the payload was skipped in silence. The
// task then failed saying no target was given, about a request that named one.
//
// Direction is the thing to get right. A pull request is reviewed from its base
// to its head — main → feature, not the reverse — and reviewing the reverse diff
// produces a plausible, entirely wrong answer.

// scmTargets reads the branch pair out of a forge's own payload.
//
// Nothing is guessed: a shape either matches a forge's documented body or it is
// left for the flat reading to handle.
func scmTargets(candidate string) (from, to, commit string) {
	var body struct {
		// GitHub, Gitea and Forgejo: the pull request carries both refs.
		PullRequest struct {
			Base struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			MergeCommitSHA string `json:"merge_commit_sha"`
		} `json:"pull_request"`
		// GitLab: a merge request names its source and target the other way round.
		ObjectAttributes struct {
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			LastCommit   struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
		// Bitbucket: the pull request nests a branch name inside each side.
		Pullrequest struct {
			Source struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"source"`
			Destination struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"destination"`
		} `json:"pullrequest"`
		// A plain push, from any of them: one commit to look at.
		After  string `json:"after"`
		Before string `json:"before"`
	}
	if err := json.Unmarshal([]byte(candidate), &body); err != nil {
		return "", "", ""
	}

	switch {
	case body.PullRequest.Base.Ref != "" && body.PullRequest.Head.Ref != "":
		// base is where it is going, head is what is proposed. From base to head.
		return body.PullRequest.Base.Ref, body.PullRequest.Head.Ref, ""
	case body.ObjectAttributes.TargetBranch != "" && body.ObjectAttributes.SourceBranch != "":
		// target_branch is GitLab's word for base, source_branch for head. Reading
		// these as from/to in the order they appear reviews the reverse diff.
		return body.ObjectAttributes.TargetBranch, body.ObjectAttributes.SourceBranch, ""
	case body.Pullrequest.Destination.Branch.Name != "" && body.Pullrequest.Source.Branch.Name != "":
		return body.Pullrequest.Destination.Branch.Name, body.Pullrequest.Source.Branch.Name, ""
	}

	// A push names the commit it arrived at. `before` is not used as a base: a
	// force-push or a first push makes it a commit that is not there any more, or
	// forty zeros, and comparing against it fails in a way that reads like the
	// review being broken.
	if isCommitish(body.After) {
		return "", "", body.After
	}
	return "", "", ""
}

// isCommitish keeps the all-zero sha a first push sends out of the answer.
func isCommitish(value string) bool {
	if len(value) < 7 {
		return false
	}
	for _, char := range value {
		if char != '0' {
			return true
		}
	}
	return false
}
