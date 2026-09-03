// Package dlp finds sensitive data in text on its way out of the platform.
//
// An agent is a program that reads whatever it is pointed at and then sends it
// somewhere else: to a model gateway, to an MCP server, to a tool that writes a
// ticket. On an offline site that is exactly the risk — the data never had to
// leave the building until an agent helpfully summarised it into a prompt.
//
// The detectors here are deliberately conservative. A scanner that cries wolf is
// turned off within a week, so anything with a checksum is checked against it: a
// resident registration number that fails its check digit is not a resident
// registration number, and thirteen digits in a log line should not stop a
// production run.
package dlp

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// SettingKey is the system_settings row these settings live in.
const SettingKey = "dlp"

// Actions, in increasing order of severity.
const (
	// Off does not scan for this class at all.
	Off = "off"
	// Audit records what was found and lets the text through unchanged. It is
	// how a site learns what its agents actually handle before it starts
	// blocking.
	Audit = "audit"
	// Redact replaces the finding with a marker and lets the rest through. This
	// is the useful default for model calls: the agent still gets its context,
	// and the number never leaves.
	Redact = "redact"
	// Block refuses the call. For a tool that writes to another system, a partial
	// send is worse than no send.
	Block = "block"
)

// Actions is every action, for validation and for the console.
var Actions = []string{Off, Audit, Redact, Block}

// Detector is one kind of sensitive data.
type Detector struct {
	// Class is the identifier a policy rule names.
	Class string
	// Label is what a person reads.
	Label       string
	Description string
	pattern     *regexp.Regexp
	// validate rejects a candidate that matched the shape but is not the thing —
	// a check digit that does not add up, a card number that fails Luhn.
	validate func(string) bool
	// keep is how many leading characters survive redaction, so a redacted line
	// is still readable as a line.
	keep int
}

// detectors are ordered so that the more specific patterns run first, and Scan
// gives each match to the first detector that claims it. Specific here means
// "has something to check": a resident registration number, a card number and a
// 사업자등록번호 all carry a check digit, and all three also fit the account-number
// shape, which has nothing but its grouping to go on. Whoever runs first is who
// the value is reported as.
var detectors = []Detector{
	{
		Class: "rrn", Label: "주민등록번호", Description: "13자리 주민등록번호(체크섬 검증)",
		pattern: regexp.MustCompile(`\b\d{6}[-\s]?[1-8]\d{6}\b`), validate: validRRN, keep: 6,
	},
	{
		Class: "card", Label: "신용카드번호", Description: "13~19자리 카드번호(Luhn 검증)",
		pattern: regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`), validate: validCard, keep: 4,
	},
	{
		Class: "business", Label: "사업자등록번호", Description: "10자리 사업자등록번호(체크섬 검증)",
		pattern: regexp.MustCompile(`\b\d{3}-?\d{2}-?\d{5}\b`), validate: validBusiness, keep: 3,
	},
	{
		Class: "phone", Label: "휴대전화번호", Description: "01x-xxxx-xxxx 형식",
		pattern: regexp.MustCompile(`\b01[016789][-\s.]?\d{3,4}[-\s.]?\d{4}\b`), keep: 3,
	},
	{
		Class: "account", Label: "계좌번호", Description: "은행 계좌 형식(3-2-6 이상, 하이픈 포함)",
		pattern: regexp.MustCompile(`\b\d{2,6}-\d{2,6}-\d{2,8}(?:-\d{1,6})?\b`), validate: notPhone, keep: 4,
	},
	{
		Class: "passport", Label: "여권번호", Description: "대한민국 여권번호 형식",
		pattern: regexp.MustCompile(`\b[MSRODmsrod]\d{8}\b`), keep: 1,
	},
	{
		Class: "email", Label: "이메일 주소", Description: "이메일 주소",
		pattern: regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`), keep: 2,
	},
	{
		Class: "secret", Label: "자격증명·API 키", Description: "API 키, 토큰, 개인 키 블록",
		pattern: regexp.MustCompile(`(?:sk-[A-Za-z0-9]{16,}|ghp_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{12,}|xox[baprs]-[A-Za-z0-9-]{10,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`),
		keep:    4,
	},
}

// Detectors lists what the platform can find, for the console and for
// validation. The order is the scan order, which is also the order a person
// reads them in.
func Detectors() []Detector { return append([]Detector{}, detectors...) }

// Settings is what an administrator configures: one action per class.
type Settings struct {
	// Enabled turns the scanner on. Off means no text is inspected at all, which
	// is what a deployment that has not configured this gets.
	Enabled bool `json:"enabled"`
	// Classes maps a class to its action. A class that is not listed is not
	// scanned, so adding a detector to the platform never starts blocking
	// anybody's traffic without them choosing it.
	Classes map[string]string `json:"classes,omitempty"`
	// ScanResponses also inspects what comes back from a model or a tool. It is
	// separate because it is the expensive half and the less common risk.
	ScanResponses bool `json:"scanResponses,omitempty"`
	// MaxBytes bounds how much of one payload is scanned. A 2 MB tool result
	// should not turn one call into a CPU incident.
	MaxBytes int `json:"maxBytes,omitempty"`
}

// DefaultMaxBytes is what an unset MaxBytes means.
const DefaultMaxBytes = 256 * 1024

// Validate rejects settings the scanner would only misapply later.
func (s Settings) Validate() error {
	known := map[string]bool{}
	for _, detector := range detectors {
		known[detector.Class] = true
	}
	for class, action := range s.Classes {
		if !known[class] {
			return fmt.Errorf("지원하지 않는 데이터 등급입니다: %s", class)
		}
		if !contains(Actions, action) {
			return fmt.Errorf("%s: 처리 방식은 %s 중 하나여야 합니다", class, strings.Join(Actions, ", "))
		}
	}
	if s.MaxBytes < 0 || s.MaxBytes > 4*1024*1024 {
		return fmt.Errorf("검사 크기 상한은 0~%d 바이트여야 합니다", 4*1024*1024)
	}
	return nil
}

// Action reports what to do about one class.
func (s Settings) Action(class string) string {
	if !s.Enabled {
		return Off
	}
	if action, ok := s.Classes[class]; ok {
		return action
	}
	return Off
}

// active reports whether any class is scanned at all, so the hot path can skip
// the work entirely.
func (s Settings) active() bool {
	if !s.Enabled {
		return false
	}
	for _, action := range s.Classes {
		if action != Off {
			return true
		}
	}
	return false
}

// Finding is one class found in one payload.
type Finding struct {
	Class string `json:"class"`
	Label string `json:"label"`
	Count int    `json:"count"`
	// Action is what the settings said to do about it.
	Action string `json:"action"`
	// Sample is a masked example, so a person reading the audit trail can tell a
	// real finding from a false positive without the value being stored anywhere.
	Sample string `json:"sample"`
}

// Result is what a scan found and what the text became.
type Result struct {
	Findings []Finding `json:"findings"`
	// Text is the payload after redaction. It is the input unchanged when
	// nothing was redacted.
	Text string `json:"text"`
	// Blocked is set when a class configured to block was found. The caller
	// refuses the call; the reason names the classes, never the values.
	Blocked bool   `json:"blocked"`
	Reason  string `json:"reason,omitempty"`
	// Truncated reports that the payload was longer than the scan limit, so a
	// clean result is not mistaken for a complete one.
	Truncated bool `json:"truncated,omitempty"`
}

// Outcomes name what a scan did to the payload. They are what the audit trail
// files the event under and what the console shows in its 결과 column.
const (
	// OutcomeBlocked is a call that did not go out.
	OutcomeBlocked = "blocked"
	// OutcomeRedacted is text that left with a marker in place of a value.
	OutcomeRedacted = "redacted"
	// OutcomeAudited is a finding that was recorded and nothing else: the payload
	// went out exactly as the agent wrote it.
	OutcomeAudited = "audited"
)

// Outcome is what actually happened to the text.
//
// Every boundary used to record "redacted" for anything it found and did not
// block, which is not what Audit means. Audit is the documented way a site
// learns what its agents really handle before it starts blocking anything — and
// for as long as it ran that way, every entry in its trail claimed the platform
// had rewritten traffic it had in fact passed through untouched. The operator
// deciding whether to move a class from 기록만 to 가리고 전송 reads that trail;
// it was answering a question nobody had asked the settings.
//
// A class can be redacted and another only recorded in the same payload, and
// then the text did change, so one Redact finding is enough to say so.
func (r Result) Outcome() string {
	if r.Blocked {
		return OutcomeBlocked
	}
	for _, finding := range r.Findings {
		if finding.Action == Redact {
			return OutcomeRedacted
		}
	}
	return OutcomeAudited
}

// Classes lists what was found, for a policy decision.
func (r Result) Classes() []string {
	classes := make([]string, 0, len(r.Findings))
	for _, finding := range r.Findings {
		classes = append(classes, finding.Class)
	}
	return classes
}

// Labels is what a person reads. Classes are the names this platform files a
// finding under — "rrn", "card" — and they belong in an audit entry and a log,
// where something machine-readable is the point. A sentence shown to somebody
// whose data was held back is not that place: it said "rrn 가 포함되어" in a
// product whose users speak Korean.
func (r Result) Labels() []string {
	labels := make([]string, 0, len(r.Findings))
	for _, finding := range r.Findings {
		labels = append(labels, finding.Label)
	}
	return labels
}

// Summary is a one-line description for a log or an audit entry.
func (r Result) Summary() string {
	parts := make([]string, 0, len(r.Findings))
	for _, finding := range r.Findings {
		parts = append(parts, fmt.Sprintf("%s %d건", finding.Label, finding.Count))
	}
	return strings.Join(parts, ", ")
}

// Scan inspects text and applies the configured action.
//
// The input is never logged and never stored: only the class, the count and a
// masked sample leave this function. A DLP tool that writes what it found into
// an audit trail has moved the problem rather than solved it.
func Scan(settings Settings, text string) Result {
	result := Result{Text: text}
	if !settings.active() || text == "" {
		return result
	}
	limit := settings.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	scanned := text
	if len(scanned) > limit {
		end := valueEnd(scanned, limit)
		scanned, result.Truncated = scanned[:end], end < len(text)
	}

	blocked := []string{}
	claimed := newClaim(len(scanned))
	cuts := []cut{}
	for _, detector := range detectors {
		action := settings.Action(detector.Class)
		if action == Off {
			continue
		}
		matches := detector.pattern.FindAllStringIndex(scanned, -1)
		hits := make([]string, 0, len(matches))
		for _, span := range matches {
			if claimed.overlaps(span[0], span[1]) {
				continue
			}
			candidate := scanned[span[0]:span[1]]
			if detector.validate != nil && !detector.validate(candidate) {
				continue
			}
			claimed.take(span[0], span[1])
			hits = append(hits, candidate)
			if action == Redact {
				cuts = append(cuts, cut{start: span[0], end: span[1], marker: marker(detector)})
			}
		}
		if len(hits) == 0 {
			continue
		}
		result.Findings = append(result.Findings, Finding{
			Class: detector.Class, Label: detector.Label, Count: len(hits),
			Action: action, Sample: mask(hits[0], detector.keep),
		})
		if action == Block {
			blocked = append(blocked, detector.Label)
		}
	}
	if len(blocked) > 0 {
		result.Blocked = true
		result.Reason = fmt.Sprintf("민감정보(%s)가 포함되어 전송을 차단했습니다.", strings.Join(blocked, ", "))
		return result
	}
	if len(cuts) > 0 {
		// The tail beyond the scan limit is carried through unchanged, because
		// dropping it would silently truncate the payload instead of protecting it.
		result.Text = apply(scanned, cuts) + text[len(scanned):]
	}
	return result
}

// maxStraddle bounds how far past the scan limit the window is stretched to
// finish a value the cut landed inside. Every shape here is far shorter than
// this: the longest is a private key header at 31 characters and a nineteen
// digit card written in groups at 37.
const maxStraddle = 128

// valueEnd moves the scan boundary forward until it is somewhere a value cannot
// be.
//
// Cutting the payload at exactly MaxBytes cuts wherever the byte count lands,
// and half of a resident registration number matches nothing: a payload set to
// block national IDs went out carrying one, with a finding count of zero and the
// only trace a truncated flag on an entry that otherwise read clean. The tail is
// not scanned by design — that is what a scan limit is — but a value that begins
// inside the scanned region is one the operator asked us to look at, and the
// byte offset is an arbitrary place to stop looking.
//
// It cuts the other way too. Twenty digits of an order number sliced down to
// sixteen is a candidate the whole number never was, and a scanner that invents
// a card number out of where the payload happened to be long is one that gets
// switched off.
//
// Stopping at a byte no match can contain also restores the \b the patterns are
// written against, so the last value in the window is matched as itself rather
// than as its prefix.
func valueEnd(text string, limit int) int {
	end := limit
	for end < len(text) && end-limit < maxStraddle && continuesValue(text[end]) {
		end++
	}
	return end
}

// continuesValue reports whether a byte can appear inside something a detector
// matches — the digits and letters the shapes are made of, the separators they
// are written with, and the punctuation an email address carries.
func continuesValue(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z':
		return true
	}
	return strings.IndexByte("-_.@%+ ", b) >= 0
}

// cut is one byte range to replace with a marker.
type cut struct {
	start, end int
	marker     string
}

// apply rewrites the payload at the ranges the detectors claimed.
//
// Replacing by value instead — strings.ReplaceAll of the matched text, which is
// what this used to do — rewrites every other place those same bytes appear,
// including the ones no detector matched. The number shapes nest: an account
// number 111-1111-1111 sits inside the card number 4111-1111-1111-1111, so a
// payload holding both came out as "4[계좌번호 삭제됨]-1111" — the card was set to
// audit and was supposed to pass through untouched, one account number was
// reported, and two places in the text had changed. Whatever the model or the
// tool downstream was given, it was not the payload, and nothing in the findings
// said so.
//
// Claiming guarantees the ranges do not overlap, so one left-to-right pass is
// enough.
func apply(text string, cuts []cut) string {
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].start < cuts[j].start })
	var b strings.Builder
	end := 0
	for _, c := range cuts {
		b.WriteString(text[end:c.start])
		b.WriteString(c.marker)
		end = c.end
	}
	b.WriteString(text[end:])
	return b.String()
}

// claim records which bytes of the payload a detector has already taken, so one
// value is reported once.
//
// The number shapes overlap by construction: 220-81-62517 is a 사업자등록번호 and
// also a valid account grouping, and 4111-1111-1111-1111 is a card number and
// also a four-group one. Without this, an operator counting findings sees each of
// those twice, and the redaction marker carries whichever label happened to run
// second — a 사업자등록번호 leaving the platform as "[계좌번호 삭제됨]" tells the
// person reading the transcript the wrong thing about their own data.
//
// A class set to off never runs, so it never claims: a site that turned on only
// 계좌번호 still gets its finding, under the only label it asked about.
type claim struct{ taken []bool }

func newClaim(size int) claim { return claim{taken: make([]bool, size)} }

func (c claim) overlaps(start, end int) bool {
	for i := start; i < end; i++ {
		if c.taken[i] {
			return true
		}
	}
	return false
}

func (c claim) take(start, end int) {
	for i := start; i < end; i++ {
		c.taken[i] = true
	}
}

// marker is what replaces a redacted value. It names the class so the model, the
// tool and the person reading the transcript can all tell why the text changed.
func marker(detector Detector) string { return "[" + detector.Label + " 삭제됨]" }

// mask keeps a short prefix and hides the rest, which is enough to recognise a
// value without disclosing it.
func mask(value string, keep int) string {
	trimmed := strings.TrimSpace(value)
	runes := []rune(trimmed)
	if keep <= 0 || keep >= len(runes) {
		keep = len(runes) / 3
	}
	if keep <= 0 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:keep]) + strings.Repeat("*", len(runes)-keep)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// digits keeps only the digits of a candidate, so a value written with hyphens
// or spaces is validated the same as one without.
func digits(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// validRRN checks the resident registration number's check digit.
//
// Without it, any thirteen digits — an order number, a timestamp pair — would
// stop a production run, and the scanner would be switched off by the end of the
// week.
func validRRN(value string) bool {
	number := digits(value)
	if len(number) != 13 {
		return false
	}
	month, _ := strconv.Atoi(number[2:4])
	day, _ := strconv.Atoi(number[4:6])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	weights := []int{2, 3, 4, 5, 6, 7, 8, 9, 2, 3, 4, 5}
	sum := 0
	for i, weight := range weights {
		sum += int(number[i]-'0') * weight
	}
	check := (11 - sum%11) % 10
	return check == int(number[12]-'0')
}

// validBusiness checks the business registration number's check digit.
func validBusiness(value string) bool {
	number := digits(value)
	if len(number) != 10 {
		return false
	}
	weights := []int{1, 3, 7, 1, 3, 7, 1, 3, 5}
	sum := 0
	for i, weight := range weights {
		sum += int(number[i]-'0') * weight
	}
	sum += (int(number[8]-'0') * 5) / 10
	return (10-sum%10)%10 == int(number[9]-'0')
}

// validCard is the Luhn check every payment card satisfies.
func validCard(value string) bool {
	number := digits(value)
	if len(number) < 13 || len(number) > 19 {
		return false
	}
	sum, double := 0, false
	for i := len(number) - 1; i >= 0; i-- {
		digit := int(number[i] - '0')
		if double {
			if digit *= 2; digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

// notPhone keeps the account-number pattern from claiming phone numbers, which
// share its shape. The phone detector reports those, and reporting one value as
// two classes would double every count an operator reads.
//
// Neither has a check digit, so the grouping is all there is to go on. A Korean
// telephone number written with hyphens is always three groups — an area code
// that starts with a zero, a three or four digit exchange, and exactly four
// digits — and an account number is almost never all three at once.
//
// Refusing every candidate that starts with a zero, which is what this did, was
// the wrong reading of the same fact: 기업은행·우체국 계좌번호 and a good many
// 국민은행 ones begin with a zero, and a scanner told to block them passed every
// one of them through instead.
func notPhone(value string) bool {
	number := digits(value)
	if len(number) < 9 {
		return false
	}
	groups := strings.Split(value, "-")
	if len(groups) != 3 {
		return true
	}
	area, exchange, subscriber := groups[0], groups[1], groups[2]
	if !strings.HasPrefix(area, "0") || len(area) < 2 || len(area) > 4 {
		return true
	}
	return len(exchange) < 3 || len(exchange) > 4 || len(subscriber) != 4
}

// SortFindings orders findings for display: the most serious first, then by
// count, so an operator reads the worst thing first.
func SortFindings(findings []Finding) {
	severity := map[string]int{Block: 0, Redact: 1, Audit: 2, Off: 3}
	sort.SliceStable(findings, func(i, j int) bool {
		if severity[findings[i].Action] != severity[findings[j].Action] {
			return severity[findings[i].Action] < severity[findings[j].Action]
		}
		return findings[i].Count > findings[j].Count
	})
}
