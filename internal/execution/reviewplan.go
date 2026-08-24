package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// What a review would do, before it costs anything.
//
// The review engine's delegation mode answers two questions with no model at
// all: which files a review would look at, and which standard each of them would
// be held to. Both are decided deterministically from the diff and the rule
// table, so asking is free — and until now the only way to find out was to run
// the review and read the result afterwards, by which point the tokens are spent
// and the answer to "why was my file not reviewed" is still missing.

// ReviewPlan is the answer.
type ReviewPlan struct {
	Mode       string `json:"mode"`
	Repository string `json:"repository"`
	// Reviewable and Excluded are counted separately because a review that looked
	// at nothing and a review with nothing to look at are different situations.
	Reviewable []ReviewPlanFile  `json:"reviewable"`
	Excluded   []ReviewPlanFile  `json:"excluded"`
	Insertions int               `json:"insertions"`
	Deletions  int               `json:"deletions"`
	Groups     []ReviewPlanGroup `json:"groups"`
}

// ReviewPlanFile is one file and what changed in it.
type ReviewPlanFile struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	// Reason says why an excluded file was left out.
	Reason string `json:"excludeReason,omitempty"`
}

// ReviewPlanGroup is a set of files that share one standard.
type ReviewPlanGroup struct {
	ID      int      `json:"id"`
	Source  string   `json:"source"`
	Pattern string   `json:"pattern"`
	Files   []string `json:"files"`
	// Rule is the standard itself, which is long. It is carried so a person can
	// read what their code will be judged against rather than trusting a name.
	Rule string `json:"rule"`
}

// planPreview and planRules are the two documents the engine emits.
type planPreview struct {
	SchemaVersion   string     `json:"schema_version"`
	Mode            string     `json:"mode"`
	Repository      string     `json:"repository"`
	ReviewableFiles []planFile `json:"reviewable_files"`
	ExcludedFiles   []planFile `json:"excluded_files"`
	TotalInsertions int        `json:"total_insertions"`
	TotalDeletions  int        `json:"total_deletions"`
}

// planFile is the engine's spelling, which is not this API's spelling.
//
// One struct carrying both would have to choose, and the choice is invisible
// until somebody reads the wrong end: the engine says exclude_reason and every
// other field this platform serves is camelCase. The first version of this file
// tried to do both jobs at once and dropped the reason on the floor.
type planFile struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	Reason     string `json:"exclude_reason"`
}

func asPlanFiles(files []planFile) []ReviewPlanFile {
	out := make([]ReviewPlanFile, 0, len(files))
	for _, file := range files {
		out = append(out, ReviewPlanFile{
			Path: file.Path, Status: file.Status,
			Insertions: file.Insertions, Deletions: file.Deletions, Reason: file.Reason,
		})
	}
	return out
}

type planRules struct {
	SchemaVersion string `json:"schema_version"`
	Groups        []struct {
		GroupID int      `json:"group_id"`
		Source  string   `json:"source"`
		Pattern string   `json:"pattern"`
		Files   []string `json:"files"`
		Rule    string   `json:"rule"`
	} `json:"groups"`
}

// parseReviewPlan reads what the engine printed.
//
// The engine prints human text after the JSON on some paths, so the document is
// decoded from a stream rather than by unmarshalling the whole output — the same
// reason the review parser does it, learned the same way.
func parseReviewPlan(previewOut, rulesOut string) (ReviewPlan, error) {
	var preview planPreview
	if err := json.NewDecoder(strings.NewReader(previewOut)).Decode(&preview); err != nil {
		return ReviewPlan{}, fmt.Errorf("리뷰 대상 목록을 읽지 못했습니다: %w", err)
	}
	if preview.SchemaVersion == "" {
		return ReviewPlan{}, errors.New("리뷰 대상 목록이 예상한 형식이 아닙니다")
	}
	plan := ReviewPlan{
		Mode: preview.Mode, Repository: preview.Repository,
		Reviewable: asPlanFiles(preview.ReviewableFiles), Excluded: asPlanFiles(preview.ExcludedFiles),
		Insertions: preview.TotalInsertions, Deletions: preview.TotalDeletions,
		Groups: []ReviewPlanGroup{},
	}
	if plan.Reviewable == nil {
		plan.Reviewable = []ReviewPlanFile{}
	}
	if plan.Excluded == nil {
		plan.Excluded = []ReviewPlanFile{}
	}
	// The rules half is optional: a plan with no reviewable file has no group, and
	// saying so is better than failing the whole answer.
	if strings.TrimSpace(rulesOut) == "" {
		return plan, nil
	}
	var rules planRules
	if err := json.NewDecoder(strings.NewReader(rulesOut)).Decode(&rules); err != nil {
		return plan, nil
	}
	for _, group := range rules.Groups {
		plan.Groups = append(plan.Groups, ReviewPlanGroup{
			ID: group.GroupID, Source: group.Source, Pattern: group.Pattern,
			Files: group.Files, Rule: group.Rule,
		})
	}
	return plan, nil
}

// ReviewPlanFor asks the engine what a review would do, without running one.
//
// It takes the spawner and the spec rather than an orchestrator because the
// question belongs to whoever is asking — the console, before anybody spends a
// token — and not to the execution loop.
func ReviewPlanFor(ctx context.Context, spawner appRuntime.Spawner, spec appRuntime.Spec, goal store.AgentGoal) (ReviewPlan, error) {
	preview, err := spawner.Exec(ctx, spec, appRuntime.ExecRequest{Command: reviewPlanCommand(goal, "preview", nil)})
	if err != nil {
		return ReviewPlan{}, fmt.Errorf("리뷰 대상을 확인하지 못했습니다: %w", err)
	}
	plan, err := parseReviewPlan(preview.Stdout, "")
	if err != nil {
		return ReviewPlan{}, err
	}
	if len(plan.Reviewable) == 0 {
		return plan, nil
	}
	paths := make([]string, 0, len(plan.Reviewable))
	for _, file := range plan.Reviewable {
		paths = append(paths, file.Path)
	}
	rules, err := spawner.Exec(ctx, spec, appRuntime.ExecRequest{Command: reviewPlanCommand(goal, "rule", paths)})
	if err != nil {
		// The file list is still worth returning: knowing what will be reviewed is
		// most of the question, and the standard can be read afterwards.
		return plan, nil
	}
	return parseReviewPlan(preview.Stdout, rules.Stdout)
}

// reviewPlanCommand builds the delegation call for one of the two subcommands.
//
// The refs come from the same Goal fields a real review uses, so the plan is
// about the review that would actually run rather than a different one.
func reviewPlanCommand(goal store.AgentGoal, sub string, paths []string) []string {
	argv := []string{"ocr", "delegate", sub, "--format", "json"}
	// The refs are checked the way the runner checks them: a ref carrying shell
	// punctuation is left out rather than passed on, and a plan for the default
	// mode is still an answer.
	switch reviewModeOf(goal) {
	case "range":
		if safeRef(goal.ReviewBaseRef) && safeRef(goal.ReviewHeadRef) {
			argv = append(argv, "--from", goal.ReviewBaseRef, "--to", goal.ReviewHeadRef)
		}
	case "commit":
		if safeRef(goal.ReviewBaseRef) {
			argv = append(argv, "--commit", goal.ReviewBaseRef)
		}
	}
	if strings.TrimSpace(goal.ReviewExclude) != "" {
		argv = append(argv, "--exclude", goal.ReviewExclude)
	}
	return append(argv, paths...)
}

// reviewModeOf is the Goal's review mode with the same default the runner uses.
func reviewModeOf(goal store.AgentGoal) string {
	if goal.ReviewMode == "" {
		return "workspace"
	}
	return goal.ReviewMode
}
