// Package agentserver asks a registered server whether it is what it claims to
// be.
//
// It lives apart from the API because two callers need it: an administrator
// pressing the button, and the sweep that keeps the answer from going stale.
// A health kept forever reads as current, and a machine verified in March is not
// the same fact as one verified an hour ago.
package agentserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// The verdicts a check can reach.
const (
	Healthy     = "healthy"
	Unreachable = "unreachable"
	Refused     = "refused"
	Unknown     = "unknown"
)

// The check reads the server's own API description rather than a health path,
// because "something answered on this port" is not the same as "this is an agent
// server". A proxy, a parked domain or the wrong service will all answer 200 to
// a bare GET; only the right thing describes the endpoints this platform is
// going to call.
// Probe asks one server what it is.
func Probe(ctx context.Context, baseURL string) (string, string) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/openapi.json", nil)
	if err != nil {
		return Unreachable, "주소를 해석하지 못했습니다: " + err.Error()
	}
	client := &http.Client{Timeout: 10 * time.Second}
	answer, err := client.Do(request)
	if err != nil {
		return Unreachable, reason(err)
	}
	defer answer.Body.Close()
	if answer.StatusCode == http.StatusUnauthorized || answer.StatusCode == http.StatusForbidden {
		return Refused, "서버가 이 배포의 자격 증명을 받아들이지 않았습니다."
	}
	if answer.StatusCode >= 300 {
		return Unreachable, "서버가 " + answer.Status + " 로 답했습니다."
	}
	var described struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(answer.Body).Decode(&described); err != nil {
		return Unreachable, "이 주소는 API 설명을 돌려주지 않습니다 — 에이전트 서버가 맞는지 확인해 주세요."
	}
	// The endpoints this platform will actually call. A server that answers but
	// cannot start a conversation is not usable, and finding that out at
	// registration is the whole point of checking.
	for _, path := range []string{"/api/conversations"} {
		if _, present := described.Paths[path]; !present {
			return Refused, "이 서버에는 " + path + " 가 없습니다 — 에이전트 서버가 맞는지 확인해 주세요."
		}
	}
	return Healthy, "대화를 시작할 수 있는 서버입니다."
}

// agentServerReason says what went wrong in words an operator can act on.
func reason(err error) string {
	text := err.Error()
	switch {
	case strings.Contains(text, "no such host"):
		return "주소를 찾지 못했습니다."
	case strings.Contains(text, "connection refused"):
		return "연결이 거부됐습니다 — 서버가 떠 있는지 확인해 주세요."
	case strings.Contains(text, "certificate"):
		return "인증서를 확인하지 못했습니다."
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timeout"):
		return "제때 답하지 않았습니다."
	}
	return "연결하지 못했습니다: " + trimForMessage(text)
}

func trimForMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		return value[:160] + "…"
	}
	return value
}
