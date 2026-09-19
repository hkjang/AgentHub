package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/dlp"
)

// The document the gateway posts has to be one the handler reads.
//
// The in-Pod gateway reports the same entry it writes to its own log, and that
// entry carries a discriminator — "event": "dlp" — the handler has no field for.
// Behind a decoder that refuses unknown keys, that was a 400 for every report
// the scanner ever made, and the gateway is built to ignore the answer. The
// live test proves it end to end against a database; this one runs everywhere,
// with the gateway's document spelled out as cmd/runtime-proxy's record() writes
// it, so the shape cannot drift apart again without a test noticing.
func TestTheGatewaysOwnReportIsAccepted(t *testing.T) {
	// One masked finding, recorded and not blocked, as the gateway serialises it.
	document := `{"runtimeId":"rt-1","event":{"event":"dlp","server":"jira","tool":"create_issue","direction":"요청",` +
		`"blocked":false,"findings":[{"class":"rrn","label":"주민등록번호","count":1,"action":"audit","sample":"900101-*******"}],"truncated":false}}`

	var report dlpReport
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-gateway/dlp-events", strings.NewReader(document))
	if !decodePodReport(recorder, request, &report) {
		t.Fatalf("the gateway's own report was refused: %s", recorder.Body)
	}
	if report.RuntimeID != "rt-1" || report.Event.Tool != "create_issue" || report.Event.Server != "jira" {
		t.Errorf("the report was read wrong: %+v", report)
	}
	if len(report.Event.Findings) != 1 || report.Event.Findings[0].Sample != "900101-*******" {
		t.Errorf("the finding did not survive decoding: %+v", report.Event.Findings)
	}
	if got := report.result().Outcome(); got != dlp.OutcomeAudited {
		t.Errorf("a finding the gateway only recorded is filed as %q", got)
	}
}

// What the trail files a report under is what the gateway did, and a report
// that describes nothing is refused before it can be filed as anything.
func TestAReportIsFiledUnderWhatTheGatewayDid(t *testing.T) {
	recorded := []dlp.Finding{{Class: "rrn", Action: dlp.Audit, Count: 1}}
	refused := []dlp.Finding{{Class: "rrn", Action: dlp.Block, Count: 1}}
	cases := []struct {
		name       string
		blocked    bool
		truncated  bool
		findings   []dlp.Finding
		reportable bool
		outcome    string
	}{
		{"기록만 한 발견", false, false, recorded, true, dlp.OutcomeAudited},
		{"거절된 호출", true, false, refused, true, dlp.OutcomeBlocked},
		{"한도까지만 읽은 깨끗한 페이로드", false, true, nil, true, dlp.OutcomeUnscanned},
		{"잘렸지만 발견이 있는 페이로드", false, true, recorded, true, dlp.OutcomeAudited},
		// The one the handler used to file as "audited": nothing found, nothing
		// cut short, nothing to say.
		{"아무것도 없는 보고", false, false, nil, false, ""},
		{"발견 없이 거절됐다는 보고", true, false, nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var report dlpReport
			report.Event.Blocked, report.Event.Truncated, report.Event.Findings = tc.blocked, tc.truncated, tc.findings
			result := report.result()
			if result.Reportable() != tc.reportable {
				t.Fatalf("reportable = %v, want %v", result.Reportable(), tc.reportable)
			}
			if tc.reportable && result.Outcome() != tc.outcome {
				t.Errorf("filed as %q, want %q", result.Outcome(), tc.outcome)
			}
		})
	}
}

// What the trail holds of a finding is the control plane's masking, not the
// Pod's word.
//
// The gateway masks the sample before it leaves the Pod, but the gateway is code
// in a Pod the agent runs in, reporting under a token that Pod holds — and the
// trail is exported as stored. A value sent in the sample's place, or in the
// label's, is filed as the scanner would have filed the finding; the report the
// real gateway makes is filed exactly as sent.
func TestTheTrailHoldsTheControlPlanesMaskingNotThePods(t *testing.T) {
	genuine := dlp.Scan(dlp.Settings{Enabled: true, Classes: map[string]string{"rrn": dlp.Audit}}, "주민번호 900101-1234568").Findings
	if len(genuine) != 1 {
		t.Fatalf("the scanner found %d findings in a line with one value", len(genuine))
	}
	var report dlpReport
	report.Event.Findings = genuine
	if filed := report.findings(); len(filed) != 1 || filed[0] != genuine[0] {
		t.Errorf("the gateway's own finding %+v is filed as %+v", genuine, filed)
	}

	report.Event.Findings = []dlp.Finding{{Class: "rrn", Label: "주민등록번호 900101-1234568", Count: 1, Action: dlp.Audit, Sample: "900101-1234568"}}
	filed := report.findings()
	if len(filed) != 1 {
		t.Fatalf("%d findings filed for one reported", len(filed))
	}
	if filed[0].Sample != genuine[0].Sample || filed[0].Label != genuine[0].Label {
		t.Errorf("the value the Pod put in the report is filed as sent: %+v", filed[0])
	}
	if raw := fmt.Sprintf("%+v", filed); strings.Contains(raw, "1234568") {
		t.Errorf("the value reaches the trail: %s", raw)
	}

	// The class is the Pod's word too: a value sent there is not filed as the
	// class, nor as the label of a class nobody knows.
	report.Event.Findings = []dlp.Finding{{Class: "900101-1234568", Label: "900101-1234568", Count: 1, Action: dlp.Audit, Sample: "900101-1234568"}}
	filed = report.findings()
	if len(filed) != 1 || filed[0].Class != dlp.UnknownClass {
		t.Errorf("a finding of a class this build does not know is filed as %+v", filed)
	}
	if raw := fmt.Sprintf("%+v", filed); strings.Contains(raw, "1234568") {
		t.Errorf("the value in the class's place reaches the trail: %s", raw)
	}

	// The bound on one report is still the bound.
	report.Event.Findings = make([]dlp.Finding, maxReportedFindings+5)
	if filed := report.findings(); len(filed) != maxReportedFindings {
		t.Errorf("%d findings filed of a report carrying %d", len(filed), maxReportedFindings+5)
	}
}
