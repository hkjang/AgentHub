package api

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// The review queue has to show the request that has waited longest.
//
// It held two hundred rows, waiting ones first and newest-first inside that, and
// said nothing about how many there were. On a deployment with more than two
// hundred requests waiting, the ones that fell off the end were the oldest — and
// a request a reviewer cannot see is not a slow decision, it is a task that will
// never run again. Measured against a running deployment before the fix: 210
// waiting, 200 returned, the ten missing were the ten oldest, and the admin
// screen's 대기 card counted the two hundred it could see.
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run ReviewQueue -v
func TestTheReviewQueueShowsTheLongestWaitFirst(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check the review queue against a running deployment")
	}
	client := login(t, base)
	body, status := client.get("/api/v1/approvals")
	if status != http.StatusOK {
		t.Fatalf("the review queue could not be read (%d)", status)
	}
	var list struct {
		Items []struct {
			ID        string    `json:"id"`
			Status    string    `json:"status"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"items"`
		Pending int `json:"pending"`
		Hidden  int `json:"hidden"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}

	waiting := []time.Time{}
	for _, item := range list.Items {
		if item.Status == "pending" {
			waiting = append(waiting, item.CreatedAt)
		}
	}
	if len(waiting) < 2 {
		t.Fatalf("this deployment has %d request(s) waiting; with fewer than two there is no order to check — seed a few pending approvals first", len(waiting))
	}
	for i := 1; i < len(waiting); i++ {
		if waiting[i].Before(waiting[i-1]) {
			t.Fatalf("the queue is not oldest-first at position %d (%s came before %s); the longest wait is what falls off the end", i, waiting[i].Format(time.RFC3339), waiting[i-1].Format(time.RFC3339))
		}
	}
	// A decided request is an archive entry, so it belongs after everything still
	// waiting — a reviewer should never have to scroll past history to find work.
	seenDecided := false
	for _, item := range list.Items {
		if item.Status != "pending" {
			seenDecided = true
			continue
		}
		if seenDecided {
			t.Error("a waiting request is listed after a decided one; the queue is mixed with its own archive")
			break
		}
	}

	if list.Pending < len(waiting) {
		t.Errorf("the queue reports %d waiting but returned %d; the count is of the page rather than of the table", list.Pending, len(waiting))
	}
	if want := list.Pending - len(waiting); list.Hidden != want {
		t.Errorf("the queue hides %d waiting request(s) and reports %d; a reviewer cannot be told what they cannot see if the number is wrong", want, list.Hidden)
	}
	// What this cannot see: whether the waiting count was taken over the table or
	// over the rows that were returned. A count of the page satisfies every
	// assertion above — it reports zero hidden and matches the rows exactly — and
	// it is the bug the bell had. That one is guarded where it is visible, in
	// store.TestTheWaitingCountIsOfTheTable.
	t.Logf("%d waiting, %d shown oldest-first, %d reported hidden", list.Pending, len(waiting), list.Hidden)
}
