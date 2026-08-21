package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// Whether a model endpoint is actually there.
//
// An administrator registers a base URL and a model name, and the platform finds
// out whether either is right at the moment a task runs — which is usually at
// night, on somebody else's agent, as a failure that reads like the agent's
// fault. On the cluster these releases were tested against, forty-five of
// sixty-five failed runs in one window were the same connection refused to a
// gateway that had stopped; every one of them was reported as a task failure.
//
// So the endpoint can be asked directly, and asked the second question too: the
// model list it answers with either contains the default model somebody typed or
// it does not. A typo there is invisible until inference time, and then it is an
// error from the provider that names neither the setting nor the screen.

const modelCheckTimeout = 6 * time.Second

func (s *Server) modelCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	endpoint, key, err := s.store.ModelEndpointByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	verdict, detail, models := s.askModelEndpoint(r, endpoint.BaseURL, key, endpoint.DefaultModel)
	s.store.Audit(r.Context(), &u, "model.check", "model", endpoint.ID, verdict, clientIP(r),
		map[string]any{"baseUrl": endpoint.BaseURL, "detail": detail})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": endpoint.ID, "verdict": verdict, "detail": detail, "models": models,
	})
}

// askModelEndpoint performs the request and reads the answer.
//
// Every failure is named for what an administrator would do about it: a refused
// connection is an address or a service, a 401 is the key, a 404 is a base URL
// with the wrong suffix — the mistake this shape of API invites most, because
// half the providers want /v1 on the end and half append it themselves.
func (s *Server) askModelEndpoint(r *http.Request, baseURL, key, defaultModel string) (verdict, detail string, models []string) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return "unconfigured", "주소가 비어 있습니다.", nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), modelCheckTimeout)
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
			return "unreachable", "응답이 없어 " + modelCheckTimeout.String() + " 만에 포기했습니다. 주소와 네트워크 경로를 확인해 주세요.", nil
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
	if defaultModel != "" && !contains(models, defaultModel) {
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

func firstFew(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return append(values[:limit:limit], "…")
}
