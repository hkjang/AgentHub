package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every gate that stops work has to leave a record.
//
// Four can refuse a task before it exists — the platform policy, the promotion
// gate, a spent budget, and an agent with no model bound — and only the policy
// wrote anything down. So "who was refused, and by what" was answerable for one
// of the four, and for the other three the platform said no and remembered
// nothing. "Why did last night's run not happen" is the question this log is for.
//
// Refusals go through refuseTask now, and this allows no other way out: a gate
// added later cannot quietly be the fifth.
func TestEveryRefusalToStartWorkIsRecorded(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) enqueueTask(")
	if at < 0 {
		t.Fatal("enqueueTask is gone; this guard is reading nothing")
	}
	fn := source[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	// One writeError is allowed: the policy refusal, which records itself under
	// the rule that refused it before this function ever sees the decision.
	direct := regexp.MustCompile(`writeError\(`).FindAllString(fn, -1)
	if len(direct) > 1 {
		t.Errorf("enqueueTask refuses a request %d times without going through refuseTask; only the policy refusal records itself elsewhere", len(direct))
	}
	if !strings.Contains(fn, "policy_denied") {
		t.Error("the one allowed direct refusal is no longer the policy one; check what this exemption is now covering")
	}
	for _, gate := range []string{`"promotion"`, `"quota"`, `"model"`} {
		if !strings.Contains(fn, gate) {
			t.Errorf("the %s gate no longer names itself in the record; a refusal that does not say which gate stopped it is a refusal nobody can act on", gate)
		}
	}

	refusal := source[strings.Index(source, "func (s *Server) refuseTask("):]
	if end := strings.Index(refusal, "\n}\n"); end >= 0 {
		refusal = refusal[:end]
	}
	if !strings.Contains(refusal, `.Audit(`) || !strings.Contains(refusal, `"denied"`) {
		t.Error("refuseTask no longer records the refusal; it is then just a way of writing an error")
	}
	if !strings.Contains(refusal, "writeError(") {
		t.Error("refuseTask no longer answers the caller")
	}
}

// And the wider rule this came from: a route that changes something writes an
// audit record somewhere in reach. The three exceptions are named, with reasons,
// so adding a fourth is a decision somebody makes on purpose.
func TestEveryStateChangingRouteAudits(t *testing.T) {
	unaudited := map[string]string{
		"readNotification": "marking one's own notice as read changes nothing anybody reviews",
		"simulatePolicy":   "a dry run against a policy document; it decides nothing",
		"scanSample":       "a dry run against the content scanner; recording it would record the sample",
	}
	files := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			body, readErr := os.ReadFile(name)
			if readErr != nil {
				t.Fatal(readErr)
			}
			files[name] = string(body)
		}
	}
	all := strings.Join(valuesOf(files), "\n")
	handlerBody := func(name string) string {
		at := strings.Index(all, "func (s *Server) "+name+"(")
		if at < 0 {
			return ""
		}
		rest := all[at:]
		if end := strings.Index(rest, "\n}\n"); end >= 0 {
			return rest[:end]
		}
		return rest
	}
	var reaches func(name string, depth int, seen map[string]bool) bool
	reaches = func(name string, depth int, seen map[string]bool) bool {
		if depth > 2 || seen[name] {
			return false
		}
		seen[name] = true
		body := handlerBody(name)
		if body == "" {
			return false
		}
		if strings.Contains(body, ".Audit(") {
			return true
		}
		for _, callee := range regexp.MustCompile(`s\.(\w+)\(`).FindAllStringSubmatch(body, -1) {
			if reaches(callee[1], depth+1, seen) {
				return true
			}
		}
		return false
	}
	routes := regexp.MustCompile(`(?:write|admin|manage)\((http\.Method\w+), "[^"]+", "[^"]+", "[^"]+", s\.(\w+)`)
	checked := 0
	for _, route := range routes.FindAllStringSubmatch(files["catalog.go"], -1) {
		if route[1] == "http.MethodGet" {
			continue
		}
		checked++
		if _, allowed := unaudited[route[2]]; allowed {
			continue
		}
		if !reaches(route[2], 0, map[string]bool{}) {
			t.Errorf("%s changes something and writes no audit record; the log a compliance review reads would not know it happened", route[2])
		}
	}
	if checked < 50 {
		t.Fatalf("only %d state-changing routes found; this guard is not reading the catalog", checked)
	}
}

func valuesOf(files map[string]string) []string {
	out := make([]string, 0, len(files))
	for _, body := range files {
		out = append(out, body)
	}
	return out
}
