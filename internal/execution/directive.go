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
const (
	directiveArtifact = "ARTIFACT"
	directiveMemory   = "MEMORY"
	directiveApproval = "APPROVAL"
	directiveDelegate = "DELEGATE"
)

const directiveOpen = "<<<"
const directiveClose = ">>>"

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
	switch kind {
	case directiveArtifact, directiveMemory, directiveApproval, directiveDelegate:
		return kind, strings.TrimSpace(arg), true
	}
	return "", "", false
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
