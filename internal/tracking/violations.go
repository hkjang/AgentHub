package tracking

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxViolations bounds the recorder. A blocked request repeats on every page
// view, so what matters is which origins are blocked, not how often — a small
// buffer of distinct origins is enough to fix a snippet.
const MaxViolations = 100

// The upper bounds on the strings a report brings in. A report is posted
// without a session — the policy names the address and the browser, or anything
// else inside a pod, posts to it — so the only thing between the report and the
// administration screen is this file, and the size has to be decided here.
// An origin is a scheme and a host, so 300 runes leaves room above the 253
// characters a DNS name can hold and cuts nothing anybody would want to allow
// with one click. A directive is a single policy keyword, where 64 runes is
// generous enough that a future one still arrives whole. A page was already
// being cut at 200, so that number stays and only its unit changes.
const (
	maxOriginRunes    = 300
	maxDirectiveRunes = 64
	maxPageRunes      = 200
)

// cutRunes shortens a string to at most limit runes, on a letter boundary.
// Cutting by byte would leave a fragment of the last letter of a Korean
// address behind, and the JSON response would carry it out as a replacement
// character.
func cutRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	seen := 0
	for index := range text {
		if seen == limit {
			return text[:index]
		}
		seen++
	}
	return text
}

// Violation is one origin the content security policy refused, kept with the
// directive that refused it so the console can say what to allow.
type Violation struct {
	Origin    string    `json:"origin"`
	Directive string    `json:"directive"`
	Page      string    `json:"page"`
	Count     int       `json:"count"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	// Allowed marks an origin the current settings already let through, so a
	// fixed snippet stops nagging without the list having to be cleared.
	Allowed bool `json:"allowed"`
}

// Recorder collects the policy violations browsers report. It is deliberately
// in memory: the reports are a live troubleshooting aid for the person pasting
// a snippet, not an audit record, and keeping them out of the database means a
// browser can report freely without growing storage.
type Recorder struct {
	mutex      sync.Mutex
	violations map[string]*Violation
	now        func() time.Time
}

func NewRecorder() *Recorder {
	return &Recorder{violations: make(map[string]*Violation), now: time.Now}
}

// Record notes one blocked request. Anything that is not an http origin — a
// browser extension, a data: URL — is ignored, because allowing it is neither
// possible nor useful.
func (r *Recorder) Record(blockedURI, directive, page string) {
	if r == nil {
		return
	}
	origin := originOf(blockedURI)
	if origin == "" || !strings.HasPrefix(strings.ToLower(blockedURI), "http") {
		return
	}
	directive = strings.TrimSpace(strings.ToLower(directive))
	if index := strings.IndexByte(directive, ' '); index > 0 {
		directive = directive[:index]
	}
	if directive == "" {
		directive = "connect-src"
	}
	// Cut before the key is built: a key made from the full strings would give
	// the same oversized report a different entry every time the report grew by
	// a letter, and the repeat count administrators read would never rise.
	origin = cutRunes(origin, maxOriginRunes)
	directive = cutRunes(directive, maxDirectiveRunes)
	page = cutRunes(page, maxPageRunes)
	r.mutex.Lock()
	defer r.mutex.Unlock()
	key := directive + " " + strings.ToLower(origin)
	if existing, found := r.violations[key]; found {
		existing.Count++
		existing.LastSeen = r.now()
		existing.Page = page
		return
	}
	if len(r.violations) >= MaxViolations {
		r.evictOldest()
	}
	moment := r.now()
	r.violations[key] = &Violation{Origin: origin, Directive: directive, Page: page, Count: 1, FirstSeen: moment, LastSeen: moment}
}

func (r *Recorder) evictOldest() {
	var oldestKey string
	var oldest time.Time
	for key, violation := range r.violations {
		if oldestKey == "" || violation.LastSeen.Before(oldest) {
			oldestKey, oldest = key, violation.LastSeen
		}
	}
	delete(r.violations, oldestKey)
}

// List returns the blocked origins, most recent first, marking the ones the
// given settings already allow.
func (r *Recorder) List(settings Settings) []Violation {
	items := []Violation{}
	if r == nil {
		return items
	}
	allowed := map[string]struct{}{}
	for _, origin := range settings.PolicySources().all() {
		allowed[strings.ToLower(strings.TrimSuffix(origin, "/"))] = struct{}{}
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	for _, violation := range r.violations {
		copied := *violation
		_, known := allowed[strings.ToLower(copied.Origin)]
		copied.Allowed = known || matchesWildcard(copied.Origin, allowed)
		items = append(items, copied)
	}
	sort.Slice(items, func(first, second int) bool {
		if items[first].LastSeen.Equal(items[second].LastSeen) {
			return items[first].Origin < items[second].Origin
		}
		return items[first].LastSeen.After(items[second].LastSeen)
	})
	return items
}

// Forget drops the recorded violations, which is what an administrator does
// after fixing a snippet to see whether anything is still blocked.
func (r *Recorder) Forget() {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.violations = make(map[string]*Violation)
}

// matchesWildcard covers policy entries such as https://*.google-analytics.com.
func matchesWildcard(origin string, allowed map[string]struct{}) bool {
	lowered := strings.ToLower(origin)
	for pattern := range allowed {
		star := strings.Index(pattern, "*.")
		if star < 0 {
			continue
		}
		if strings.HasPrefix(lowered, pattern[:star]) && strings.HasSuffix(lowered, pattern[star+1:]) {
			return true
		}
	}
	return false
}
