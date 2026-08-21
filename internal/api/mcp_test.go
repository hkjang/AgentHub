package api

import (
	"slices"
	"testing"
)

// Listing a tool a key will be refused for is not a security hole — the refusal
// still happens — but it is a lie to the agent reading the list, which tries,
// fails, and has no way to know the failure was structural. The platform's own
// gateway already filters the tool lists it forwards; this is the same courtesy
// for its own.
func TestTheToolListMatchesWhatTheKeyMayCall(t *testing.T) {
	names := func(scopes []string) []string {
		out := []string{}
		for _, tool := range mcpTools(scopes) {
			out = append(out, tool["name"].(string))
		}
		return out
	}
	readOnly := names([]string{ScopeMCP})
	for _, forbidden := range []string{"agenthub_runtime_action", "agenthub_queue_task"} {
		if slices.Contains(readOnly, forbidden) {
			t.Errorf("a read-only key is offered %q", forbidden)
		}
	}
	if !slices.Contains(readOnly, "agenthub_list_agents") || !slices.Contains(readOnly, "agenthub_task_status") {
		t.Errorf("a read-only key should still see what it can read: %v", readOnly)
	}
	writer := names([]string{ScopeMCP, ScopeWrite})
	if !slices.Contains(writer, "agenthub_queue_task") {
		t.Errorf("a key with agent:write is not offered the tool it holds the scope for: %v", writer)
	}
	if slices.Contains(writer, "agenthub_runtime_action") {
		t.Errorf("agent:write is not runtime:manage: %v", writer)
	}
	// A key with everything sees everything, and nothing is listed twice.
	all := names([]string{"*"})
	if len(all) != 5 {
		t.Errorf("a full key sees %d tools: %v", len(all), all)
	}
	seen := map[string]bool{}
	for _, name := range all {
		if seen[name] {
			t.Errorf("%q is listed twice", name)
		}
		seen[name] = true
	}
	// Nothing is offered to a key with no scopes at all.
	if got := names(nil); len(got) != 0 {
		t.Errorf("an unscoped key is offered %v", got)
	}
}

// Every tool has to describe its own arguments, because the agent reading this
// list has nothing else to go on.
func TestEveryToolDeclaresItsArguments(t *testing.T) {
	for _, tool := range mcpTools([]string{"*"}) {
		name, _ := tool["name"].(string)
		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok {
			t.Errorf("%s has no input schema", name)
			continue
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s accepts undeclared arguments", name)
		}
		if _, hasDescription := tool["description"].(string); !hasDescription {
			t.Errorf("%s has no description", name)
		}
	}
}
