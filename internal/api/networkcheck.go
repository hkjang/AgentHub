package api

import (
	"net/http"
	"strconv"
	"strings"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
)

// Whether this cluster actually enforces the egress rules the platform writes.
//
// The console offers Network Profiles and shows them attached, and the operator
// writes NetworkPolicy objects that the API server accepts. None of that means
// anything is enforced: NetworkPolicy is enforced by the CNI, and a cluster
// running a plugin without a policy controller — several common ones — accepts
// every policy and applies none of them. The screen says "기본 차단" and the
// runtime reaches the whole internet.
//
// So this asks the runtime itself. It is asymmetric on purpose: a connection
// that succeeds to a destination the profile does not allow is proof the policy
// is not being enforced, while a connection that fails is evidence and not
// proof, because it could have failed for its own reasons. The answer says
// which of the two it is rather than rounding both to a verdict.

// networkProbeTarget is reached from inside the runtime. The Kubernetes API
// service exists in every cluster, needs no internet, and is not something a
// restricted egress profile allows — so a runtime that can open a connection to
// it is a runtime nothing is confining.
const networkProbeTarget = "https://kubernetes.default.svc/version"

func (s *Server) networkCheck(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var input struct {
		RuntimeID string `json:"runtimeId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	instance, err := s.store.RuntimeByID(r.Context(), strings.TrimSpace(input.RuntimeID), u.ID, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if instance.Status != "running" {
		writeError(w, http.StatusConflict, "runtime_not_running",
			"실행 중인 Runtime에서만 확인할 수 있습니다. 먼저 Runtime을 시작해 주세요.")
		return
	}
	agent, err := s.store.AgentByID(r.Context(), instance.AgentID, u.ID, true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	spec, err := s.runtimeSpec(r, instance, agent)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A fixed script the platform wrote in full — nothing here comes from a
	// request — so a shell is safe and buys portability across images that ship
	// different tools.
	result, execErr := s.spawner.Exec(r.Context(), spec, appRuntime.ExecRequest{Command: []string{
		"/bin/sh", "-c", "curl -s -m 5 -o /dev/null -k " + networkProbeTarget + "; echo EXIT=$?",
	}})
	if execErr != nil {
		writeError(w, http.StatusBadGateway, "probe_failed",
			"Runtime 안에서 확인을 실행하지 못했습니다: "+execErr.Error())
		return
	}
	verdict, detail := networkVerdict(result.Stdout + result.Stderr)
	s.store.Audit(r.Context(), &u, "network.check", "runtime", instance.ID, verdict, clientIP(r),
		map[string]any{"target": networkProbeTarget, "detail": detail})
	writeJSON(w, http.StatusOK, map[string]any{
		"runtimeId": instance.ID, "target": networkProbeTarget,
		"verdict": verdict, "detail": detail,
	})
}

// networkVerdict reads what the probe printed.
//
// The three answers are deliberately not two. "확인 불가" is what an image
// without curl deserves, and calling it "enforced" because a command failed to
// start would be the same mistake as calling an unenforced policy enforced.
func networkVerdict(output string) (verdict, detail string) {
	code := -1
	if at := strings.LastIndex(output, "EXIT="); at >= 0 {
		code, _ = strconv.Atoi(strings.TrimSpace(strings.SplitN(output[at+5:], "\n", 2)[0]))
	}
	switch code {
	case 0:
		return "unenforced", "런타임이 " + networkProbeTarget + " 에 연결했습니다. 이 프로파일은 그 목적지를 허용하지 않으므로, 이 클러스터는 NetworkPolicy를 적용하고 있지 않습니다."
	case 7, 28, 6, 35:
		return "enforced", "연결이 막혔습니다(curl 종료 코드 " + strconv.Itoa(code) + "). 정책이 적용되고 있는 것과 일치합니다 — 다만 다른 이유로 실패했을 가능성까지 배제하지는 못합니다."
	case 127:
		return "unknown", "이 이미지에는 curl이 없어 확인하지 못했습니다. 정책이 적용되는지는 여전히 알 수 없습니다."
	case -1:
		return "unknown", "확인 명령이 아무 결과도 남기지 않았습니다. 정책이 적용되는지는 여전히 알 수 없습니다."
	}
	return "unknown", "확인 명령이 예상치 못한 코드(" + strconv.Itoa(code) + ")로 끝났습니다."
}
