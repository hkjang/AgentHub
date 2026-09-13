package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hkjang/AgentHub/internal/mail"
)

// Mail notifications: the settings live in the mail row of system_settings and
// are saved through putAdminSetting like every other; what is here is the two
// things the settings form cannot do by saving — prove the relay works, and
// show what has gone out.

// decodeMailSettings re-reads a submitted document into its typed form,
// starting from the defaults so a key the console did not send keeps its
// default rather than becoming false or empty.
func decodeMailSettings(value map[string]any) (mail.Settings, error) {
	settings := mail.Defaults()
	raw, err := json.Marshal(value)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, errors.New("메일 설정 형식을 확인해 주세요")
	}
	return settings, nil
}

// decisionWord is the subject-line word for an approval's outcome.
func decisionWord(decision string) string {
	if decision == "approved" {
		return "승인됨"
	}
	return "거절됨"
}

func (s *Server) adminMailDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	page, err := s.mailer.Deliveries(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// adminSendTestMail sends one real mail through the saved settings and says
// what happened, right there. A relay setting is rarely right the first time,
// and a form that saves cannot tell "saved" from "works". The attempt is
// recorded like any other delivery and audited either way, because a test
// mail is still a mail that left the building.
func (s *Server) adminSendTestMail(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		Recipient string `json:"recipient"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	recipient := strings.TrimSpace(input.Recipient)
	if recipient == "" {
		recipient = strings.TrimSpace(u.Email)
	}
	if !strings.Contains(recipient, "@") {
		writeError(w, http.StatusBadRequest, "invalid_recipient", "받는 사람 메일 주소를 입력해 주세요. 계정에 메일 주소가 없으면 비워 둘 수 없습니다.")
		return
	}
	if s.mailer == nil {
		writeError(w, http.StatusServiceUnavailable, "mail_unavailable", "이 프로세스에는 메일 서비스가 구성되지 않았습니다.")
		return
	}
	delivery, err := s.mailer.SendNow(r.Context(), recipient, u.ID)
	switch {
	case errors.Is(err, mail.ErrDisabled):
		writeError(w, http.StatusBadRequest, "mail_disabled", "메일 알림이 꺼져 있습니다. 먼저 켜고 저장한 뒤 시험 발송하세요.")
		return
	case err != nil:
		s.store.Audit(r.Context(), &u, "mail.test", "mail", delivery.ID, "failure", clientIP(r),
			map[string]any{"recipient": recipient, "error": shortError(err.Error())})
		writeJSON(w, http.StatusBadGateway, map[string]any{"sent": false, "recipient": recipient, "error": err.Error(), "deliveryId": delivery.ID})
		return
	}
	s.store.Audit(r.Context(), &u, "mail.test", "mail", delivery.ID, "success", clientIP(r), map[string]any{"recipient": recipient})
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "recipient": recipient, "deliveryId": delivery.ID})
}
