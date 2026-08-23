package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/hkjang/AgentHub/internal/modelprobe"
	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// One place to ask everything the deployment depends on.
//
// Five things can be asked now — the cluster, single sign-on, each model
// endpoint, each shared MCP server, and whether egress policy is really enforced
// — and each lives on the screen where it is configured. That is the right place
// to fix one and the wrong place to find out that three are broken. Somebody
// bringing a deployment up, or looking at one that has started behaving oddly,
// wants the whole list at once.
//
// It is a button rather than something the page does on arrival: every one of
// these is a network call to somebody else's service, and a screen that quietly
// probes five external systems each time it loads is a screen that gets blamed
// for their outages.

// readinessItem is one dependency and what it said.
type readinessItem struct {
	// Area groups the row, and Name identifies which one when there are several.
	Area string `json:"area"`
	Name string `json:"name"`
	// Verdict is the checker's own word, kept rather than flattened so the row
	// can say "확인 불가" where that is the honest answer.
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
	// Fix is where somebody goes to change it.
	Fix string `json:"fix"`
}

// readinessOK is the set of verdicts that mean nothing needs doing. Everything
// else is shown, including the ones that are nobody's fault: a dependency that
// could not be checked is not a dependency that works.
var readinessOK = map[string]bool{"ok": true, "enforced": true}

func (s *Server) readiness(w http.ResponseWriter, r *http.Request) {
	u, _ := userFromContext(r.Context())
	var mu sync.Mutex
	items := []readinessItem{}
	add := func(item readinessItem) {
		mu.Lock()
		defer mu.Unlock()
		items = append(items, item)
	}

	var wait sync.WaitGroup
	run := func(fn func()) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			fn()
		}()
	}

	// The cluster. Without it nothing runs at all, so it is first in the list
	// however the checks finish.
	run(func() {
		checker, ok := s.spawner.(appRuntime.Checker)
		if !ok {
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "unconfigured",
				Detail: "Kubernetes 연결이 구성되어 있지 않아 Runtime을 시작할 수 없습니다.", Fix: "/admin/settings"})
			return
		}
		result, err := checker.CheckCluster(r.Context())
		switch {
		case err != nil:
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "unreachable",
				Detail: shortError(err.Error()), Fix: "/admin/settings"})
		case len(missingPermissions(result)) > 0:
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "incomplete",
				Detail: "권한이 없습니다: " + joinNames(missingPermissions(result)), Fix: "/admin/settings"})
		case result.CRDExpected && !result.CRDInstalled:
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "incomplete",
				Detail: "AgentRuntime CRD가 설치되어 있지 않습니다.", Fix: "/admin/settings"})
		default:
			detail := "Kubernetes " + result.ServerVersion + " · 네임스페이스 " + result.Namespace
			if !result.SnapshotsInstalled {
				detail += " · 작업공간 스냅샷 불가 (VolumeSnapshot API 없음)"
			}
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "ok", Detail: detail, Fix: "/admin/settings"})
		}
	})

	// Single sign-on, but only when somebody turned it on: a deployment using
	// local login is not incomplete for having no identity provider.
	run(func() {
		var auth authSettings
		if err := s.store.Setting(r.Context(), "authentication", &auth); err != nil || !auth.OIDCEnabled {
			return
		}
		secret, _ := s.store.SettingSecret(r.Context(), "authentication")
		result := checkOIDC(r.Context(), auth.IssuerURL, auth.ClientID, secret)
		add(readinessItem{Area: "인증", Name: "SSO", Verdict: result.Verdict, Detail: result.Detail, Fix: "/admin/settings"})
	})

	// Every model endpoint an agent could be pointed at.
	run(func() {
		endpoints, err := s.store.ModelEndpoints(r.Context())
		if err != nil {
			return
		}
		enabled := 0
		for _, endpoint := range endpoints {
			if endpoint.Enabled {
				enabled++
			}
		}
		if enabled == 0 {
			// The absence of a dependency is the one state a loop over dependencies
			// cannot report, and it is the state a new deployment is in. Every prose,
			// flow and investigation agent calls a model; with none configured the
			// queue fills and every task fails, and this screen said nothing at all.
			add(readinessItem{Area: "모델", Name: "모델 엔드포인트", Verdict: "unconfigured",
				Detail: "사용 가능한 모델 엔드포인트가 없습니다. 에이전트가 모델을 호출하는 순간 모든 작업이 실패합니다.", Fix: "/admin/models"})
			return
		}
		for _, endpoint := range endpoints {
			if !endpoint.Enabled {
				continue
			}
			_, key, keyErr := s.store.ModelEndpointByID(r.Context(), endpoint.ID)
			if keyErr != nil {
				continue
			}
			verdict, detail, _ := modelprobe.Ask(r.Context(), endpoint.BaseURL, key, endpoint.DefaultModel)
			add(readinessItem{Area: "모델", Name: endpoint.Name, Verdict: verdict, Detail: detail, Fix: "/admin/models"})
		}
	})

	// The execution plane. A control plane with no worker looks perfectly healthy
	// from the outside: the console answers, agents save, tasks queue — and nothing
	// ever claims one. It is the most common way a first deployment stalls, and
	// every screen that could have said so was reporting on something else.
	run(func() {
		workers, err := s.store.LiveWorkers(r.Context())
		if err != nil {
			add(readinessItem{Area: "실행", Name: "워커", Verdict: "unknown",
				Detail: "워커 상태를 확인하지 못했습니다: " + shortError(err.Error()), Fix: "/admin/execution"})
			return
		}
		if workers == 0 {
			add(readinessItem{Area: "실행", Name: "워커", Verdict: "none",
				Detail: "실행 중인 워커가 없습니다. 작업이 큐에 쌓이기만 하고 아무도 가져가지 않습니다.", Fix: "/admin/execution"})
			return
		}
		detail := fmt.Sprintf("워커 %d대가 작업을 가져가고 있습니다.", workers)
		var operations store.OperationsSettings
		if err := s.store.Setting(r.Context(), store.OperationsSettingKey, &operations); err == nil && operations.Paused {
			// Paused is somebody's decision rather than a fault, and it is also the
			// answer to "why is nothing running", which is what this list is for.
			add(readinessItem{Area: "실행", Name: "워커", Verdict: "paused",
				Detail: "실행이 일시 중지되어 있습니다" + pauseReason(operations) + ". " + detail, Fix: "/admin/execution"})
			return
		}
		add(readinessItem{Area: "실행", Name: "워커", Verdict: "ok", Detail: detail, Fix: "/admin/execution"})
	})

	// Shared MCP servers. The ones that run inside a runtime Pod answer
	// "not_checkable", which is true and not worth a row here.
	run(func() {
		servers, err := s.store.MCPServers(r.Context())
		if err != nil {
			return
		}
		for _, server := range servers {
			if !server.Enabled || (server.Mode != "" && server.Mode != "shared") {
				continue
			}
			verdict, detail, _ := askMCPServer(r.Context(), server.Mode, server.Endpoint)
			add(readinessItem{Area: "MCP", Name: server.Name, Verdict: verdict, Detail: detail, Fix: "/admin/mcp"})
		}
	})

	wait.Wait()
	sort.SliceStable(items, func(a, b int) bool {
		if items[a].Area != items[b].Area {
			return readinessRank(items[a].Area) < readinessRank(items[b].Area)
		}
		return items[a].Name < items[b].Name
	})
	problems := 0
	for _, item := range items {
		if !readinessOK[item.Verdict] {
			problems++
		}
	}
	s.store.Audit(r.Context(), &u, "deployment.readiness", "deployment", "", verdictWord(problems == 0), clientIP(r),
		map[string]any{"checked": len(items), "problems": problems})
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "problems": problems})
}

// readinessRank keeps the list in the order somebody would fix things: nothing
// runs without the cluster, nobody logs in without the identity provider, and
// the rest matter once those two do.
func readinessRank(area string) int {
	switch area {
	case "Kubernetes":
		return 0
	case "인증":
		return 1
	case "모델":
		return 2
	case "실행":
		return 3
	}
	return 4
}

// pauseReason repeats the sentence somebody typed when they paused, because "실행이
// 일시 중지되어 있습니다" without it is the same unexplained stop the pause screen
// exists to avoid.
func pauseReason(operations store.OperationsSettings) string {
	if strings.TrimSpace(operations.Reason) == "" {
		return ""
	}
	return " — " + operations.Reason
}

func missingPermissions(result appRuntime.ClusterCheck) []string {
	missing := []string{}
	for _, permission := range result.Permissions {
		if !permission.Allowed {
			missing = append(missing, permission.What)
		}
	}
	return missing
}

func joinNames(values []string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
