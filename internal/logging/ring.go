package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Entry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Source  string         `json:"source"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type Ring struct {
	mu      sync.RWMutex
	entries []Entry
	next    int
	full    bool
	limit   int
}

func NewRing(limit int) *Ring {
	if limit < 100 {
		limit = 100
	}
	return &Ring{entries: make([]Entry, limit), limit: limit}
}

func (r *Ring) Add(entry Entry) {
	r.mu.Lock()
	r.entries[r.next] = entry
	r.next = (r.next + 1) % r.limit
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *Ring) Entries(min slog.Level, query string, limit int) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := r.next
	start := 0
	if r.full {
		count = r.limit
		start = r.next
	}
	if limit <= 0 || limit > count {
		limit = count
	}
	result := make([]Entry, 0, limit)
	for offset := count - 1; offset >= 0 && len(result) < limit; offset-- {
		entry := r.entries[(start+offset)%r.limit]
		if parseLevel(entry.Level) < min {
			continue
		}
		if query != "" && !matches(entry, query) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

// matches reports whether a search term appears anywhere in the entry a person
// is actually looking at.
//
// The console prints the structured fields on the line, right after the message
// — the agent's name, the runtime id, the error the platform gave up with. The
// search box next to that line used to read the message and the source and
// nothing else, so an operator who typed a task id they could see on screen was
// told 조건에 맞는 로그가 없습니다 about the very line in front of them, and the
// only way to find it was to stop searching and scroll. A field that is worth
// rendering is worth finding.
func matches(entry Entry, query string) bool {
	if containsFold(entry.Message, query) || containsFold(entry.Source, query) {
		return true
	}
	for key, value := range entry.Fields {
		// The key too: "error" is what an operator narrows to when they want the
		// failures and do not yet know what any of them say.
		if containsFold(key, query) {
			return true
		}
		// A search that misses reads every field of every entry, and it does that
		// while holding the lock every request path needs to write its own line.
		// Almost all of them are already strings, so the formatter is for the few
		// that are not rather than for all of them.
		if text, ok := value.(string); ok {
			if containsFold(text, query) {
				return true
			}
			continue
		}
		if containsFold(fmt.Sprint(value), query) {
			return true
		}
	}
	return false
}

func parseLevel(value string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return slog.LevelInfo
	}
	return level
}

func containsFold(value, query string) bool {
	if len(query) > len(value) {
		return false
	}
	for i := 0; i+len(query) <= len(value); i++ {
		match := true
		for j := range query {
			a, b := value[i+j], query[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return query == ""
}

type captureHandler struct {
	next   slog.Handler
	ring   *Ring
	attrs  []slog.Attr
	groups []string
}

func Capture(next slog.Handler, ring *Ring) slog.Handler {
	return &captureHandler{next: next, ring: ring}
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	fields := map[string]any{}
	for _, attr := range h.attrs {
		fields[attr.Key] = attr.Value.Any()
	}
	record.Attrs(func(attr slog.Attr) bool { fields[attr.Key] = attr.Value.Any(); return true })
	h.ring.Add(Entry{Time: record.Time, Level: record.Level.String(), Message: record.Message, Source: "control-plane", Fields: fields})
	return h.next.Handle(ctx, record)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copyAttrs := append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &captureHandler{next: h.next.WithAttrs(attrs), ring: h.ring, attrs: copyAttrs, groups: h.groups}
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	groups := append(append([]string{}, h.groups...), name)
	return &captureHandler{next: h.next.WithGroup(name), ring: h.ring, attrs: h.attrs, groups: groups}
}
