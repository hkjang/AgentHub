package api

import (
	"os"
	"strings"
	"testing"
)

// A broken forge connection is invisible: the review that would have posted
// simply posts nothing, which is exactly what a clean review looks like. The one
// place a deployment's troubles are meant to gather has to include it.
func TestReadinessAsksAboutForgeConnections(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "UncertainSCMConnections(") {
		t.Error("readiness never asks about forge connections; a revoked token stays a mystery")
	}
	// Reported from what is recorded. A readiness button that borrowed people's
	// tokens to talk to their forges is a different thing than the one an
	// administrator pressed.
	if strings.Contains(source, "SCMTokenFor(") || strings.Contains(source, "CheckSCMConnection(") {
		t.Error("readiness uses somebody's stored credential to probe their forge")
	}
	// And it names where to fix it, like every other row here.
	at := strings.Index(source, "UncertainSCMConnections(")
	if at < 0 {
		t.Fatal("nothing to read")
	}
	section := source[at:]
	if end := strings.Index(section, "// The cluster."); end >= 0 {
		section = section[:end]
	}
	if !strings.Contains(section, `Fix: "/developer"`) {
		t.Error("a failing connection does not say where it is repaired")
	}
	// A connection nothing has ever checked is not a connection that works.
	if !strings.Contains(section, `"unknown"`) {
		t.Error("an unchecked connection is reported as though it were fine")
	}
}
