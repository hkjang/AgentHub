package dlp

import (
	"strings"
	"testing"
)

// all turns the scanner on for one class, which is how every test here starts.
func all(class, action string) Settings {
	return Settings{Enabled: true, Classes: map[string]string{class: action}}
}

// A scanner that cries wolf is switched off within a week, so the checksums are
// the feature: these are the numbers that look like the thing and are not.
func TestDetectorsRejectLookalikes(t *testing.T) {
	cases := []struct {
		class string
		real  string
		fake  string
	}{
		// A valid RRN and thirteen digits that fail the check digit.
		{class: "rrn", real: "900101-1234568", fake: "900101-1234567"},
		// A Luhn-valid test card and one digit off.
		{class: "card", real: "4111 1111 1111 1111", fake: "4111 1111 1111 1112"},
		// A valid business number and a broken one.
		{class: "business", real: "220-81-62517", fake: "220-81-62518"},
	}
	for _, test := range cases {
		t.Run(test.class, func(t *testing.T) {
			settings := all(test.class, Audit)
			if got := Scan(settings, "값: "+test.real).Findings; len(got) != 1 || got[0].Count != 1 {
				t.Fatalf("the real value was not found: %#v", got)
			}
			if got := Scan(settings, "값: "+test.fake).Findings; len(got) != 0 {
				t.Fatalf("a lookalike was reported as %s: %#v", test.class, got)
			}
		})
	}
}

func TestDetectorsFindWhatTheyShould(t *testing.T) {
	cases := []struct{ class, text string }{
		{"rrn", "주민번호는 900101-1234568 입니다"},
		{"rrn", "9001011234568"},
		{"card", "카드 4111-1111-1111-1111 로 결제"},
		{"phone", "연락처 010-1234-5678"},
		{"phone", "01012345678"},
		{"email", "메일 hong@example.co.kr 로 보내주세요"},
		{"passport", "여권 M12345678"},
		{"secret", "export OPENAI_API_KEY=sk-abcdefghijklmnop1234"},
		{"secret", "-----BEGIN RSA PRIVATE KEY-----"},
		{"account", "국민 123456-01-123456 으로 입금"},
	}
	for _, test := range cases {
		t.Run(test.class+"/"+test.text[:min(12, len(test.text))], func(t *testing.T) {
			result := Scan(all(test.class, Audit), test.text)
			if len(result.Findings) == 0 {
				t.Fatalf("%s was not found in %q", test.class, test.text)
			}
			if result.Findings[0].Class != test.class {
				t.Fatalf("found %s, want %s", result.Findings[0].Class, test.class)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Redaction has to leave the text usable: the agent still needs its context, and
// the marker has to say what was removed.
func TestRedactionKeepsTheRestOfTheText(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"rrn": Redact, "phone": Redact}}
	result := Scan(settings, "고객 홍길동(900101-1234568), 연락처 010-1234-5678, 주문번호 A-1234")
	if result.Blocked {
		t.Fatal("redaction must not block")
	}
	if strings.Contains(result.Text, "900101-1234568") || strings.Contains(result.Text, "010-1234-5678") {
		t.Fatalf("the values survived redaction: %s", result.Text)
	}
	for _, keep := range []string{"고객 홍길동", "주문번호 A-1234", "주민등록번호 삭제됨", "휴대전화번호 삭제됨"} {
		if !strings.Contains(result.Text, keep) {
			t.Fatalf("%q is missing from %s", keep, result.Text)
		}
	}
}

// Nothing that was found may appear in what the platform records about it.
func TestFindingsNeverCarryTheValue(t *testing.T) {
	result := Scan(all("rrn", Audit), "주민번호 900101-1234568")
	finding := result.Findings[0]
	if strings.Contains(finding.Sample, "1234568") {
		t.Fatalf("the sample discloses the value: %q", finding.Sample)
	}
	if !strings.HasPrefix(finding.Sample, "900101") || !strings.Contains(finding.Sample, "*") {
		t.Fatalf("the sample must be recognisable but masked: %q", finding.Sample)
	}
	if strings.Contains(result.Summary(), "1234568") {
		t.Fatalf("the summary discloses the value: %q", result.Summary())
	}
}

func TestBlockRefusesAndNamesTheClassOnly(t *testing.T) {
	result := Scan(all("rrn", Block), "주민번호 900101-1234568 입니다")
	if !result.Blocked {
		t.Fatal("a blocking class must block")
	}
	if !strings.Contains(result.Reason, "주민등록번호") {
		t.Fatalf("the reason must name the class: %q", result.Reason)
	}
	if strings.Contains(result.Reason, "900101") {
		t.Fatalf("the reason discloses the value: %q", result.Reason)
	}
	// A blocked payload is not partly sent: the text is left alone and the caller
	// refuses the whole call.
	if result.Text != "주민번호 900101-1234568 입니다" {
		t.Fatalf("a blocked scan must not rewrite the text: %q", result.Text)
	}
}

// A class nobody configured is not scanned, so adding a detector to the platform
// never starts blocking somebody's traffic without them choosing it.
func TestOnlyConfiguredClassesAreScanned(t *testing.T) {
	text := "주민번호 900101-1234568, 메일 a@b.co"
	if findings := Scan(all("email", Audit), text).Findings; len(findings) != 1 || findings[0].Class != "email" {
		t.Fatalf("an unconfigured class was scanned: %#v", findings)
	}
	if findings := Scan(Settings{Enabled: false, Classes: map[string]string{"rrn": Block}}, text).Findings; len(findings) != 0 {
		t.Fatal("a disabled scanner must not scan")
	}
	if result := Scan(Settings{Enabled: true}, text); len(result.Findings) != 0 || result.Text != text {
		t.Fatal("no configured classes means no scanning")
	}
}

// A large payload must not turn one call into a CPU incident, and a partial scan
// must not read as a clean one.
func TestScanIsBounded(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"rrn": Redact}, MaxBytes: 64}
	tail := strings.Repeat("x", 200) + " 900101-1234568"
	result := Scan(settings, tail)
	if !result.Truncated {
		t.Fatal("a payload past the limit must be reported as truncated")
	}
	// What was beyond the limit is carried through unchanged rather than dropped:
	// silently truncating the payload would be a worse failure than not scanning.
	if !strings.HasSuffix(result.Text, "900101-1234568") {
		t.Fatalf("the tail was dropped: %q", result.Text)
	}
}

func TestSettingsValidate(t *testing.T) {
	if err := (Settings{Enabled: true, Classes: map[string]string{"rrn": Block}}).Validate(); err != nil {
		t.Fatalf("a normal configuration must be accepted: %v", err)
	}
	if err := (Settings{Classes: map[string]string{"passport-number": Block}}).Validate(); err == nil {
		t.Fatal("an unknown class must be refused; it would silently never scan")
	}
	if err := (Settings{Classes: map[string]string{"rrn": "quarantine"}}).Validate(); err == nil {
		t.Fatal("an unknown action must be refused")
	}
	if err := (Settings{MaxBytes: 64 << 20}).Validate(); err == nil {
		t.Fatal("an unbounded scan limit must be refused")
	}
}

// One value must not be reported as two classes, or every count an operator
// reads is inflated.
func TestPhoneNumbersAreNotAlsoAccounts(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"phone": Audit, "account": Audit}}
	result := Scan(settings, "연락처 010-1234-5678")
	if len(result.Findings) != 1 || result.Findings[0].Class != "phone" {
		t.Fatalf("a phone number was double-counted: %#v", result.Findings)
	}
}

// Telling accounts and phone numbers apart by "does it start with a zero" cost
// the whole class of accounts that do. These are the banks an offline site
// actually uses, and a scanner told to block them was letting every one of them
// out — the quiet half of a false negative nobody sees in an audit trail.
func TestAccountsThatBeginWithZeroAreStillFound(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"phone": Audit, "account": Block}}
	accounts := []string{
		"012-345678-01-011", // 기업은행
		"012345-01-123456",  // 우체국
		"0123-456-789012",   // 4자리 지점번호 + 6자리 계좌
	}
	for _, account := range accounts {
		t.Run(account, func(t *testing.T) {
			result := Scan(settings, "입금 계좌는 "+account+" 입니다")
			if !result.Blocked {
				t.Fatalf("an account number was not found: %q", account)
			}
			if len(result.Findings) != 1 || result.Findings[0].Class != "account" {
				t.Fatalf("%q was reported as %#v", account, result.Findings)
			}
		})
	}
}

// The shape a phone number takes is the reason the account detector has to be
// careful: three groups, an area code starting with zero, and exactly four
// digits at the end. Landlines are not reported by the phone detector either,
// but claiming them as bank accounts is the false positive that gets a scanner
// switched off.
func TestPhoneShapedNumbersAreNotAccounts(t *testing.T) {
	settings := all("account", Audit)
	for _, phone := range []string{"02-1234-5678", "031-123-4567", "070-1234-5678", "010-1234-5678"} {
		t.Run(phone, func(t *testing.T) {
			if findings := Scan(settings, "연락처 "+phone).Findings; len(findings) != 0 {
				t.Fatalf("%q was reported as an account: %#v", phone, findings)
			}
		})
	}
}

// The account shape has nothing but its grouping to go on, so it fits every other
// number this scanner knows about. A value that a check digit already identified
// must not be counted a second time as an account: an operator reading "계좌번호
// 12건" wants to know how many account numbers there were, and every card number
// and 사업자등록번호 in the payload was being added to that total.
func TestValuesWithACheckDigitAreNotAlsoAccounts(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{
		"card": Audit, "business": Audit, "rrn": Audit, "account": Audit,
	}}
	cases := []struct{ class, text string }{
		{"business", "사업자등록번호 220-81-62517"},
		{"card", "카드 4111-1111-1111-1111 로 결제"},
		{"rrn", "주민번호 900101-1234568"},
	}
	for _, test := range cases {
		t.Run(test.class, func(t *testing.T) {
			findings := Scan(settings, test.text).Findings
			if len(findings) != 1 {
				t.Fatalf("%q was reported under more than one class: %#v", test.text, findings)
			}
			if findings[0].Class != test.class {
				t.Fatalf("%q was reported as %s, want %s", test.text, findings[0].Class, test.class)
			}
		})
	}
}

// Whichever class claimed the value is also the one that names the marker. A
// 사업자등록번호 that leaves as "[계좌번호 삭제됨]" tells whoever reads the
// transcript the wrong thing about their own data.
func TestRedactionMarkerNamesTheClassThatClaimedTheValue(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"business": Redact, "account": Redact}}
	result := Scan(settings, "사업자등록번호 220-81-62517 입니다")
	if !strings.Contains(result.Text, "[사업자등록번호 삭제됨]") {
		t.Fatalf("the marker names the wrong class: %q", result.Text)
	}
	if strings.Contains(result.Text, "220-81-62517") {
		t.Fatalf("the value survived redaction: %q", result.Text)
	}
}

// An account number that is nothing else is still an account number — the claim
// only takes what an earlier detector actually matched.
func TestAccountsAreStillFoundAlongsideTheOtherClasses(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{
		"card": Audit, "business": Audit, "phone": Audit, "account": Audit,
	}}
	result := Scan(settings, "계좌 012345-01-123456, 카드 4111-1111-1111-1111, 연락처 010-1234-5678")
	got := map[string]int{}
	for _, finding := range result.Findings {
		got[finding.Class] = finding.Count
	}
	want := map[string]int{"card": 1, "phone": 1, "account": 1}
	if len(got) != len(want) {
		t.Fatalf("findings were double-counted: %#v", result.Findings)
	}
	for class, count := range want {
		if got[class] != count {
			t.Fatalf("%s: got %d, want %d (%#v)", class, got[class], count, result.Findings)
		}
	}
}

// Redaction replaces what a detector matched, and only that. The shapes nest —
// the account number here is a substring of the card number — so substituting by
// value rewrote a card number that was set to audit and was supposed to pass
// through whole. The payload the model or the tool receives has to be the payload
// minus the findings, not minus every place those bytes happen to appear.
func TestRedactionOnlyTouchesWhatWasMatched(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"card": Audit, "account": Redact}}
	result := Scan(settings, "카드 4111-1111-1111-1111 이고 사번 111-1111-1111 입니다")
	want := "카드 4111-1111-1111-1111 이고 사번 [계좌번호 삭제됨] 입니다"
	if result.Text != want {
		t.Fatalf("got  %q\nwant %q", result.Text, want)
	}
}

// Two classes redacting at once must each mark their own value where it stands.
// The detectors run in their own order, not the payload's, so the ranges are
// rewritten out of order unless they are sorted first.
func TestRedactionMarksEveryValueInPlace(t *testing.T) {
	settings := Settings{Enabled: true, Classes: map[string]string{"email": Redact, "phone": Redact, "rrn": Redact}}
	result := Scan(settings, "메일 hong@example.com, 연락처 010-1234-5678, 주민 900101-1234568, 그리고 010-9876-5432")
	want := "메일 [이메일 주소 삭제됨], 연락처 [휴대전화번호 삭제됨], 주민 [주민등록번호 삭제됨], 그리고 [휴대전화번호 삭제됨]"
	if result.Text != want {
		t.Fatalf("got  %q\nwant %q", result.Text, want)
	}
}

// A date is not an account number, which is what the minimum digit count is for.
func TestShortHyphenatedNumbersAreNotAccounts(t *testing.T) {
	settings := all("account", Audit)
	for _, text := range []string{"2026-09-02", "버전 1-2-3", "10-20-30"} {
		if findings := Scan(settings, text).Findings; len(findings) != 0 {
			t.Fatalf("%q was reported as an account: %#v", text, findings)
		}
	}
}
