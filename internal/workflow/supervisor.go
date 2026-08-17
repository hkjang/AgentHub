package workflow

import (
	"context"
	"fmt"
	"strings"
)

// Supervisor mode.
//
// The mode ran the graph like any other and concatenated the terminal step's
// answer, which made the "supervisor" a last speaker rather than a supervisor:
// it could describe a problem with a specialist's work but had no way to have it
// fixed, and nothing recorded whether it had approved anything.
//
// The supervisor now reviews. It sees the specialists' answers, and either
// approves or names the ones that need another pass with what to change. Those
// specialists run again with that feedback and the supervisor reviews the new
// answers. Rounds are bounded, because a supervisor and a specialist that
// disagree will disagree indefinitely, and an unbounded loop spends a model
// budget discovering that.

// maxRevisionRounds bounds the review loop. Two rounds is enough for the case
// this exists for — an answer that missed a requirement — while a genuine
// disagreement surfaces as an unapproved result instead of running forever.
const maxRevisionRounds = 2

// supervisorInstruction is appended to the supervising step's system prompt.
const supervisorInstruction = "\n\n당신은 다른 에이전트들의 결과를 검토하는 감독자입니다. " +
	"결과가 충분하면 마지막 줄에 `APPROVE` 라고만 쓰세요. " +
	"보완이 필요하면 보완이 필요한 에이전트마다 `REVISE: <에이전트 이름> — <무엇을 어떻게 고칠지>` 형식으로 한 줄씩 쓰세요. " +
	"고칠 점이 없는 에이전트는 적지 마세요."

// revisionRequest is one instruction the supervisor gave a specialist.
type revisionRequest struct {
	// StepID is the specialist being asked to revise.
	StepID  string `json:"stepId"`
	Agent   string `json:"agent"`
	Request string `json:"request"`
}

// SupervisionRound records one review pass.
type SupervisionRound struct {
	Round     int               `json:"round"`
	Approved  bool              `json:"approved"`
	Revisions []revisionRequest `json:"revisions"`
}

// SupervisionResult is the review record kept on the run, so an answer a
// supervisor signed off on can be told apart from one it merely spoke last on.
type SupervisionResult struct {
	Supervisor string             `json:"supervisor"`
	Approved   bool               `json:"approved"`
	Rounds     []SupervisionRound `json:"rounds"`
	// Exhausted marks a review that ran out of rounds still asking for changes.
	Exhausted bool `json:"exhausted"`
}

// parseSupervision reads a supervisor's verdict.
//
// Approval is only taken from a line that is the word on its own: a review that
// says "이대로는 APPROVE 할 수 없습니다" is the opposite of an approval, and
// substring matching would read it as one.
func parseSupervision(output string, byName map[string]string) (approved bool, revisions []revisionRequest) {
	for _, raw := range strings.Split(output, "\n") {
		// Models write verdicts as bullets and headings as often as plain lines,
		// so the decoration is stripped before the line is read.
		line := trimDecoration(raw, "*_`#-•> ")
		if strings.EqualFold(line, "APPROVE") {
			approved = true
			continue
		}
		rest, found := cutPrefixFold(line, "REVISE:")
		if !found {
			continue
		}
		name, request := splitRevision(rest)
		if name == "" {
			continue
		}
		stepID, known := byName[strings.ToLower(name)]
		if !known {
			// A name nobody answers to cannot be actioned, and guessing which
			// specialist was meant would send the wrong one back to work.
			continue
		}
		revisions = append(revisions, revisionRequest{StepID: stepID, Agent: name, Request: strings.TrimSpace(request)})
	}
	// A request for changes is a request for changes, whatever else was said.
	if len(revisions) > 0 {
		approved = false
	}
	return approved, revisions
}

func cutPrefixFold(line, prefix string) (string, bool) {
	if len(line) < len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return "", false
	}
	return line[len(prefix):], true
}

// splitRevision separates the agent from the instruction. Models reach for an
// em dash, a hyphen or a colon, so all three are accepted.
func splitRevision(rest string) (name, request string) {
	rest = strings.TrimSpace(rest)
	for _, separator := range []string{"—", " - ", " – ", ":"} {
		if name, request, found := strings.Cut(rest, separator); found {
			return trimDecoration(name, "*_`"), request
		}
	}
	return trimDecoration(rest, "*_`"), ""
}

// trimDecoration removes surrounding whitespace and markdown. Both passes of
// whitespace matter: the markdown sits inside the spaces on one side and
// outside them on the other, depending on how the model wrote the line.
func trimDecoration(value, cutset string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(value), cutset))
}

// supervise runs the review loop after the graph's first pass.
//
// It returns the record and updates outputs and results in place, so the run's
// trace shows the revised answers rather than the ones that were rejected.
func (e *Engine) supervise(ctx context.Context, supervisor Step, steps []Step, byID map[string]Step,
	outputs map[string]string, results map[string]*StepResult, input string, guard Guardrails, calls *int) SupervisionResult {

	record := SupervisionResult{Supervisor: displayName(supervisor), Rounds: []SupervisionRound{}}
	byName := map[string]string{}
	for _, step := range steps {
		if step.ID == supervisor.ID {
			continue
		}
		byName[strings.ToLower(displayName(step))] = step.ID
		byName[strings.ToLower(step.ID)] = step.ID
	}

	for round := 1; round <= maxRevisionRounds; round++ {
		approved, revisions := parseSupervision(outputs[supervisor.ID], byName)
		record.Rounds = append(record.Rounds, SupervisionRound{Round: round, Approved: approved, Revisions: revisions})
		if approved || len(revisions) == 0 {
			// No approval and nothing to fix means the supervisor said neither;
			// treating silence as approval would put its name on work it never
			// signed off.
			record.Approved = approved
			return record
		}
		if guard.MaxAgentCalls > 0 && *calls+len(revisions)+1 > guard.MaxAgentCalls {
			record.Exhausted = true
			return record
		}

		// Re-run only the specialists that were named, each with the feedback
		// aimed at it.
		pending := make([]Step, 0, len(revisions))
		feedback := map[string]string{}
		for _, revision := range revisions {
			step := byID[revision.StepID]
			pending = append(pending, step)
			feedback[step.ID] = revision.Request
		}
		*calls += len(pending)
		revised := e.runLevel(ctx, pending, len(record.Rounds), outputs, byID, reviseInput(input, feedback), guard)
		for id, item := range revised {
			if item.Status != "succeeded" {
				// A specialist that cannot answer again leaves the previous
				// answer standing; the supervisor sees it unchanged and can end
				// the round on its own terms.
				continue
			}
			item.Level = results[id].Level
			results[id] = item
			outputs[id] = item.Output
		}

		// The supervisor reviews the new answers.
		*calls++
		again := e.runLevel(ctx, []Step{supervisor}, len(record.Rounds), outputs, byID, input, guard)
		item, ok := again[supervisor.ID]
		if !ok || item.Status != "succeeded" {
			record.Exhausted = true
			return record
		}
		item.Level = results[supervisor.ID].Level
		results[supervisor.ID] = item
		outputs[supervisor.ID] = item.Output
	}

	// Out of rounds while still asking for changes.
	approved, revisions := parseSupervision(outputs[supervisor.ID], byName)
	record.Rounds = append(record.Rounds, SupervisionRound{Round: maxRevisionRounds + 1, Approved: approved, Revisions: revisions})
	record.Approved = approved
	record.Exhausted = !approved
	return record
}

// reviseInput carries the supervisor's instructions into the specialists' next
// prompt. Each sees only what was addressed to it.
func reviseInput(input string, feedback map[string]string) string {
	var b strings.Builder
	b.WriteString(input)
	b.WriteString("\n\n# 감독자의 보완 요청\n")
	// Sorted so the same review produces the same prompt on a rerun.
	keys := make([]string, 0, len(feedback))
	for key := range feedback {
		keys = append(keys, key)
	}
	sortStringsAsc(keys)
	for _, key := range keys {
		if request := feedback[key]; request != "" {
			b.WriteString("- ")
			b.WriteString(request)
			b.WriteString("\n")
		}
	}
	b.WriteString("\n위 요청을 반영해 답변을 다시 작성하세요.")
	return b.String()
}

// composeSupervised renders the verdict above the work it judged.
func composeSupervised(record SupervisionResult, supervisor Step, steps []Step, results map[string]*StepResult, outputs map[string]string) string {
	var b strings.Builder
	switch {
	case record.Approved:
		b.WriteString(fmt.Sprintf("## 감독 결과: 승인 (%s)\n", record.Supervisor))
	case record.Exhausted:
		b.WriteString(fmt.Sprintf("## 감독 결과: 보완 요청이 남은 채 종료 (%s)\n", record.Supervisor))
	default:
		b.WriteString(fmt.Sprintf("## 감독 결과: 승인 표시 없음 (%s)\n", record.Supervisor))
	}
	revised := 0
	for _, round := range record.Rounds {
		revised += len(round.Revisions)
	}
	if revised > 0 {
		b.WriteString(fmt.Sprintf("보완 요청 %d건, 검토 %d회\n", revised, len(record.Rounds)))
		for _, round := range record.Rounds {
			for _, revision := range round.Revisions {
				b.WriteString(fmt.Sprintf("- %d차: %s — %s\n", round.Round, revision.Agent, revision.Request))
			}
		}
	}
	b.WriteString("\n")
	b.WriteString(outputs[supervisor.ID])
	b.WriteString("\n")
	for _, step := range steps {
		if step.ID == supervisor.ID {
			continue
		}
		item, ok := results[step.ID]
		if !ok || item.Status != "succeeded" {
			continue
		}
		b.WriteString("\n## ")
		b.WriteString(displayName(step))
		b.WriteString("\n")
		b.WriteString(item.Output)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func displayName(step Step) string {
	if strings.TrimSpace(step.AgentName) != "" {
		return step.AgentName
	}
	return step.ID
}

// sortStringsAsc keeps rendered feedback stable without pulling in a dependency
// the package does not otherwise need here.
func sortStringsAsc(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
