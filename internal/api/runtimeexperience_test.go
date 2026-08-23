package api

import (
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// A verdict about a runtime type is a claim about this deployment, and the ways
// to get it wrong are asymmetric.
//
// Saying "failed" about something that did not fail invents bad news and steers
// somebody away from a type nobody had trouble with. Saying "proven" about
// something that never ran is the failure this platform keeps removing: a choice
// that looks available and is not.
//
// The case that made this necessary was real. Two runtimes on this deployment
// both sat at status "stopped"; one had started and one never had, and the first
// version called the second one failed.
func TestAVerdictSaysOnlyWhatHappened(t *testing.T) {
	for _, one := range []struct {
		name       string
		experience store.RuntimeTypeExperience
		want       string
	}{
		{"nothing was ever created", store.RuntimeTypeExperience{}, "untried"},
		{"one ran", store.RuntimeTypeExperience{Attempts: 1, Started: 1, LastStatus: "stopped"}, "proven"},
		{
			"created and stopped without ever starting",
			store.RuntimeTypeExperience{Attempts: 1, Started: 0, LastStatus: "stopped"},
			"attempted",
		},
		{
			"the runtime said why it failed",
			store.RuntimeTypeExperience{Attempts: 1, LastStatus: "failed", LastFailure: "이미지를 가져오지 못했습니다"},
			"failed",
		},
		{
			"a failure state with nothing said about it",
			store.RuntimeTypeExperience{Attempts: 2, LastStatus: "crashed"},
			"failed",
		},
		{
			"one ran and a later one crashed — it still works here",
			store.RuntimeTypeExperience{Attempts: 3, Started: 1, LastStatus: "crashed"},
			"proven",
		},
	} {
		got, _ := runtimeExperienceOf(one.experience)["verdict"].(string)
		if got != one.want {
			t.Errorf("%s: verdict %q, want %q", one.name, got, one.want)
		}
	}
}

// A failure a runtime explained has to carry the explanation, or the person is
// told something went wrong and sent looking for what.
func TestAFailedVerdictCarriesTheReason(t *testing.T) {
	answer := runtimeExperienceOf(store.RuntimeTypeExperience{
		Attempts: 1, LastStatus: "spawn_failed", LastFailure: "이미지를 가져오지 못했습니다",
	})
	detail, _ := answer["detail"].(string)
	if detail == "" {
		t.Fatal("a failed verdict says nothing")
	}
	if !contains([]string{detail}, detail) || len(detail) == 0 {
		t.Fatal("unreachable")
	}
	if got := answer["verdict"]; got != "failed" {
		t.Fatalf("verdict %v", got)
	}
	if !containsSubstring(detail, "이미지를 가져오지 못했습니다") {
		t.Errorf("the reason the runtime gave was dropped: %s", detail)
	}
}

// The states a runtime passes through are not failures. Calling them one would
// mark a type badly for having been created, or stopped on purpose.
func TestOrdinaryStatesAreNotFailures(t *testing.T) {
	for _, status := range []string{"created", "pending", "starting", "ready", "running", "stopped", "stopping"} {
		if runtimeFailureStatus(status) {
			t.Errorf("%q is treated as a failure", status)
		}
	}
	for _, status := range []string{"failed", "crashed", "spawn_failed", "unhealthy"} {
		if !runtimeFailureStatus(status) {
			t.Errorf("%q is not treated as a failure", status)
		}
	}
}

func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 ||
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
