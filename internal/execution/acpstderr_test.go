package execution

import (
	"strings"
	"testing"
)

// Agents print their failures as pretty JSON. Taking the last line of that gives
// `}` — and a run whose whole explanation was "— }" is what sent somebody
// looking at the platform when their model credential had been refused.
//
// Observed on a real cluster: the gateway answered 401 with
// {"error":{"message":"Incorrect API key provided"...}} and the task said
// "ACP 실행이 실패했습니다: agent error -32603: Internal error — }".
func TestAJSONFailureIsNotReportedAsAClosingBrace(t *testing.T) {
	stderr := `starting agent
{
  "error": {
    "message": "Incorrect API key provided",
    "type": "invalid_request_error"
  }
}`
	suffix := acpStderrSuffix((stderr))
	if strings.TrimSpace(strings.TrimPrefix(suffix, " — ")) == "}" {
		t.Fatal("the explanation is a closing brace")
	}
	if !strings.Contains(suffix, "Incorrect API key provided") {
		t.Errorf("the sentence that explains the failure was dropped: %q", suffix)
	}
	if strings.Contains(suffix, "\n") {
		t.Errorf("a run's failure line carries a newline: %q", suffix)
	}
}

// A single line stays exactly itself.
func TestASingleLineFailureIsUnchanged(t *testing.T) {
	suffix := acpStderrSuffix(("connection refused"))
	if suffix != " — connection refused" {
		t.Fatalf("got %q", suffix)
	}
}

// Nothing to say is nothing to append.
func TestNoStderrAddsNothing(t *testing.T) {
	if suffix := acpStderrSuffix(("   \n  ")); suffix != "" {
		t.Fatalf("got %q", suffix)
	}
}

// A long log keeps its end: that is where the failure is.
func TestALongLogKeepsItsEnd(t *testing.T) {
	stderr := strings.Repeat("noise line that means nothing\n", 60) + "fatal: the key was refused"
	suffix := acpStderrSuffix((stderr))
	if !strings.Contains(suffix, "the key was refused") {
		t.Errorf("the end of the log was cut off: %q", suffix)
	}
	if len(suffix) > 460 {
		t.Errorf("the failure line is %d characters", len(suffix))
	}
}
