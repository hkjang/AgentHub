package api

import (
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

// Cutting a report down for the trail must not change the scan it was cut from.
//
// dlpReport.result() hands dlp.Result the very slice the request decoded into,
// and Result.Outcome() decides 가리고 전송 by comparing each finding's Action
// against dlp.Redact. Cut those strings in place and the length of a Pod's string
// can change what the trail says the Pod did — and the log line, which reads the
// same fields back off input.Event, would disagree with the audit row. So the cut
// produces a copy, and the scanned result is still readable afterwards.
func TestCuttingAReportDownLeavesTheScanAlone(t *testing.T) {
	long := strings.Repeat("한", maxReportedTextLen+1)
	var report dlpReport
	report.Event.Findings = []dlp.Finding{
		{Class: "rrn", Label: "주민등록번호", Count: 1, Action: dlp.Redact, Sample: "900101-*******"},
		{Class: long, Label: long, Count: 2, Action: long, Sample: long},
	}
	result := report.result()

	clamped := clampReportedFindings(result.Findings)

	if got := result.Outcome(); got != dlp.OutcomeRedacted {
		t.Errorf("after the cut the scan reads as %q; the cut reached back into what was scanned", got)
	}
	if report.Event.Findings[1].Sample != long || report.Event.Findings[1].Action != long {
		t.Errorf("the decoded report was shortened under the log line that reads it: %d runes of sample",
			len([]rune(report.Event.Findings[1].Sample)))
	}
	if clamped[0] != report.Event.Findings[0] {
		t.Errorf("a finding under the limit was changed: %+v", clamped[0])
	}
	// Cut on a rune boundary, not a byte one: this platform's own labels are Korean.
	edge := strings.Repeat("한", maxReportedTextLen)
	if clamped[1].Sample != edge || clamped[1].Label != edge || clamped[1].Class != edge || clamped[1].Action != edge {
		t.Errorf("an oversized finding was not cut to %d runes on a rune boundary: %d runes of sample",
			maxReportedTextLen, len([]rune(clamped[1].Sample)))
	}
	if clamped[1].Count != 2 {
		t.Errorf("the cut changed a field that is not a string: count = %d", clamped[1].Count)
	}
	// A report that carried no findings array is stored as one that had none, not
	// as one that had an empty list.
	if clampReportedFindings(nil) != nil {
		t.Error("a report with no findings array came back with one")
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
