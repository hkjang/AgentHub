package workflow

import "testing"

// A workflow's steps belong to different agents and so possibly different
// endpoints. That is why there is no single rate to apply to a run afterwards,
// and why the money went uncounted while the tokens were counted — the guide
// called it the honest half.
//
// The rate travels with the step now, so each call is priced by the endpoint that
// answered it, at the moment it answered.
func TestEachStepIsPricedByTheEndpointThatAnsweredIt(t *testing.T) {
	steps := []Step{
		{ID: "a", InputPricePerMTok: 3000, OutputPricePerMTok: 15000, Currency: "KRW"},
		{ID: "b", InputPricePerMTok: 1, OutputPricePerMTok: 2, Currency: "KRW"},
	}
	results := map[string]*StepResult{
		"a": {ID: "a", PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000},
		"b": {ID: "b", PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000},
	}
	out := finish(Result{}, results, steps, 2)
	if out.TotalTokens != 4_000_000 {
		t.Errorf("tokens = %d", out.TotalTokens)
	}
	// 3000+15000 for the first step, 1+2 for the second: each at its own rate,
	// which is the whole point — one rate for the run would be wrong for one of
	// them however it was chosen.
	if want := 18003.0; out.Cost != want {
		t.Errorf("cost = %v, want %v", out.Cost, want)
	}
	if out.Currency != "KRW" {
		t.Errorf("currency = %q", out.Currency)
	}
}

// A run whose steps were charged in different currencies says so rather than
// picking one. Summing across currencies is what the platform's own bill had to
// stop pretending about.
func TestARunThatMixedCurrenciesSaysSo(t *testing.T) {
	steps := []Step{
		{ID: "a", InputPricePerMTok: 10000, Currency: "KRW"},
		{ID: "b", InputPricePerMTok: 7, Currency: "USD"},
	}
	results := map[string]*StepResult{
		"a": {ID: "a", PromptTokens: 1_000_000, TotalTokens: 1_000_000},
		"b": {ID: "b", PromptTokens: 1_000_000, TotalTokens: 1_000_000},
	}
	out := finish(Result{}, results, steps, 2)
	if out.Currency != "KRW+USD" {
		t.Errorf("currency = %q; a run that mixed currencies must not wear one of their names", out.Currency)
	}
}

// An unpriced endpoint contributes no money and no currency. Naming its currency
// would report a mixture that is not in the number.
func TestUnpricedStepsAddNothingAndNameNothing(t *testing.T) {
	steps := []Step{
		{ID: "a", InputPricePerMTok: 1000, Currency: "KRW"},
		{ID: "b", Currency: "USD"},
	}
	results := map[string]*StepResult{
		"a": {ID: "a", PromptTokens: 1_000_000, TotalTokens: 1_000_000},
		"b": {ID: "b", PromptTokens: 5_000_000, TotalTokens: 5_000_000},
	}
	out := finish(Result{}, results, steps, 2)
	if out.Cost != 1000 {
		t.Errorf("cost = %v; an unpriced step must add nothing", out.Cost)
	}
	if out.Currency != "KRW" {
		t.Errorf("currency = %q; an unpriced step must not put its currency in the mix", out.Currency)
	}
	// Its tokens still count: unpriced is not unmetered.
	if out.TotalTokens != 6_000_000 {
		t.Errorf("tokens = %d; an unpriced step's tokens still count", out.TotalTokens)
	}
}
