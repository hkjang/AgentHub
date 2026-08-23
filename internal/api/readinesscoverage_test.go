package api

import (
	"os"
	"strings"
	"testing"
)

// Everything this deployment depends on has to be on the one page that says what
// is wrong.
//
// This guard exists because the omission it catches already happened: agent
// servers were added as an execution backend over several releases, and the
// screen whose entire job is "what is broken right now" did not know they
// existed. A pool that had gone away looked exactly like one nobody had used.
//
// It is the same failure this codebase keeps finding — a rule applied on one
// path and not its siblings — so the guard is written against the list of things
// a deployment registers rather than against today's checks.
func TestEveryRegisteredDependencyIsAsked(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)

	// Each entry is something an administrator registers and the platform then
	// depends on at run time, named by the store call that lists them. A new one
	// added to this map fails here until the readiness list asks about it, which
	// is the point: the failure lands on whoever adds the dependency.
	for what, accessor := range map[string]string{
		"모델 엔드포인트":   "ModelEndpoints(",
		"MCP 서버":     "MCPServers(",
		"에이전트 서버":    "AgentServers(",
		"실행 워커":      "LiveWorkers(",
		"Kubernetes": "CheckCluster(",
	} {
		if !strings.Contains(source, accessor) {
			t.Errorf("%s 는 이 배포가 의존하는데 준비 상태 목록이 묻지 않습니다 (%s)", what, accessor)
		}
	}
}

// TestTheReadinessListSaysWhereToFixEachThing — a list of what is broken with no
// way to act on it is a list somebody reads once.
func TestTheReadinessListSaysWhereToFixEachThing(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	// Every row constructed in this file names a console path. The rows are read
	// up to the next one rather than by matching braces: a guard that tries to
	// parse Go with string surgery reports its own confusion as a defect, which
	// is exactly what the first version of this did.
	rows := strings.Split(source, "readinessItem{")[1:]
	for index, row := range rows {
		if cut := strings.Index(row, "readinessItem{"); cut >= 0 {
			row = row[:cut]
		}
		// `[]readinessItem{}` is the empty list this page starts from, not a row.
		if strings.HasPrefix(strings.TrimSpace(row), "}") {
			continue
		}
		if !strings.Contains(row, "Fix:") {
			t.Errorf("readiness row %d does not say where to fix it: %s", index+1, strings.TrimSpace(firstLine(row)))
		}
	}
}

func firstLine(value string) string {
	if at := strings.Index(value, "\n"); at >= 0 {
		return value[:at]
	}
	return value
}
