package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/cryptox"
)

// A held task must not be charged for waiting. Claiming increments the attempt
// count before anything decides whether the task may run, so the hold has to hand
// it back — otherwise a task held twice reaches its first real failure with its
// retry budget already spent.
func TestAHoldDoesNotSpendTheRetryBudget(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no database")
	}
	ctx := context.Background()
	cipher, err := cryptox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var agentID, ownerID string
	if err := s.pool.QueryRow(ctx, `SELECT id, owner_id FROM agent_definitions LIMIT 1`).Scan(&agentID, &ownerID); err != nil {
		t.Skip("no agent to attach a task to")
	}
	id := "blockattempt-live"
	if _, err := s.pool.Exec(ctx, `DELETE FROM agent_tasks WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO agent_tasks(id,agent_id,owner_id,title,input,priority,status,source,scheduled_at)
		VALUES($1,$2,$3,'hold budget','hold budget','normal','queued','manual',now())`, id, agentID, ownerID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM agent_tasks WHERE id=$1`, id)

	attempts := func() int {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT attempts FROM agent_tasks WHERE id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for round := 1; round <= 3; round++ {
		// Anything else that is queued gets claimed first and is put straight back
		// through the defer path, which hands its attempt back — so a database with
		// real work in it neither skips this test nor is disturbed by it.
		for tries := 0; ; tries++ {
			claimed, claimErr := s.ClaimAgentTask(ctx, "test-worker", time.Minute)
			if claimErr != nil {
				t.Fatalf("round %d: nothing claimable: %v", round, claimErr)
			}
			if claimed.ID == id {
				break
			}
			if deferErr := s.DeferAgentTask(ctx, claimed.ID, 0, ""); deferErr != nil {
				t.Fatalf("could not return %s to the queue: %v", claimed.ID, deferErr)
			}
			if tries > 50 {
				t.Fatalf("round %d: the queue never reached this task", round)
			}
		}
		// One, every round: the previous hold gave the last one back, so three
		// holds in a row leave the task with its full budget rather than none of it.
		if got := attempts(); got != 1 {
			t.Fatalf("round %d: the claim should charge one attempt, got %d", round, got)
		}
		if err := s.BlockAgentTask(ctx, id, "정책이 막았습니다"); err != nil {
			t.Fatal(err)
		}
		if got := attempts(); got != 0 {
			t.Fatalf("round %d: a hold left %d attempt(s) spent; waiting is not a failed attempt", round, got)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE agent_tasks SET status='queued', scheduled_at=now() WHERE id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
}

// The event-triggered insert reads its row back through the shared column list
// now. It used to spell the list out and scan it by hand, one column behind, and
// the two failures that pattern already caused were both silent: a row that does
// not decode is a login refused or a task that never runs, not an error anybody
// sees. So this reads one back and looks at it.
func TestAnEventCreatesATaskThatDecodes(t *testing.T) {
	dsn := os.Getenv("AGENTHUB_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("no database")
	}
	ctx := context.Background()
	cipher, err := cryptox.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dsn, cipher)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var agentID, ownerID string
	if err := s.pool.QueryRow(ctx, `SELECT id, owner_id FROM agent_definitions LIMIT 1`).Scan(&agentID, &ownerID); err != nil {
		t.Skip("no agent to attach a task to")
	}
	eventID := "eventtask-live"
	if _, err := s.pool.Exec(ctx, `INSERT INTO platform_events(id,type,owner_id,payload) VALUES($1,'test.event',$2,'{}')
		ON CONFLICT (id) DO NOTHING`, eventID, ownerID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM platform_events WHERE id=$1`, eventID)

	trigger := AgentTrigger{ID: "eventtrigger-live", AgentID: agentID, OwnerID: ownerID}
	if _, err := s.pool.Exec(ctx, `INSERT INTO agent_triggers(id,agent_id,owner_id,name,type,event_type)
		VALUES($1,$2,$3,'라이브 확인','event','test.event') ON CONFLICT (id) DO NOTHING`,
		trigger.ID, agentID, ownerID); err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM agent_triggers WHERE id=$1`, trigger.ID)
	deadline := time.Now().UTC().Add(time.Hour)
	task, delivered, err := s.DeliverEventToTrigger(ctx, eventID, trigger, CreateTaskInput{
		AgentID: agentID, OwnerID: ownerID, Title: "이벤트 작업", Input: "이벤트로 만든 작업",
		Priority: "high", Source: "event", DeadlineAt: &deadline, Delegation: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Exec(ctx, `DELETE FROM agent_tasks WHERE id=$1`, task.ID)
	if !delivered {
		t.Fatal("the event was reported as already delivered")
	}
	// Every field the column list carries, in the order it carries them: a scan
	// one target out of step puts the wrong value in each of these.
	if task.ID == "" || task.AgentID != agentID || task.OwnerID != ownerID {
		t.Fatalf("identity fields did not decode: %+v", task)
	}
	if task.Title != "이벤트 작업" || task.Input != "이벤트로 만든 작업" || task.Priority != "high" {
		t.Fatalf("the task did not come back as it was written: %+v", task)
	}
	if task.Status != "queued" || task.Source != "event" || task.Attempts != 0 {
		t.Fatalf("a new task should be queued, from an event, unattempted: %+v", task)
	}
	if task.Delegation != 2 || task.DeadlineAt == nil || task.WaitingReason != "" || task.LastError != "" {
		t.Fatalf("the tail of the column list did not decode: %+v", task)
	}
	// The second delivery is the same event arriving again after a worker died.
	if _, again, err := s.DeliverEventToTrigger(ctx, eventID, trigger, CreateTaskInput{
		AgentID: agentID, OwnerID: ownerID, Title: "이벤트 작업", Input: "두 번째", Source: "event",
	}); err != nil || again {
		t.Fatalf("a redelivery queued the work twice (delivered=%v, err=%v)", again, err)
	}
}
