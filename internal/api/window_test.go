package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

// Three reports now share this window, and a report for the wrong period is
// worse than an error: nobody checks the dates on a number that looks plausible.
func TestReportWindow(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		days    float64
		refused bool
	}{
		{name: "the default window is used when nothing is asked for", query: "", days: 7},
		{name: "days shortens it", query: "?days=1", days: 1},
		{name: "explicit endpoints win", query: "?from=2026-08-01T00:00:00Z&to=2026-08-11T00:00:00Z", days: 10},
		{name: "days and an explicit end combine", query: "?days=2&to=2026-08-11T00:00:00Z", days: 2},
		{name: "zero days is refused rather than treated as now", query: "?days=0", refused: true},
		{name: "a year is refused", query: "?days=400", refused: true},
		{name: "a non-numeric window is refused", query: "?days=lots", refused: true},
		{name: "an unparseable start is refused", query: "?from=yesterday", refused: true},
		{name: "an unparseable end is refused", query: "?to=soon", refused: true},
		{name: "a backwards window is refused", query: "?from=2026-08-11T00:00:00Z&to=2026-08-01T00:00:00Z", refused: true},
		{name: "too wide a window is refused", query: "?from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", refused: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			from, to, err := reportWindow(httptest.NewRequest("GET", "/x"+test.query, nil), 7)
			if test.refused {
				if err == nil {
					t.Fatalf("the window must be refused; got %s..%s", from, to)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			// Two days of tolerance is meaningless here; the window is exact.
			if got := to.Sub(from); got != time.Duration(test.days*24)*time.Hour {
				t.Fatalf("window is %s, want %g days", got, test.days)
			}
		})
	}
	// The rolling default is anchored to now, not to a fixed date.
	from, to, err := reportWindow(httptest.NewRequest("GET", "/x", nil), 30)
	if err != nil || time.Since(to) > time.Minute || to.Sub(from) != 30*24*time.Hour {
		t.Fatalf("default window is %s..%s (%v)", from, to, err)
	}
}
