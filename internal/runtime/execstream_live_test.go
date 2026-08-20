package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/acp"
	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// Holding a command's stdin open across a Kubernetes exec, against a real
// cluster.
//
// This is the one piece of the ACP backend that cannot be proved anywhere else.
// A one-shot exec writes its input and reads what comes back; a protocol
// conversation needs both directions open at once, with each line delivered as it
// is written rather than when the stream closes. If the SPDY stream buffered, or
// if closing the writer ended the whole exec, everything above it would deadlock
// on the first request and no unit test would notice.
//
// It needs a cluster, a Pod running an agent, and a database to hold the
// connection settings, so it is skipped unless all of them are named:
//
//	AGENTHUB_LIVE_DSN=postgres://…/agenthub_acp \
//	AGENTHUB_LIVE_APISERVER=https://127.0.0.1:8443 \
//	AGENTHUB_LIVE_TOKEN=$(kubectl create token …) \
//	AGENTHUB_LIVE_POD=acp-probe \
//	go test ./internal/runtime/ -run Live -v
func TestLiveExecStreamHoldsAConversation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	spawner, spec := liveSpawner(ctx, t)

	// The argv the runtime descriptor names, read from the descriptor rather than
	// repeated here: a command that drifted from the one production uses would
	// prove the wrong thing.
	session, err := spawner.ExecStream(ctx, spec, ExecRequest{
		Command: runtimetype.Describe(runtimetype.QwenCode).ACPCommand,
	})
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer session.Close()

	// Deliberately hand-rolled rather than driven through the acp package: what is
	// being proved here is the transport, and a failure should point at the stream
	// rather than at a client that also happens to be under test.
	request := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,` +
		`"clientCapabilities":{"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false}}}` + "\n"
	if _, err := session.Stdin.Write([]byte(request)); err != nil {
		t.Fatalf("write to the agent: %v — %s", err, session.Stderr())
	}

	// The answer has to arrive while stdin is still open. This is the assertion: a
	// stream that only delivered on close would time out here.
	answered := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(session.Stdout)
		for {
			line, readErr := reader.ReadString('\n')
			if strings.Contains(line, `"id":1`) {
				answered <- line
				return
			}
			if readErr != nil {
				answered <- ""
				return
			}
		}
	}()
	select {
	case line := <-answered:
		if line == "" {
			t.Fatalf("the agent's stream ended before it answered — %s", session.Stderr())
		}
		var frame struct {
			Result struct {
				ProtocolVersion int `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("the answer was not JSON-RPC: %q", line)
		}
		if frame.Result.ProtocolVersion == 0 {
			t.Fatalf("the agent did not negotiate a protocol version: %q", line)
		}
		t.Logf("a real agent in a real Pod negotiated protocol %d over a held-open exec", frame.Result.ProtocolVersion)
	case <-time.After(90 * time.Second):
		t.Fatalf("no answer while stdin was open — the exec stream is not full duplex. stderr: %s", session.Stderr())
	}

	// Closing this side ends the conversation, and Wait then says how it ended.
	session.Close()
	if err := session.Wait(); err != nil {
		t.Logf("the agent exited with: %v", err)
	}
}

// The same stream driven by the client the execution runner uses. This is the
// join between the two halves — the transport above, and the protocol package
// proved separately against the same agent in a container.
func TestLiveExecStreamCarriesTheProtocolClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	spawner, spec := liveSpawner(ctx, t)

	session, err := spawner.ExecStream(ctx, spec, ExecRequest{
		Command: runtimetype.Describe(runtimetype.QwenCode).ACPCommand,
	})
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	defer session.Close()

	client := acp.New(session.Stdout, session.Stdin)
	go client.Run(ctx)
	capabilities, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("initialize: %v — %s", err, session.Stderr())
	}
	if capabilities.ProtocolVersion != acp.ProtocolVersion {
		t.Errorf("negotiated protocol %d, this client speaks %d", capabilities.ProtocolVersion, acp.ProtocolVersion)
	}
	sessionID, err := client.NewSession(ctx, "/tmp", nil)
	if err != nil {
		t.Fatalf("session/new: %v — %s", err, session.Stderr())
	}
	if sessionID == "" {
		t.Fatal("the agent opened a session with no identifier")
	}
	t.Logf("session %s opened in a Pod over the platform's own exec stream", sessionID)
	client.Cancel(sessionID)
}

// liveSpawner builds the real spawner against a real cluster, reading the
// connection the same way production does: from the settings row.
func liveSpawner(ctx context.Context, t *testing.T) (*KubernetesSpawner, Spec) {
	t.Helper()
	dsn := os.Getenv("AGENTHUB_LIVE_DSN")
	apiServer := os.Getenv("AGENTHUB_LIVE_APISERVER")
	token := os.Getenv("AGENTHUB_LIVE_TOKEN")
	pod := os.Getenv("AGENTHUB_LIVE_POD")
	if dsn == "" || apiServer == "" || token == "" || pod == "" {
		t.Skip("set AGENTHUB_LIVE_DSN, AGENTHUB_LIVE_APISERVER, AGENTHUB_LIVE_TOKEN and AGENTHUB_LIVE_POD to run this against a cluster")
	}
	namespace := os.Getenv("AGENTHUB_LIVE_NAMESPACE")
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	// Any key will do: this test writes the settings it then reads back, and the
	// database it uses is its own.
	cipher, err := cryptox.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Skipf("no database here: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// The settings row records who changed it, and that has to be a real account.
	if err := db.BootstrapAdmin(ctx, "live-test", "live-test-password-2026"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	actor, err := db.AuthenticateLocal(ctx, "live-test", "live-test-password-2026")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	settings := map[string]any{
		"enabled": true, "mode": "token", "apiServer": apiServer,
		"namespace": namespace, "verifyTls": false, "crdEnabled": true,
	}
	if err := db.PutSetting(ctx, "kubernetes", settings, &token, actor.ID); err != nil {
		t.Fatalf("store the connection settings: %v", err)
	}
	return NewKubernetesSpawner(db), Spec{Runtime: store.Runtime{PodName: pod}, Namespace: namespace}
}
