package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every setting the console saves has to be read by something.
//
// Four were not. The console offered a switch for runtime log access and the
// endpoint served logs regardless; it offered an audit retention period that a
// different screen actually owned; it offered two toggles over behaviour with no
// off; and it offered a default idle timeout while the culler used a constant.
// None of them looked broken — a settings screen that saves is a settings screen
// that works, until somebody checks what happens next.
//
// They were found by hand, once. This is the same sweep, run every time — and it
// spent a while unable to fail: it also accepted a bare quoted key, which words
// like "enabled", "mode" and "level" match all over a codebase this size. Proved
// now by renaming one real setting's struct tag and watching the test name it.
func TestEverySettingTheConsoleSavesIsReadSomewhere(t *testing.T) {
	console, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "AdminSettings.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	keys := map[string]bool{}
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`update\('([a-zA-Z]+)'`),
		regexp.MustCompile(`name="([a-zA-Z]+)"`),
	} {
		for _, match := range pattern.FindAllStringSubmatch(string(console), -1) {
			keys[match[1]] = true
		}
	}
	if len(keys) < 20 {
		t.Fatalf("found only %d settings keys; the pattern this test reads by has probably changed", len(keys))
	}

	// Keys the platform deliberately does not act on, each with the reason it is
	// allowed to stay. Adding a name here is a decision somebody has to defend in
	// review, which is the point: the default is that a saved setting does
	// something.
	displayOnly := map[string]string{
		"secret": "not a setting — the write-only field every secret-bearing form posts alongside its value",
	}

	body := goSource(t)
	var dead []string
	for key := range keys {
		if _, allowed := displayOnly[key]; allowed {
			continue
		}
		// Only a struct tag counts. Settings are read by decoding the blob into a
		// struct, so that is what reading one looks like — while a bare quoted
		// string matches any unrelated use of a common word, and keys like
		// "enabled", "mode" and "level" appear all over a codebase this size. The
		// policy-action guard had the same hole and could not fail because of it.
		if strings.Contains(body, `json:"`+key+`"`) {
			continue
		}
		dead = append(dead, key)
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("the console saves these settings and nothing reads them: %s\n"+
			"Either act on the value, or take the control off the screen and say where the real one is.",
			strings.Join(dead, ", "))
	}
}

// Every field on a Goal has to be read by something too.
//
// A Goal is where an operator writes the guardrails they intend to be bound by —
// how many tool calls, how deep a delegation may go, how long before the runtime
// is given back. A field that is stored and never consulted is the same failure
// as a settings switch that saves and does nothing, with more at stake: nobody
// checks whether a limit held until the day it did not.
func TestEveryGoalFieldIsReadSomewhere(t *testing.T) {
	definition, err := os.ReadFile(filepath.Join("..", "..", "internal", "store", "execution.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(definition)
	start := strings.Index(body, "type AgentGoal struct {")
	if start < 0 {
		t.Fatal("AgentGoal is not where this test looks for it")
	}
	end := strings.Index(body[start:], "\n}\n")
	fields := regexp.MustCompile(`(\w+)\s+[\w\[\]\.]+\s+`+"`"+`json:"(\w+)"`).
		FindAllStringSubmatch(body[start:start+end], -1)
	if len(fields) < 20 {
		t.Fatalf("found only %d goal fields; the shape this test reads by has probably changed", len(fields))
	}

	// Fields kept for a reason other than being acted on.
	writeOnly := map[string]string{
		"LegacyApprovalMode": "written for clients on the old field name and read by none of them here; due to be dropped once the documented deprecation passes",
	}

	// Everything except the file that defines them — where every field is
	// naturally mentioned by the insert, which would make this test pass for free.
	source := goSourceExcept(t, filepath.Join("..", "..", "internal", "store", "execution.go"))
	var dead []string
	for _, field := range fields {
		if _, allowed := writeOnly[field[1]]; allowed {
			continue
		}
		if strings.Contains(source, "."+field[1]) {
			continue
		}
		dead = append(dead, field[2])
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("these Goal fields are stored and never read: %s\n"+
			"A guardrail nobody consults is not a guardrail.", strings.Join(dead, ", "))
	}
}

// goSource is every non-test Go file in the platform, concatenated. Reading them
// all is cruder than following the types, and it is what makes the test hard to
// fool: a key read anywhere at all counts, so a failure means genuinely nowhere.
func goSource(t *testing.T) string { return goSourceExcept(t) }

func goSourceExcept(t *testing.T, skip ...string) string {
	t.Helper()
	skipped := map[string]bool{}
	for _, name := range skip {
		skipped[filepath.Clean(name)] = true
	}
	var out strings.Builder
	root := filepath.Join("..", "..", "internal")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		if skipped[filepath.Clean(path)] {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out.Write(body)
		return nil
	})
	if err != nil {
		t.Fatalf("read the platform's own source: %v", err)
	}
	return out.String()
}
