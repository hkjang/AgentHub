package api

import (
	"os"
	"strings"
	"testing"
)

// Every way a webhook is turned away has to reach the person who owns the
// trigger.
//
// Each rejection wrote a line in the server log and nothing else, so a sender
// calling with the wrong secret for two days looked exactly like a trigger
// nobody had wired up: the caller saw 401, the owner saw an empty trigger, and
// neither could see the other's half. A rejection path added later and left out
// of this is the same silence returning.
func TestEveryWebhookRejectionIsRecorded(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) triggerWebhook(")
	if at < 0 {
		t.Fatal("the webhook handler is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\nfunc "); end >= 0 {
		handler = handler[:end]
	}

	// Every refusal in the handler, counted by the answers it can send.
	refusals := strings.Count(handler, "writeError(w, http.StatusUnauthorized") +
		strings.Count(handler, "writeError(w, http.StatusConflict")
	recorded := strings.Count(handler, "s.rejectedWebhook(")
	if refusals == 0 {
		t.Fatal("the handler refuses nothing; this guard is reading the wrong function")
	}
	if recorded < refusals {
		t.Errorf("웹훅을 거절하는 경로가 %d개인데 %d개만 기록합니다 — 기록되지 않는 거절은 보낸 쪽만 알고 트리거 주인은 모릅니다", refusals, recorded)
	}
}

// TestARejectionTellsTheCallerNothingNew keeps the reasons on the owner's side.
// The person knocking is told the same thing they were told before; the detail
// is for whoever owns the trigger.
func TestARejectionTellsTheCallerNothingNew(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) triggerWebhook(")
	handler := source[at:]
	if end := strings.Index(handler, "\nfunc "); end >= 0 {
		handler = handler[:end]
	}
	// The unauthorised answers all say the same sentence, whatever the reason.
	for _, reason := range []string{"서명이 맞지 않습니다", "서명을 확인할 설정이 없습니다"} {
		for _, line := range strings.Split(handler, "\n") {
			if strings.Contains(line, "writeError(") && strings.Contains(line, reason) {
				t.Errorf("거절 사유가 요청자에게 그대로 전달됩니다: %s", strings.TrimSpace(line))
			}
		}
	}
}
