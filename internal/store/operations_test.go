package store

import (
	"strings"
	"testing"
)

// Recovering the wrong tasks in bulk is the expensive mistake here: a completed
// task would do its work twice, and a cancelled one was stopped by somebody.
func TestRequeueFilterNormalise(t *testing.T) {
	for _, status := range []string{TaskDeadLetter, TaskFailed} {
		if _, err := (RequeueFilter{Status: status}).normalise(); err != nil {
			t.Errorf("%s must be recoverable: %v", status, err)
		}
	}
	for _, status := range []string{"", TaskCompleted, TaskCancelled, TaskRunning, TaskQueued, TaskBlocked, "everything"} {
		if _, err := (RequeueFilter{Status: status}).normalise(); err == nil {
			t.Errorf("%q must not be requeued in bulk", status)
		}
	}
	// A bulk requeue fills the queue, so it is always bounded.
	for _, limit := range []int{0, -5, 100000} {
		normalised, err := (RequeueFilter{Status: TaskFailed, Limit: limit}).normalise()
		if err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
		if normalised.Limit <= 0 || normalised.Limit > 1000 {
			t.Errorf("limit %d became %d, which is not a bound", limit, normalised.Limit)
		}
	}
	if normalised, _ := (RequeueFilter{Status: TaskFailed, Limit: 50}).normalise(); normalised.Limit != 50 {
		t.Errorf("a caller's limit must be respected; got %d", normalised.Limit)
	}
}

// Retention deletes history that cannot be reconstructed, so the floors are the
// safeguard: a mistyped "3" must not take the audit trail with it.
func TestRetentionPolicyValidate(t *testing.T) {
	cases := []struct {
		name   string
		policy RetentionPolicy
		refuse string
	}{
		{name: "keeping everything is valid", policy: RetentionPolicy{}},
		{name: "a sensible policy is accepted",
			policy: RetentionPolicy{RunDays: 30, EventDays: 14, TaskDays: 30, AuditDays: 365}},
		{name: "audit has the longest floor",
			policy: RetentionPolicy{AuditDays: 29}, refuse: "감사 로그"},
		{name: "runs cannot be swept after a day",
			policy: RetentionPolicy{RunDays: 1}, refuse: "실행 기록"},
		{name: "events have a shorter floor but still one",
			policy: RetentionPolicy{EventDays: 2}, refuse: "이벤트"},
		{name: "tasks too", policy: RetentionPolicy{TaskDays: 6}, refuse: "작업"},
		{name: "a decade is the ceiling", policy: RetentionPolicy{AuditDays: 4000}, refuse: "최대"},
		// Zero is "keep", not "delete immediately" — the difference is the whole
		// deployment's history.
		{name: "zero on one field does not drag the others in",
			policy: RetentionPolicy{RunDays: 0, AuditDays: 90}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.policy.Validate()
			if test.refuse == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("the policy must be refused")
			}
			if !strings.Contains(err.Error(), test.refuse) {
				t.Fatalf("the message must name %s; got %q", test.refuse, err)
			}
		})
	}
}

// The pause carries a reason because the people whose work stopped are not the
// person who stopped it.
func TestOperationsSettingsValidate(t *testing.T) {
	if err := (OperationsSettings{Paused: true, Reason: "업그레이드"}).Validate(); err != nil {
		t.Fatalf("a normal pause must be accepted: %v", err)
	}
	if err := (OperationsSettings{Reason: strings.Repeat("가", 301)}).Validate(); err == nil {
		t.Fatal("an essay is not a reason")
	}
	if err := (OperationsSettings{Retention: RetentionPolicy{AuditDays: 1}}).Validate(); err == nil {
		t.Fatal("the retention policy must be validated with the rest")
	}
}
