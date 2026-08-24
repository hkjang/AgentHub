package store

import (
	"strings"
	"testing"
)

// The sentence an administrator reads when a version is waiting for promotion
// interpolates version numbers, and a digit's particle follows how the digit is
// said: v3 is 삼, which ends in ㅁ, so it takes 은 rather than 는.
func TestThePromotionNoticeIsKorean(t *testing.T) {
	for _, testCase := range []struct {
		current, promoted int
		wants, avoids     []string
	}{
		{current: 3, promoted: 2, wants: []string{"v3은", "v3을", "v2를"}, avoids: []string{"v3는", "v3를", "v2을"}},
		{current: 2, promoted: 1, wants: []string{"v2는", "v2를", "v1을"}, avoids: []string{"v2은", "v2을", "v1를"}},
		{current: 10, promoted: 9, wants: []string{"v10은", "v10을", "v9를"}, avoids: []string{"v10는", "v9을"}},
	} {
		promoted := testCase.promoted
		notice := promotionBlock(AgentRelease{RequirePromotion: true, Current: testCase.current, PromotedVersion: &promoted})
		for _, want := range testCase.wants {
			if !strings.Contains(notice, want) {
				t.Errorf("v%d/v%d: %q is missing from %q", testCase.current, promoted, want, notice)
			}
		}
		for _, avoid := range testCase.avoids {
			if strings.Contains(notice, avoid) {
				t.Errorf("v%d/v%d: %q reads as broken Korean in %q", testCase.current, promoted, avoid, notice)
			}
		}
	}
}
