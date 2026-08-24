package api

import (
	"os"
	"strings"
	"testing"

	appRuntime "github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// Upgrading the control plane does not upgrade the definition installed in the
// cluster. A runtime type this build knows and that definition does not is
// accepted by the console, stored by the database, given an approved image —
// and refused by Kubernetes at spawn, in a validation error nobody was watching
// for. Observed exactly that way: an openhands agent was created and started,
// and the cluster answered "Unsupported value".
func TestAnOlderDefinitionInTheClusterIsReported(t *testing.T) {
	older := appRuntime.ClusterCheck{CRDRuntimeTypes: []string{"opencode", "hermes", "custom"}}
	refused := refusedRuntimeTypes(older)
	if len(refused) == 0 {
		t.Fatal("a definition missing most of this build's runtime types was reported as fine")
	}
	found := map[string]bool{}
	for _, name := range refused {
		found[name] = true
	}
	for _, expected := range []string{runtimetype.OpenHands, runtimetype.Orca, runtimetype.Pi} {
		if !found[expected] {
			t.Errorf("%s would be refused by that definition and is not named", expected)
		}
	}
	if found["opencode"] || found["custom"] {
		t.Errorf("types the definition does accept were named as refused: %v", refused)
	}
}

// A definition that matches is not a finding.
func TestACurrentDefinitionIsNotReportedAsOutdated(t *testing.T) {
	current := appRuntime.ClusterCheck{CRDRuntimeTypes: append([]string{}, runtimetype.Supported...)}
	if refused := refusedRuntimeTypes(current); len(refused) > 0 {
		t.Fatalf("a definition that accepts everything this build supports was called outdated: %v", refused)
	}
}

// Reading a cluster-scoped object is a permission many deployments never grant.
// Turning "I may not look" into "your cluster is wrong" sends somebody to repair
// something that is not broken.
func TestADefinitionThatCouldNotBeReadIsNotAVerdict(t *testing.T) {
	if refused := refusedRuntimeTypes(appRuntime.ClusterCheck{}); len(refused) > 0 {
		t.Fatalf("an unreadable definition produced a verdict: %v", refused)
	}
}

// And the report has to reach the list. A helper that names the refused types
// and a readiness run that never calls it is the same silence as before.
func TestReadinessSaysWhenTheDefinitionIsBehind(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "case result.CRDExpected && !result.CRDInstalled:")
	if at < 0 {
		t.Fatal("the cluster's readiness cases are gone; this guard is reading nothing")
	}
	section := source[at:]
	if end := strings.Index(section, "\n\t\tdefault:"); end >= 0 {
		section = section[:end]
	}
	// The condition, not just a mention: a case whose body names the refused
	// types but whose test is something else never fires.
	if !strings.Contains(section, "case len(refusedRuntimeTypes(result)) > 0:") {
		t.Error("readiness never asks whether the cluster's definition is behind this build")
	}
	// Named, not counted: "3 types" sends nobody anywhere.
	if !strings.Contains(section, "joinNames(refusedRuntimeTypes(result))") {
		t.Error("the refused runtime types are not named in the report")
	}
	// And it says what to do about it.
	if !strings.Contains(section, "crd.yaml") {
		t.Error("the report does not say what to re-apply")
	}
}
