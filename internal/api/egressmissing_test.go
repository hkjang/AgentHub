package api

import (
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

// A runtime under a default-deny profile with no destinations cannot reach the
// model gateway. Every way of running that happens inside a Pod then fails —
// and it fails as an agent that does not work, which is nobody's idea of a
// network rule. Observed on a real cluster: the NetworkPolicy allowed port 53
// and nothing else, and the runtime could not call out at all.
func TestNoEgressAnywhereIsSaidBeforeTheAgentLooksBroken(t *testing.T) {
	shut := deploymentState{
		noEgressAnywhere: true,
		approvedImages:   map[string]int{runtimetype.OpenCode: 1},
		modelEndpoints:   1, workingEndpoints: 1, checkedEndpoints: 1,
	}
	found := ""
	for _, piece := range runnerMissing(store.RunnerCLI, shut) {
		if strings.Contains(piece.What, "네트워크 프로파일") {
			found = piece.Where
		}
	}
	if found == "" {
		t.Fatal("a deployment whose profiles allow nothing says nothing about it")
	}
	if !strings.Contains(found, "네트워크 프로파일") {
		t.Errorf("the report does not say where this is changed: %q", found)
	}

	// The prose loop calls the model from the worker, not from a Pod, so this is
	// not its problem and saying so beside it would be noise.
	for _, piece := range runnerMissing(store.RunnerProse, shut) {
		if strings.Contains(piece.What, "네트워크 프로파일") {
			t.Error("the prose loop was warned about a rule that does not affect it")
		}
	}
}

// With a choice of profiles, which one an agent uses is the agent's business.
func TestAProfileThatAllowsSomethingIsNotAWarning(t *testing.T) {
	open := deploymentState{
		noEgressAnywhere: false,
		approvedImages:   map[string]int{runtimetype.OpenCode: 1},
		modelEndpoints:   1, workingEndpoints: 1, checkedEndpoints: 1,
	}
	for _, piece := range runnerMissing(store.RunnerCLI, open) {
		if strings.Contains(piece.What, "네트워크 프로파일") {
			t.Fatal("a deployment with a usable profile was warned anyway")
		}
	}
}

// An OpenHands runtime is an agent server this deployment starts itself. Telling
// somebody who has one to go and register a machine is advice about a problem
// they do not have.
func TestAnOpenHandsImageAnswersTheAgentServerRequirement(t *testing.T) {
	withRuntime := deploymentState{
		approvedImages: map[string]int{runtimetype.OpenHands: 1},
		modelEndpoints: 1, workingEndpoints: 1, checkedEndpoints: 1,
	}
	for _, piece := range runnerMissing(store.RunnerAgentServer, withRuntime) {
		if strings.Contains(piece.What, "에이전트 서버가 등록") {
			t.Fatal("a deployment that can start its own agent server was told to register one")
		}
	}
	// And a deployment with neither is still told what it needs.
	bare := deploymentState{approvedImages: map[string]int{}, modelEndpoints: 1, workingEndpoints: 1, checkedEndpoints: 1}
	said := false
	for _, piece := range runnerMissing(store.RunnerAgentServer, bare) {
		said = said || strings.Contains(piece.What, "에이전트 서버가 등록")
	}
	if !said {
		t.Fatal("a deployment with no server and no runtime was told nothing")
	}
}
