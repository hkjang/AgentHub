package quota

import (
	"errors"
	"strings"
	"testing"
)

// GPUs became grantable before they became limitable: until a profile's GPU
// count reached the Pod there was nothing to limit, and a deployment that
// limited CPU, memory and storage but not GPUs lets one person hold every card
// in the cluster while every other dimension says everything is fine.
func TestAGPULimitRefusesTheOneThatWouldExceedIt(t *testing.T) {
	limits := Limits{MaxGPUs: 4}
	if err := CheckHeld(ScopeUser, limits, Held{GPUs: 2}, 0, 0, 2); err != nil {
		t.Fatalf("a request that exactly fills the limit was refused: %v", err)
	}
	err := CheckHeld(ScopeUser, limits, Held{GPUs: 2}, 0, 0, 3)
	if err == nil {
		t.Fatal("a fifth GPU was granted against a limit of four")
	}
	if !errors.Is(err, ErrExceeded) {
		t.Errorf("a limit saying no must be a refusal, not a failure: %v", err)
	}
	if !strings.Contains(err.Error(), "GPU") {
		t.Errorf("the refusal does not say which limit was hit: %v", err)
	}
}

// An unset limit inherits rather than forbidding, like every other dimension
// here: zero means nobody chose, not "no GPUs for anyone".
func TestNoGPULimitIsNotALimitOfZero(t *testing.T) {
	if err := CheckHeld(ScopeUser, Limits{MaxCPUMillis: 8000}, Held{GPUs: 6}, 0, 0, 2); err != nil {
		t.Fatalf("a deployment that never set a GPU limit refused a GPU: %v", err)
	}
}

// The tests above ask CheckHeld directly, which is how a GPU limit could pass
// all of them and still never stop anybody: the limits CheckHeld is given at the
// user scope come out of Resolve, and Resolve merged every dimension except this
// one. A platform default of four GPUs became zero on the way through, and zero
// is how this package spells unlimited — so the scarcest resource in the cluster
// was the only one with no ceiling, on a screen that showed the number the
// administrator had typed.
func TestTheGPULimitSurvivesResolution(t *testing.T) {
	platform := Limits{MaxRuntimes: 4, MaxGPUs: 4}
	resolved := Resolve(platform, Limits{}, Limits{})
	if resolved.MaxGPUs != 4 {
		t.Fatalf("GPUs = %d, want the platform's 4", resolved.MaxGPUs)
	}
	// And the limit that survived is the one that refuses, through the same call
	// the runtime path makes.
	if err := CheckHeld(ScopeUser, resolved, Held{GPUs: 4}, 0, 0, 1); err == nil {
		t.Error("a fifth GPU was granted against a resolved limit of four")
	}
}

// GPUs inherit field by field like every other dimension: a department may raise
// the platform's default and one person may be given more than their colleagues,
// and a level that says nothing leaves the level above it standing.
func TestTheGPULimitInheritsLikeEveryOtherDimension(t *testing.T) {
	platform := Limits{MaxGPUs: 1, MaxRuntimes: 4}
	department := Limits{MaxGPUs: 2}
	person := Limits{MaxGPUs: 8}

	if got := Resolve(platform, department, Limits{}).MaxGPUs; got != 2 {
		t.Errorf("GPUs = %d, want the department's 2", got)
	}
	if got := Resolve(platform, department, person).MaxGPUs; got != 8 {
		t.Errorf("GPUs = %d, want the person's 8", got)
	}
	// Nobody overrode it, so it is still the platform's rather than unlimited.
	if got := Resolve(platform, Limits{MaxRuntimes: 8}, Limits{}).MaxGPUs; got != 1 {
		t.Errorf("GPUs = %d, want the platform's 1", got)
	}
}

// The scope has to be in the message. A department's number with a personal
// limit in mind is how somebody spends an afternoon raising the wrong one.
func TestAGPURefusalNamesWhoseLimitItWas(t *testing.T) {
	personal := CheckHeld(ScopeUser, Limits{MaxGPUs: 1}, Held{GPUs: 1}, 0, 0, 1)
	departmental := CheckHeld(ScopeDepartment, Limits{MaxGPUs: 1}, Held{GPUs: 1}, 0, 0, 1)
	if personal == nil || departmental == nil {
		t.Fatal("both scopes must refuse")
	}
	if personal.Error() == departmental.Error() {
		t.Fatalf("both scopes say the same thing: %v", personal)
	}
}
