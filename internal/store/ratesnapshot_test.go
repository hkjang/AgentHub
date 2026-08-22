package store

import (
	"os"
	"strings"
	"testing"
)

// A bill has to stay put.
//
// Cost was computed by joining model_endpoints at query time, so every past month
// was priced at today's rates. On this platform's own data a window that read 0
// before an administrator entered the real prices read 52.17 afterwards, for work
// that had already happened; deleting an endpoint took its history to zero,
// because the join found nothing.
//
// The same expression prices the cost quota, so a correction also decided
// retroactively whether somebody was over budget — refusing new work on the
// strength of a number that had moved under them.
func TestCostIsPricedAtTheRateTheRunWasCharged(t *testing.T) {
	body, err := os.ReadFile("usage.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "const usageCostSQL = ")
	if at < 0 {
		t.Fatal("the cost expression is gone; this guard is reading nothing")
	}
	expression := source[at:]
	if end := strings.Index(expression[strings.Index(expression, "`")+1:], "`"); end >= 0 {
		expression = expression[:strings.Index(expression, "`")+end+2]
	}
	// The run's own rate comes first.
	for _, half := range []string{"r.input_price_per_mtok", "r.output_price_per_mtok"} {
		if !strings.Contains(expression, half) {
			t.Errorf("cost does not use the run's own %s; the bill is priced at today's rates again", half)
		}
	}
	if input := strings.Index(expression, "r.input_price_per_mtok"); input < 0 || input > strings.Index(expression, "m.input_price_per_mtok") {
		t.Error("the endpoint's price is preferred over the run's own; a correction would rewrite history")
	}
	// And the endpoint stays as the fallback, or every run from before the
	// snapshot existed drops to zero on upgrade.
	for _, fallback := range []string{"m.input_price_per_mtok", "m.output_price_per_mtok"} {
		if !strings.Contains(expression, fallback) {
			t.Errorf("there is no fallback to %s; runs recorded before the snapshot would price at zero", fallback)
		}
	}
}

// The rate has to be written when the run is created. Reading it at billing time
// is the thing being fixed, so a run that records no rate is a run that will be
// repriced by the next correction.
func TestARunRecordsTheRateItWasChargedAt(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) CreateAgentRun(")
	if at < 0 {
		t.Fatal("CreateAgentRun is gone; this guard is reading nothing")
	}
	create := source[at:]
	if end := strings.Index(create, "\n}\n"); end >= 0 {
		create = create[:end]
	}
	for _, column := range []string{"input_price_per_mtok", "output_price_per_mtok", "price_currency"} {
		if !strings.Contains(create, column) {
			t.Errorf("a new run does not record %s; it will be priced at whatever the rate is when somebody asks", column)
		}
	}
	if !strings.Contains(create, "model_endpoints m") {
		t.Error("the rate is not read from the endpoint at creation time")
	}
}
