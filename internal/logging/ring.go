package logging

import (
	"context"
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
		if query != "" && !containsFold(entry.Message, query) && !containsFold(entry.Source, query) {
			continue
		}
		result = append(result, entry)
	}
	return result
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
