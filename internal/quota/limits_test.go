package quota

import (
	"strings"
	"testing"
)

// Inheritance is field by field, because that is what an administrator means by
// "the platform allows four, our department eight, and this one person sixteen":
// the fields nobody overrode keep falling through rather than reverting to
// unlimited.
func TestLimitsInheritFieldByField(t *testing.T) {
	platform := Limits{MaxRuntimes: 4, MaxCPUMillis: 8000, MaxStorageGB: 100, TokenBudget: 1_000_000}
	department := Limits{MaxRuntimes: 8, MaxStorageGB: 500}
	person := Limits{MaxRuntimes: 16}

	got := Resolve(platform, department, person)
	if got.MaxRuntimes != 16 {
		t.Errorf("runtimes = %d, want the person's", got.MaxRuntimes)
	}
	if got.MaxStorageGB != 500 {
		t.Errorf("storage = %d, want the department's", got.MaxStorageGB)
	}
	// Nobody overrode these, so they are still the platform's rather than
	// unlimited — the failure that would quietly hand one person the whole
	// cluster because their department set a runtime count.
	if got.MaxCPUMillis != 8000 {
		t.Errorf("cpu = %d, want the platform's", got.MaxCPUMillis)
	}
	if got.TokenBudget != 1_000_000 {
		t.Errorf("tokens = %d, want the platform's", got.TokenBudget)
	}
}

// A deployment that configures nothing keeps behaving as it did: every field
// zero means unlimited, not "limited to zero".
func TestNoQuotaAnywhereLimitsNothing(t *testing.T) {
	got := Resolve(Limits{}, Limits{}, Limits{})
	if !got.Empty() {
		t.Errorf("limits = %#v, want none", got)
	}
	if err := CheckHeld(ScopeUser, got, Held{Runtimes: 9000, CPUMillis: 9_000_000}, 4000, 8192); err != nil {
		t.Errorf("unlimited refused something: %v", err)
	}
	if err := CheckStorage(ScopeUser, got, 9000, 500); err != nil {
		t.Errorf("unlimited refused storage: %v", err)
	}
}

// The limit that stopped somebody has to say whose it was. A department's
// ceiling and a personal one produce the same shortage and need different
// answers, and a message that does not distinguish them sends an administrator
// to raise the wrong number.
func TestARefusalNamesTheScopeItCameFrom(t *testing.T) {
	limits := Limits{MaxRuntimes: 2, MaxCPUMillis: 4000, MaxMemoryMB: 4096, MaxStorageGB: 50}
	held := Held{Runtimes: 2, CPUMillis: 4000, MemoryMB: 4096}

	personal := CheckHeld(ScopeUser, limits, held, 0, 0)
	if personal == nil || !strings.HasPrefix(personal.Error(), "사용자") {
		t.Errorf("personal refusal = %v", personal)
	}
	departmental := CheckHeld(ScopeDepartment, limits, held, 0, 0)
	if departmental == nil || !strings.HasPrefix(departmental.Error(), "부서") {
		t.Errorf("departmental refusal = %v", departmental)
	}
	if storage := CheckStorage(ScopeDepartment, limits, 50, 1); storage == nil || !strings.HasPrefix(storage.Error(), "부서") {
		t.Errorf("storage refusal = %v", storage)
	}
}

// Each dimension is checked on its own: a person under their runtime count can
// still be over their memory, and the message says which.
func TestEachDimensionIsCheckedSeparately(t *testing.T) {
	limits := Limits{MaxRuntimes: 10, MaxCPUMillis: 4000, MaxMemoryMB: 4096}
	held := Held{Runtimes: 1, CPUMillis: 2000, MemoryMB: 4096}

	if err := CheckHeld(ScopeUser, limits, held, 1000, 0); err != nil {
		t.Errorf("a request within every limit was refused: %v", err)
	}
	err := CheckHeld(ScopeUser, limits, held, 0, 1024)
	if err == nil || !strings.Contains(err.Error(), "Memory") {
		t.Errorf("error = %v, want the memory limit named", err)
	}
	err = CheckHeld(ScopeUser, limits, held, 3000, 0)
	if err == nil || !strings.Contains(err.Error(), "CPU") {
		t.Errorf("error = %v, want the cpu limit named", err)
	}
}

// A department's two quotas are different questions and both are kept: what one
// member may use, and what the department may use altogether.
func TestADepartmentHoldsTwoDifferentLimits(t *testing.T) {
	department := Department{
		PerMember: Limits{MaxRuntimes: 4},
		Total:     Limits{MaxRuntimes: 20},
	}
	if department.PerMember.MaxRuntimes == department.Total.MaxRuntimes {
		t.Error("per-member and total are the same value; they answer different questions")
	}
	// A member's own limit comes from PerMember, never from Total: five members
	// with four each is what a total of twenty is for.
	member := Resolve(Limits{MaxRuntimes: 2}, department.PerMember, Limits{})
	if member.MaxRuntimes != 4 {
		t.Errorf("member limit = %d, want the department's per-member value", member.MaxRuntimes)
	}
}
