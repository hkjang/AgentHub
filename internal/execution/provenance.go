package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hkjang/AgentHub/internal/store"
)

// provenanceTimeout bounds one export. The dispatcher is a loop with other
// events waiting, and a sink that has stopped answering must not hold it.
const provenanceTimeout = 10 * time.Second

// exportDecision sends this platform's account of a finished task to whatever a
// deployment has configured to receive it.
//
// A deployment with no endpoint configured does nothing here — no request, no
// error, no log line — because that is almost every deployment.
//
// A failure is reported to the caller rather than swallowed: the dispatcher
// already knows how to keep an event pending, back off and eventually dead-letter
// it with somebody told. A record this platform decided to export and then lost
// quietly would be worse than one that arrives late.
func (d *Dispatcher) exportDecision(ctx context.Context, event store.PlatformEvent) error {
	// The three endings the platform decides. Giving up is one of them: a task
	// that ran out of retries is the outcome an auditor asks about most, and it
	// publishes task.dead_lettered rather than task.failed — so leaving it out
	// exported everything except the endings somebody wants explained.
	switch event.Type {
	case store.EventTaskCompleted, store.EventTaskFailed, store.EventTaskDeadLettered:
	default:
		return nil
	}
	settings, err := d.store.ProvenanceEndpoint(ctx)
	if err != nil {
		return fmt.Errorf("결정 기록을 보낼 곳을 읽지 못했습니다: %w", err)
	}
	if !settings.Configured() {
		return nil
	}
	record, err := d.store.DecisionForTask(ctx, event.SubjectID)
	if err != nil {
		return fmt.Errorf("결정 기록을 만들지 못했습니다: %w", err)
	}
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("결정 기록을 인코딩하지 못했습니다: %w", err)
	}
	send, cancel := context.WithTimeout(ctx, provenanceTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(send, http.MethodPost, settings.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("결정 기록을 보내지 못했습니다: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if settings.Header != "" && settings.Token != "" {
		request.Header.Set(settings.Header, settings.Token)
	}
	response, err := provenanceClient.Do(request)
	if err != nil {
		return fmt.Errorf("결정 기록을 보내지 못했습니다: %s", modelCallReasonLike(err))
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("결정 기록을 받는 쪽이 HTTP %d 로 답했습니다", response.StatusCode)
	}
	d.logger.Info("decision exported", "task", record.TaskID, "type", event.Type, "outcome", record.Outcome)
	return nil
}

var provenanceClient = &http.Client{Timeout: provenanceTimeout}

// modelCallReasonLike keeps what an operator can act on out of a transport
// error and drops the rest, the same way the workflow engine does with its own.
func modelCallReasonLike(err error) string {
	message := err.Error()
	if at := lastIndex(message, ": "); at >= 0 && len(message)-at < 80 {
		return message[at+2:]
	}
	return message
}

func lastIndex(value, sep string) int {
	for i := len(value) - len(sep); i >= 0; i-- {
		if value[i:i+len(sep)] == sep {
			return i
		}
	}
	return -1
}
