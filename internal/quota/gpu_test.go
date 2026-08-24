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
