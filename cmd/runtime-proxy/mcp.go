package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hkjang/AgentHub/internal/policy"
)

// The MCP egress gateway.
//
// A tool policy the agent process could ignore would not be a policy, so the
// runtime never talks to an MCP server directly: its generated configuration
// points at this gateway, which sits in the same Pod and is the only thing that
// knows the upstream address. That also keeps MCP credentials out of the agent
// container entirely — they are attached here, on the way out.

// mcpUpstream is one MCP server this gateway fronts.
type mcpUpstream struct {
	Name     string `json:"name"`
	Upstream string `json:"upstream"`
	// AuthHeader and CredentialEnv attach the credential on the way out. The
	// value is read from the environment rather than the config so it never
	// appears in a ConfigMap or in a process listing of the agent container.
	AuthHeader    string `json:"authHeader"`
	AuthTemplate  string `json:"authTemplate"`
	CredentialEnv string `json:"credentialEnv"`
	// Mode is allow or deny. An allow policy with no tools listed permits
	// nothing, which is the safe reading of "allow exactly these".
	Mode  string   `json:"mode"`
	Tools []string `json:"tools"`
	// ApprovalTools need a person's decision before they run, and
	// ApprovalRequired gates every tool on this server. The gate is here rather
	// than in the agent's prompt because this is the only place the agent cannot
	// route around.
	ApprovalTools    []string `json:"approvalTools"`
	ApprovalRequired bool     `json:"approvalRequired"`
	// PolicyDenied and PolicyGated are patterns compiled from the platform-wide
	// policy — the rules an agent's owner cannot change. They are patterns rather
	// than names because the tool list is not known when a runtime is provisioned,
	// and they are matched with the control plane's own matcher so both ends
	// decide the same way.
	PolicyDenied  []string `json:"policyDenied,omitempty"`
	PolicyGated   []string `json:"policyGated,omitempty"`
	PolicyDenyAll bool     `json:"policyDenyAll,omitempty"`
}

// needsApproval reports whether a call has to wait for a person.
func (u mcpUpstream) needsApproval(tool string) bool {
	return u.ApprovalRequired || contains(u.ApprovalTools, tool) || policy.MatchTool(u.PolicyGated, u.Name, tool)
}

// permits reports whether a tool may be called.
//
// The platform's policy is checked first and separately: an agent's own allow
// list is its owner's statement about what the agent needs, and it cannot widen
// what the platform forbids.
func (u mcpUpstream) permits(tool string) bool {
	if u.PolicyDenyAll || policy.MatchTool(u.PolicyDenied, u.Name, tool) {
		return false
	}
	switch u.Mode {
	case "allow":
		return contains(u.Tools, tool)
	case "deny":
		return !contains(u.Tools, tool)
	default:
		// No per-agent policy configured: the bundle binding and the platform
		// policy above are the only restrictions.
		return true
	}
}

// restricts reports whether anything at all limits what this agent may call, and
// therefore whether the advertised tool list has to be filtered.
func (u mcpUpstream) restricts() bool {
	return u.Mode != "" || u.PolicyDenyAll || len(u.PolicyDenied) > 0
}

// deniedByPlatform separates the two refusals in the audit trail and in what the
// agent is told: "your owner did not give you this" and "the platform forbids
// this" need different follow-ups.
func (u mcpUpstream) deniedByPlatform(tool string) bool {
	return u.PolicyDenyAll || policy.MatchTool(u.PolicyDenied, u.Name, tool)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// rpcRequest is the part of a JSON-RPC message the gateway needs to decide.
// isBatch reports whether the body is a JSON-RPC batch: a JSON array at the top
// level, whatever is inside it.
func isBatch(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '['
}

// methodAndTool reads the method and the tool name from the message as JSON,
// independently of whether the rest of it fits rpcRequest.
func methodAndTool(body []byte) (string, string, bool) {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return "", "", false
	}
	var method string
	if err := json.Unmarshal(message["method"], &method); err != nil {
		return "", "", false
	}
	var params struct {
		Name string `json:"name"`
	}
	// A tools/call with unreadable params is still a tools/call, and it is answered
	// by the policy for the empty tool name rather than by being waved through.
	_ = json.Unmarshal(message["params"], &params)
	return method, params.Name, true
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
		// Arguments are shown to whoever decides on a gated call: "delete_branch"
		// on its own is not enough to approve or refuse.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

func loadUpstreams(raw string) ([]mcpUpstream, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var upstreams []mcpUpstream
	if err := json.Unmarshal([]byte(raw), &upstreams); err != nil {
		return nil, fmt.Errorf("parse MCP gateway config: %w", err)
	}
	for i, upstream := range upstreams {
		if upstream.Name == "" || upstream.Upstream == "" {
			return nil, fmt.Errorf("MCP gateway entry %d needs a name and an upstream", i)
		}
	}
	return upstreams, nil
}

// mcpGateway serves /mcp/<name> for each configured upstream.
func mcpGateway(upstreams []mcpUpstream, auditor func(entry map[string]any)) http.Handler {
	return mcpGatewayWithApprover(upstreams, auditor, newApprover())
}

// mcpGatewayWithApprover is the constructor the tests drive, so the gate can be
// exercised against a stand-in control plane.
func mcpGatewayWithApprover(upstreams []mcpUpstream, auditor func(entry map[string]any), gate *approver) http.Handler {
	return mcpGatewayWith(upstreams, auditor, gate, nil)
}

// mcpGatewayWith is the full constructor: the approval gate and the content
// scanner are both stand-ins the tests can supply.
func mcpGatewayWith(upstreams []mcpUpstream, auditor func(entry map[string]any), gate *approver, inspect *scanner) http.Handler {
	byName := make(map[string]mcpUpstream, len(upstreams))
	for _, upstream := range upstreams {
		byName[upstream.Name] = upstream
	}
	client := &http.Client{Timeout: 5 * time.Minute}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
		upstream, ok := byName[name]
		if !ok {
			http.Error(w, "unknown MCP server", http.StatusNotFound)
			return
		}
		// A request body is bounded: everything here is a JSON-RPC message, and
		// an unbounded read would let one call exhaust the sidecar.
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			http.Error(w, "request body could not be read", http.StatusBadRequest)
			return
		}

		// A batch is a way past every check below.
		//
		// JSON-RPC 2.0 lets a client send an array of requests, and an array does
		// not decode into one request struct — so Method stayed empty, every check
		// compared against "", and the body went upstream unread with the
		// credential attached. The tool policy, the approval gate and the content
		// scanner were one bracket away from not applying.
		//
		// Refused rather than policed element by element: the current revision of
		// MCP has removed batching, so nothing legitimate needs it, and a refusal
		// that says so is better than a second policing path to keep in step with
		// this one.
		if isBatch(body) {
			auditor(map[string]any{"server": name, "decision": "denied", "reason": "batch"})
			writeRPCError(w, nil, -32600, "이 게이트웨이는 JSON-RPC 일괄 요청을 처리하지 않습니다. 도구 호출은 한 번에 하나씩 보내 주세요.")
			return
		}

		// Read the method and the tool name out of the message itself rather than
		// out of a struct that has to fit all of it. A field elsewhere in the
		// message with an unexpected type used to fail the whole decode and leave
		// the method empty, which is the same bypass wearing a different hat.
		var request rpcRequest
		_ = json.Unmarshal(body, &request)
		if method, tool, ok := methodAndTool(body); ok {
			request.Method, request.Params.Name = method, tool
		}
		if request.Method == "tools/call" && !upstream.permits(request.Params.Name) {
			platform := upstream.deniedByPlatform(request.Params.Name)
			auditor(map[string]any{"server": name, "tool": request.Params.Name, "decision": "denied", "mode": upstream.Mode, "policy": platform})
			message := fmt.Sprintf("도구 %q 는 이 Agent의 MCP 도구 정책에 의해 차단되었습니다.", request.Params.Name)
			if platform {
				message = fmt.Sprintf("도구 %q 는 플랫폼 정책에 의해 차단되었습니다. 관리자에게 문의하세요.", request.Params.Name)
			}
			writeRPCError(w, request.ID, -32601, message)
			return
		}
		if request.Method == "tools/call" && upstream.needsApproval(request.Params.Name) {
			if gate == nil {
				// Configured to need a decision with no way to ask for one: refuse.
				// Letting the call through would make the gate advisory again.
				auditor(map[string]any{"server": name, "tool": request.Params.Name, "decision": "denied", "reason": "approval_unavailable"})
				writeRPCError(w, request.ID, -32003, fmt.Sprintf("도구 %q 는 승인이 필요하지만 이 Runtime에서 승인 요청 경로가 설정되지 않았습니다.", request.Params.Name))
				return
			}
			decision, approvalID, err := gate.decide(r.Context(), name, request.Params.Name, request.Params.Arguments)
			entry := map[string]any{"server": name, "tool": request.Params.Name, "approvalId": approvalID, "decision": string(decision)}
			if err != nil {
				entry["error"] = err.Error()
			}
			auditor(entry)
			switch decision {
			case approvalGranted:
				// Fall through to the upstream call.
			case approvalRejected:
				writeRPCError(w, request.ID, -32004, fmt.Sprintf("도구 %q 실행이 검토자에 의해 거절되었습니다.", request.Params.Name))
				return
			case approvalExpired:
				writeRPCError(w, request.ID, -32005, fmt.Sprintf("도구 %q 실행 승인이 대기 시간 안에 처리되지 않았습니다. 승인 후 다시 시도하세요.", request.Params.Name))
				return
			default:
				writeRPCError(w, request.ID, -32003, fmt.Sprintf("도구 %q 실행 승인을 요청할 수 없어 호출을 차단했습니다.", request.Params.Name))
				return
			}
		}
		// The arguments are scanned before the call leaves the Pod. A tool call
		// never passes through the control plane, so this is the only place a
		// customer record on its way into a ticket can be caught.
		if request.Method == "tools/call" && len(request.Params.Arguments) > 0 {
			replacement, found := inspect.inspect(r.Context(), name, request.Params.Name, "요청", string(request.Params.Arguments))
			if found != nil {
				auditor(map[string]any{"server": name, "tool": request.Params.Name,
					"decision": scanDecision(found), "dlp": found.Summary()})
				if found.Blocked {
					writeRPCError(w, request.ID, -32006, found.Reason+" (도구 "+request.Params.Name+")")
					return
				}
				// The whole body is rewritten rather than just the arguments: the
				// upstream reads the body, not our parsed copy of it.
				rewritten, err := replaceArguments(body, replacement)
				if err == nil {
					body = rewritten
				} else {
					// A body we cannot rewrite is a body we cannot redact, and sending
					// it unchanged would be the one outcome the setting forbids.
					writeRPCError(w, request.ID, -32006, "민감정보를 제거할 수 없어 호출을 차단했습니다.")
					return
				}
			}
		}
		if request.Method == "tools/call" {
			auditor(map[string]any{"server": name, "tool": request.Params.Name, "decision": "allowed", "mode": upstream.Mode})
		}

		outbound, err := http.NewRequestWithContext(r.Context(), r.Method, upstream.Upstream, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "upstream request could not be built", http.StatusBadGateway)
			return
		}
		copyHeaders(outbound.Header, r.Header)
		// The agent never holds the credential; it is attached here.
		outbound.Header.Del("Authorization")
		if upstream.AuthHeader != "" && upstream.CredentialEnv != "" {
			if secret := os.Getenv(upstream.CredentialEnv); secret != "" {
				value := upstream.AuthTemplate
				if value == "" {
					value = "%s"
				}
				outbound.Header.Set(upstream.AuthHeader, strings.ReplaceAll(value, "%s", secret))
			}
		}

		response, err := client.Do(outbound)
		if err != nil {
			http.Error(w, "MCP server is unreachable", http.StatusBadGateway)
			return
		}
		defer func() { _ = response.Body.Close() }()

		// tools/list is the only response the gateway rewrites: a tool the agent
		// may not call must not be advertised to it either, or the model will
		// keep planning around something that always fails.
		// A tool the agent may not call must not be advertised to it either, and
		// that is as true of a platform denial as of the agent's own list — the
		// model would otherwise keep planning around something that always fails.
		if request.Method == "tools/list" && upstream.restricts() {
			filterToolList(w, response, upstream)
			return
		}
		// What comes back is scanned too, when configured: a tool that returns a
		// customer record hands it straight to the model, and from there into a run
		// transcript that far more people can read.
		if inspect != nil && inspect.settings.ScanResponses && request.Method == "tools/call" {
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<20))
			if readErr != nil {
				http.Error(w, "MCP response could not be read", http.StatusBadGateway)
				return
			}
			replacement, found := inspect.inspect(r.Context(), name, request.Params.Name, "응답", string(raw))
			if found != nil && found.Blocked {
				auditor(map[string]any{"server": name, "tool": request.Params.Name, "decision": "denied", "dlp": found.Summary(), "direction": "response"})
				writeRPCError(w, request.ID, -32006, found.Reason+" (도구 "+request.Params.Name+" 응답)")
				return
			}
			copyHeaders(w.Header(), response.Header)
			w.Header().Set("Content-Length", fmt.Sprint(len(replacement)))
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write([]byte(replacement))
			return
		}
		copyHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
}

// replaceArguments puts redacted arguments back into the JSON-RPC body.
//
// The body is rewritten rather than re-marshalled from a parsed struct so that
// everything the gateway does not understand — fields a newer MCP version added,
// the caller's own metadata — survives the trip unchanged.
func replaceArguments(body []byte, arguments string) ([]byte, error) {
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	params, ok := envelope["params"].(map[string]any)
	if !ok {
		return nil, errors.New("no params to rewrite")
	}
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return nil, err
	}
	params["arguments"] = decoded
	return json.Marshal(envelope)
}

// filterToolList rewrites a tools/list result. Streamable HTTP may answer with
// either JSON or a single-event SSE frame, so both are handled.
func filterToolList(w http.ResponseWriter, response *http.Response, upstream mcpUpstream) {
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		http.Error(w, "MCP response could not be read", http.StatusBadGateway)
		return
	}
	contentType := response.Header.Get("Content-Type")
	var rewritten []byte
	switch {
	case strings.HasPrefix(contentType, "text/event-stream"):
		rewritten = rewriteEventStream(raw, upstream)
	default:
		rewritten = rewriteToolsPayload(raw, upstream)
	}
	copyHeaders(w.Header(), response.Header)
	w.Header().Set("Content-Length", fmt.Sprint(len(rewritten)))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(rewritten)
}

// rewriteEventStream filters the JSON payload of every data: line, leaving the
// framing untouched.
func rewriteEventStream(raw []byte, upstream mcpUpstream) []byte {
	var out bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			trimmed := strings.TrimSpace(payload)
			if strings.HasPrefix(trimmed, "{") {
				out.WriteString("data: ")
				out.Write(rewriteToolsPayload([]byte(trimmed), upstream))
				out.WriteString("\n")
				continue
			}
		}
		out.WriteString(line)
		out.WriteString("\n")
	}
	if scanner.Err() != nil {
		return raw
	}
	return out.Bytes()
}

// rewriteToolsPayload drops the tools the policy forbids. Anything that does not
// look like a tools/list result is returned untouched, so an error response or
// an unexpected shape passes through rather than being mangled.
func rewriteToolsPayload(raw []byte, upstream mcpUpstream) []byte {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw
	}
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		return raw
	}
	tools, ok := result["tools"].([]any)
	if !ok {
		return raw
	}
	kept := make([]any, 0, len(tools))
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := tool["name"].(string); ok && upstream.permits(name) {
			kept = append(kept, entry)
		}
	}
	result["tools"] = kept
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return raw
	}
	return encoded
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	// A JSON-RPC error is still a successful transport exchange, so the status
	// stays 200; a 4xx here would make the client report a connection problem
	// rather than the policy decision.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	payload := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": message}}
	_ = json.NewEncoder(w).Encode(payload)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		switch strings.ToLower(key) {
		case "content-length", "connection", "transfer-encoding", "host":
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
}

// auditToLog is the default auditor. Tool decisions go to the Pod log, which is
// collected like every other runtime log.
func auditToLog(entry map[string]any) {
	encoded, err := json.Marshal(entry)
	if err != nil {
		return
	}
	log.Printf("mcp tool policy %s", encoded)
}
