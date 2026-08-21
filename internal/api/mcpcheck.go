package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Whether an MCP server is there, and what it offers.
//
// Registering one is two fields and a save. Whether the endpoint answers, speaks
// the protocol, and offers the tools somebody is about to write a policy about
// is discovered later — by an agent, mid-task, as a tool call that fails.
//
// It also answers a question the tool policy screen could not: what the tools
// are called. An allow or deny list is written against names, and until now
// those names came from the vendor's documentation or from a guess.

const mcpCheckTimeout = 8 * time.Second

func (s *Server) mcpServerCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	server, err := s.store.MCPServerByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	verdict, detail, tools := askMCPServer(r.Context(), server.Mode, server.Endpoint)
	s.store.Audit(r.Context(), &u, "mcp.check", "mcp", server.ID, verdict, clientIP(r),
		map[string]any{"mode": server.Mode, "endpoint": server.Endpoint, "detail": detail})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": server.ID, "verdict": verdict, "detail": detail, "tools": tools,
	})
}

// askMCPServer performs the handshake and asks for the tool list.
//
// A server the control plane cannot reach is not necessarily a broken server:
// the sidecar and dedicated modes run it inside the runtime's own Pod, where
// there is no address to call from here. That is reported as not-checkable
// rather than as a failure, because the two need different answers and only one
// of them is somebody's mistake.
func askMCPServer(ctx context.Context, mode, endpoint string) (verdict, detail string, tools []string) {
	if mode != "" && mode != "shared" {
		return "not_checkable", "이 서버는 런타임 Pod 안에서 실행되므로 컨트롤 플레인에서 확인할 수 없습니다. 런타임을 시작한 뒤 에이전트가 도구를 쓸 수 있는지로 확인하세요.", nil
	}
	target := strings.TrimSpace(endpoint)
	if target == "" {
		return "unconfigured", "주소가 비어 있습니다.", nil
	}
	ctx, cancel := context.WithTimeout(ctx, mcpCheckTimeout)
	defer cancel()

	session, verdict, detail := mcpInitialize(ctx, target)
	if verdict != "" {
		return verdict, detail, nil
	}
	tools, err := mcpToolNames(ctx, target, session)
	if err != nil {
		return "no_tools", "핸드셰이크는 됐지만 도구 목록을 읽지 못했습니다: " + shortError(err.Error()), nil
	}
	if len(tools) == 0 {
		return "no_tools", "서버가 응답했지만 도구를 하나도 제공하지 않습니다.", nil
	}
	return "ok", fmt.Sprintf("정상입니다. 도구 %d개를 제공합니다.", len(tools)), tools
}

// mcpInitialize opens the session, and returns the id when the server issues
// one. Servers that do not are equally correct — the header is optional — so an
// empty id is not an error.
func mcpInitialize(ctx context.Context, target string) (session, verdict, detail string) {
	body := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": currentMCPVersion,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "AgentHub", "version": "check"},
		},
	}
	response, raw, err := mcpPost(ctx, target, "", body)
	if err != nil {
		return "", "unreachable", "연결하지 못했습니다: " + shortError(err.Error())
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return "", "unauthorised", "서버가 인증을 거절했습니다(HTTP " + response.Status + "). 이 서버에 필요한 인증 방식과 자격을 확인해 주세요."
	case response.StatusCode == http.StatusNotFound:
		return "", "wrong_path", "이 주소에 MCP 엔드포인트가 없습니다(HTTP 404). 대개 경로 끝의 /mcp 가 빠졌거나 다른 경로를 씁니다."
	case response.StatusCode >= 400:
		return "", "error", "서버가 HTTP " + response.Status + " 로 답했습니다."
	}
	if !bytes.Contains(raw, []byte("protocolVersion")) && !bytes.Contains(raw, []byte("serverInfo")) {
		return "", "not_mcp", "주소는 응답하지만 MCP 핸드셰이크로 보이지 않습니다. 이 엔드포인트가 정말 MCP 서버인지 확인해 주세요."
	}
	return response.Header.Get("Mcp-Session-Id"), "", ""
}

func mcpToolNames(ctx context.Context, target, session string) ([]string, error) {
	response, raw, err := mcpPost(ctx, target, session,
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpPayload(raw), &payload); err != nil {
		return nil, err
	}
	names := []string{}
	for _, tool := range payload.Result.Tools {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names, nil
}

func mcpPost(ctx context.Context, target, session string, body any) (*http.Response, []byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	// Both, because a streamable-HTTP server may answer either way and a server
	// that only does one of them should not be recorded as broken for it.
	request.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		request.Header.Set("Mcp-Session-Id", session)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, nil, err
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response, raw, nil
}

// mcpPayload unwraps a server-sent-events frame, which is how half of them
// answer. A plain JSON body passes through untouched.
func mcpPayload(raw []byte) []byte {
	if !bytes.Contains(raw, []byte("data:")) {
		return raw
	}
	var out bytes.Buffer
	for _, line := range strings.Split(string(raw), "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), "data:"); found {
			out.WriteString(strings.TrimSpace(after))
		}
	}
	return out.Bytes()
}
