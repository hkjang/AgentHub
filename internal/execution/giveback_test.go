package execution

import (
	"os"
	"strings"
	"testing"
)

// Every way the pool gives up on a runtime has to give the capacity back.
//
// The record is created already counted as running, so abandoning one by only
// releasing the pool's hold left it holding its owner's quota with no Pod — and
// out of reach of the cooling sweep, which only considers runtimes that still
// hold a claim. Releasing the hold was worse than doing nothing.
func TestEveryWayThePoolGivesUpGivesTheCapacityBack(t *testing.T) {
	body, err := os.ReadFile("pool.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (p *Pool) warm(")
	if at < 0 {
		at = strings.Index(source, "func (p *Pool) Run(")
	}
	if at < 0 {
		t.Fatal("the pool's warm loop is gone; this guard is reading nothing")
	}
	// The warming half ends where the cooling half begins: cooling has already
	// set the state to stopped, so releasing the hold there is correct.
	end := strings.Index(source, "RuntimesToCool(")
	if end < 0 {
		end = len(source)
	}
	warming := source[at:end]

	bare := strings.Count(warming, "ReleaseWarmRuntime(")
	if bare > 0 {
		t.Errorf("경고: 워밍 경로에서 %d곳이 홀드만 풀고 있습니다 — 시작되지 않은 런타임이 소유자 한도를 계속 잡고, 냉각 대상에서도 빠집니다", bare)
	}
	if strings.Count(warming, "p.giveBack(") < 4 {
		t.Errorf("워밍을 포기하는 경로 중 일부가 용량을 돌려주지 않습니다 (giveBack %d곳)", strings.Count(warming, "p.giveBack("))
	}
}
