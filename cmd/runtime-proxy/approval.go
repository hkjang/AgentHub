package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The approval gate.
//
// Approval used to be something the agent volunteered: its goal asked it to
// declare a state-changing action and wait for a decision. An agent that simply
// called the tool went around the gate, and nothing enforced the "approval
// required" flag an administrator had set on an MCP server.
//
// The gate lives here instead, in front of the call, because this gateway is the
// only thing in the Pod the agent process cannot route around: it holds the
// JSON-RPC request open, asks the control plane to create an approval, waits for
// a person, and then either forwards the call or answers with a refusal the model
// can read.
//
// It fails closed. If the control plane cannot be reached, or the wait runs out,
// the call does not happen.

const (
	envApprovalURL   = "AGENTHUB_APPROVAL_URL"
	envRuntimeID     = "AGENTHUB_RUNTIME_ID"
	envRuntimeToken  = "AGENTHUB_RUNTIME_TOKEN"
	envApprovalWait  = "AGENTHUB_APPROVAL_WAIT_SECONDS"
	envApprovalPoll  = "AGENTHUB_APPROVAL_POLL_SECONDS"
	defaultWait      = 15 * time.Minute
	defaultPoll      = 3 * time.Second
	maxArgumentChars = 2000
)

// approvalDecision is what the gate concluded.
type approvalDecision string

const (
	approvalGranted  approvalDecision = "approved"
	approvalRejected approvalDecision = "rejected"
	approvalExpired  approvalDecision = "expired"
	approvalBroken   approvalDecision = "unavailable"
)

// approver asks the control plane for a decision and waits for it.
type approver struct {
	baseURL   string
	runtimeID string
	token     string
	client    *http.Client
	wait      time.Duration
	poll      time.Duration
}

// newApprover reads the gate's configuration. It returns nil when the runtime has
// no gated tool, which is the common case and costs nothing.
func newApprover() *approver {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv(envApprovalURL)), "/")
	runtimeID := strings.TrimSpace(os.Getenv(envRuntimeID))
	token := strings.TrimSpace(os.Getenv(envRuntimeToken))
	if base == "" || runtimeID == "" || token == "" {
		return nil
	}
	return &approver{
		baseURL: base, runtimeID: runtimeID, token: token,
		client: &http.Client{Timeout: 30 * time.Second},
		wait:   durationFromEnv(envApprovalWait, defaultWait),
		poll:   durationFromEnv(envApprovalPoll, defaultPoll),
	}
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// decide creates the approval and blocks until somebody decides, the wait runs
// out, or the request is abandoned by the caller.
func (a *approver) decide(ctx context.Context, server, tool string, arguments []byte) (approvalDecision, string, error) {
	id, status, err := a.request(ctx, server, tool, arguments)
	if err != nil {
		return approvalBroken, "", err
	}
	deadline := time.Now().Add(a.wait)
	for {
		switch status {
		case "approved":
			return approvalGranted, id, nil
		case "rejected", "cancelled":
			return approvalRejected, id, nil
		}
		if time.Now().After(deadline) {
			return approvalExpired, id, nil
		}
		select {
		case <-ctx.Done():
			return approvalExpired, id, ctx.Err()
		case <-time.After(a.poll):
		}
		status, err = a.status(ctx, id)
		if err != nil {
			// A hiccup while polling is not a decision. Keep waiting until the
			// deadline rather than letting the call through.
			if time.Now().After(deadline) {
				return approvalExpired, id, err
			}
		}
	}
}

type approvalResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (a *approver) request(ctx context.Context, server, tool string, arguments []byte) (string, string, error) {
	payload, err := json.Marshal(map[string]any{
		"runtimeId": a.runtimeID, "server": server, "tool": tool,
		"arguments": trimArguments(arguments),
	})
	if err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/v1/runtime-gateway/tool-approvals", bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(request)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("control plane answered %d", response.StatusCode)
	}
	var decoded approvalResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", "", err
	}
	if decoded.ID == "" {
		return "", "", errors.New("control plane returned no approval id")
	}
	return decoded.ID, decoded.Status, nil
}

func (a *approver) status(ctx context.Context, id string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/api/v1/runtime-gateway/tool-approvals/"+id, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	response, err := a.client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("control plane answered %d", response.StatusCode)
	}
	var decoded approvalResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", err
	}
	return decoded.Status, nil
}

// trimArguments renders the call's arguments for the person deciding. It is
// bounded: a reviewer needs to see what the call would do, and a tool argument
// can be a whole file.
func trimArguments(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", " "); err != nil {
		pretty.Reset()
		pretty.Write(raw)
	}
	text := pretty.String()
	if len(text) > maxArgumentChars {
		return text[:maxArgumentChars] + "\n… (이하 생략)"
	}
	return text
}
