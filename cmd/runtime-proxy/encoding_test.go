package main

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gzipUpstream answers with a compressed body whenever the caller said it could
// read one, which is what an MCP server behind nginx — or any Node server with
// the compression middleware — does with every response.
func gzipUpstream(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			_, _ = w.Write([]byte(payload))
			return
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write([]byte(payload))
		_ = writer.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(server.Close)
	return server
}

// read decodes what the agent would actually see, decompressing if the gateway
// passed a compressed body along.
func read(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	if !strings.Contains(recorder.Header().Get("Content-Encoding"), "gzip") {
		return recorder.Body.String()
	}
	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("the gateway sent a body it labelled gzip and is not: %v", err)
	}
	var plain bytes.Buffer
	_, _ = plain.ReadFrom(reader)
	return plain.String()
}

// toolCall builds the request an agent's HTTP client really sends. Every one of
// them — Go's, undici's, requests' — advertises gzip without being asked to.
func compressedRequest(path, body string) *http.Request {
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("Accept-Encoding", "gzip")
	return request
}

// A tool the platform forbids must not be advertised, whatever encoding the
// answer arrives in.
//
// The agent's own Accept-Encoding was copied onto the outbound request, so Go's
// transport did not add its own and therefore did not decompress what came back.
// rewriteToolsPayload was handed gzip, failed to parse it, and returned it
// untouched — which is the documented behaviour for a response that is not a
// tool list. The list went to the model with every denied tool still in it, and
// nothing anywhere said so.
func TestAToolListIsFilteredEvenWhenTheServerCompressesIt(t *testing.T) {
	origin := gzipUpstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"},{"name":"delete_branch"}]}}`)
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: origin.URL, PolicyDenied: []string{"delete_*"},
	}}, func(map[string]any) {}, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, compressedRequest("/mcp/github", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	body := read(t, recorder)
	if strings.Contains(body, "delete_branch") {
		t.Fatalf("a forbidden tool was advertised because the answer was compressed: %s", body)
	}
	if !strings.Contains(body, "read_file") {
		t.Fatalf("the permitted tool disappeared: %s", body)
	}
}

// The same hole on the half that carries data rather than names: a compressed
// answer was scanned as compressed bytes, which match no detector, so a customer
// record went to the model with the finding count at zero.
func TestAToolAnswerIsScannedEvenWhenTheServerCompressesIt(t *testing.T) {
	origin := gzipUpstream(t, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"홍길동 900101-1234568"}]}}`)
	inspect := scannerFor("rrn", "redact")
	inspect.settings.ScanResponses = true
	handler := mcpGatewayWith([]mcpUpstream{{Name: "jira", Upstream: origin.URL}}, func(map[string]any) {}, nil, inspect)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, compressedRequest("/mcp/jira",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_issue","arguments":{}}}`))
	answer := read(t, recorder)
	if strings.Contains(answer, "900101-1234568") {
		t.Fatalf("the value reached the agent because the answer was compressed: %s", answer)
	}
	if !strings.Contains(answer, "홍길동") {
		t.Fatalf("the rest of the answer was lost: %s", answer)
	}
}

// A response the gateway rewrote must not still claim to be compressed, or the
// agent's client fails on a body that is exactly what it asked for.
func TestARewrittenAnswerIsNotLabelledCompressed(t *testing.T) {
	origin := gzipUpstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"}]}}`)
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: origin.URL, PolicyDenied: []string{"delete_*"},
	}}, func(map[string]any) {}, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, compressedRequest("/mcp/github", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if encoding := recorder.Header().Get("Content-Encoding"); encoding != "" {
		t.Fatalf("Content-Encoding = %q on a body the gateway wrote itself", encoding)
	}
	if !strings.Contains(recorder.Body.String(), "read_file") {
		t.Fatalf("the answer did not survive: %s", recorder.Body.String())
	}
}

// An uncompressed deployment behaves exactly as it did: the gateway asks for an
// encoding it can read, and everything else about the exchange is unchanged.
func TestAnUncompressedServerIsUnaffected(t *testing.T) {
	origin := gzipUpstream(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file"},{"name":"delete_branch"}]}}`)
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: origin.URL, PolicyDenied: []string{"delete_*"},
	}}, func(map[string]any) {}, nil)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/mcp/github",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	body := recorder.Body.String()
	if strings.Contains(body, "delete_branch") || !strings.Contains(body, "read_file") {
		t.Fatalf("the ordinary path changed: %s", body)
	}
}

// The other direction, and the same argument as the batch: a request body in an
// encoding the gateway cannot read is a request body it has not policed. The
// method stays empty, every check compares against "", and the call goes upstream
// with the credential attached — so it is refused rather than forwarded.
func TestACompressedRequestBodyIsRefused(t *testing.T) {
	reached := false
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer origin.Close()
	var audited []map[string]any
	handler := mcpGatewayWithApprover([]mcpUpstream{{
		Name: "github", Upstream: origin.URL, PolicyDenied: []string{"delete_*"},
	}}, func(entry map[string]any) { audited = append(audited, entry) }, nil)

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_branch"}}`))
	_ = writer.Close()

	request := httptest.NewRequest("POST", "/mcp/github", bytes.NewReader(compressed.Bytes()))
	request.Header.Set("Content-Encoding", "gzip")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if reached {
		t.Fatal("a denied tool reached the server by being sent compressed")
	}
	if !strings.Contains(recorder.Body.String(), "압축") {
		t.Fatalf("the refusal does not say why: %s", recorder.Body.String())
	}
	if len(audited) == 0 {
		t.Fatal("nothing was recorded; a refused call is exactly what an audit log is for")
	}
}

// identity is the spelling of "not compressed", and it is a body the gateway can
// read like any other.
func TestAnIdentityEncodedRequestStillPassesThrough(t *testing.T) {
	reached := false
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer origin.Close()
	handler := mcpGatewayWithApprover([]mcpUpstream{{Name: "github", Upstream: origin.URL}},
		func(map[string]any) {}, nil)

	request := httptest.NewRequest("POST", "/mcp/github",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file"}}`))
	request.Header.Set("Content-Encoding", "identity")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !reached {
		t.Error("an uncompressed body was refused for saying so")
	}
}
