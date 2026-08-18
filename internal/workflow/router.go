package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Routing is what the router decided, kept on the result so the choice can be
// read afterwards instead of inferred from which steps happened to run.
type Routing struct {
	// Step is the router that decided.
	Step string `json:"step"`
	// Chosen are the branch step ids it selected.
	Chosen []string `json:"chosen"`
	// Reason is what it said about the choice.
	Reason string `json:"reason,omitempty"`
	// Validated is true when the gateway accepted the schema rather than refusing
	// it. A gateway that ignores response_format also accepts it, so the answer is
	// validated against the candidate ids regardless.
	Validated bool `json:"validated"`
	// FellBack is set when no usable decision came back and every branch was run.
	// A router whose answer could not be read is worth seeing, not hiding.
	FellBack bool   `json:"fellBack,omitempty"`
	Note     string `json:"note,omitempty"`
}

// routerDecision is the answer the router is asked for.
type routerDecision struct {
	Branches []string `json:"branches"`
	Reason   string   `json:"reason"`
	// Handoff is what the chosen branch should be told. Without it the branch
	// would receive the decision JSON as its context, which says which branch was
	// picked but not what to do.
	Handoff string `json:"handoff"`
}

// routerInstruction and routerSchema are how a routing decision is asked for.
//
// It used to be read out of prose by looking for a branch's id or agent name
// anywhere in the answer, so "이 건은 배포팀에 보내지 않습니다" selected 배포팀 and
// any text that happened to mention two names selected both. The decision is a
// list of ids now, constrained to the ids that exist.
func routerInstruction(candidates []routerCandidate) string {
	var b strings.Builder
	b.WriteString("\n\n# 분기 선택\n")
	b.WriteString("당신은 이 요청을 어느 담당에게 보낼지 결정합니다. 아래 후보 중에서 고르고, 반드시 JSON으로만 답하세요.\n")
	for _, candidate := range candidates {
		b.WriteString("- id: ")
		b.WriteString(candidate.ID)
		if candidate.Name != "" {
			b.WriteString(" (")
			b.WriteString(candidate.Name)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString(`{"branches": ["<id>"], "reason": "<선택 이유>", "handoff": "<선택된 담당에게 전달할 내용>"}`)
	b.WriteString("\nbranches 에는 위 id만 넣으세요. 여러 담당이 필요하면 여러 id를 넣을 수 있습니다.\n")
	return b.String()
}

// routerCandidate is one branch the router may choose.
type routerCandidate struct {
	ID   string
	Name string
}

func routerSchema(candidates []routerCandidate) Schema {
	ids := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	return Schema{
		Name: "agenthub_router_decision",
		Body: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"branches": map[string]any{
					"type":        "array",
					"minItems":    1,
					"items":       map[string]any{"type": "string", "enum": ids},
					"description": "실행할 분기의 id",
				},
				"reason":  map[string]any{"type": "string"},
				"handoff": map[string]any{"type": "string"},
			},
			"required":             []any{"branches", "reason", "handoff"},
			"additionalProperties": false,
		},
	}
}

// routerCandidates lists the steps a router may send work to: everything that is
// not the router itself, in a stable order so the prompt and the schema agree
// between runs.
func routerCandidates(byID map[string]Step, routers map[string]*StepResult) []routerCandidate {
	candidates := make([]routerCandidate, 0, len(byID))
	for id, step := range byID {
		if _, isRouter := routers[id]; isRouter {
			continue
		}
		candidates = append(candidates, routerCandidate{ID: id, Name: step.AgentName})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	return candidates
}

// decodeRouting reads a routing decision and keeps only the branches that exist.
//
// An answer that cannot be read, or that names nothing real, is reported as no
// decision — the caller then runs the whole graph, which is more useful than an
// empty result, and says so on the record rather than looking like a choice.
func decodeRouting(output string, candidates []routerCandidate) (routerDecision, error) {
	allowed := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		allowed[candidate.ID] = true
	}
	var decision routerDecision
	if err := json.Unmarshal([]byte(extractJSONObject(output)), &decision); err != nil {
		return routerDecision{}, fmt.Errorf("분기 결정을 JSON으로 해석하지 못했습니다")
	}
	kept := make([]string, 0, len(decision.Branches))
	seen := map[string]bool{}
	for _, id := range decision.Branches {
		id = strings.TrimSpace(id)
		if allowed[id] && !seen[id] {
			seen[id] = true
			kept = append(kept, id)
		}
	}
	decision.Branches = kept
	if len(kept) == 0 {
		return decision, fmt.Errorf("분기 결정에 실행 가능한 분기가 없습니다")
	}
	return decision, nil
}

// extractJSONObject pulls the object out of a reply that may be wrapped in prose
// or a code fence, which models do whatever the instruction says. A gateway that
// honoured the schema returns the object on its own and this is a no-op.
func extractJSONObject(output string) string {
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end <= start {
		return output
	}
	return output[start : end+1]
}

// completeStructured asks for a schema-constrained answer when the completion
// implementation can, and falls back to asking in prose when it cannot. Either
// way the caller validates what comes back.
func (e *Engine) completeStructured(ctx context.Context, step Step, prompt string, schema Schema) (StructuredResult, error) {
	if structured, ok := e.completion.(StructuredCompleter); ok {
		return structured.CompleteStructured(ctx, step, prompt, schema)
	}
	if reporter, ok := e.completion.(UsageReporter); ok {
		output, usage, err := reporter.CompleteWithUsage(ctx, step, prompt)
		return StructuredResult{Output: output, Usage: usage}, err
	}
	output, err := e.completion.Complete(ctx, step, prompt)
	return StructuredResult{Output: output}, err
}

func isUsageReporter(completion Completion) bool {
	_, ok := completion.(UsageReporter)
	return ok
}

// route turns the router's answers into the set of steps that may run, and the
// record of why. Several entry steps are merged: any branch a router chose runs.
//
// When no router produced a usable decision the whole graph runs, which is more
// useful than an empty result, and the record says so — a routing that could not
// be read must not look like a choice somebody made.
func (e *Engine) route(levelResults map[string]*StepResult, candidates []routerCandidate, byID map[string]Step, outputs map[string]string) (map[string]bool, Routing) {
	record := Routing{Chosen: []string{}}
	selection := map[string]bool{}
	routers := make([]string, 0, len(levelResults))
	for id := range levelResults {
		routers = append(routers, id)
	}
	sort.Strings(routers)

	notes := []string{}
	for _, id := range routers {
		item := levelResults[id]
		record.Step = strings.TrimPrefix(record.Step+" "+id, " ")
		if item.SchemaValidated {
			record.Validated = true
		}
		decision, err := decodeRouting(item.Output, candidates)
		if err != nil {
			notes = append(notes, id+": "+err.Error())
			continue
		}
		for _, branch := range decision.Branches {
			if !selection[branch] {
				selection[branch] = true
				record.Chosen = append(record.Chosen, branch)
			}
		}
		if record.Reason == "" {
			record.Reason = strings.TrimSpace(decision.Reason)
		}
		// The branch is told what to do rather than handed the decision JSON. The
		// raw answer stays on the step record, so the decision is still auditable.
		if handoff := strings.TrimSpace(decision.Handoff); handoff != "" {
			outputs[id] = handoff
		} else if record.Reason != "" {
			outputs[id] = record.Reason
		}
	}
	if len(selection) == 0 {
		record.FellBack = true
		record.Note = strings.Join(notes, "; ")
		for id := range byID {
			selection[id] = true
		}
		record.Chosen = record.Chosen[:0]
		return selection, record
	}
	// The router's own id stays out of the selection on purpose: every branch
	// depends on it, so a router that counted as a permitting dependency would let
	// the whole graph through and undo its own decision.
	sort.Strings(record.Chosen)
	return selection, record
}
