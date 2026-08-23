package api

import (
	"strings"
	"testing"
	"time"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// What an operator is told to do about a choice that will not work.
//
// The verdicts already say a runtime has never run here. That is where the
// platform used to stop, and where the operator's question begins: which of
// these fifteen is one image away from working, and which needs a cluster.

func fullyEquipped() deploymentState {
	return deploymentState{
		kubernetesEnabled: true,
		// Checked, answering, and permitted. A flag on its own is not a working
		// cluster, which is the distinction the advice is built on.
		cluster:        clusterHealth{Reachable: true, CheckedAt: time.Now().Add(-time.Hour)},
		modelEndpoints: 1,
		approvedImages: map[string]int{runtimetype.OpenCode: 1, runtimetype.Pi: 1},
		agentServers:   1,
		healthyServers: 1,
		externalApps:   1,
	}
}

func TestAWorkingDeploymentIsToldNothing(t *testing.T) {
	// A list of reassurances beside every choice buries the one entry that needs
	// attention, which is how a readiness screen becomes wallpaper.
	state := fullyEquipped()
	if missing := runtimeMissing(runtimetype.Describe(runtimetype.OpenCode), state); len(missing) > 0 {
		t.Errorf("a runtime with everything it needs was reported as missing %d thing(s): %v", len(missing), missing)
	}
	if missing := runnerMissing(store.RunnerProse, state); len(missing) > 0 {
		t.Errorf("reasoning, which needs only a model, was reported as missing: %v", missing)
	}
}

func TestARuntimeWithNoApprovedImageSaysSo(t *testing.T) {
	state := fullyEquipped()
	missing := runtimeMissing(runtimetype.Describe(runtimetype.Orca), state)
	if len(missing) == 0 {
		t.Fatal("a runtime type with no approved image was reported as ready")
	}
	// And it says where. "설정이 필요합니다" is not help.
	if !strings.Contains(missing[0].Where, "이미지") {
		t.Errorf("the missing piece does not say where it is fixed: %+v", missing[0])
	}
}

// TestACustomRuntimeIsNotAskedForAnImageNobodyCouldApprove keeps the advice
// truthful. A custom runtime names its own image when it is created, so there is
// nothing for an administrator to have approved in advance — telling them to
// approve one sends them looking for a screen that will not help.
func TestACustomRuntimeIsNotAskedForAnImageNobodyCouldApprove(t *testing.T) {
	state := fullyEquipped()
	for _, piece := range runtimeMissing(runtimetype.Describe(runtimetype.Custom), state) {
		if strings.Contains(piece.Where, "런타임 이미지") {
			t.Errorf("a custom runtime was told to approve an image: %+v", piece)
		}
	}
}

func TestABackendWithNothingToRunOnSaysWhatToRegister(t *testing.T) {
	empty := deploymentState{kubernetesEnabled: true, modelEndpoints: 1, approvedImages: map[string]int{},
		cluster: clusterHealth{Reachable: true, CheckedAt: time.Now()}}

	server := runnerMissing(store.RunnerAgentServer, empty)
	if len(server) == 0 {
		t.Fatal("the agent server backend was reported as ready with no server registered")
	}
	if !strings.Contains(server[0].Where, "에이전트 서버") {
		t.Errorf("the missing piece does not point at the agent server page: %+v", server[0])
	}

	app := runnerMissing(store.RunnerDify, empty)
	if len(app) == 0 {
		t.Fatal("the external application backend was reported as ready with no app registered")
	}
	if !strings.Contains(app[0].Where, "외부 앱") {
		t.Errorf("the missing piece does not point at the external app page: %+v", app[0])
	}
}

// TestABackendThatNeedsAPodSaysWhichImage is the difference between "this does
// not work" and something an operator can act on: the backends that run inside a
// Pod need a runtime that offers them, and there are usually only one or two.
func TestABackendThatNeedsAPodSaysWhichImage(t *testing.T) {
	empty := deploymentState{kubernetesEnabled: true, modelEndpoints: 1, approvedImages: map[string]int{},
		cluster: clusterHealth{Reachable: true, CheckedAt: time.Now()}}
	missing := runnerMissing(store.RunnerReview, empty)
	if len(missing) == 0 {
		t.Fatal("a backend with no runtime image was reported as ready")
	}
	if !strings.Contains(missing[0].What, runtimetype.Describe(runtimetype.OpenCodeReview).Label) {
		t.Errorf("the advice does not name the runtime that offers this backend: %+v", missing[0])
	}
}

// TestAnUnregisteredServerAndAnUncheckedOneAreDifferent — one means register a
// machine, the other means press a button. Collapsing them sends an operator
// looking for a thing they already have.
func TestAnUnregisteredServerAndAnUncheckedOneAreDifferent(t *testing.T) {
	unchecked := fullyEquipped()
	unchecked.healthyServers = 0
	missing := runnerMissing(store.RunnerAgentServer, unchecked)
	if len(missing) == 0 {
		t.Fatal("a pool where nothing has been checked was reported as ready")
	}
	if strings.Contains(missing[0].What, "등록돼 있지 않습니다") {
		t.Errorf("a registered but unchecked server is reported as unregistered: %+v", missing[0])
	}
}

func TestADeploymentWithNoClusterSaysThatFirst(t *testing.T) {
	state := fullyEquipped()
	state.kubernetesEnabled = false
	missing := runtimeMissing(runtimetype.Describe(runtimetype.OpenCode), state)
	if len(missing) == 0 || !strings.Contains(missing[0].What, "Kubernetes") {
		t.Errorf("a deployment that cannot start a Pod does not say so first: %v", missing)
	}
	// And the backend that starts nothing here is not blocked by it, because it
	// is not true: the work runs on a machine somebody else operates.
	for _, piece := range runnerMissing(store.RunnerAgentServer, state) {
		if strings.Contains(piece.What, "Kubernetes") {
			t.Errorf("a backend that needs no Pod was told to fix Kubernetes: %+v", piece)
		}
	}
}

// TestTheSummaryDoesNotHideHowMuchIsMissing keeps the one-line version honest:
// a console that shows only the first of four blockers reads as one small fix.
func TestTheSummaryDoesNotHideHowMuchIsMissing(t *testing.T) {
	if summary := missingSummary(nil); summary != "" {
		t.Errorf("a choice with nothing missing produced %q", summary)
	}
	one := []missingPiece{{What: "이미지가 없습니다.", Where: "관리자"}}
	if summary := missingSummary(one); summary != one[0].What {
		t.Errorf("a single missing piece was rewritten as %q", summary)
	}
	three := []missingPiece{{What: "하나."}, {What: "둘."}, {What: "셋."}}
	summary := missingSummary(three)
	if !strings.Contains(summary, "2") {
		t.Errorf("the summary of three missing pieces does not say two more are hidden: %q", summary)
	}
}

// TestASwitchedOnClusterIsNotTheSameAsAWorkingOne is the difference between what
// somebody typed and what answered.
//
// The flag says a form was filled in. A deployment whose flag says enabled and
// whose cluster has never been asked is the common case, and it is the one where
// a person debugs a runtime that was never going to start.
func TestASwitchedOnClusterIsNotTheSameAsAWorkingOne(t *testing.T) {
	never := fullyEquipped()
	never.cluster = clusterHealth{}
	missing := runtimeMissing(runtimetype.Describe(runtimetype.OpenCode), never)
	if len(missing) == 0 {
		t.Fatal("a cluster nobody has ever checked was reported as ready")
	}
	if !strings.Contains(missing[0].Where, "연결 확인") {
		t.Errorf("the advice does not point at the check that would answer it: %+v", missing[0])
	}
	// And it says nobody has asked, rather than that the asking failed. A
	// deployment that has never been checked has no failed check to report, and
	// telling an operator the cluster did not answer sends them looking at a
	// cluster that may be perfectly fine.
	if strings.Contains(missing[0].What, "마지막 확인") {
		t.Errorf("a cluster nobody has checked is reported as having failed a check: %+v", missing[0])
	}

	unreachable := fullyEquipped()
	unreachable.cluster = clusterHealth{Reachable: false, Detail: "i/o timeout", CheckedAt: time.Now()}
	missing = runtimeMissing(runtimetype.Describe(runtimetype.OpenCode), unreachable)
	if len(missing) == 0 {
		t.Fatal("a cluster that did not answer its last check was reported as ready")
	}
	// What the cluster said, not a paraphrase: an operator fixes a timeout
	// differently from a refused certificate.
	if !strings.Contains(missing[0].What, "i/o timeout") {
		t.Errorf("the advice drops what the cluster actually said: %+v", missing[0])
	}
}

// TestAClusterThatAnswersButRefusesSaysWhichPermission — the runtime is created
// and never starts, which reads as the runtime failing rather than as this
// account lacking a permission.
func TestAClusterThatAnswersButRefusesSaysWhichPermission(t *testing.T) {
	state := fullyEquipped()
	state.cluster = clusterHealth{Reachable: true, CheckedAt: time.Now(), Missing: []string{"create pods", "watch agentruntimes"}}
	missing := runtimeMissing(runtimetype.Describe(runtimetype.OpenCode), state)
	if len(missing) == 0 {
		t.Fatal("a cluster that refuses what the platform does was reported as ready")
	}
	if !strings.Contains(missing[0].What, "create pods") {
		t.Errorf("the advice does not name the permission that is missing: %+v", missing[0])
	}
}
