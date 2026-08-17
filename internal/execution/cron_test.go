package execution

import (
	"testing"
	"time"
)

func mustSchedule(t *testing.T, expression string) *Schedule {
	t.Helper()
	schedule, err := ParseSchedule(expression)
	if err != nil {
		t.Fatalf("ParseSchedule(%q): %v", expression, err)
	}
	return schedule
}

func TestScheduleNextComputesTheExpectedTime(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("tzdata is unavailable in this environment")
	}
	tests := []struct {
		name       string
		expression string
		after      time.Time
		want       time.Time
	}{
		{
			name: "daily at 08:00 rolls to tomorrow once passed",
			// The document's example: 매일 08:00.
			expression: "0 8 * * *",
			after:      time.Date(2026, 8, 17, 9, 0, 0, 0, seoul),
			want:       time.Date(2026, 8, 18, 8, 0, 0, 0, seoul),
		},
		{
			name:       "daily at 08:00 fires later the same day",
			expression: "0 8 * * *",
			after:      time.Date(2026, 8, 17, 3, 30, 0, 0, seoul),
			want:       time.Date(2026, 8, 17, 8, 0, 0, 0, seoul),
		},
		{
			name:       "every 30 minutes",
			expression: "*/30 * * * *",
			after:      time.Date(2026, 8, 17, 10, 5, 0, 0, seoul),
			want:       time.Date(2026, 8, 17, 10, 30, 0, 0, seoul),
		},
		{
			name:       "weekday-only schedule skips the weekend",
			expression: "0 9 * * 1-5",
			after:      time.Date(2026, 8, 15, 12, 0, 0, 0, seoul), // Saturday
			want:       time.Date(2026, 8, 17, 9, 0, 0, 0, seoul),  // Monday
		},
		{
			name:       "a specific day of month crosses into the next month",
			expression: "0 0 1 * *",
			after:      time.Date(2026, 8, 17, 0, 0, 0, 0, seoul),
			want:       time.Date(2026, 9, 1, 0, 0, 0, 0, seoul),
		},
		{
			name:       "a list of hours picks the next one",
			expression: "15 9,13,18 * * *",
			after:      time.Date(2026, 8, 17, 10, 0, 0, 0, seoul),
			want:       time.Date(2026, 8, 17, 13, 15, 0, 0, seoul),
		},
	}
	for _, test := range tests {
		got := mustSchedule(t, test.expression).Next(test.after, seoul)
		if !got.Equal(test.want) {
			t.Errorf("%s: Next() = %s, want %s", test.name, got.Format(time.RFC3339), test.want.Format(time.RFC3339))
		}
	}
}

func TestScheduleNextIsAlwaysStrictlyLater(t *testing.T) {
	// A schedule that matches the current minute must not return that minute
	// again, or the scheduler would fire the same occurrence repeatedly.
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	next := mustSchedule(t, "0 8 * * *").Next(now, time.UTC)
	if !next.After(now) {
		t.Fatalf("Next(%s) = %s, which is not later", now, next)
	}
}

func TestParseScheduleRejectsMalformedExpressions(t *testing.T) {
	for _, expression := range []string{
		"", "0 8 * *", "0 8 * * * *", "60 8 * * *", "0 24 * * *",
		"0 8 0 * *", "0 8 * 13 *", "0 8 * * 7", "*/0 * * * *", "a * * * *", "5-1 * * * *",
	} {
		if _, err := ParseSchedule(expression); err == nil {
			t.Errorf("ParseSchedule(%q) must be rejected", expression)
		}
	}
}

func TestNextFireAtReturnsUTCAndHonoursTheTimezone(t *testing.T) {
	if _, err := time.LoadLocation("Asia/Seoul"); err != nil {
		t.Skip("tzdata is unavailable in this environment")
	}
	after := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC) // 09:00 KST
	next, err := NextFireAt("0 8 * * *", "Asia/Seoul", after)
	if err != nil {
		t.Fatal(err)
	}
	// 08:00 KST already passed, so the next fire is tomorrow 08:00 KST = 23:00 UTC today.
	want := time.Date(2026, 8, 17, 23, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("NextFireAt = %s, want %s", next.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if next.Location() != time.UTC {
		t.Fatalf("the schedule must be stored in UTC, got %s", next.Location())
	}
}

func TestNextFireAtFallsBackToUTCForAnUnknownTimezone(t *testing.T) {
	// An unknown timezone must still produce a schedule; silently never firing
	// would be far worse than firing on UTC.
	next, err := NextFireAt("0 8 * * *", "Mars/Olympus", time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next.Hour() != 8 {
		t.Fatalf("expected 08:00 UTC, got %s", next.Format(time.RFC3339))
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 1; attempt <= 4; attempt++ {
		delay := backoff(attempt)
		if delay <= previous {
			t.Fatalf("backoff(%d) = %s, which did not grow past %s", attempt, delay, previous)
		}
		previous = delay
	}
	if capped := backoff(50); capped != 300*time.Second {
		t.Fatalf("backoff must be capped at 300s, got %s", capped)
	}
	if first := backoff(0); first <= 0 {
		t.Fatal("backoff must be positive even for a zero attempt")
	}
}
