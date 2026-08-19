package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/dlp"
)

// Scanning tool calls on their way out of the Pod.
//
// The control plane scans what it sends to a model, but a tool call never passes
// through the control plane: the agent talks to this gateway and the gateway
// talks to the MCP server. So this is where a customer record on its way into a
// ticket, or a credential on its way into a chat message, has to be caught —
// and, like the tool policy, it is here because the agent process cannot route
// around it.
//
// The detectors are the same package the control plane uses. Two implementations
// of "what is a resident registration number" would agree until they did not.

const (
	envDLP = "AGENTHUB_DLP"
	// envDLPReportURL is where findings are reported. It reuses the approval
	// endpoint's base URL and the runtime's own token: a scanner whose findings
	// stay in a Pod log is one nobody can report on.
	dlpReportPath = "/api/v1/runtime-gateway/dlp-events"
)

// scanner inspects tool traffic and reports what it finds.
type scanner struct {
	settings dlp.Settings
	// report is nil when the control plane's address was not configured, in which
	// case findings are logged and nothing else. Scanning still happens: local
	// enforcement without central reporting is worth more than neither.
	report *reporter
}

// loadScanner reads the configuration the operator handed us.
func loadScanner(raw string) (*scanner, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var settings dlp.Settings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, err
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, nil
	}
	return &scanner{settings: settings, report: newReporter()}, nil
}

// inspect scans one payload and returns what should be sent instead, or a
// refusal.
func (s *scanner) inspect(ctx context.Context, server, tool, direction, text string) (string, *dlp.Result) {
	if s == nil || text == "" {
		return text, nil
	}
	result := dlp.Scan(s.settings, text)
	if len(result.Findings) == 0 {
		return text, nil
	}
	dlp.SortFindings(result.Findings)
	s.record(ctx, server, tool, direction, result)
	if result.Blocked {
		return text, &result
	}
	return result.Text, &result
}

// record sends the finding to the control plane and logs it locally.
//
// What leaves the Pod is the class, the count and the masked sample the scanner
// produced — never the value. A DLP trail that quotes what it found has moved the
// problem rather than solved it.
func (s *scanner) record(ctx context.Context, server, tool, direction string, result dlp.Result) {
	entry := map[string]any{
		"event": "dlp", "server": server, "tool": tool, "direction": direction,
		"blocked": result.Blocked, "findings": result.Findings, "truncated": result.Truncated,
	}
	raw, _ := json.Marshal(entry)
	log.Printf("agenthub-dlp %s", raw)
	if s.report == nil {
		return
	}
	s.report.send(ctx, entry)
}

// reporter posts findings to the control plane.
type reporter struct {
	url       string
	runtimeID string
	token     string
	client    *http.Client
}

func newReporter() *reporter {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(envApprovalURL)), "/")
	runtimeID := strings.TrimSpace(os.Getenv(envRuntimeID))
	token := strings.TrimSpace(os.Getenv(envRuntimeToken))
	if base == "" || runtimeID == "" || token == "" {
		return nil
	}
	return &reporter{url: base + dlpReportPath, runtimeID: runtimeID, token: token,
		client: &http.Client{Timeout: 10 * time.Second}}
}

// send is best effort and never blocks the call it describes.
//
// A tool call that was already refused must not also fail because the report
// could not be delivered, and one that was allowed must not be held up by it. The
// finding is in the Pod log either way.
func (r *reporter) send(ctx context.Context, entry map[string]any) {
	body, err := json.Marshal(map[string]any{"runtimeId": r.runtimeID, "event": entry})
	if err != nil {
		return
	}
	request, err := http.NewRequestWithContext(context.WithoutCancel(ctx), http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+r.token)
	response, err := r.client.Do(request)
	if err != nil {
		log.Printf("agenthub-dlp report failed: %v", err)
		return
	}
	_ = response.Body.Close()
}
