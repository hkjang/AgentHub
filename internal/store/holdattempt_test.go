package store

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Claiming a task charges an attempt before anything has decided whether the task
// may run. Every path that then parks the task without running it has to hand
// that attempt back, and two of the three did: the defer path when a runtime slot
// was full, the approval path when a person eventually answered. The hold path
// said so in a comment and did not do it, so a task held twice met its first real
// failure with a retry budget already spent — and dead-lettered looking like a
// flaky agent.
//
// This reads the SQL rather than trusting the comments, because it was the
// comment that was wrong.
func TestEveryParkedTaskGetsItsAttemptBack(t *testing.T) {
	// The function that parks, and the function that hands the attempt back — the
	// same one where the rollback happens at park time.
	for _, pair := range []struct{ parks, refunds string }{
		{"BlockAgentTask", "BlockAgentTask"},
		{"DeferAgentTask", "DeferAgentTask"},
		{"ParkTaskForApproval", "ResumeApprovedTask"},
	} {
		body := functionBody(t, pair.refunds)
		if !strings.Contains(body, "attempts") {
			t.Errorf("%s parks a task without running it, and %s never touches the attempt count; waiting is not a failed attempt",
				pair.parks, pair.refunds)
		}
	}
}

// And no new way to park a task may appear without deciding the same question.
// A guard that only knows the three paths it was written for is escaped by adding
// a fourth, which is how the hold path came to disagree with its neighbours in the
// first place.
func TestNoUnknownWayToParkATask(t *testing.T) {
	known := map[string]bool{"BlockAgentTask": true, "ParkTaskForApproval": true, "HandOffTask": true}
	parked := regexp.MustCompile(`status='(blocked|waiting_approval|handoff)'`)
	for _, name := range packageFiles(t) {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, statement := range strings.Split(source, "UPDATE agent_tasks")[1:] {
			// Only what is being written. A WHERE clause naming a parked status is
			// reading one — releasing it, usually — and that is the opposite of this.
			if at := strings.Index(statement, "WHERE"); at >= 0 {
				statement = statement[:at]
			}
			if !parked.MatchString(statement) {
				continue
			}
			owner := enclosingFunction(source[:strings.Index(source, statement)])
			if !known[owner] {
				t.Errorf("%s.%s parks a task and this guard has not been told whether it hands the attempt back", name, owner)
			}
		}
	}
}

var functionStart = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)

// functionBody is everything from a function's signature to the closing brace in
// column one — enough to read its SQL, which is all this file needs.
func functionBody(t *testing.T, name string) string {
	t.Helper()
	for _, file := range packageFiles(t) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		for _, match := range functionStart.FindAllStringSubmatchIndex(source, -1) {
			if source[match[2]:match[3]] != name {
				continue
			}
			rest := source[match[0]:]
			if end := strings.Index(rest, "\n}\n"); end >= 0 {
				rest = rest[:end]
			}
			// A query kept in a named constant is still this function's SQL — the
			// defer path holds its statement that way so the requeue can share it.
			return rest + sqlConstants(t, rest)
		}
	}
	t.Fatalf("%s is not in this package any more; this guard is reading nothing", name)
	return ""
}

// sqlConstants returns the text of any `…SQL` constant the body refers to, so a
// query written once and used twice is read where it is used.
func sqlConstants(t *testing.T, body string) string {
	t.Helper()
	var out strings.Builder
	for _, ref := range regexp.MustCompile(`\b(\w+SQL)\b`).FindAllStringSubmatch(body, -1) {
		for _, file := range packageFiles(t) {
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			at := strings.Index(string(source), "const "+ref[1]+" = ")
			if at < 0 {
				continue
			}
			rest := string(source)[at:]
			if end := strings.Index(rest[strings.Index(rest, "`")+1:], "`"); end >= 0 {
				out.WriteString(rest[:strings.Index(rest, "`")+end+2])
			}
		}
	}
	return out.String()
}

// enclosingFunction names the function a position is inside, by looking backwards
// for the nearest signature.
func enclosingFunction(before string) string {
	matches := functionStart.FindAllStringSubmatch(before, -1)
	if len(matches) == 0 {
		return "(top level)"
	}
	return matches[len(matches)-1][1]
}
