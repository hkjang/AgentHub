package quota

import (
	"errors"
	"testing"
)

// Every refusal in this package has to answer to the sentinel.
//
// The sentinel exists so a caller can tell "a limit said no" from "the limit
// could not be evaluated": the first belongs in front of the person as a 409,
// the second is an internal failure. Storage alone returned a bare error, so
// that one refusal was classified as a fault — and the message a person saw
// depended on which limit they happened to hit.
func TestEveryLimitRefusesWithTheSentinel(t *testing.T) {
	limits := Limits{MaxRuntimes: 1, MaxCPUMillis: 1000, MaxMemoryMB: 1024, MaxStorageGB: 10,
		MaxRunningTasks: 1, TokenBudget: 100, CostBudget: 100}
	held := Held{Runtimes: 5, CPUMillis: 5000, MemoryMB: 8192, StorageGB: 100}

	for _, refusal := range []struct {
		what string
		err  error
	}{
		{"runtime", CheckHeld(ScopeUser, limits, held, 1000, 1024, 0)},
		{"storage", CheckStorage(ScopeUser, limits, held.StorageGB, 10)},
	} {
		if refusal.err == nil {
			t.Fatalf("%s: the limit did not refuse; this guard is checking nothing", refusal.what)
		}
		if !errors.Is(refusal.err, ErrExceeded) {
			t.Errorf("%s 한도 거절이 sentinel에 답하지 않습니다 (%T) — 호출부는 이걸 내부 오류로 처리합니다",
				refusal.what, refusal.err)
		}
		// And the sentinel's own words stay out of what a person reads.
		if errors.Is(refusal.err, ErrExceeded) && refusal.err.Error() == ErrExceeded.Error() {
			t.Errorf("%s: the message somebody reads is the sentinel's own text", refusal.what)
		}
	}
}
