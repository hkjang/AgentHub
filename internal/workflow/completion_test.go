package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// gatewayStub answers like an OpenAI-compatible endpoint and records what it was
// asked, which is how these tests see whether the schema was sent.
type gatewayStub struct {
	server *httptest.Server
	bodies []map[string]any
	// rejectSchema makes the first schema-carrying request fail the way a gateway
	// without response_format support does.
	rejectSchema bool
}

func newGatewayStub(rejectSchema bool) *gatewayStub {
	stub := &gatewayStub{rejectSchema: rejectSchema}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		stub.bodies = append(stub.bodies, body)
		if _, asked := body["response_format"]; asked && stub.rejectSchema {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format is not supported"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"passed\":true}"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	return stub
}

func (g *gatewayStub) step() Step {
	return Step{ID: "s", AgentName: "A", ModelBaseURL: g.server.URL, ModelName: "m"}
}

func schemaForTest() Schema {
	return Schema{Name: "verdict", Body: map[string]any{
		"type":       "object",
		"properties": map[string]any{"passed": map[string]any{"type": "boolean"}},
		"required":   []any{"passed"},
	}}
}

func TestStructuredRequestCarriesTheSchema(t *testing.T) {
	stub := newGatewayStub(false)
	defer stub.server.Close()
	client := &ModelCompletion{Client: stub.server.Client()}

	result, err := client.CompleteStructured(context.Background(), stub.step(), "판정하라", schemaForTest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Validated {
		t.Fatal("a gateway that accepted the schema must be reported as validated")
	}
	if result.Usage.TotalTokens != 12 {
		t.Fatalf("usage was lost: %#v", result.Usage)
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("the gateway was called %d times", len(stub.bodies))
	}
	format, ok := stub.bodies[0]["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("the request did not ask for a schema: %#v", stub.bodies[0])
	}
	inner, ok := format["json_schema"].(map[string]any)
	if !ok || inner["name"] != "verdict" || inner["strict"] != true {
		t.Fatalf("unexpected json_schema block: %#v", format)
	}
}

func TestGatewayWithoutSchemaSupportIsAskedAgainInProse(t *testing.T) {
	// An offline site cannot choose its gateway freely, so a refusal of
	// response_format has to keep working rather than failing the run.
	stub := newGatewayStub(true)
	defer stub.server.Close()
	client := &ModelCompletion{Client: stub.server.Client()}

	result, err := client.CompleteStructured(context.Background(), stub.step(), "판정하라", schemaForTest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Validated {
		t.Fatal("an answer the gateway did not constrain must not be reported as validated")
	}
	if !strings.Contains(result.Output, "passed") {
		t.Fatalf("unexpected answer: %q", result.Output)
	}
	if len(stub.bodies) != 2 {
		t.Fatalf("expected a retry without the schema, got %d requests", len(stub.bodies))
	}
	if _, asked := stub.bodies[1]["response_format"]; asked {
		t.Fatal("the retry still carried the schema")
	}
}

func TestARealFailureIsNotRetried(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"model is down"}}`))
	}))
	defer server.Close()
	client := &ModelCompletion{Client: server.Client()}
	step := Step{ID: "s", ModelBaseURL: server.URL, ModelName: "m"}

	if _, err := client.CompleteStructured(context.Background(), step, "x", schemaForTest()); err == nil {
		t.Fatal("a 500 must surface as an error rather than a prose fallback")
	}
}

func TestSchemaUnsupportedRecognisesTheRefusals(t *testing.T) {
	for _, message := range []string{"model gateway returned 400: bad", "model gateway returned 422: nope", "response_format unknown field"} {
		if !schemaUnsupported(errText(message)) {
			t.Fatalf("%q should be treated as an unsupported schema", message)
		}
	}
	for _, message := range []string{"model gateway returned 500: down", "context deadline exceeded"} {
		if schemaUnsupported(errText(message)) {
			t.Fatalf("%q must not be treated as an unsupported schema", message)
		}
	}
	if schemaUnsupported(nil) {
		t.Fatal("no error is not an unsupported schema")
	}
}

type errText string

func (e errText) Error() string { return string(e) }

// What a workflow step says when it fails is read by a person on the Workflows
// screen, and it was Go talking about itself.
//
// Measured on a running deployment: a two-step workflow whose first step could
// not reach the model recorded
//
//	Post "http://stub-gateway…/v1/chat/completions": dial tcp: lookup
//	stub-gateway… on 127.0.0.11:53: no such host
//
// The package already knew how to speak to a person — its own blocked-content
// error is a Korean sentence — and every other message here was English written
// for whoever was reading a stack trace.
func TestAStepSaysWhyItFailedInThePlatformsWords(t *testing.T) {
	body, err := os.ReadFile("completion.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, leftover := range []string{
		`fmt.Errorf("agent %q has no model endpoint bound"`,
		`fmt.Errorf("model gateway returned %d with an unreadable body"`,
		`fmt.Errorf("model gateway returned %d: %s"`,
		`fmt.Errorf("model gateway returned no completion")`,
	} {
		if strings.Contains(source, leftover) {
			t.Errorf("a step still reports %s to a person", leftover)
		}
	}
	if !strings.Contains(source, "modelCallReason(callErr)") {
		t.Error("a transport failure is recorded as Go produced it, URL and resolver and all")
	}
	// And the reason itself survives: an operator needs to know it was refused
	// rather than unreachable.
	raw := errors.New(`Post "http://gw.svc/v1/chat/completions": dial tcp: lookup gw.svc on 127.0.0.11:53: no such host`)
	if got := modelCallReason(raw); got != "no such host" {
		t.Errorf("the reason a call failed reads as %q", got)
	}
	if got := modelCallReason(errors.New("context deadline exceeded")); got != "context deadline exceeded" {
		t.Errorf("a message with nothing to trim was rewritten to %q", got)
	}
}
