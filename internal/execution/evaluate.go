package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/hkjang/AgentHub/internal/store"
	"github.com/hkjang/AgentHub/internal/telemetry"
	"github.com/hkjang/AgentHub/internal/workflow"
)

// Verdict is what the evaluator concluded, and is stored on the run so a
// completed task can be defended later.
type Verdict struct {
	Strategy string   `json:"strategy"`
	Passed   bool     `json:"passed"`
	Reason   string   `json:"reason"`
	Met      []string `json:"met,omitempty"`
	Unmet    []string `json:"unmet,omitempty"`
	// Validated is set when the gateway accepted the schema rather than refusing
	// it. It says how the answer was asked for, not that the gateway enforced it —
	// which is why the verdict is validated against the configured criteria anyway.
	Validated bool `json:"validated,omitempty"`
}

// evaluate decides whether the goal was actually met and saves whatever the run
// produced.
//
// The agent claiming "완료했습니다" is evidence, not proof. Which strategy is
// applied is the agent's own configuration:
//
//	agent     — trust the agent's declaration
//	rule      — every success criterion must appear in the transcript
//	judge     — a separate model call assesses the transcript against the criteria
//	composite — rule first, then judge; both must pass
func (o *Orchestrator) evaluate(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, goal store.AgentGoal, model resolvedModel, transcript []string) Outcome {
	result := strings.TrimSpace(strings.Join(transcript, "\n\n"))
	o.saveArtifacts(ctx, run, task, agent, transcript)

	verdict := o.judgeCompletion(ctx, run, goal, model, transcript)
	encoded, _ := json.Marshal(verdict)
	run.Completion = encoded

	o.recordVerdict(ctx, run, verdict)
	if !verdict.Passed {
		return Outcome{Status: store.TaskFailed, Result: result, Failure: verdict.Reason}
	}
	return Outcome{Status: store.TaskCompleted, Result: result}
}

func (o *Orchestrator) judgeCompletion(ctx context.Context, run *store.AgentRun, goal store.AgentGoal, model resolvedModel, transcript []string) Verdict {
	// With nothing to check against, only the agent's own declaration is available
	// whatever the configured strategy claims.
	if len(goal.SuccessCriteria) == 0 {
		return Verdict{Strategy: "agent", Passed: true, Reason: "완료 조건이 정의되어 있지 않아 Agent 선언을 그대로 사용했습니다."}
	}
	switch goal.CompletionStrategy {
	case "agent":
		return Verdict{Strategy: "agent", Passed: true, Reason: "Agent가 완료를 선언했습니다."}
	case "rule":
		return ruleVerdict(goal, transcript)
	case "judge":
		return o.judgeVerdict(ctx, run, goal, model, transcript)
	case "composite":
		rule := ruleVerdict(goal, transcript)
		if !rule.Passed {
			rule.Strategy = "composite"
			return rule
		}
		judge := o.judgeVerdict(ctx, run, goal, model, transcript)
		judge.Strategy = "composite"
		judge.Met, judge.Unmet = rule.Met, rule.Unmet
		return judge
	default:
		return Verdict{Strategy: goal.CompletionStrategy, Passed: true, Reason: "알 수 없는 완료 판정 방식이라 Agent 선언을 사용했습니다."}
	}
}

// ruleVerdict checks each success criterion against the transcript. The match is
// deliberately shallow — it proves the agent addressed the criterion, not that
// it did so correctly, which is what the judge strategy is for.
func ruleVerdict(goal store.AgentGoal, transcript []string) Verdict {
	haystack := strings.ToLower(strings.Join(transcript, "\n"))
	verdict := Verdict{Strategy: "rule", Met: []string{}, Unmet: []string{}}
	for _, criterion := range goal.SuccessCriteria {
		if criterionMentioned(haystack, criterion) {
			verdict.Met = append(verdict.Met, criterion)
		} else {
			verdict.Unmet = append(verdict.Unmet, criterion)
		}
	}
	verdict.Passed = len(verdict.Unmet) == 0
	if verdict.Passed {
		verdict.Reason = "모든 완료 조건이 실행 기록에서 확인되었습니다."
	} else {
		verdict.Reason = "확인되지 않은 완료 조건: " + strings.Join(verdict.Unmet, ", ")
	}
	return verdict
}

// criterionMentioned looks for the criterion's significant words rather than the
// whole phrase, because an agent restates a requirement in its own words.
func criterionMentioned(haystack, criterion string) bool {
	words := significantWords(criterion)
	if len(words) == 0 {
		return true
	}
	matched := 0
	for _, word := range words {
		if strings.Contains(haystack, word) {
			matched++
		}
	}
	// A clear majority of the meaningful words has to appear; requiring all of
	// them rejects any legitimate paraphrase.
	return matched*2 > len(words)
}

func significantWords(criterion string) []string {
	fields := strings.FieldsFunc(strings.ToLower(criterion), func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == '·' || r == '/' || r == '(' || r == ')'
	})
	words := make([]string, 0, len(fields))
	for _, field := range fields {
		// Single characters and particles carry no signal.
		if len([]rune(field)) > 1 {
			words = append(words, field)
		}
	}
	return words
}

// judgeVerdict asks the model to assess the transcript against the criteria.
//
// The answer is asked for as schema-constrained JSON, and the unmet criteria are
// an enum of the criteria that actually exist: a verdict a task's completion rests
// on should not be read out of prose, and a judge should not be able to fail a
// task against a requirement nobody wrote. A gateway that cannot constrain the
// answer is asked in prose instead and the verdict says so.
func (o *Orchestrator) judgeVerdict(ctx context.Context, run *store.AgentRun, goal store.AgentGoal, model resolvedModel, transcript []string) Verdict {
	step := workflow.Step{
		ID: "judge", AgentName: "Completion Evaluator",
		SystemPrompt: "당신은 엄격한 평가자입니다. 실행 기록이 완료 조건을 실제로 충족했는지 판정하고, 반드시 " +
			`{"passed": true|false, "reason": "...", "unmet": ["충족되지 않은 완료 조건"]} 형식의 JSON만 출력하세요. ` +
			"unmet 에는 주어진 완료 조건 문장만 그대로 넣고, 새로운 조건을 만들지 마세요. " +
			"Agent가 완료했다고 주장하더라도 근거가 없으면 passed는 false입니다.",
		ModelBaseURL: model.BaseURL, ModelName: model.ModelName, ModelAPIKey: model.APIKey,
	}
	var b strings.Builder
	b.WriteString("# 완료 조건\n")
	for _, criterion := range goal.SuccessCriteria {
		b.WriteString("- ")
		b.WriteString(criterion)
		b.WriteString("\n")
	}
	b.WriteString("\n# 실행 기록\n")
	b.WriteString(strings.Join(transcript, "\n\n"))

	startedAt := time.Now()
	judgeCtx, judgeSpan := telemetry.Start(ctx, "task.evaluate",
		attribute.String("agenthub.completion.strategy", goal.CompletionStrategy),
		attribute.Int("agenthub.completion.criteria", len(goal.SuccessCriteria)))
	structured, err := o.completeStructured(judgeCtx, step, b.String(), verdictSchema(goal.SuccessCriteria))
	telemetry.Fail(judgeSpan, err)
	judgeSpan.SetAttributes(attribute.Int("agenthub.tokens.total", structured.Usage.TotalTokens))
	judgeSpan.End()
	output, usage := structured.Output, structured.Usage
	run.TotalTokens += usage.TotalTokens
	if _, storeErr := o.store.AppendRunStep(recordStepContext(ctx), store.AgentRunStep{
		RunID: run.ID, Sequence: run.StepCount + 1, Type: "completion", Title: "완료 판정",
		Input: b.String(), Output: output, Status: stepStatus(err), Error: errorText(err),
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens,
		DurationMs: time.Since(startedAt).Milliseconds(),
	}); storeErr != nil {
		o.logger.Error("completion step could not be recorded", "run", run.ID, "error", storeErr)
	}
	run.StepCount++
	if err != nil {
		// A judge that cannot be reached must not silently pass the task.
		return Verdict{Strategy: "judge", Passed: false, Reason: "완료 판정 모델을 호출하지 못했습니다: " + err.Error()}
	}

	var decoded struct {
		Passed bool     `json:"passed"`
		Reason string   `json:"reason"`
		Unmet  []string `json:"unmet"`
	}
	if err := json.Unmarshal([]byte(extractJSON(output)), &decoded); err != nil {
		// A verdict that cannot be read is not a pass. The task fails with the
		// judge's own words attached, so the failure is diagnosable.
		return Verdict{Strategy: "judge", Passed: false, Validated: structured.Validated,
			Reason: "완료 판정 결과를 해석하지 못했습니다: " + firstLine(output)}
	}
	unmet, invented := knownCriteria(goal.SuccessCriteria, decoded.Unmet)
	verdict := Verdict{Strategy: "judge", Passed: decoded.Passed, Reason: decoded.Reason, Unmet: unmet, Validated: structured.Validated}
	if verdict.Reason == "" {
		verdict.Reason = fmt.Sprintf("완료 판정 결과: %v", decoded.Passed)
	}
	if len(invented) > 0 {
		// A judge that fails a task against a requirement nobody wrote is reporting
		// its own opinion as a criterion. The verdict stands, but it says so.
		o.logger.Warn("judge named criteria that were not configured", "run", run.ID, "criteria", invented)
		verdict.Reason += " (설정되지 않은 조건 언급: " + strings.Join(invented, ", ") + ")"
	}
	return verdict
}

// verdictSchema constrains the judge's answer. The unmet list is an enum of the
// criteria that exist, so the answer can only be about them.
func verdictSchema(criteria []string) workflow.Schema {
	allowed := make([]any, 0, len(criteria))
	for _, criterion := range criteria {
		allowed = append(allowed, criterion)
	}
	unmet := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	if len(allowed) > 0 {
		unmet["items"] = map[string]any{"type": "string", "enum": allowed}
	}
	return workflow.Schema{
		Name: "agenthub_completion_verdict",
		Body: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"passed": map[string]any{"type": "boolean"},
				"reason": map[string]any{"type": "string"},
				"unmet":  unmet,
			},
			"required":             []any{"passed", "reason", "unmet"},
			"additionalProperties": false,
		},
	}
}

// knownCriteria splits what the judge named into the criteria that were actually
// configured and the ones it made up.
func knownCriteria(criteria, named []string) (known, invented []string) {
	exists := make(map[string]bool, len(criteria))
	for _, criterion := range criteria {
		exists[strings.TrimSpace(criterion)] = true
	}
	for _, item := range named {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if exists[item] {
			known = append(known, item)
		} else {
			invented = append(invented, item)
		}
	}
	return known, invented
}

// extractJSON pulls the object out of a reply that may be wrapped in prose or a
// code fence, which models do regardless of instructions.
func extractJSON(output string) string {
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start < 0 || end <= start {
		return output
	}
	return output[start : end+1]
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(value), "\n")
	if len([]rune(line)) > 200 {
		return string([]rune(line)[:200])
	}
	return line
}

func stepStatus(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (o *Orchestrator) recordVerdict(ctx context.Context, run *store.AgentRun, verdict Verdict) {
	o.event(ctx, *run, "completion.evaluated", verdict.Reason, map[string]any{
		"strategy": verdict.Strategy, "passed": verdict.Passed, "unmet": verdict.Unmet,
	})
}

// saveArtifacts persists whatever the agent fenced as a deliverable. A failure to
// store one is reported but never fails the run: the work itself was still done.
func (o *Orchestrator) saveArtifacts(ctx context.Context, run *store.AgentRun, task store.AgentTask, agent store.Agent, transcript []string) {
	for _, entry := range transcript {
		for _, artifact := range extractArtifacts(entry, taskGiven(task)) {
			artifact.RunID, artifact.TaskID, artifact.AgentID, artifact.OwnerID = run.ID, task.ID, agent.ID, task.OwnerID
			saved, err := o.store.CreateArtifact(ctx, artifact)
			if err != nil {
				o.logger.Warn("artifact could not be stored", "run", run.ID, "name", artifact.Name, "error", err)
				continue
			}
			o.artifactSaved(ctx, *run, task, agent, saved, nil)
		}
	}
}

// artifactSaved announces one stored artifact in both of the places it has to
// appear, because there are two and only one of them is obvious.
//
// The run event is the artifact showing up in this run's own timeline. The
// platform event is the one a trigger subscribes to — "산출물 생성" in the console —
// and it is what lets one agent's output start another agent's work. An artifact
// that records only the first is visible to whoever opens the run and invisible
// to everything the operator set up to react to it.
//
// This was written as a function because it had already been got wrong once: the
// pictures an ACP agent takes were stored and shown in the run, and every
// artifact.created trigger stayed silent for them — screenshots being, by some
// distance, the artifact people most want routed somewhere.
func (o *Orchestrator) artifactSaved(ctx context.Context, run store.AgentRun, task store.AgentTask, agent store.Agent, saved store.AgentArtifact, extra map[string]any) {
	details := map[string]any{"artifactId": saved.ID, "type": saved.Type, "sizeBytes": saved.SizeBytes}
	for key, value := range extra {
		details[key] = value
	}
	o.event(ctx, run, "artifact.created", saved.Name, details)
	payload, _ := json.Marshal(map[string]any{"name": saved.Name, "type": saved.Type, "agentId": agent.ID, "taskId": task.ID})
	if err := o.store.PublishEvent(ctx, store.PlatformEvent{
		Type: store.EventArtifactCreated, OwnerID: task.OwnerID,
		SubjectType: "artifact", SubjectID: saved.ID, Payload: payload, CauseTriggerID: task.TriggerID,
	}); err != nil {
		o.logger.Warn("artifact event could not be published", "run", run.ID, "artifact", saved.ID, "error", err)
	}
}
