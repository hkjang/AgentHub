package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bell is the platform's only way of telling somebody that something is
// waiting for them, and both of its numbers were about the page rather than the
// person.
//
// The feed took the fifty most recent notices, so an unread one older than fifty
// newer ones could not be reached at all — and the console counted the unread
// among those fifty and printed that as the unread count. On a busy platform the
// approval somebody was waiting on was neither in the list nor in the number.
//
// This reads both ends: the server has to send the count, and the console has to
// use it rather than tallying what it was handed.
func TestTheBellCountsWhatIsUnreadAndNotWhatWasFetched(t *testing.T) {
	shell, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "components", "AppShell.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	source := string(shell)
	if strings.Contains(source, "notifications.filter((item)=>!item.readAt).length}건") {
		t.Error("the bell counts unread notices among the ones it fetched; the feed is capped, so that number is about the page")
	}
	if strings.Contains(source, "notifications.some((item)=>!item.readAt)&&<i/>") {
		t.Error("the bell's dot is decided by the fetched page; an older unread notice leaves it dark")
	}
	if !strings.Contains(source, "unread?:number") {
		t.Error("the console no longer reads the unread count the server sends")
	}

	handler, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(handler)
	at := strings.Index(body, "func (s *Server) notifications(")
	if at < 0 {
		t.Fatal("the notifications handler is gone; this guard is reading nothing")
	}
	fn := body[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	if !strings.Contains(fn, `"unread"`) {
		t.Error("the notifications endpoint no longer sends an unread count; the console falls back to counting its page")
	}
}
