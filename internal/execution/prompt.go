package execution

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// completionMarker is what an agent emits to claim it is finished. It is a
// literal token rather than natural language so a claim cannot be made by
// accident in ordinary prose.
const completionMarker = "TASK_COMPLETE"

// systemPrompt turns an agent definition and its goal into the instruction the
// model runs under. The agent's own system prompt still leads, so an agent that
// was written for interactive use keeps its character when driven autonomously.
// environment describes what this run can actually reach, which the prompt has to
// state plainly. The model is bound to a runtime whose files and terminal it
// cannot touch from this loop, and a prompt that leaves that unsaid produces an
// agent that reports edits it never made.
type environment struct {
	// Runtime is the adapter this agent is bound to, described for a reader.
	Runtime runtimetype.Descriptor
	// RuntimeReady reports whether a Pod is actually up while this task runs, and
	// therefore whether a person can be handed the workspace right now.
	RuntimeReady bool
	// WorkspaceName is the persistent volume behind the runtime, empty when the
	// agent has none and its files vanish with the Pod.
	WorkspaceName string
	// Tools are the MCP servers bound to the agent. They are reachable when a
	// person drives the runtime, not from this loop — which is exactly why they
	// are worth naming here.
	Tools []string
	// HandoffAllowed reports whether this agent may hand the task to a person.
	HandoffAllowed bool
}

func systemPrompt(agent store.Agent, goal store.AgentGoal) string {
	return systemPromptWithEnvironment(agent, goal, environment{Runtime: runtimetype.Describe(agent.RuntimeType)})
}

// systemPromptWithEnvironment is the full instruction, including what the run can
// and cannot do.
func systemPromptWithEnvironment(agent store.Agent, goal store.AgentGoal, env environment) string {
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
	b.WriteString(environmentSection(env))
	b.WriteString("\n# 진행 방식\n")
	b.WriteString("- 한 번에 한 단계씩 진행하고, 그 단계에서 무엇을 했는지 서술하세요.\n")
	b.WriteString("- 아직 끝나지 않았다면 다음에 무엇을 할지 적고 응답을 마치세요.\n")
	b.WriteString(fmt.Sprintf("- 모든 완료 조건을 충족했다면 마지막 줄에 %s 를 단독으로 출력하세요.\n", completionMarker))
	b.WriteString(fmt.Sprintf("- 보고서나 산출물을 남기려면 %s%s 이름 ... %s 형식으로 감싸세요.\n", directiveOpen, directiveArtifact, directiveClose))
	b.WriteString(fmt.Sprintf("- 다음 실행에서도 기억해야 할 사실은 %s%s 키 ... %s 로 남기세요.\n", directiveOpen, directiveMemory, directiveClose))
	if goal.ApprovalRequired {
		b.WriteString(fmt.Sprintf("- 상태를 변경하는 작업(배포, 삭제, 재시작, DB 쓰기, 권한 변경 등)은 직접 수행하지 말고 %s%s 작업요약 ... 상세 ... %s 로 승인을 요청한 뒤 응답을 마치세요. 승인 결과는 다음 단계에서 전달됩니다.\n", directiveOpen, directiveApproval, directiveClose))
	}
	if goal.MaxDelegationDepth > 0 {
		b.WriteString(fmt.Sprintf("- 다른 에이전트가 처리해야 할 일은 %s%s 에이전트이름 ... 작업내용 ... %s 로 위임하세요.\n", directiveOpen, directiveDelegate, directiveClose))
	}
	if env.HandoffAllowed {
		b.WriteString(fmt.Sprintf("- 파일 편집·명령 실행·브라우저 조작처럼 이 루프에서 할 수 없는 일이 남았다면, 했다고 쓰지 말고 %s%s 남은 작업 요약 ... 사람이 이어서 할 내용 ... %s 로 런타임 인계를 요청하세요. 작업은 실패가 아니라 '런타임 인계' 상태로 대기하고, 담당자가 같은 작업공간에서 이어받습니다.\n", directiveOpen, directiveHandoff, directiveClose))
	}
	return b.String()
}

// environmentSection tells the model where it is running and what that means.
//
// Everything here is stated rather than implied, including the limits: the loop
// is prose in and prose out, the runtime's editor and terminal belong to whoever
// opens it, and the tools are named so the agent can ask for them by name instead
// of inventing an API for them.
func environmentSection(env environment) string {
	var b strings.Builder
	b.WriteString("\n# 실행 환경\n")
	if env.Runtime.Label != "" {
		b.WriteString(fmt.Sprintf("- 이 에이전트는 %s 런타임에 연결되어 있습니다. %s\n", env.Runtime.Label, env.Runtime.Summary))
	}
	if env.WorkspaceName != "" {
		b.WriteString(fmt.Sprintf("- 작업공간 %s 가 %s 에 연결되어 있고, Pod가 재시작되어도 내용이 유지됩니다.\n", env.WorkspaceName, env.Runtime.Workspace))
	} else {
		b.WriteString("- 영속 작업공간이 연결되어 있지 않습니다. 런타임 안에 남긴 파일은 Pod와 함께 사라집니다.\n")
	}
	if len(env.Tools) > 0 {
		b.WriteString(fmt.Sprintf("- 이 에이전트에 연결된 MCP 도구: %s. 사람이 런타임을 직접 열었을 때 사용할 수 있습니다.\n", strings.Join(env.Tools, ", ")))
	}
	// The limit, stated once and without hedging. This is the sentence that stops
	// a model from reporting a commit it never made.
	b.WriteString("- 지금 이 실행은 모델과 글로만 주고받는 루프입니다. 파일을 직접 편집하거나 명령을 실행하거나 도구를 호출할 수 없습니다. 하지 않은 일을 했다고 쓰지 마세요.\n")
	if env.HandoffAllowed {
		if env.RuntimeReady {
			b.WriteString("- 런타임 Pod는 지금 실행 중입니다. 담당자가 브라우저로 같은 작업공간을 열어 곧바로 이어받을 수 있습니다.\n")
		} else {
			b.WriteString("- 런타임 Pod는 지금 실행 중이 아니지만, 담당자가 시작해 같은 작업공간에서 이어받을 수 있습니다.\n")
		}
	}
	return b.String()
}

// handoffNote turns a HANDOFF directive into the sentence the person picking the
// task up reads. The argument is the summary and the body is the detail, so both
// survive rather than only the one that happened to be filled in.
func handoffNote(directive Directive) string {
	summary := strings.TrimSpace(directive.Arg)
	body := strings.TrimSpace(directive.Body)
	switch {
	case summary != "" && body != "":
		return summary + "\n\n" + body
	case summary != "":
		return summary
	case body != "":
		return body
	default:
		return "에이전트가 런타임에서 직접 수행해야 하는 작업이라고 판단했습니다."
	}
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

// extractArtifacts turns ARTIFACT directives into storable records.
func extractArtifacts(output, given string) []store.AgentArtifact {
	artifacts := []store.AgentArtifact{}
	kept, _ := agentDirectives(output, given, directiveArtifact)
	for _, directive := range kept {
		if directive.Arg == "" || directive.Body == "" {
			continue
		}
		artifacts = append(artifacts, store.AgentArtifact{
			Name: sanitiseArtifactName(directive.Arg), Type: artifactType(directive.Arg),
			ContentType: "text/markdown", Content: directive.Body,
		})
	}
	return artifacts
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
