package execution

import (
	"strings"

	"github.com/hkjang/AgentHub/internal/store"
)

// Directives an agent can emit in its output. They are fenced rather than
// natural language so that describing one cannot accidentally trigger it, which
// matters most for APPROVAL and DELEGATE.
//
//	<<<ARTIFACT report.md ... >>>     preserve a document
//	<<<MEMORY key ... >>>             remember something for the next run
//	<<<APPROVAL 서비스 재시작 ... >>>   ask a human before a state-changing action
//	<<<DELEGATE agent-name ... >>>    hand a sub-task to another agent
//	<<<HANDOFF 남은 작업 ... >>>       hand the task to a person in the runtime
const (
	directiveArtifact = "ARTIFACT"
	directiveMemory   = "MEMORY"
	directiveApproval = "APPROVAL"
	directiveDelegate = "DELEGATE"
	// directiveHandoff is how an agent says the rest of this work needs the
	// runtime it cannot drive: a file to edit, a command to run, a browser to
	// click. Autonomous execution is a prose loop against the model gateway, so
	// without this the honest options were to fail the task or to claim work that
	// never happened — and models reliably chose the second.
	directiveHandoff = "HANDOFF"
)

const directiveOpen = "<<<"
const directiveClose = ">>>"

// knownDirectives is the set the parser accepts. It is a set rather than a switch
// because the prompt and the parser have to agree: a kind offered to the model and
// missing here is silently ignored, which is how the first HANDOFF request was
// dropped while the model kept dutifully asking for it.
var knownDirectives = map[string]bool{
	directiveArtifact: true, directiveMemory: true, directiveApproval: true,
	directiveDelegate: true, directiveHandoff: true,
}

// Directive is one fenced block: its kind, the argument on the opening line, and
// the body between the fences.
type Directive struct {
	Kind string
	Arg  string
	Body string
}

// parseDirectives extracts every fenced block from one transcript entry.
//
// Unterminated fences are ignored: a truncated response must not produce a
// half-written artifact or, worse, a half-read approval request.
func parseDirectives(output string) []Directive {
	directives := []Directive{}
	rest := output
	for {
		start := strings.Index(rest, directiveOpen)
		if start < 0 {
			return directives
		}
		afterOpen := rest[start+len(directiveOpen):]
		newline := strings.Index(afterOpen, "\n")
		if newline < 0 {
			return directives
		}
		header := strings.TrimSpace(afterOpen[:newline])
		body := afterOpen[newline+1:]
		end := strings.Index(body, directiveClose)
		if end < 0 {
			return directives
		}
		content := strings.TrimSpace(body[:end])
		if kind, arg, ok := splitHeader(header); ok {
			directives = append(directives, Directive{Kind: kind, Arg: arg, Body: content})
		}
		rest = body[end+len(directiveClose):]
	}
}

// splitHeader reads `KIND argument` from an opening fence, rejecting anything
// that is not a directive the platform acts on.
func splitHeader(header string) (string, string, bool) {
	kind, arg, _ := strings.Cut(header, " ")
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if !knownDirectives[kind] {
		return "", "", false
	}
	return kind, strings.TrimSpace(arg), true
}

// agentDirectives keeps the directives the agent composed and drops the ones it
// only repeated back.
//
// A directive is how an agent asks this platform to act — park the task for a
// person, delegate work, remember something. The text an agent is given is not
// always written by whoever owns it: a webhook appends its payload verbatim, and
// a payload carries titles and bodies written by anybody who can open a pull
// request. An agent that quotes its input, which is what summarising looks like,
// hands those words back as its own.
//
// Measured on a cluster: a MEMORY directive in an answer wrote a memory row and
// announced it on the run's timeline — on the ACP backend, which is never told
// this vocabulary at all. Nothing separated a decision the agent made from a
// sentence somebody sent it.
//
// The given text is compared against rather than stripped from: the agent reads
// exactly what it was sent, and a directive it composed itself is never mistaken
// for an echo.
func agentDirectives(output, given, kind string) (kept []Directive, echoed int) {
	matched := directivesOfKind(output, kind)
	if len(matched) == 0 || strings.TrimSpace(given) == "" {
		return matched, 0
	}
	repeated := map[string]bool{}
	for _, directive := range parseDirectives(given) {
		repeated[directiveKey(directive)] = true
	}
	kept = make([]Directive, 0, len(matched))
	for _, directive := range matched {
		if repeated[directiveKey(directive)] {
			echoed++
			continue
		}
		kept = append(kept, directive)
	}
	return kept, echoed
}

func directiveKey(directive Directive) string {
	return directive.Kind + "\x00" + strings.TrimSpace(directive.Arg) + "\x00" + strings.TrimSpace(directive.Body)
}

// directivesOfKind filters a transcript entry to one directive type.
func directivesOfKind(output, kind string) []Directive {
	matched := []Directive{}
	for _, directive := range parseDirectives(output) {
		if directive.Kind == kind {
			matched = append(matched, directive)
		}
	}
	return matched
}

// taskGiven is the text a task handed the agent: its title and its input. The
// webhook payload arrives inside the input, appended to whatever the trigger
// itself says.
func taskGiven(task store.AgentTask) string {
	return task.Title + "\n" + task.Input
}

// WebhookPayloadHeader is what this platform writes in front of a webhook's body
// when it appends it to a task's input. It is the line between text somebody
// with an account wrote and text that arrived from outside, and it is written
// here so both sides of that boundary agree on where it is.
const WebhookPayloadHeader = "# Webhook payload"

// untrustedGiven is the part of a task's input that came from outside.
//
// A trigger's own template is written by whoever owns the agent; the payload
// appended after it is whatever a stranger typed into a pull request. Only the
// second half is treated as hostile, so an owner who mentions a marker or a
// directive in their own instructions is not punished for it.
func untrustedGiven(task store.AgentTask) string {
	at := strings.Index(task.Input, WebhookPayloadHeader)
	if at < 0 {
		return ""
	}
	return task.Input[at+len(WebhookPayloadHeader):]
}
