package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every action the policy engine defines has to be evaluated somewhere.
//
// `agent.update` was not. It appeared in the rule editor, an administrator could
// write "이 역할은 에이전트를 수정할 수 없다", the rule saved, and that role went on
// editing agents — because no code ever asked the engine about it. A rule in the
// engine that nothing consults is worse than no rule at all, since the screen
// says it is in force.
//
// This is the same sweep that found it, run every time. It reads the repository
// rather than the types: an action consulted anywhere at all counts, so a failure
// means genuinely nowhere. It was proved by deleting an evaluation and watching
// the test name the action — the first version could not, because it also
// accepted the bare value, which an audit event happened to share.
func TestEveryPolicyActionIsEvaluatedSomewhere(t *testing.T) {
	definition, err := os.ReadFile(filepath.Join("..", "policy", "policy.go"))
	if err != nil {
		t.Fatal(err)
	}
	// The constant names are what code uses; the values are what documents use.
	constants := regexp.MustCompile(`(Action\w+)\s*=\s*"([\w.]+)"`).FindAllStringSubmatch(string(definition), -1)
	if len(constants) < 5 {
		t.Fatalf("found only %d policy actions; the shape this test reads by has probably changed", len(constants))
	}

	// Actions reached through a dedicated compiler rather than by naming the
	// constant at the call site. Each entry says where, because the first draft of
	// this test reported tool.call as dead and it is not — the runtime spec
	// compiles those rules into the in-Pod gateway, which is the only place they
	// could be enforced without asking the control plane on every tool call.
	compiled := map[string]string{
		"tool.call": "policy.CompileServer, called by internal/runtimespec when a runtime's MCP bindings are built",
	}

	source := platformSourceExcept(t, filepath.Join("..", "policy", "policy.go"))
	var dead []string
	for _, action := range constants {
		if where, ok := compiled[action[2]]; ok {
			if !strings.Contains(source, "policy.CompileServer") {
				t.Errorf("%s was reached through %s and that call is gone", action[2], where)
			}
			continue
		}
		// Only the constant counts. Code that evaluates policy always names it;
		// the bare value belongs to documents and audit records — and accepting it
		// made this test unable to fail, because `s.store.Audit(…, "agent.update",
		// …)` matched the very action whose evaluation had been removed.
		if strings.Contains(source, "policy."+action[1]) {
			continue
		}
		dead = append(dead, action[2])
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("the policy engine offers these actions and nothing evaluates them: %s\n"+
			"Either consult the action where it belongs, or stop offering it in the rule editor.",
			strings.Join(dead, ", "))
	}
}

// platformSourceExcept is every non-test Go file in the platform and its
// commands, minus the ones named — so a definition cannot satisfy a test about
// its own use.
func platformSourceExcept(t *testing.T, skip ...string) string {
	t.Helper()
	skipped := map[string]bool{}
	for _, name := range skip {
		skipped[filepath.Clean(name)] = true
	}
	var out strings.Builder
	for _, root := range []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if skipped[filepath.Clean(path)] || strings.HasSuffix(filepath.Clean(path), filepath.Join("policy", "policy.go")) {
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
	}
	return out.String()
}
