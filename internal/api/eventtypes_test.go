package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/store"
)

// The console has to offer every event a trigger may subscribe to.
//
// task.handoff was published by the platform, accepted by the API, and written
// about in the guide — "인계된 작업은 … `task.handoff` 이벤트로 Trigger를 걸 수도 있습니다" —
// and the dropdown a person picks from did not list it. There was no way to
// subscribe to it from the console at all, and the recent-events panel showed it
// as a bare `task.handoff` because the label map had never heard of it either.
//
// Both lists are read here: one is what somebody can choose, the other is how it
// reads once it happens.
func TestTheConsoleOffersEveryPublishableEvent(t *testing.T) {
	for _, list := range []struct{ file, constant string }{
		{filepath.Join("..", "..", "web", "src", "pages", "Agents.tsx"), "EVENT_TYPES"},
		{filepath.Join("..", "..", "web", "src", "pages", "Tasks.tsx"), "EVENT_LABELS"},
	} {
		body, err := os.ReadFile(list.file)
		if err != nil {
			t.Skipf("console source is not present in this checkout: %v", err)
		}
		at := strings.Index(string(body), list.constant)
		if at < 0 {
			t.Fatalf("%s no longer holds %s; this guard is reading nothing", list.file, list.constant)
		}
		declaration := string(body)[at:]
		if end := strings.Index(declaration, "\n]"); end >= 0 {
			declaration = declaration[:end]
		}
		offered := map[string]bool{}
		for _, match := range regexp.MustCompile(`'([a-z]+\.[a-z_]+)'`).FindAllStringSubmatch(declaration, -1) {
			offered[match[1]] = true
		}
		var missing []string
		for _, event := range store.PublishableEvents {
			if !offered[event] {
				missing = append(missing, event)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s in %s does not know about %s; the platform publishes it and the API accepts a trigger for it",
				list.constant, filepath.Base(list.file), strings.Join(missing, ", "))
		}
	}
}
