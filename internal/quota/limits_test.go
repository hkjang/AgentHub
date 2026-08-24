package quota

import (
	"errors"
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
	if err := CheckHeld(ScopeUser, got, Held{Runtimes: 9000, CPUMillis: 9_000_000}, 4000, 8192, 0); err != nil {
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

	personal := CheckHeld(ScopeUser, limits, held, 0, 0, 0)
	if personal == nil || !strings.HasPrefix(personal.Error(), "사용자") {
		t.Errorf("personal refusal = %v", personal)
	}
	departmental := CheckHeld(ScopeDepartment, limits, held, 0, 0, 0)
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

	if err := CheckHeld(ScopeUser, limits, held, 1000, 0, 0); err != nil {
		t.Errorf("a request within every limit was refused: %v", err)
	}
	err := CheckHeld(ScopeUser, limits, held, 0, 1024, 0)
	if err == nil || !strings.Contains(err.Error(), "Memory") {
		t.Errorf("error = %v, want the memory limit named", err)
	}
	err = CheckHeld(ScopeUser, limits, held, 3000, 0, 0)
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

// A limit that says no and a database that could not be read are different
// answers, and only one of them belongs in front of a person. Callers were left
// comparing message text until this could be asked directly.
func TestARefusalIsDistinguishableFromAFailureToCheck(t *testing.T) {
	refusal := CheckHeld(ScopeUser, Limits{MaxRuntimes: 1}, Held{Runtimes: 1}, 0, 0, 0)
	if !errors.Is(refusal, ErrExceeded) {
		t.Fatalf("a limit's refusal is not recognisable: %v", refusal)
	}
	// And the sentence stays the sentence: err.Error() is printed by more places
	// than the one that classifies it, so the sentinel must not appear in it.
	if strings.Contains(refusal.Error(), "quota exceeded") {
		t.Errorf("the sentinel leaked into the message: %q", refusal.Error())
	}
	if !strings.Contains(refusal.Error(), "사용자") {
		t.Errorf("the refusal no longer names the scope: %q", refusal.Error())
	}
	// Something that is not a refusal must not answer to the sentinel.
	if errors.Is(errors.New("connection refused"), ErrExceeded) {
		t.Error("an unrelated error was recognised as a quota refusal")
	}
	if err := CheckHeld(ScopeUser, Limits{MaxRuntimes: 5}, Held{Runtimes: 1}, 0, 0, 0); err != nil {
		t.Errorf("a request inside the limit was refused: %v", err)
	}
}

// The runtime being decided about must not be counted as already held.
//
// The autonomous path creates the runtime's record before it knows which profile
// to check against, so by the time the limit is asked the row is there — and the
// check adds one for the runtime it is about to start, which is that same row. A
// person allowed one runtime was refused because of the runtime they were asking
// about, and the task waited behind itself until somebody noticed.
func TestTheRuntimeBeingStartedIsNotCountedTwice(t *testing.T) {
	limits := Limits{MaxRuntimes: 1}
	// Nothing else running: the one being started fits.
	if err := CheckHeld(ScopeUser, limits, Held{Runtimes: 0}, 0, 0, 0); err != nil {
		t.Errorf("the first runtime was refused: %v", err)
	}
	// Its own record counted as held is what the wedge looked like.
	if err := CheckHeld(ScopeUser, limits, Held{Runtimes: 1}, 0, 0, 0); err == nil {
		t.Error("a limit of one allowed a second runtime")
	}
}
