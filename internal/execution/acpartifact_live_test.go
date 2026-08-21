package execution

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/hkjang/AgentHub/internal/acp"
	"github.com/hkjang/AgentHub/internal/cryptox"
	"github.com/hkjang/AgentHub/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A picture an ACP agent takes has to reach the platform event stream, not only
// the run's own timeline. It did not: the screenshots were stored and shown, and
// every trigger subscribed to 산출물 생성 stayed silent for them — which is most of
// what such a trigger is for, since a screenshot is the artifact people most want
// routed somewhere.
//
// The guard next door proves the call goes through the shared announcement. This
// runs it against the real database and reads the event back, because "it calls
// the right function" and "the event is in the table" have been different answers
// before.
//
//	AGENTHUB_POSTGRES_DSN=… go test ./internal/execution/ -run ACPPicture
func TestACPPicturesAreAnnouncedToTheWholePlatform(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no database")
	}
	ctx := context.Background()
	cipher, err := cryptox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	agent, task, run, cleanup := aRunToAttachPicturesTo(t, ctx, db)
	defer cleanup()

	// The smallest thing the platform will accept as a picture: a one-pixel PNG.
	pixel, err := base64.StdEncoding.DecodeString(onePixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	turn := &acpTurn{images: map[string][]acp.Image{}, inputs: map[string]string{}}
	turn.images["call-1"] = []acp.Image{{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(pixel)}}
	turn.imageOrder = []string{"call-1"}

	orchestrator := New(db, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})), "test")
	orchestrator.saveACPPictures(ctx, &run, task, agent, turn)

	artifacts, err := db.Artifacts(ctx, task.OwnerID, run.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("the picture was not stored at all (%d artifacts)", len(artifacts))
	}
	events, err := db.RecentEvents(ctx, task.OwnerID, store.EventArtifactCreated, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.SubjectID == artifacts[0].ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the picture was stored and no platform event was published for it; a 산출물 생성 trigger never hears about it")
	}
}

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// aRunToAttachPicturesTo borrows an agent that already exists in this database
// and gives it one task and one run, both removed afterwards.
func aRunToAttachPicturesTo(t *testing.T, ctx context.Context, db *store.Store) (store.Agent, store.AgentTask, store.AgentRun, func()) {
	t.Helper()
	agents, err := db.Agents(ctx, "", true)
	if err != nil || len(agents) == 0 {
		t.Skipf("no agent to attach a run to: %v", err)
	}
	agent := agents[0]
	task, err := db.CreateAgentTask(ctx, store.CreateTaskInput{
		AgentID: agent.ID, OwnerID: agent.OwnerID, Title: "ACP 그림 확인",
		Input: "그림 하나", Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.CreateAgentRun(ctx, store.AgentRun{
		ID: uuid.NewString(), TaskID: task.ID, AgentID: agent.ID, OwnerID: agent.OwnerID,
		Attempt: 1, AgentVersion: agent.Version, WorkerID: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The store has no delete for a task, so the fixture cleans up the way the
	// operator's retention job does: straight at the rows it created.
	return agent, task, run, func() {
		pool, err := pgxpool.New(ctx, os.Getenv("AGENTHUB_POSTGRES_DSN"))
		if err != nil {
			t.Logf("probe rows for task %s were left behind: %v", task.ID, err)
			return
		}
		defer pool.Close()
		for _, statement := range []string{
			`DELETE FROM platform_events WHERE subject_id IN (SELECT id FROM agent_artifacts WHERE task_id=$1)`,
			`DELETE FROM agent_artifacts WHERE task_id=$1`,
			`DELETE FROM agent_runs WHERE task_id=$1`,
			`DELETE FROM agent_tasks WHERE id=$1`,
		} {
			if _, err := pool.Exec(ctx, statement, task.ID); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	}
}
