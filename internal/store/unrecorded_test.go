package store

import (
	"os"
	"strings"
	"testing"
)

// The bill has to say how short it is.
//
// Every total in this file is built from steps. A run that says it metered real
// usage and left none on its steps is spend the totals are confidently missing —
// and unlike an unmetered run, nothing about it looks unusual. It was true of
// four backends at once, and no number anywhere said so.
//
// Both places that add up money count it, because they are read by different
// people: one is a person's own usage, the other is what a bill is reconciled
// against and the export is written from.
func TestBothReportsCountWhatTheirTotalsAreMissing(t *testing.T) {
	for _, file := range []string{"usage.go", "platform.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		if !strings.Contains(source, "UnrecordedRuns") {
			t.Errorf("%s 는 합계에서 빠진 실행을 세지 않습니다 — 짧은 합계가 정확한 합계처럼 보입니다", file)
			continue
		}
		// The three conditions that make it a contradiction rather than a
		// preference. Dropping any one of them turns the count into something else:
		// without the metering filter it counts runs that never claimed to know,
		// without the token test it counts runs that spent nothing, and without the
		// zero-steps test it counts runs the totals can see perfectly well.
		for _, condition := range []struct{ what, sql string }{
			{"계량됐다고 표시된 실행만", "r.metering = $"},
			{"실제로 토큰을 쓴 실행만", "r.total_tokens > 0"},
			{"단계에 아무것도 없는 실행만", "prompt_tokens + s.completion_tokens"},
		} {
			if !strings.Contains(source, condition.sql) {
				t.Errorf("%s: %s 이라는 조건이 없습니다 (%s)", file, condition.what, condition.sql)
			}
		}
	}
}
