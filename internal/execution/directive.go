package execution

import (
	"strings"
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
