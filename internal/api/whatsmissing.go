package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// What is missing before a choice can be used here.
//
// The verdicts next to each runtime and each way of running say what this
// deployment has seen: proven, failed, untried. That answers "has this worked"
// and stops exactly where an operator's question begins — "then what do I do
// about it". Fifteen runtimes read "아직 실행해 본 적이 없습니다" and the person
// looking at them has no way to tell which are one image away from working and
// which need a cluster.
//
// So this is the other half, and it is deliberately made of things the platform
// can check rather than advice: no approved image, no model endpoint, no
// registered server, Kubernetes switched off. Each one names where it is fixed,
// because "설정이 필요합니다" is not help.

// missingPiece is one thing to fix, and where.
type missingPiece struct {
	// What is not there, in the words an operator would use.
	What string `json:"what"`
	// Where they go to change it. A console path, not an API route: the person
	// reading this is looking at a screen.
	Where string `json:"where"`
}

// deploymentState is what the platform knows about itself, read once and asked
// many times. Reading it per runtime type would be fifteen copies of the same
// four queries on every page load.
type deploymentState struct {
	kubernetesEnabled bool
	// What the last check of the cluster found, and when. The setting says
	// somebody filled in a form; this says the cluster answered.
	cluster        clusterHealth
	modelEndpoints int
	approvedImages map[string]int
	agentServers   int
	healthyServers int
	externalApps   int
}

func (s *Server) readDeploymentState(ctx context.Context) deploymentState {
	state := deploymentState{approvedImages: map[string]int{}}
	var kubernetes struct {
		Enabled bool `json:"enabled"`
	}
	_ = s.store.Setting(ctx, "kubernetes", &kubernetes)
	state.kubernetesEnabled = kubernetes.Enabled
	_ = s.store.Setting(ctx, clusterHealthKey, &state.cluster)

	if endpoints, err := s.store.ModelEndpoints(ctx); err == nil {
		for _, endpoint := range endpoints {
			if endpoint.Enabled {
				state.modelEndpoints++
			}
		}
	}
	if images, err := s.store.RuntimeImages(ctx); err == nil {
		for _, image := range images {
			if image.Approved && !image.Deprecated {
				state.approvedImages[image.RuntimeType]++
			}
		}
	}
	if servers, err := s.store.AgentServers(ctx); err == nil {
		for _, server := range servers {
			if !server.Enabled {
				continue
			}
			state.agentServers++
			if server.Health == "healthy" {
				state.healthyServers++
			}
		}
	}
	if apps, err := s.store.ExternalApps(ctx); err == nil {
		for _, app := range apps {
			if app.Enabled {
				state.externalApps++
			}
		}
	}
	return state
}

// clusterMissing is what the cluster is short of, in the words of the last thing
// that actually asked it.
//
// A settings flag and a working cluster are different facts, and the difference
// is the one an operator is trying to find out. A deployment whose flag says
// enabled and whose cluster has never been checked is the common case, and
// saying nothing about it is how a person ends up debugging a runtime that was
// never going to start.
func clusterMissing(state deploymentState) (missingPiece, bool) {
	if !state.kubernetesEnabled {
		return missingPiece{
			What:  "이 배포에 Kubernetes 연결이 꺼져 있어 런타임을 띄울 수 없습니다.",
			Where: "관리자 ▸ 설정 ▸ Kubernetes",
		}, true
	}
	if state.cluster.CheckedAt.IsZero() {
		return missingPiece{
			What:  "Kubernetes 연결을 아직 한 번도 확인하지 않았습니다. 설정이 켜져 있다는 것과 클러스터가 응답한다는 것은 다른 이야기입니다.",
			Where: "관리자 ▸ 설정 ▸ Kubernetes ▸ 연결 확인",
		}, true
	}
	if !state.cluster.Reachable {
		what := "마지막 확인에서 클러스터에 연결되지 않았습니다"
		if state.cluster.Detail != "" {
			what += ": " + state.cluster.Detail
		}
		return missingPiece{What: what + ".", Where: "관리자 ▸ 설정 ▸ Kubernetes ▸ 연결 확인"}, true
	}
	if len(state.cluster.Missing) > 0 {
		// Reachable and not permitted. The runtime would be created and never
		// start, which reads as the runtime failing rather than as this account
		// lacking a permission.
		return missingPiece{
			What:  "클러스터는 응답하지만 이 계정에 권한이 없는 동작이 있습니다: " + strings.Join(state.cluster.Missing, ", "),
			Where: "관리자 ▸ 설정 ▸ Kubernetes ▸ 연결 확인",
		}, true
	}
	return missingPiece{}, false
}

// runtimeMissing is what stands between this deployment and a working runtime of
// one type.
//
// It says nothing when nothing is missing. A list of reassurances next to every
// type would bury the two entries that actually need attention, which is the
// failure mode of every readiness screen that reports everything.
func runtimeMissing(descriptor runtimetype.Descriptor, state deploymentState) []missingPiece {
	missing := []missingPiece{}
	if piece, short := clusterMissing(state); short {
		missing = append(missing, piece)
	}
	// A custom runtime is whatever image somebody names when they create it, so
	// there is nothing for an administrator to have approved in advance.
	if descriptor.Type != runtimetype.Custom && state.approvedImages[descriptor.Type] == 0 {
		missing = append(missing, missingPiece{
			What:  descriptor.Label + " 이미지가 승인돼 있지 않습니다. 승인된 이미지가 없으면 이 유형으로는 런타임이 시작되지 않습니다.",
			Where: "관리자 ▸ 리소스 ▸ 런타임 이미지",
		})
	}
	if state.modelEndpoints == 0 {
		missing = append(missing, missingPiece{
			What:  "사용할 수 있는 Model Endpoint가 없습니다. 에이전트는 모델 없이 자동 실행되지 않습니다.",
			Where: "관리자 ▸ 리소스 ▸ 모델 엔드포인트",
		})
	}
	return missing
}

// runnerMissing is what stands between this deployment and one way of running.
//
// The backends that run in a Pod need a runtime that offers them, which is a
// fact about the images this deployment has approved rather than about the
// catalogue: a type nobody has an image for cannot run anything.
func runnerMissing(runner string, state deploymentState) []missingPiece {
	missing := []missingPiece{}
	switch runner {
	case store.RunnerProse:
		// Reasoning needs nothing but a model, which every backend needs.
	case store.RunnerDify:
		if state.externalApps == 0 {
			missing = append(missing, missingPiece{
				What:  "작업을 맡길 외부 앱이 등록돼 있지 않습니다.",
				Where: "관리자 ▸ 리소스 ▸ 외부 앱",
			})
		}
	case store.RunnerAgentServer:
		switch {
		case state.agentServers == 0:
			missing = append(missing, missingPiece{
				What:  "작업을 맡길 에이전트 서버가 등록돼 있지 않습니다.",
				Where: "관리자 ▸ 에이전트 서버",
			})
		case state.healthyServers == 0:
			missing = append(missing, missingPiece{
				What:  "등록된 에이전트 서버 중 연결이 확인된 것이 없습니다. 확인되지 않은 서버로도 보내지만, 먼저 확인해 두면 실패를 작업 전에 알 수 있습니다.",
				Where: "관리자 ▸ 에이전트 서버 ▸ 연결 확인",
			})
		}
	default:
		// The rest happen inside a Pod, so they need a runtime type that offers
		// this way of running and an approved image for it.
		if !anyRuntimeOffers(runner, state) {
			missing = append(missing, missingPiece{
				What:  "이 실행 방식을 지원하는 런타임 이미지가 승인돼 있지 않습니다(" + runnerRuntimeNames(runner) + ").",
				Where: "관리자 ▸ 리소스 ▸ 런타임 이미지",
			})
		}
		if piece, short := clusterMissing(state); short {
			missing = append(missing, piece)
		}
	}
	if state.modelEndpoints == 0 && runner != store.RunnerDify {
		missing = append(missing, missingPiece{
			What:  "사용할 수 있는 Model Endpoint가 없습니다.",
			Where: "관리자 ▸ 리소스 ▸ 모델 엔드포인트",
		})
	}
	return missing
}

// anyRuntimeOffers says whether a runtime type that can be handed a task this
// way has an approved image here.
func anyRuntimeOffers(runner string, state deploymentState) bool {
	for _, descriptor := range runtimetype.Descriptors() {
		if !runtimetype.SupportsRunner(descriptor.Type, runner) {
			continue
		}
		// A custom runtime names its own image, so it counts as available whenever
		// somebody can create one.
		if descriptor.Type == runtimetype.Custom || state.approvedImages[descriptor.Type] > 0 {
			return true
		}
	}
	return false
}

// runnerRuntimeNames lists the runtimes that offer one way of running, so the
// missing piece says which image to approve rather than that one is missing.
func runnerRuntimeNames(runner string) string {
	names := []string{}
	for _, descriptor := range runtimetype.Descriptors() {
		if runtimetype.SupportsRunner(descriptor.Type, runner) {
			names = append(names, descriptor.Label)
		}
	}
	if len(names) == 0 {
		return "지원하는 런타임 없음"
	}
	return strings.Join(names, ", ")
}

// missingSummary is the one line a console shows without opening anything.
func missingSummary(missing []missingPiece) string {
	if len(missing) == 0 {
		return ""
	}
	if len(missing) == 1 {
		return missing[0].What
	}
	return fmt.Sprintf("%s 외 %d가지가 더 필요합니다.", missing[0].What, len(missing)-1)
}
