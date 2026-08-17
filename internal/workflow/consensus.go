package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// Consensus mode.
//
// The mode was selectable long before it meant anything: a consensus workflow
// ran as a chain and concatenated whatever the last step said, which is neither
// a consensus nor a disagreement — just one agent's answer with company. It now
// does what the label promises. Every participant answers the same question
// without seeing the others, each ends with an explicit vote, and the engine
// counts the votes rather than asking a model to summarise them, so the outcome
// is reproducible and an operator can check the arithmetic.

// voteMarker is the line a participant ends with. An explicit marker beats
// comparing whole answers: two agents that agree rarely phrase it identically,
// and a similarity score would make the verdict depend on how it was tuned.
const voteMarker = "VOTE:"

// consensusInstruction is appended to a participant's system prompt.
const consensusInstruction = "\n\n여러 에이전트가 같은 질문에 독립적으로 답한 뒤 표결로 결론을 정합니다. " +
	"근거를 먼저 설명하고, 마지막 줄은 반드시 `VOTE: <결론>` 형식으로 한 줄만 쓰세요. " +
	"결론은 다른 에이전트와 비교할 수 있도록 짧고 단정적으로 적으세요."

// Vote is one participant's position.
type Vote struct {
	StepID    string `json:"stepId"`
	AgentID   string `json:"agentId"`
	AgentName string `json:"agentName"`
	// Choice is the participant's own wording; Normalised is what was compared.
	Choice     string `json:"choice"`
	Normalised string `json:"normalised"`
	// Abstained marks a participant that answered without casting a vote.
	Abstained bool `json:"abstained"`
}

// ConsensusResult is the tally. It is stored on the run so a decision made by
// several agents can be defended later with the individual positions intact.
type ConsensusResult struct {
	Winner string `json:"winner"`
	// Votes for the winner, over the participants that actually voted.
	Agreed    int  `json:"agreed"`
	Total     int  `json:"total"`
	Unanimous bool `json:"unanimous"`
	// Tie marks a split with no majority: there is a most-voted answer, but
	// another answer has as many votes.
	Tie   bool   `json:"tie"`
	Votes []Vote `json:"votes"`
}

// extractVote reads the last VOTE: line of an answer.
//
// The last one wins because a model that restates the format, or corrects
// itself, leaves earlier candidates behind; the final line is its conclusion.
func extractVote(output string) (string, bool) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		// Tolerate the markdown a model reaches for on its own.
		line = strings.Trim(line, "*_` ")
		if len(line) < len(voteMarker) || !strings.EqualFold(line[:len(voteMarker)], voteMarker) {
			continue
		}
		if choice := strings.TrimSpace(strings.Trim(line[len(voteMarker):], "*_` ")); choice != "" {
			return choice, true
		}
	}
	return "", false
}

// normaliseChoice makes two differently typed versions of the same answer
// compare equal, without trying to judge meaning.
func normaliseChoice(choice string) string {
	lowered := strings.ToLower(strings.TrimSpace(choice))
	var b strings.Builder
	space := false
	for _, r := range lowered {
		switch {
		case r == ' ' || r == '\t':
			space = true
		case strings.ContainsRune(".,;:!?'\"()[]{}", r):
			// Punctuation is presentation, not position.
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tallyConsensus counts the votes of every participant that succeeded.
func tallyConsensus(steps []Step, results map[string]*StepResult, outputs map[string]string) ConsensusResult {
	tally := ConsensusResult{Votes: []Vote{}}
	counts := map[string]int{}
	wording := map[string]string{}
	for _, step := range steps {
		item, ok := results[step.ID]
		if !ok || item.Status != "succeeded" {
			continue
		}
		vote := Vote{StepID: step.ID, AgentID: step.AgentID, AgentName: step.AgentName}
		choice, found := extractVote(outputs[step.ID])
		if !found {
			// An answer with no vote is recorded as an abstention rather than
			// guessed at: inventing a position is worse than reporting silence.
			vote.Abstained = true
			tally.Votes = append(tally.Votes, vote)
			continue
		}
		vote.Choice = choice
		vote.Normalised = normaliseChoice(choice)
		counts[vote.Normalised]++
		if _, seen := wording[vote.Normalised]; !seen {
			wording[vote.Normalised] = choice
		}
		tally.Votes = append(tally.Votes, vote)
		tally.Total++
	}
	if tally.Total == 0 {
		return tally
	}

	// Sort for a stable winner when two answers tie: the tally is reported to a
	// person, and an outcome that changes between identical runs is unreadable.
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	tally.Winner = wording[keys[0]]
	tally.Agreed = counts[keys[0]]
	tally.Unanimous = tally.Agreed == tally.Total
	tally.Tie = len(keys) > 1 && counts[keys[1]] == tally.Agreed
	return tally
}

// composeConsensus renders the tally above the contributions it came from.
func composeConsensus(tally ConsensusResult, steps []Step, results map[string]*StepResult) string {
	var b strings.Builder
	switch {
	case tally.Total == 0:
		b.WriteString("## 표결 결과\n어떤 에이전트도 표를 던지지 않아 합의를 판정하지 못했습니다.\n")
	case tally.Tie:
		b.WriteString(fmt.Sprintf("## 표결 결과: 동률\n최다 득표 %q (%d/%d) 이지만 같은 표를 받은 답이 있어 합의에 이르지 못했습니다.\n",
			tally.Winner, tally.Agreed, tally.Total))
	case tally.Unanimous:
		b.WriteString(fmt.Sprintf("## 표결 결과: 만장일치\n%q (%d/%d)\n", tally.Winner, tally.Agreed, tally.Total))
	default:
		b.WriteString(fmt.Sprintf("## 표결 결과: 다수결\n%q (%d/%d)\n", tally.Winner, tally.Agreed, tally.Total))
	}
	b.WriteString("\n| 에이전트 | 표 |\n| --- | --- |\n")
	for _, vote := range tally.Votes {
		name := vote.AgentName
		if name == "" {
			name = vote.StepID
		}
		choice := vote.Choice
		if vote.Abstained {
			choice = "기권 (VOTE 없음)"
		}
		b.WriteString("| ")
		b.WriteString(name)
		b.WriteString(" | ")
		b.WriteString(choice)
		b.WriteString(" |\n")
	}
	for _, step := range steps {
		item, ok := results[step.ID]
		if !ok || item.Status != "succeeded" {
			continue
		}
		name := step.AgentName
		if name == "" {
			name = step.ID
		}
		b.WriteString("\n## ")
		b.WriteString(name)
		b.WriteString("\n")
		b.WriteString(item.Output)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
