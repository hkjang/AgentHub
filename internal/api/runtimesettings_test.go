package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The states an operator reads have to distinguish "not applied yet" from "did not
// apply". Calling the first one a failure sends somebody debugging a Pod that is
// working exactly as designed; calling the second one fine leaves a fleet running
// settings nobody chose.
func TestInjectionState(t *testing.T) {
	applied := store.RuntimeConfigReport{Status: "applied", Fingerprint: "abc"}
	cases := []struct {
		name          string
		expected      string
		report        store.RuntimeConfigReport
		reported      bool
		runtimeStatus string
		want          string
	}{
		{name: "nothing configured is not a problem", expected: "", want: "none"},
		{name: "configured but the runtime has never run",
			expected: "abc", runtimeStatus: "stopped", want: "pending_start"},
		{name: "running with no report yet is unverified, not failed",
			expected: "abc", runtimeStatus: "running", want: "unverified"},
		{name: "the Pod reported the current settings",
			expected: "abc", report: applied, reported: true, runtimeStatus: "running", want: "applied"},
		{name: "the Pod started before the settings changed",
			expected: "def", report: applied, reported: true, runtimeStatus: "running", want: "stale"},
		{name: "the Pod could not write the file",
			expected: "abc", report: store.RuntimeConfigReport{Status: "missing", Fingerprint: "abc"}, reported: true,
			runtimeStatus: "running", want: "failed"},
		{name: "an unreadable file is a failure too",
			expected: "abc", report: store.RuntimeConfigReport{Status: "unreadable", Fingerprint: "abc"}, reported: true,
			runtimeStatus: "running", want: "failed"},
		// A report that arrives for a runtime with no settings configured says
		// nothing interesting: there is nothing to be stale against.
		{name: "a report with nothing configured is still none",
			expected: "", report: applied, reported: true, runtimeStatus: "running", want: "none"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := injectionState(test.expected, test.report, test.reported, test.runtimeStatus); got != test.want {
				t.Fatalf("injectionState = %q, want %q", got, test.want)
			}
		})
	}
}

// The saved message has to say whether anything happened to the fleet, because
// "저장했습니다" alone is what made the runtime environment look broken before.
func TestRuntimeSettingsSavedMessage(t *testing.T) {
	cases := []struct {
		name   string
		result syncResult
		expect string
	}{
		{name: "nothing running says when it will apply", result: syncResult{}, expect: "새로 시작하는"},
		{name: "a push that reached Pods says they restart", result: syncResult{applied: 2}, expect: "재시작"},
		{name: "a push that failed does not read as success", result: syncResult{failed: 1}, expect: "전달하지 못했습니다"},
		{name: "an outdated CRD names the fix", result: syncResult{pruned: true}, expect: "crd.yaml"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := runtimeSettingsSaved(test.result); !strings.Contains(got, test.expect) {
				t.Fatalf("message %q does not mention %q", got, test.expect)
			}
		})
	}
}
