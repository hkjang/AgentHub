package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hkjang/AgentHub/internal/store"
)

// completionMarker is what an agent emits to claim it is finished. It is a
// literal token rather than natural language so a claim cannot be made by
// accident in ordinary prose.
const completionMarker = "TASK_COMPLETE"

// artifactFence wraps a document the agent wants preserved. Everything between
// the fences becomes an artifact rather than being buried in the transcript.
const artifactOpen = "<<<ARTIFACT"
const artifactClose = ">>>"

// systemPrompt turns an agent definition and its goal into the instruction the
// model runs under. The agent's own system prompt still leads, so an agent that
// was written for interactive use keeps its character when driven autonomously.
func systemPrompt(agent store.Agent, goal store.AgentGoal) string {
	var b strings.Builder
	if prompt := agentSystemPrompt(agent); prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}
	b.WriteString("당신은 AgentHub에서 자동으로 실행되는 에이전트입니다. 사용자와 대화하는 것이 아니라 주어진 목표를 스스로 완료해야 합니다.\n")
	if strings.TrimSpace(goal.Description) != "" {
		b.WriteString("\n# 목표\n")
		b.WriteString(goal.Description)
		b.WriteString("\n")
	}
	if len(goal.SuccessCriteria) > 0 {
		b.WriteString("\n# 완료 조건 (모두 충족해야 함)\n")
		for _, item := range goal.SuccessCriteria {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	if len(goal.FailureCriteria) > 0 {
		b.WriteString("\n# 실패 조건 (해당되면 즉시 중단)\n")
		for _, item := range goal.FailureCriteria {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(goal.Constraints) != "" {
		b.WriteString("\n# 제약\n")
		b.WriteString(goal.Constraints)
		b.WriteString("\n")
	}
	b.WriteString("\n# 진행 방식\n")
	b.WriteString("- 한 번에 한 단계씩 진행하고, 그 단계에서 무엇을 했는지 서술하세요.\n")
	b.WriteString("- 아직 끝나지 않았다면 다음에 무엇을 할지 적고 응답을 마치세요.\n")
	b.WriteString(fmt.Sprintf("- 모든 완료 조건을 충족했다면 마지막 줄에 %s 를 단독으로 출력하세요.\n", completionMarker))
	b.WriteString(fmt.Sprintf("- 보고서나 산출물을 남기려면 %s 이름 ... %s 형식으로 감싸세요.\n", artifactOpen, artifactClose))
	return b.String()
}

// stepPrompt gives the model the task and everything it has already said, so a
// stateless gateway still behaves like a continuing agent.
func stepPrompt(task store.AgentTask, goal store.AgentGoal, transcript []string) string {
	var b strings.Builder
	b.WriteString("# Task\n")
	b.WriteString(task.Title)
	b.WriteString("\n")
	if strings.TrimSpace(task.Input) != "" {
		b.WriteString("\n# 입력\n")
		b.WriteString(task.Input)
		b.WriteString("\n")
	}
	for index, entry := range transcript {
		b.WriteString(fmt.Sprintf("\n# 이전 단계 %d\n", index+1))
		b.WriteString(entry)
		b.WriteString("\n")
	}
	if len(transcript) == 0 {
		b.WriteString("\n첫 단계를 시작하세요.")
	} else {
		b.WriteString(fmt.Sprintf("\n남은 작업을 이어서 진행하세요. 최대 %d단계까지 사용할 수 있습니다.", goal.MaxSteps))
	}
	return b.String()
}

// declaresCompletion reports whether the agent claimed it is finished. The claim
// alone never completes a task — the evaluator decides — but it ends the loop.
func declaresCompletion(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == completionMarker {
			return true
		}
	}
	return false
}

// extractArtifacts pulls fenced documents out of a transcript entry.
func extractArtifacts(output string) []store.AgentArtifact {
	artifacts := []store.AgentArtifact{}
	rest := output
	for {
		start := strings.Index(rest, artifactOpen)
		if start < 0 {
			return artifacts
		}
		afterOpen := rest[start+len(artifactOpen):]
		newline := strings.Index(afterOpen, "\n")
		if newline < 0 {
			return artifacts
		}
		name := strings.TrimSpace(afterOpen[:newline])
		body := afterOpen[newline+1:]
		end := strings.Index(body, artifactClose)
		if end < 0 {
			return artifacts
		}
		content := strings.TrimSpace(body[:end])
		if name != "" && content != "" {
			artifacts = append(artifacts, store.AgentArtifact{
				Name: sanitiseArtifactName(name), Type: artifactType(name),
				ContentType: "text/markdown", Content: content,
			})
		}
		rest = body[end+len(artifactClose):]
	}
}

// sanitiseArtifactName keeps a model-supplied name from becoming a path.
func sanitiseArtifactName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "..", "-")
	if len(name) > 120 {
		name = name[:120]
	}
	if name == "" {
		return "artifact"
	}
	return name
}

// artifactType guesses the catalogue type from the file name so the UI can group
// results without the model having to declare it.
func artifactType(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".patch"), strings.HasSuffix(lower, ".diff"):
		return "patch"
	case strings.HasSuffix(lower, ".sql"):
		return "sql"
	case strings.HasSuffix(lower, ".json"):
		return "json"
	case strings.HasSuffix(lower, ".log"):
		return "log"
	case strings.HasSuffix(lower, ".csv"):
		return "dataset"
	default:
		return "report"
	}
}

// agentSystemPrompt reads the instruction stored on the definition.
func agentSystemPrompt(agent store.Agent) string {
	var spec struct {
		SystemPrompt string `json:"systemPrompt"`
	}
	if len(agent.Spec) > 0 {
		_ = json.Unmarshal(agent.Spec, &spec)
	}
	return strings.TrimSpace(spec.SystemPrompt)
}
