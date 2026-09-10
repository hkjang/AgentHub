package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A tool result is the payload most likely to run past the scan limit — bounding
// them is what the limit is for — and the gateway said nothing about the ones
// that did.
//
// It returned the moment the findings were empty, so a 2 MB answer whose first
// 64 kB happened to be clean left the Pod log and the control plane looking
// exactly like a short answer read end to end: no entry, no finding, nothing.
// The operator deciding whether their limit is set too low reads that trail.
func TestAToolPayloadPastTheScanLimitIsReported(t *testing.T) {
	reported := make(chan map[string]any, 4)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RuntimeID string         `json:"runtimeId"`
			Event     map[string]any `json:"event"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the gateway reported something the control plane cannot read: %v", err)
		}
		reported <- body.Event
		w.WriteHeader(http.StatusAccepted)
	}))
	defer control.Close()

	inspect := scannerFor("rrn", "block")
	// Small enough that the fixture need not be a megabyte, and a limit an
	// operator can really set: Validate accepts anything from 0 to 4 MB.
	inspect.settings.MaxBytes = 64
	inspect.report = &reporter{url: control.URL, runtimeID: "rt-1", token: "s3cret", client: control.Client()}

	// Nothing sensitive anywhere in it. There is simply more of it than the limit.
	long := strings.Repeat("점검 항목을 순서대로 확인했습니다. ", 8)
	replacement, found := inspect.inspect(context.Background(), "jira", "read_issue", "응답", long)
	// The call itself is untouched: nothing was found, so the caller is handed the
	// same nil it always was and the body is not rewritten around a payload that
	// did not change.
	if found != nil {
		t.Errorf("a clean call was handed back as a finding: %+v", found)
	}
	if replacement != long {
		t.Error("the answer was rewritten although the scanner found nothing in it")
	}

	select {
	case event := <-reported:
		if event["truncated"] != true {
			t.Errorf("the report does not say the payload outran the limit: %v", event)
		}
		if findings, _ := event["findings"].([]any); len(findings) != 0 {
			t.Errorf("a clean payload was reported as carrying something: %v", findings)
		}
		if event["blocked"] != false {
			t.Errorf("a payload nothing was found in was reported as refused: %v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway read part of a tool payload and told nobody, so a partial scan looks exactly like a clean one")
	}

	// What fits is still silent. An entry per tool call would bury the findings
	// among the ordinary traffic, which is the state this gateway started from.
	if _, found := inspect.inspect(context.Background(), "jira", "read_issue", "응답", "확인했습니다."); found != nil {
		t.Errorf("a short clean payload came back as a finding: %+v", found)
	}
	select {
	case event := <-reported:
		t.Errorf("a payload read to its end and found clean was reported: %v", event)
	default:
	}
}
