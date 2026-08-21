package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hkjang/AgentHub/internal/store"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(w, status, body)
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "요청한 리소스를 찾을 수 없습니다.")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	slog.Error("store or runtime operation failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "요청을 처리하지 못했습니다: "+err.Error())
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", decodeComplaint(err))
		return false
	}
	return true
}

// decodeComplaint names what was refused.
//
// Every write endpoint answered "입력 형식이 올바르지 않습니다" to a mistyped field,
// an unknown one, a truncated body and an empty one alike — true of all four and
// useful for none, which leaves whoever is writing a GitOps document or a script
// bisecting their own JSON to find out which key the platform disliked. The
// decoder already knows; it was only being thrown away.
func decodeComplaint(err error) string {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Sprintf("%s 항목의 형식이 올바르지 않습니다(%s 값을 받았습니다).", jsonFieldPath(typeErr.Field), jsonTypeName(typeErr.Value))
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("JSON을 해석하지 못했습니다(%d번째 문자 부근).", syntaxErr.Offset)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		// The decoder quotes the name for us, which is what somebody needs to
		// search their document for.
		return "받지 않는 항목입니다: " + strings.TrimPrefix(err.Error(), "json: unknown field ") + ". 이름을 확인해 주세요."
	case errors.As(err, &tooLarge):
		return "요청 본문이 너무 큽니다."
	case errors.Is(err, io.EOF):
		return "요청 본문이 비어 있습니다."
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "JSON이 중간에 끊겼습니다. 본문이 완전한지 확인해 주세요."
	}
	return "입력 형식이 올바르지 않습니다."
}

// jsonFieldPath is the field as the caller wrote it. The decoder prefixes the
// path with the Go type it was filling — "AgentGoal.maxSteps" — and that name
// appears nowhere in the API somebody is writing against. Every field this
// platform accepts is lowerCamelCase, so a leading segment that starts with a
// capital is Go's and not theirs.
func jsonFieldPath(field string) string {
	parts := strings.Split(field, ".")
	for len(parts) > 1 && parts[0] != "" && strings.ToUpper(parts[0][:1]) == parts[0][:1] {
		parts = parts[1:]
	}
	return strings.Join(parts, ".")
}

// jsonTypeName says what arrived, in the words of the format the caller wrote
// rather than the ones Go uses internally.
func jsonTypeName(value string) string {
	switch value {
	case "string":
		return "문자열"
	case "number":
		return "숫자"
	case "bool", "true", "false":
		return "참/거짓"
	case "array":
		return "배열"
	case "object":
		return "객체"
	case "null":
		return "null"
	}
	return value
}
