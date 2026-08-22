package main

import (
	"net/http"
	"strings"
	"testing"
)

// A tool the policy denies must not reach the server however it is asked for.
//
// The gateway parsed one JSON-RPC request out of the body and ignored a parse
// failure. JSON-RPC 2.0 lets a client send an array of requests instead, and an
// array does not decode into one request struct — so Method stayed empty, every
// check compared against "", and the body went upstream unread with the
// credential attached. The tool policy, the approval gate and the content scanner
// were all one bracket away from not applying.
func TestABatchedCallIsNotAWayPast(t *testing.T) {
	reached := false
	gateway, audit := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`[{"jsonrpc":"2.0","id":1,"result":{}}]`))
	}, mcpUpstream{Mode: "deny", Tools: []string{"delete-library"}})

	response, body := call(t, gateway, "context7",
		`[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete-library","arguments":{}}}]`)
	if reached {
		t.Error("a denied tool reached the server by being wrapped in a batch")
	}
	if response.StatusCode != 200 {
		t.Errorf("the refusal should be a JSON-RPC error, not an HTTP one: %d", response.StatusCode)
	}
	if !strings.Contains(body, "차단") && !strings.Contains(body, "일괄") {
		t.Errorf("the refusal does not say why: %s", body)
	}
	if len(*audit) == 0 {
		t.Error("nothing was recorded; a refused call is exactly what an audit log is for")
	}
}

// Every top-level array is refused, not only one that carries a tools/call. The
// gateway does not police batches at all, so a batch it lets past is a batch it
// has not read — and "this one looked harmless" is not something it can know.
func TestEveryBatchIsRefused(t *testing.T) {
	for _, body := range []string{
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`,
		`[]`,
		`  [ {"jsonrpc":"2.0","method":"initialize"} ]`,
	} {
		reached := false
		gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		}, mcpUpstream{Mode: "deny", Tools: []string{"delete-library"}})
		if _, _ = call(t, gateway, "context7", body); reached {
			t.Errorf("a batch the gateway does not police was forwarded anyway: %s", body)
		}
	}
}

// A tools/call is policed whatever shape the rest of the message is in.
//
// This one is hardening rather than a hole that was demonstrated. JSON-RPC allows
// positional params, which do not fit the request struct, and the decode of such a
// message fails — but encoding/json records the first type error and carries on,
// so the method field survives and the check still runs. That is a property of the
// standard library's error recovery rather than a promise, and the check should
// not rest on it: the method and the tool name are read from the message as JSON
// now, so the policy applies whether or not the rest of it decodes.
func TestAToolCallIsRecognisedEvenWhenItsShapeIsOdd(t *testing.T) {
	reached := false
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}, mcpUpstream{Mode: "allow", Tools: []string{"read-library"}})
	if _, body := call(t, gateway, "context7",
		`{"jsonrpc":"2.0","id":1,"params":["delete-library",{}],"method":"tools/call"}`); reached {
		t.Errorf("a tools/call the struct could not hold went upstream unchecked: %s", body)
	}
}

// And what was already true stays true: a body that is not JSON at all cannot be
// a tool call, so it goes upstream to be rejected there as it always did. The
// gateway refuses what it can prove is dangerous rather than everything it cannot
// parse — an empty body is the ordinary case for a stream request.
func TestNonJSONStillPassesThrough(t *testing.T) {
	reached := false
	gateway, _ := gatewayFor(t, func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}, mcpUpstream{Mode: "deny", Tools: []string{"delete-library"}})
	if _, _ = call(t, gateway, "context7", `not json at all`); !reached {
		t.Error("a body that cannot be a tool call is now refused; that breaks ordinary traffic the gateway never policed")
	}
}
