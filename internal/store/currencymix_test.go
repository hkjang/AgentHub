package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every number under the bill is a single figure in a single currency. The price
// table is not: each endpoint carries its own currency string, typed by hand in
// the console, and two endpoints in different currencies were summed and the
// total labelled with whichever string sorted highest. ₩10,000 of local
// inference plus $7 of hosted inference was reported as "10007 USD" — a
// confident number that is not a quantity of anything.
//
// The arithmetic is deliberately unchanged: rewriting it on the strength of a
// misconfiguration would be worse than saying what happened. What must not
// happen is the number going on claiming to be something it is not.
func TestTheBillSaysWhichCurrenciesItMixed(t *testing.T) {
	body, err := os.ReadFile("platform.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) PlatformSpend(")
	if at < 0 {
		t.Fatal("PlatformSpend is gone; this guard is reading nothing")
	}
	spend := source[at:]
	if end := strings.Index(spend, "\nfunc "); end >= 0 {
		spend = spend[:end]
	}
	if !strings.Contains(spend, "SELECT DISTINCT") || !strings.Contains(spend, "price_currency") {
		t.Error("the bill does not report which currencies it added together")
	}
	// Only priced work counts: an unpriced endpoint contributes nothing to the
	// total, so naming its currency would report a mix that is not in the number.
	// The source concatenates the constant into the query, so this looks for the
	// concatenation rather than for the expanded string — comparing against the
	// expanded constant is a guard that fails on how the code is written.
	if !strings.Contains(spend, "usageCostSQL+` > 0") {
		t.Error("unpriced work is counted as a currency in the mix; it contributes nothing to the total")
	}
	// The run's own currency comes first, for the same reason its own rate does.
	if run, endpoint := strings.Index(spend, "r.price_currency"), strings.Index(spend, "m.currency"); run < 0 || (endpoint >= 0 && run > endpoint) {
		t.Error("the endpoint's currency is preferred over the run's own; a currency change would relabel history")
	}
}

// And the console has to act on it rather than printing the total as though one
// currency were meant.
func TestTheConsoleRefusesToLabelAMixedTotal(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "AdminInsights.tsx"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	source := string(body)
	if !strings.Contains(source, "currencies?: string[]") {
		t.Error("the console does not read which currencies the bill mixed")
	}
	if !strings.Contains(source, "통화 혼재") {
		t.Error("a mixed total is still printed with one currency's name on it")
	}
	if !strings.Contains(source, "비용 예산도 같은 계산을 쓰므로") {
		t.Error("the warning does not say that the cost budget is computed the same way; that is the part with consequences")
	}
}
