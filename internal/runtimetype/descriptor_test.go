package runtimetype

import "testing"

// Every supported adapter needs a description, or the console renders a blank
// card for a runtime somebody is being asked to choose.
func TestEverySupportedRuntimeIsDescribed(t *testing.T) {
	for _, name := range Supported {
		item := Describe(name)
		if item.Type != name || item.Label == "" || item.Summary == "" || item.BestFor == "" {
			t.Errorf("%s is not described: %#v", name, item)
		}
		if item.Code == "" || item.Workspace == "" {
			t.Errorf("%s needs a badge and a workspace path: %#v", name, item)
		}
		// An honest comparison has both halves. A runtime with only strengths
		// reads as marketing, and the watchouts are what stop somebody choosing
		// the wrong one.
		if len(item.Strengths) == 0 || len(item.Watchouts) == 0 {
			t.Errorf("%s must list what it is good at and what it will not do: %#v", name, item)
		}
	}
	if got := len(Descriptors()); got != len(Supported) {
		t.Errorf("Descriptors() returned %d of %d runtimes", got, len(Supported))
	}
}

// The port in the descriptor is the one the operator opens, so a mismatch would
// have the console tell a person to look at a port nothing listens on.
func TestDescribedPortsMatchTheAdapterPorts(t *testing.T) {
	for _, name := range []string{OpenCode, Hermes, QwenPaw, Langflow} {
		if got := Describe(name).Port; got != Port(name) {
			t.Errorf("%s: descriptor port %d, adapter port %d", name, got, Port(name))
		}
	}
	// The same for the proxy: a runtime published through the platform's proxy
	// has to say so, because that is why its own port is not reachable.
	for _, name := range Supported {
		if got := Describe(name).ProxiedUI; got != UsesGatewayProxy(name) {
			t.Errorf("%s: descriptor proxiedUi=%v, UsesGatewayProxy=%v", name, got, UsesGatewayProxy(name))
		}
	}
}

// HostSessionOnly decides whether the console offers the "open" button at all,
// so the descriptor and the rule have to be the same answer.
func TestDescribedHostSessionRuleMatches(t *testing.T) {
	for _, name := range Supported {
		if got := Describe(name).HostSessionOnly; got != HostSessionOnly(name) {
			t.Errorf("%s: descriptor hostSessionOnly=%v, HostSessionOnly=%v", name, got, HostSessionOnly(name))
		}
		// A runtime that cannot be published under a path prefix must be behind
		// the platform proxy: it has an origin of its own either way, and that
		// origin is publicly resolvable.
		if HostSessionOnly(name) && !UsesGatewayProxy(name) {
			t.Errorf("%s needs its own origin but is not fronted by the proxy", name)
		}
	}
}

// An unknown type is debuggable rather than blank: the raw value survives.
func TestUnknownRuntimeStaysDebuggable(t *testing.T) {
	item := Describe("codex")
	if item.Type != "codex" || item.Label == "" || item.Workspace == "" {
		t.Fatalf("an unknown runtime must still render: %#v", item)
	}
}
