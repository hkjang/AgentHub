// Package modelprobe asks a model endpoint whether it is there, and whether it
// offers the model somebody typed.
//
// It lives apart from the API because two callers need it: an administrator
// pressing the button, and the sweep that keeps the answer from going stale. The
// question costs nothing — a listing, not a completion — so asking it on a timer
// does not spend anybody's tokens.
package modelprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Timeout bounds one endpoint's answer.
const Timeout = 6 * time.Second

// The verdicts a check can reach. Each names what an administrator would do
// about it rather than what the HTTP was.
const (
	OK           = "ok"
	Reachable    = "reachable"
	ModelMissing = "model_missing"
	Unauthorised = "unauthorised"
	WrongPath    = "wrong_path"
	Unreachable  = "unreachable"
	Unconfigured = "unconfigured"
	Failing      = "error"
)

// Ask performs the request and reads the answer.
//
// Every failure is named for what an administrator would do about it: a refused
// connection is an address or a service, a 401 is the key, a 404 is a base URL
// with the wrong suffix — the mistake this shape of API invites most, because
// half the providers want /v1 on the end and half append it themselves.
func Ask(ctx context.Context, baseURL, key, defaultModel string) (verdict, detail string, models []string) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return "unconfigured", "주소가 비어 있습니다.", nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, trimmed+"/models", nil)
	if err != nil {
		return "unreachable", "요청을 만들지 못했습니다: " + err.Error(), nil
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "context deadline") {
			return "unreachable", "응답이 없어 " + Timeout.String() + " 만에 포기했습니다. 주소와 네트워크 경로를 확인해 주세요.", nil
		}
		return "unreachable", "연결하지 못했습니다: " + shortError(err.Error()), nil
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return "unauthorised", "엔드포인트는 응답했지만 인증을 거절했습니다(HTTP " + response.Status + "). API 키를 확인해 주세요.", nil
	case response.StatusCode == http.StatusNotFound:
		return "wrong_path", "이 주소에 모델 목록이 없습니다(HTTP 404). 대개 " + trimmed + " 뒤에 /v1 이 빠졌거나 반대로 하나 더 붙은 경우입니다.", nil
	case response.StatusCode >= 400:
		return "error", "엔드포인트가 HTTP " + response.Status + " 로 답했습니다.", nil
	}
	models = modelNames(body)
	if len(models) == 0 {
		return "reachable", "응답했지만 모델 목록이 비어 있습니다. 이 엔드포인트가 어떤 모델도 제공하지 않는 상태일 수 있습니다.", nil
	}
	if defaultModel != "" && !containsName(models, defaultModel) {
		return "model_missing", "엔드포인트는 정상이지만 지정한 모델 \"" + defaultModel + "\" 이 목록에 없습니다. 사용 가능: " + strings.Join(firstFew(models, 8), ", "), models
	}
	return "ok", fmt.Sprintf("정상입니다. 모델 %d개를 제공하며 지정한 모델도 목록에 있습니다.", len(models)), models
}

// modelNames reads the OpenAI-compatible listing. A provider that answers with
// something else is not an error here — the connection worked, which is most of
// what was being asked.
func modelNames(body []byte) []string {
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &listing) != nil {
		return nil
	}
	names := []string{}
	for _, item := range listing.Data {
		if item.ID != "" {
			names = append(names, item.ID)
		}
	}
	return names
}

// shortError keeps the first line and drops the wrapping Go adds, which names
// this platform's own call stack rather than anything an administrator set.
func shortError(value string) string {
	if at := strings.Index(value, "\n"); at >= 0 {
		value = value[:at]
	}
	if at := strings.LastIndex(value, ": "); at >= 0 && len(value)-at < 80 {
		return value[at+2:]
	}
	return value
}

func containsName(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstFew(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return append(values[:limit:limit], "…")
}
