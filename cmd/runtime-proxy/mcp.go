package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
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
}

// permits reports whether a tool may be called.
func (u mcpUpstream) permits(tool string) bool {
	switch u.Mode {
	case "allow":
		return contains(u.Tools, tool)
	case "deny":
		return !contains(u.Tools, tool)
	default:
		// No policy configured: the bundle binding is the only restriction.
		return true
	}
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
type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
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

		var request rpcRequest
		_ = json.Unmarshal(body, &request)
		if request.Method == "tools/call" && !upstream.permits(request.Params.Name) {
			auditor(map[string]any{"server": name, "tool": request.Params.Name, "decision": "denied", "mode": upstream.Mode})
			writeRPCError(w, request.ID, -32601, fmt.Sprintf("도구 %q 는 이 Agent의 MCP 도구 정책에 의해 차단되었습니다.", request.Params.Name))
			return
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
		if request.Method == "tools/list" && upstream.Mode != "" {
			filterToolList(w, response, upstream)
			return
		}
		copyHeaders(w.Header(), response.Header)
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
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
