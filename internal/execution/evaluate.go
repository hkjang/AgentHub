package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
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

// judgeVerdict asks the model to assess the transcript against the criteria. The
// judge is told to answer in JSON so the outcome is machine-readable rather than
// something to grep prose for.
func (o *Orchestrator) judgeVerdict(ctx context.Context, run *store.AgentRun, goal store.AgentGoal, model resolvedModel, transcript []string) Verdict {
	step := workflow.Step{
		ID: "judge", AgentName: "Completion Evaluator",
		SystemPrompt: "당신은 엄격한 평가자입니다. 실행 기록이 완료 조건을 실제로 충족했는지 판정하고, 반드시 " +
			`{"passed": true|false, "reason": "...", "unmet": ["..."]} 형식의 JSON만 출력하세요. ` +
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
	output, usage, err := o.complete(ctx, step, b.String())
	run.TotalTokens += usage.TotalTokens
	if _, storeErr := o.store.AppendRunStep(ctx, store.AgentRunStep{
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
		return Verdict{Strategy: "judge", Passed: false, Reason: "완료 판정 결과를 해석하지 못했습니다: " + firstLine(output)}
	}
	verdict := Verdict{Strategy: "judge", Passed: decoded.Passed, Reason: decoded.Reason, Unmet: decoded.Unmet}
	if verdict.Reason == "" {
		verdict.Reason = fmt.Sprintf("완료 판정 결과: %v", decoded.Passed)
	}
	return verdict
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
		for _, artifact := range extractArtifacts(entry) {
			artifact.RunID, artifact.TaskID, artifact.AgentID, artifact.OwnerID = run.ID, task.ID, agent.ID, task.OwnerID
			saved, err := o.store.CreateArtifact(ctx, artifact)
			if err != nil {
				o.logger.Warn("artifact could not be stored", "run", run.ID, "name", artifact.Name, "error", err)
				continue
			}
			o.event(ctx, *run, "artifact.created", saved.Name, map[string]any{"artifactId": saved.ID, "type": saved.Type, "sizeBytes": saved.SizeBytes})
			// A run event is only visible inside this run; the platform event is
			// what another agent can subscribe to.
			payload, _ := json.Marshal(map[string]any{"name": saved.Name, "type": saved.Type, "agentId": agent.ID, "taskId": task.ID})
			if err := o.store.PublishEvent(ctx, store.PlatformEvent{
				Type: store.EventArtifactCreated, OwnerID: task.OwnerID,
				SubjectType: "artifact", SubjectID: saved.ID, Payload: payload, CauseTriggerID: task.TriggerID,
			}); err != nil {
				o.logger.Warn("artifact event could not be published", "run", run.ID, "artifact", saved.ID, "error", err)
			}
		}
	}
}
