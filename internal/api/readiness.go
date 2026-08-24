package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hkjang/AgentHub/internal/agentserver"
	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/modelprobe"
	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
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

	// Forge connections, from what is already recorded rather than by probing:
	// a review that posts nothing is what a clean review looks like, so a broken
	// connection is invisible everywhere except the tab it was configured on.
	run(func() {
		connections, err := s.store.UncertainSCMConnections(r.Context())
		if err != nil {
			return
		}
		for _, connection := range connections {
			if connection.LastError != "" {
				detail := connection.LastError
				if len(detail) > 200 {
					detail = detail[:200] + "…"
				}
				add(readinessItem{Area: "코드 호스트", Name: connection.Host + " (" + connection.Owner + ")",
					Verdict: "failing", Detail: detail, Fix: "/developer"})
				continue
			}
			add(readinessItem{Area: "코드 호스트", Name: connection.Host + " (" + connection.Owner + ")",
				Verdict: "unknown", Detail: "한 번도 확인되지 않았습니다. 토큰을 다시 저장하면 그 자리에서 확인합니다.",
				Fix: "/developer"})
		}
	})

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
		case len(refusedRuntimeTypes(result)) > 0:
			// Upgrading the control plane does not upgrade the definition in the
			// cluster. A runtime type this build knows and the definition does not
			// is accepted everywhere — the console offers it, the database stores
			// it, an image can be approved for it — and refused by Kubernetes at
			// spawn, hours later, in a validation error nobody was watching for.
			add(readinessItem{Area: "Kubernetes", Name: "AgentRuntime 정의", Verdict: "outdated",
				Detail: "이 클러스터의 정의가 이 빌드보다 오래됐습니다. " +
					joinNames(refusedRuntimeTypes(result)) + " 런타임은 만들 때 거절됩니다 — deploy/kubernetes/crd.yaml 을 다시 적용하세요.",
				Fix: "/admin/settings"})
		default:
			detail := "Kubernetes " + result.ServerVersion + " · 네임스페이스 " + result.Namespace
			if !result.SnapshotsInstalled {
				detail += " · 작업공간 스냅샷 불가 (VolumeSnapshot API 없음)"
			}
			add(readinessItem{Area: "Kubernetes", Name: "클러스터", Verdict: "ok", Detail: detail, Fix: "/admin/settings"})
		}
	})

	// Runtimes somebody asked for that never arrived.
	//
	// A Pod that cannot pull its image retries for ever, which is right, and the
	// reason is written on the runtime's own row — but this is the screen that
	// answers "what is broken now", and a runtime half-started for an hour is
	// exactly that. Found by leaving one in ImagePullBackOff for sixty-five
	// minutes and noticing only by hand.
	run(func() {
		stuck, err := s.store.RuntimesStuckStarting(r.Context(), runtimeStuckAfter, 10)
		if err != nil {
			return
		}
		for _, runtime := range stuck {
			detail := "시작한 지 " + humanSince(runtime.Since) + " 지났고 아직 준비되지 않았습니다"
			if reason := strings.TrimSpace(runtime.FailureReason); reason != "" {
				if len(reason) > 200 {
					reason = reason[:200] + "…"
				}
				detail += " — " + reason
			}
			add(readinessItem{Area: "런타임", Name: runtime.AgentName, Verdict: "stuck",
				Detail: detail, Fix: "/agents/" + runtime.AgentID})
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

	// The content scanner, when somebody has turned it on. Enabled with no class
	// chosen is the one state worse than off: every payload goes through
	// untouched while the screen says the control is on.
	run(func() {
		var settings dlp.Settings
		if err := s.store.Setting(r.Context(), dlp.SettingKey, &settings); err != nil || !settings.Enabled {
			return
		}
		scanned := scannedClasses(settings)
		if len(scanned) == 0 {
			add(readinessItem{Area: "보안", Name: "내용 검사", Verdict: "inactive",
				Detail: "켜져 있지만 검사할 데이터 종류가 하나도 선택되어 있지 않아 아무것도 검사하지 않습니다.",
				Fix:    "/admin/dlp"})
			return
		}
		add(readinessItem{Area: "보안", Name: "내용 검사", Verdict: "ok",
			Detail: fmt.Sprintf("%d가지 데이터 종류를 검사합니다: %s", len(scanned), strings.Join(scanned, ", ")),
			Fix:    "/admin/dlp"})
	})

	// The schedules. Every trigger overdue at once is the scheduler not running,
	// which looks from every screen exactly like a quiet week — the console
	// answers, the agents are there, and nothing happens. It is the same silence
	// as having no workers, which this list already names.
	run(func() {
		overdue, total, err := s.store.OverdueTriggers(r.Context(), overdueGrace)
		if err != nil || total == 0 {
			return
		}
		if overdue == 0 {
			add(readinessItem{Area: "실행", Name: "예약 실행", Verdict: "ok",
				Detail: fmt.Sprintf("예약 트리거 %d개가 제때 실행되고 있습니다.", total), Fix: "/agents"})
			return
		}
		add(readinessItem{Area: "실행", Name: "예약 실행", Verdict: "overdue",
			Detail: fmt.Sprintf("예약 트리거 %d개 중 %d개가 실행 시각을 %s 이상 지났습니다. 스케줄러가 도는 워커가 없거나 표현식이 맞지 않습니다.",
				total, overdue, overdueGrace), Fix: "/admin/execution"})
	})

	// The agent servers, which are the one dependency this platform does not run
	// and cannot restart. A pool that has gone away looks exactly like a pool
	// nobody has used yet, and the tasks pointed at it fail one at a time.
	run(func() {
		servers, err := s.store.AgentServers(r.Context())
		if err != nil {
			return
		}
		asked := 0
		for _, server := range servers {
			if !server.Enabled {
				continue
			}
			asked++
			health, detail := agentserver.Probe(r.Context(), server.BaseURL)
			add(readinessItem{Area: "에이전트 서버", Name: server.Name, Verdict: agentServerVerdict(health),
				Detail: detail, Fix: "/admin/agent-servers"})
		}
		// Nothing registered is not a fault. Unlike a model endpoint, a deployment
		// that never sends work to somebody else's machine is complete without one,
		// so this says nothing rather than inventing a problem.
		_ = asked
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

// overdueGrace is how late a schedule may be before it is worth reporting. Wide
// enough that a worker restarting, or a sweep that ran a minute late, is not an
// incident.
const overdueGrace = 15 * time.Minute

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
	case "보안":
		return 4
	case "에이전트 서버":
		return 5
	}
	return 6
}

// agentServerVerdict translates the probe's word into this page's vocabulary, so
// a working server reads as "ok" beside the others rather than as a word only
// that one page uses.
func agentServerVerdict(health string) string {
	if health == agentserver.Healthy {
		return "ok"
	}
	return health
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

// refusedRuntimeTypes names the runtime types this build supports that the
// cluster's installed definition would turn away.
//
// Empty when the definition could not be read: not being allowed to look at a
// cluster-scoped object is common, and reporting that as an outdated definition
// would send somebody to fix something that is not broken.
func refusedRuntimeTypes(result appRuntime.ClusterCheck) []string {
	if len(result.CRDRuntimeTypes) == 0 {
		return nil
	}
	accepted := map[string]bool{}
	for _, name := range result.CRDRuntimeTypes {
		accepted[name] = true
	}
	refused := []string{}
	for _, name := range runtimetype.Supported {
		if !accepted[name] {
			refused = append(refused, name)
		}
	}
	return refused
}

// runtimeStuckAfter is how long a runtime may be starting before this screen
// says so. Long enough that an image pull on a cold node is not reported as
// trouble, short enough that somebody waiting for an agent finds out here.
const runtimeStuckAfter = 10 * time.Minute

// humanSince is the age of something in the words somebody would use.
func humanSince(when time.Time) string {
	elapsed := time.Since(when)
	switch {
	case elapsed < time.Hour:
		return fmt.Sprintf("%d분", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%d시간", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%d일", int(elapsed.Hours()/24))
}
