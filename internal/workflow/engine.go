// Package workflow executes a multi-agent DAG.
//
// A step runs its agent's definition — the system prompt bound to that agent's
// model endpoint — against the outputs of the steps it depends on. Execution is
// therefore at the model level rather than inside the agent's runtime sandbox:
// the DAG, the guardrails and the composition are the platform's concern, while
// tool use inside a single step belongs to the runtime adapters and is reached
// through their own browser sessions.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Step is one node of the graph, already resolved against an agent definition.
type Step struct {
	ID           string
	AgentID      string
	AgentName    string
	DependsOn    []string
	SystemPrompt string
	ModelBaseURL string
	ModelName    string
	ModelAPIKey  string
}

// Guardrails bound a run. They mirror the limits validated when the workflow was
// saved, and are enforced again here because a definition can be edited between
// validation and execution.
type Guardrails struct {
	MaxDepth       int
	MaxAgentCalls  int
	MaxParallel    int
	MaxDuration    time.Duration
	MaxOutputRunes int
}

// StepResult records what one node produced, including failures: a partial trace
// is far more useful than a bare error when a long graph stops midway.
type StepResult struct {
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	AgentName  string `json:"agentName"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Level      int    `json:"level"`
	// Token accounting for this step, when the gateway reported it.
	PromptTokens     int `json:"promptTokens,omitempty"`
	CompletionTokens int `json:"completionTokens,omitempty"`
	TotalTokens      int `json:"totalTokens,omitempty"`
}

// Result is the whole run.
type Result struct {
	Mode      string       `json:"mode"`
	Status    string       `json:"status"`
	Output    string       `json:"output"`
	Steps     []StepResult `json:"steps"`
	AgentCall int          `json:"agentCalls"`
	Levels    [][]string   `json:"levels"`
	// TraceID correlates this run with the control-plane request that started it
	// and with every step's log line.
	TraceID     string `json:"traceId"`
	DurationMs  int64  `json:"durationMs"`
	TotalTokens int    `json:"totalTokens"`
	// Consensus is the vote tally, present only for consensus runs.
	Consensus *ConsensusResult `json:"consensus,omitempty"`
	// Supervision is the review record, present only for supervised runs.
	Supervision *SupervisionResult `json:"supervision,omitempty"`
}

// Completion is the single capability a step needs: send a system prompt plus a
// user message to a model and return the reply. It is an interface so the engine
// is testable without a live gateway.
type Completion interface {
	Complete(ctx context.Context, step Step, prompt string) (string, error)
}

// UsageReporter is an optional capability: a Completion that can also report the
// token accounting for the call. Implementations that cannot are simply not
// attributed rather than being forced to invent numbers.
type UsageReporter interface {
	CompleteWithUsage(ctx context.Context, step Step, prompt string) (string, Usage, error)
}

var ErrNoSteps = errors.New("workflow has no steps")

// Engine runs one graph at a time.
type Engine struct {
	completion Completion
}

func New(completion Completion) *Engine { return &Engine{completion: completion} }

// Run executes the graph. It returns a Result even when a step fails so the
// caller can persist the partial trace; err is reserved for failures that stop
// the run from being meaningful at all.
func (e *Engine) Run(ctx context.Context, mode string, steps []Step, guard Guardrails, input string) (Result, error) {
	started := time.Now()
	traceID := TraceIDFromContext(ctx)
	if len(steps) == 0 {
		return Result{}, ErrNoSteps
	}
	// Consensus asks every participant the same question independently, so the
	// graph's edges are ignored: an agent that has already read another's answer
	// is not casting an independent vote. Saved workflows were wired as chains
	// before the mode meant anything, and they must still behave as a consensus.
	if mode == "consensus" {
		steps = independentSteps(steps)
	}
	// The supervising step is the graph's single terminal: everything else feeds
	// it, which is what makes it the one with every answer in front of it.
	supervising := supervisorStep(mode, steps)
	if supervising != nil {
		steps = withSupervisorInstruction(steps, supervising.ID)
		for i := range steps {
			if steps[i].ID == supervising.ID {
				supervising = &steps[i]
			}
		}
	}
	levels, err := topologicalLevels(steps)
	if err != nil {
		return Result{}, err
	}
	if guard.MaxDepth > 0 && len(levels) > guard.MaxDepth {
		return Result{}, fmt.Errorf("graph depth %d exceeds the limit of %d", len(levels), guard.MaxDepth)
	}
	if guard.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, guard.MaxDuration)
		defer cancel()
	}

	byID := map[string]Step{}
	for _, step := range steps {
		byID[step.ID] = step
	}
	result := Result{Mode: mode, Status: "succeeded", Levels: levels, TraceID: traceID}
	outputs := map[string]string{}
	results := map[string]*StepResult{}
	// In router mode the entry step names the branch to follow; selection stays
	// nil until it has answered, and every step outside the chosen branch is
	// skipped rather than executed.
	routing := mode == "router"
	var selection map[string]bool

	calls := 0
	for depth, level := range levels {
		pending := make([]Step, 0, len(level))
		for _, id := range level {
			step := byID[id]
			if routing && selection != nil && !selection[id] && !dependsOnAllowed(step, selection) {
				results[id] = &StepResult{ID: id, AgentID: step.AgentID, AgentName: step.AgentName, Status: "skipped", Skipped: true, Level: depth}
				continue
			}
			pending = append(pending, step)
		}
		if len(pending) == 0 {
			continue
		}
		if guard.MaxAgentCalls > 0 && calls+len(pending) > guard.MaxAgentCalls {
			result.Status = "failed"
			result.Output = fmt.Sprintf("최대 Agent 호출 수(%d)를 초과했습니다.", guard.MaxAgentCalls)
			result.DurationMs = time.Since(started).Milliseconds()
			return finish(result, results, steps, calls), nil
		}
		calls += len(pending)

		levelResults := e.runLevel(ctx, pending, depth, outputs, byID, input, guard)
		failed := false
		for id, item := range levelResults {
			results[id] = item
			if item.Status == "succeeded" {
				outputs[id] = item.Output
			} else {
				failed = true
			}
		}
		if failed {
			result.Status = "failed"
			result.DurationMs = time.Since(started).Milliseconds()
			return finish(result, results, steps, calls), nil
		}
		if routing && selection == nil {
			selection = routeSelection(levelResults, byID)
			if len(selection) == 0 {
				// The router named nothing recognisable; running the whole graph is
				// more useful than silently producing an empty result.
				selection = map[string]bool{}
				for id := range byID {
					selection[id] = true
				}
			}
		} else if selection != nil {
			// A step that ran is itself a valid dependency for the next level.
			for id := range levelResults {
				selection[id] = true
			}
		}
	}

	if mode == "consensus" {
		tally := tallyConsensus(steps, results, outputs)
		result.Consensus = &tally
		result.Output = composeConsensus(tally, steps, results)
	} else if supervising != nil && results[supervising.ID] != nil && results[supervising.ID].Status == "succeeded" {
		record := e.supervise(ctx, *supervising, steps, byID, outputs, results, input, guard, &calls)
		result.Supervision = &record
		result.Output = composeSupervised(record, *supervising, steps, results, outputs)
	} else {
		result.Output = compose(mode, steps, results, outputs)
	}
	result.DurationMs = time.Since(started).Milliseconds()
	return finish(result, results, steps, calls), nil
}

// supervisorStep picks the step that reviews the others, or nil when the graph
// has no single reviewer to give the job to.
//
// Supervision needs one step that everything else feeds: with two terminals
// neither has the whole picture, and picking one of them would silently promote
// an agent the operator never nominated.
func supervisorStep(mode string, steps []Step) *Step {
	if mode != "supervisor" && mode != "reviewer" {
		return nil
	}
	if len(steps) < 2 {
		return nil
	}
	terminals := terminalSteps(steps)
	if len(terminals) != 1 {
		return nil
	}
	for i := range steps {
		if steps[i].ID == terminals[0] {
			return &steps[i]
		}
	}
	return nil
}

// withSupervisorInstruction tells the reviewing step how to ask for changes.
func withSupervisorInstruction(steps []Step, supervisorID string) []Step {
	updated := make([]Step, 0, len(steps))
	for _, step := range steps {
		if step.ID == supervisorID {
			step.SystemPrompt += supervisorInstruction
		}
		updated = append(updated, step)
	}
	return updated
}

// independentSteps strips the dependencies and appends the voting instruction,
// leaving every participant answering the original request alone.
func independentSteps(steps []Step) []Step {
	independent := make([]Step, 0, len(steps))
	for _, step := range steps {
		step.DependsOn = nil
		step.SystemPrompt += consensusInstruction
		independent = append(independent, step)
	}
	return independent
}

// runLevel executes one level, bounded by the parallelism guardrail.
func (e *Engine) runLevel(ctx context.Context, level []Step, depth int, outputs map[string]string, byID map[string]Step, input string, guard Guardrails) map[string]*StepResult {
	limit := guard.MaxParallel
	if limit <= 0 || limit > len(level) {
		limit = len(level)
	}
	results := make(map[string]*StepResult, len(level))
	var mu sync.Mutex
	var wait sync.WaitGroup
	slots := make(chan struct{}, limit)

	for _, step := range level {
		wait.Add(1)
		go func(step Step) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			started := time.Now()
			item := &StepResult{ID: step.ID, AgentID: step.AgentID, AgentName: step.AgentName, Level: depth}
			mu.Lock()
			prompt := buildPrompt(step, byID, outputs, input)
			mu.Unlock()

			var output string
			var usage Usage
			var err error
			if reporter, ok := e.completion.(UsageReporter); ok {
				output, usage, err = reporter.CompleteWithUsage(ctx, step, prompt)
			} else {
				output, err = e.completion.Complete(ctx, step, prompt)
			}
			item.DurationMs = time.Since(started).Milliseconds()
			item.PromptTokens, item.CompletionTokens, item.TotalTokens = usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens
			if err != nil {
				item.Status = "failed"
				item.Error = err.Error()
			} else {
				item.Status = "succeeded"
				item.Output = truncate(output, guard.MaxOutputRunes)
			}
			mu.Lock()
			results[step.ID] = item
			mu.Unlock()
		}(step)
	}
	wait.Wait()
	return results
}

// buildPrompt assembles the user message: the run input plus each dependency's
// output, labelled by the agent that produced it so the model can attribute it.
func buildPrompt(step Step, byID map[string]Step, outputs map[string]string, input string) string {
	var b strings.Builder
	if strings.TrimSpace(input) != "" {
		b.WriteString("# 요청\n")
		b.WriteString(input)
		b.WriteString("\n")
	}
	dependencies := append([]string(nil), step.DependsOn...)
	sort.Strings(dependencies)
	for _, id := range dependencies {
		output, ok := outputs[id]
		if !ok || strings.TrimSpace(output) == "" {
			continue
		}
		name := id
		if upstream, found := byID[id]; found && upstream.AgentName != "" {
			name = upstream.AgentName
		}
		b.WriteString("\n# 이전 단계 결과 (")
		b.WriteString(name)
		b.WriteString(")\n")
		b.WriteString(output)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "작업을 시작하세요."
	}
	return b.String()
}

// compose renders the run's final answer according to the collaboration mode.
func compose(mode string, steps []Step, results map[string]*StepResult, outputs map[string]string) string {
	terminals := terminalSteps(steps)
	switch mode {
	case "sequential", "router":
		// A chain's answer is its last successful step.
		for i := len(terminals) - 1; i >= 0; i-- {
			if output, ok := outputs[terminals[i]]; ok && strings.TrimSpace(output) != "" {
				return output
			}
		}
	case "supervisor", "reviewer":
		// These modes are only meaningful as an aggregate: the reviewing or
		// deciding step is the terminal node, and its input already carried the
		// upstream answers, so the aggregate is what the terminal produced plus
		// the contributions it judged.
		var b strings.Builder
		for _, id := range terminals {
			if output, ok := outputs[id]; ok {
				b.WriteString(output)
				b.WriteString("\n")
			}
		}
		if b.Len() > 0 {
			return strings.TrimSpace(b.String())
		}
	}
	// parallel, and any mode whose terminals produced nothing: report every
	// contribution, attributed.
	var b strings.Builder
	for _, step := range steps {
		item, ok := results[step.ID]
		if !ok || item.Status != "succeeded" {
			continue
		}
		b.WriteString("## ")
		if step.AgentName != "" {
			b.WriteString(step.AgentName)
		} else {
			b.WriteString(step.ID)
		}
		b.WriteString("\n")
		b.WriteString(item.Output)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// routeSelection reads the router's answer and returns the downstream steps it
// named. The router's own id is deliberately excluded: every branch depends on
// it, so treating it as a permitting dependency would let the whole graph
// through and defeat the routing.
func routeSelection(levelResults map[string]*StepResult, byID map[string]Step) map[string]bool {
	selected := map[string]bool{}
	for _, item := range levelResults {
		answer := strings.ToLower(item.Output)
		for id, step := range byID {
			if _, isRouter := levelResults[id]; isRouter {
				continue
			}
			if strings.Contains(answer, strings.ToLower(id)) || (step.AgentName != "" && strings.Contains(answer, strings.ToLower(step.AgentName))) {
				selected[id] = true
			}
		}
	}
	return selected
}

func dependsOnAllowed(step Step, allowed map[string]bool) bool {
	for _, id := range step.DependsOn {
		if allowed[id] {
			return true
		}
	}
	return false
}

func terminalSteps(steps []Step) []string {
	referenced := map[string]bool{}
	for _, step := range steps {
		for _, id := range step.DependsOn {
			referenced[id] = true
		}
	}
	terminals := []string{}
	for _, step := range steps {
		if !referenced[step.ID] {
			terminals = append(terminals, step.ID)
		}
	}
	sort.Strings(terminals)
	return terminals
}

func finish(result Result, results map[string]*StepResult, steps []Step, calls int) Result {
	result.AgentCall = calls
	ordered := make([]StepResult, 0, len(results))
	for _, step := range steps {
		if item, ok := results[step.ID]; ok {
			ordered = append(ordered, *item)
			result.TotalTokens += item.TotalTokens
		}
	}
	result.Steps = ordered
	return result
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "\n…(생략됨)"
}

// topologicalLevels groups steps into execution levels, rejecting cycles,
// unknown dependencies and duplicate ids.
func topologicalLevels(steps []Step) ([][]string, error) {
	remaining := map[string]Step{}
	for _, step := range steps {
		if step.ID == "" {
			return nil, errors.New("every step needs an id")
		}
		if _, duplicate := remaining[step.ID]; duplicate {
			return nil, fmt.Errorf("duplicate step id %q", step.ID)
		}
		remaining[step.ID] = step
	}
	for _, step := range steps {
		for _, id := range step.DependsOn {
			if _, known := remaining[id]; !known {
				return nil, fmt.Errorf("step %q depends on unknown step %q", step.ID, id)
			}
		}
	}
	done := map[string]bool{}
	levels := [][]string{}
	for len(remaining) > 0 {
		level := []string{}
		for id, step := range remaining {
			ready := true
			for _, dependency := range step.DependsOn {
				if !done[dependency] {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, id)
			}
		}
		if len(level) == 0 {
			return nil, errors.New("workflow graph contains a cycle")
		}
		sort.Strings(level)
		for _, id := range level {
			delete(remaining, id)
			done[id] = true
		}
		levels = append(levels, level)
	}
	return levels, nil
}
