package execution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

func TestEventInputDescribesTheEvent(t *testing.T) {
	moment := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	input := eventInput("실패 원인을 조사하라.", store.PlatformEvent{
		Type: store.EventTaskFailed, SubjectType: "task", SubjectID: "task-1",
		Payload:   json.RawMessage(`{"reason":"모델 게이트웨이 응답 없음","title":"야간 점검"}`),
		CreatedAt: moment,
	})
	for _, want := range []string{"실패 원인을 조사하라.", "task.failed", "task task-1", "2026-08-17T09:30:00Z", "모델 게이트웨이 응답 없음", "야간 점검"} {
		if !strings.Contains(input, want) {
			t.Errorf("event input is missing %q:\n%s", want, input)
		}
	}
}

// The same event must always render the same prompt, or an agent's cache and an
// operator's reading of two runs would diverge for no reason.
func TestEventPayloadRendersInAStableOrder(t *testing.T) {
	raw := json.RawMessage(`{"zeta":1,"alpha":"a","mid":true}`)
	first := eventPayload(raw)
	for range 20 {
		if again := eventPayload(raw); again != first {
			t.Fatalf("payload rendering is unstable:\n%s\n---\n%s", first, again)
		}
	}
	if want := "  - alpha: a\n  - mid: true\n  - zeta: 1"; first != want {
		t.Fatalf("payload = %q, want %q", first, want)
	}
}

func TestEventPayloadToleratesNonObjects(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`), json.RawMessage(`"text"`), json.RawMessage(`[1,2]`)} {
		if got := eventPayload(raw); got != "" {
			t.Errorf("payload %q rendered %q, want empty", raw, got)
		}
	}
}

// An event trigger with no task template still has to say what happened.
func TestEventInputWithoutATemplateStillCarriesTheEvent(t *testing.T) {
	input := eventInput("   ", store.PlatformEvent{Type: store.EventRuntimeFailed, CreatedAt: time.Now()})
	if !strings.HasPrefix(input, "# 발생한 이벤트") {
		t.Fatalf("input should start with the event section:\n%s", input)
	}
	if strings.Contains(input, "대상:") {
		t.Fatalf("an event with no subject must not claim one:\n%s", input)
	}
}

func TestPublishableEventsAreRecognised(t *testing.T) {
	for _, value := range store.PublishableEvents {
		if !store.IsPublishableEvent(value) {
			t.Errorf("%q is published but not recognised", value)
		}
	}
	for _, value := range []string{"", "task.started", "TASK.COMPLETED", "runtime.failed "} {
		if store.IsPublishableEvent(value) {
			t.Errorf("%q must not be accepted as an event type", value)
		}
	}
}
