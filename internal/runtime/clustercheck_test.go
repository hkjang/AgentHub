package runtime

import (
	"strings"
	"testing"
)

// The first draft of this list asked whether the control plane could create
// ConfigMaps, volumes and network policies. It cannot, and does not: the
// operator writes those under its own account. Every correctly configured
// deployment would have been told three permissions were missing — and a check
// that calls a healthy deployment broken is worse than no check, because the
// next real warning is read as another false one.
func TestTheClusterCheckAsksOnlyAboutWhatThisComponentDoes(t *testing.T) {
	// Resources this package never touches. Asking about them would be asking on
	// the operator's behalf, which SelfSubjectAccessReview cannot do anyway.
	theOperators := []string{"configmaps", "persistentvolumeclaims", "networkpolicies", "statefulsets", "services"}
	for _, action := range clusterActions {
		for _, foreign := range theOperators {
			if action.resource == foreign {
				t.Errorf("the check asks about %q, which the operator writes under its own account", foreign)
			}
		}
		if action.what == "" || action.verb == "" || action.resource == "" {
			t.Errorf("an action is missing a field: %#v", action)
		}
	}
	if len(clusterActions) < 6 {
		t.Errorf("only %d actions checked; the control plane does more than that", len(clusterActions))
	}
	// Every action has to name what it is in words somebody reading the screen
	// would use, not in the API's nouns.
	for _, action := range clusterActions {
		if strings.ContainsAny(action.what, "abcdefghijklmnopqrstuvwxyz") && !strings.Contains(action.what, "Runtime") &&
			!strings.Contains(action.what, "Pod") && !strings.Contains(action.what, "Secret") {
			t.Errorf("%q reads like an API noun rather than an action", action.what)
		}
	}
}
