package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// capturePodLog reads back what this Pod would have written to its log.
//
// The gateway's only durable trail is the standard logger — kubectl logs is what
// an operator actually reads — so a test about what the operator can see has to
// read the same stream. The flags go to zero for the duration so one log.Printf
// is one line and a count of lines means something; both the destination and the
// flags are put back before the next test runs.
func capturePodLog(t *testing.T) func() []string {
	t.Helper()
	var written bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&written)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	})
	return func() []string {
		trail := strings.TrimSuffix(written.String(), "\n")
		if trail == "" {
			return nil
		}
		return strings.Split(trail, "\n")
	}
}

// controlPlaneAnswering stands in for the control plane that receives reports and
// answers with one fixed status. The body is the kind of thing a real answer
// carries — an error document, or a copy of what was just reported — so a test
// can tell whether the gateway put any of it in the Pod log.
func controlPlaneAnswering(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"runtime token rejected","echo":"900101-1234568"}`))
	}))
}

// reportTo wires one scanner to one control plane. The credentials are the shape
// the real reporter is built with, so a test can assert the token stays out of
// the log rather than assume it.
func reportTo(inspect *scanner, control *httptest.Server) {
	inspect.report = &reporter{url: control.URL, runtimeID: "rt-1", token: "s3cret",
		client: control.Client()}
}

// The payload carries one 주민등록번호, the class the scanner below is configured
// for. Everything in this file goes through the real inspect so the report is
// produced the way a tool call produces it.
const reportedPayload = "고객 900101-1234568 문의"

// A report the control plane threw away is the one case the operator cannot see
// from either end: the Pod believes it reported, and the dlp.tool audit row that
// should hold the finding is simply absent.
//
// The gateway closed the body and said nothing, so a runtime token the control
// plane no longer accepts, or a control plane answering 500 for an hour, looked
// exactly like a delivered report. The operator looking for the missing audit
// row has nothing in the Pod log to connect it to.
func TestARefusedDLPReportNamesTheStatusInThePodLog(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			readLog := capturePodLog(t)
			control := controlPlaneAnswering(status)
			defer control.Close()

			inspect := scannerFor("rrn", "redact")
			reportTo(inspect, control)
			if _, found := inspect.inspect(context.Background(), "jira", "create_issue", "요청", reportedPayload); found == nil {
				t.Fatal("the fixture no longer produces a finding, so nothing was reported at all")
			}

			lines := readLog()
			if len(lines) != 2 {
				t.Fatalf("a refused report wrote %d lines, want the finding and the refusal: %q", len(lines), lines)
			}
			refusal := lines[1]
			if !strings.Contains(refusal, strconv.Itoa(status)) {
				t.Errorf("the control plane refused the report with %d and the Pod log does not say so: %q", status, refusal)
			}
			// Distinct from the line a transport error writes: one says the report
			// never arrived, the other that it arrived and was thrown away, and the
			// operator acts differently on each.
			if strings.Contains(refusal, "report failed") {
				t.Errorf("a refusal is logged as a delivery failure, so the two cases cannot be told apart: %q", refusal)
			}
			if strings.Contains(refusal, "s3cret") {
				t.Errorf("the runtime token is in the Pod log: %q", refusal)
			}
			if strings.Contains(refusal, "runtime token rejected") {
				t.Errorf("the control plane's answer body is in the Pod log: %q", refusal)
			}
			// The answer this control plane gives quotes back what was reported, which
			// is how the value would get into the log by way of the body.
			for _, line := range lines {
				if strings.Contains(line, "900101-1234568") {
					t.Errorf("the detected value is in the Pod log: %q", line)
				}
			}
		})
	}
}

// What the control plane accepted needs no second line. An entry per delivered
// report would double this gateway's ordinary output and bury the findings, which
// is the state the DLP log started from.
//
// 299 is in the table on purpose: the gateway is not meant to know a list of
// agreeable status codes, it is meant to know the 2xx range, and a control plane
// is free to answer with one this gateway has never seen.
func TestAnAcceptedDLPReportLeavesThePodLogQuiet(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent, 299} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			readLog := capturePodLog(t)
			control := controlPlaneAnswering(status)
			defer control.Close()

			inspect := scannerFor("rrn", "redact")
			reportTo(inspect, control)
			if _, found := inspect.inspect(context.Background(), "jira", "create_issue", "요청", reportedPayload); found == nil {
				t.Fatal("the fixture no longer produces a finding, so nothing was reported at all")
			}

			lines := readLog()
			if len(lines) != 1 {
				t.Fatalf("an accepted report wrote %d lines, want the finding alone: %q", len(lines), lines)
			}
			if !strings.HasPrefix(lines[0], "agenthub-dlp {") {
				t.Errorf("the one line is not the finding entry: %q", lines[0])
			}
		})
	}
}

// Reporting stays best effort. A tool call that was already refused must not also
// fail because the report could not be delivered, and one that was allowed must
// not change shape because the control plane was unhappy — the caller is handed
// the same redacted payload and the same result either way.
func TestARefusedReportDoesNotChangeWhatTheToolCallSees(t *testing.T) {
	readLog := capturePodLog(t)
	defer func() {
		// Read it so a failure here is not silently swallowed by the buffer, and so
		// this test states that both branches did report.
		if lines := readLog(); len(lines) != 3 {
			t.Errorf("want the two findings and one refusal, got %d lines: %q", len(lines), lines)
		}
	}()

	accepting := controlPlaneAnswering(http.StatusAccepted)
	defer accepting.Close()
	refusing := controlPlaneAnswering(http.StatusInternalServerError)
	defer refusing.Close()

	acceptedInspect := scannerFor("rrn", "redact")
	reportTo(acceptedInspect, accepting)
	acceptedText, acceptedResult := acceptedInspect.inspect(context.Background(), "jira", "create_issue", "요청", reportedPayload)

	refusedInspect := scannerFor("rrn", "redact")
	reportTo(refusedInspect, refusing)
	refusedText, refusedResult := refusedInspect.inspect(context.Background(), "jira", "create_issue", "요청", reportedPayload)

	if acceptedText != refusedText {
		t.Errorf("the payload differs when the report was refused: %q vs %q", acceptedText, refusedText)
	}
	if !reflect.DeepEqual(acceptedResult, refusedResult) {
		t.Errorf("the scan result differs when the report was refused: %+v vs %+v", acceptedResult, refusedResult)
	}
	if acceptedResult == nil || acceptedText == reportedPayload {
		t.Fatalf("the fixture stopped redacting, so this test compares two no-ops: %q %+v", acceptedText, acceptedResult)
	}
}
