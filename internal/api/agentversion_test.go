package api

import "testing"

// Skipping a pre-flight evaluation is the one way a definition reaches production
// unverified, so who may do it and whether they had to say why are the whole
// safeguard.
func TestPromotionNote(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		note    string
		force   bool
		want    string
		refusal string
	}{
		{name: "a normal promotion keeps the note it was given", role: "member", note: "  야간 배치 대응 ", want: "야간 배치 대응"},
		{name: "a normal promotion needs no note", role: "member"},
		{name: "only an administrator may skip the evaluation", role: "manager", note: "급합니다", force: true, refusal: "force_requires_admin"},
		{name: "skipping is never anonymous", role: "admin", note: "   ", force: true, refusal: "force_requires_note"},
		{name: "a forced promotion says so in its own record", role: "admin", note: "긴급 문구 수정", force: true, want: "검증 생략 승격: 긴급 문구 수정"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			note, refusal := promotionNote(test.role, test.note, test.force)
			if refusal != test.refusal {
				t.Fatalf("refusal = %q, want %q", refusal, test.refusal)
			}
			if note != test.want {
				t.Fatalf("note = %q, want %q", note, test.want)
			}
		})
	}
}
