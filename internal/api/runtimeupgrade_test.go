package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// A runtime whose surface is a terminal lives entirely inside a websocket: the
// page loads over plain HTTP and then everything a person types goes through the
// upgrade. If the gateway serves the page but drops the upgrade, the result is a
// terminal that renders and then says "press enter to reconnect" — which is what
// a Qwen Code session looks like when this path is broken.
//
// The upstream here is a hand-rolled upgrade rather than a websocket library:
// what is being tested is that the gateway completes the handshake and pipes
// bytes in both directions afterwards, and that is visible at the socket.
func TestRuntimeGatewayForwardsAWebsocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("the upgrade request did not arrive as one: %q", r.Header.Get("Upgrade"))
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/assets/ws" {
			t.Errorf("unexpected upstream path: %q", r.URL.Path)
		}
		// ttyd negotiates a subprotocol; losing it would fail the handshake in the
		// browser even though the status was right.
		if r.Header.Get("Sec-WebSocket-Protocol") != "tty" {
			t.Errorf("the subprotocol was not forwarded: %q", r.Header.Get("Sec-WebSocket-Protocol"))
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("upstream response writer cannot be hijacked")
		}
		connection, buffered, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijack failed: %v", err)
		}
		defer connection.Close()
		fmt.Fprint(connection, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: tty\r\n\r\n")
		fmt.Fprint(connection, "banner-from-runtime\n")
		line, err := buffered.ReadString('\n')
		if err != nil {
			t.Errorf("nothing arrived from the browser: %v", err)
			return
		}
		fmt.Fprintf(connection, "echo:%s", line)
	}))
	defer upstream.Close()

	server := pathGatewayServer(t, sessionGatewaySettings{})
	gateway := httptest.NewServer(server.runtimePathGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a runtime request reached the Portal handler")
	})))
	defer gateway.Close()

	connection, err := net.Dial("tcp", strings.TrimPrefix(gateway.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	// /assets/ws rather than ttyd's own /ws only so the request does not touch the
	// runtime's idle timer, which is the one thing in this path that needs a
	// database. The upgrade behaviour is the same either way.
	fmt.Fprintf(connection, "GET /%s/assets/ws HTTP/1.1\r\nHost: agenthub.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: tty\r\nCookie: %s=%s\r\n\r\n",
		testRuntimeID, runtimePathCookieName(testRuntimeID), sessionCookieValue(t, server, upstream.URL))

	reader := bufio.NewReader(connection)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("no response line: %v", err)
	}
	if !strings.Contains(status, "101") {
		// Read a little of the body so the failure says what came back instead.
		rest := make([]byte, 256)
		count, _ := reader.Read(rest)
		t.Fatalf("the upgrade was not forwarded: %q %s", strings.TrimSpace(status), rest[:count])
	}
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("truncated handshake: %v", err)
		}
		if strings.TrimSpace(header) == "" {
			break
		}
	}
	banner, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(banner) != "banner-from-runtime" {
		t.Fatalf("nothing came back from the runtime after the upgrade: %q %v", banner, err)
	}
	// And the other direction: a terminal is useless if only one way works.
	fmt.Fprint(connection, "typed-by-the-person\n")
	echoed, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(echoed) != "echo:typed-by-the-person" {
		t.Fatalf("what the person typed did not reach the runtime: %q %v", echoed, err)
	}
}

// A runtime served under its own base path keeps the prefix when the gateway
// proxies it. Stripping it — which is right for every other runtime — would 404
// every request the terminal makes, because ttyd was started expecting it.
func TestProxiedPathKeepsThePrefixForBasePathRuntimes(t *testing.T) {
	const runtimeID = "3f2b8c14-9d5e-4a71-8c2f-6b0d1e7a45c9"
	if got := proxiedRuntimePath(runtimetype.QwenCode, runtimeID, "ws"); got != "/"+runtimeID+"/ws" {
		t.Errorf("qwencode path = %q", got)
	}
	if got := proxiedRuntimePath(runtimetype.QwenCode, runtimeID, ""); got != "/"+runtimeID+"/" {
		t.Errorf("qwencode root = %q", got)
	}
	// Everything else still gets the prefix removed: those runtimes serve from
	// their own root and know nothing about how the platform publishes them.
	for _, runtimeType := range []string{runtimetype.OpenCode, runtimetype.Hermes, runtimetype.QwenPaw, runtimetype.Langflow, runtimetype.Custom} {
		if got := proxiedRuntimePath(runtimeType, runtimeID, "assets/app.js"); got != "/assets/app.js" {
			t.Errorf("%s path = %q", runtimeType, got)
		}
	}
}

// The launch ticket is single use, so it must not survive in the address bar: a
// runtime UI builds its own URLs from the page's location, and ttyd opens its
// websocket with the query string attached. Sending the spent ticket back is
// what turned a working terminal into one that renders and never connects.
func TestTicketFreeLocationStripsOnlyTheTicket(t *testing.T) {
	cases := map[string]string{
		"http://rt.example/?ticket=abc":                 "/",
		"http://rt.example/rt-1/?ticket=abc":            "/rt-1/",
		"http://rt.example/rt-1/?ticket=abc&theme=dark": "/rt-1/?theme=dark",
		"http://rt.example/rt-1/ws?ticket=abc":          "/rt-1/ws",
		"http://rt.example/rt-1/":                       "/rt-1/",
	}
	for raw, want := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := ticketFreeLocation(parsed); got != want {
			t.Errorf("ticketFreeLocation(%q) = %q, want %q", raw, got, want)
		}
	}
}
