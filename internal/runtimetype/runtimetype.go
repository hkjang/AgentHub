// Package runtimetype is the single source of truth for the agent runtime
// adapters AgentHub supports. It is a dependency-free leaf package so the
// control plane, the store and the operator can all agree on the same list.
package runtimetype

const (
	OpenCode = "opencode"
	Hermes   = "hermes"
	QwenPaw  = "qwenpaw"
	Custom   = "custom"
)

// Supported lists every runtime type accepted by the API, the database check
// constraints and the AgentRuntime CRD enum. Keep this in sync with
// deploy/kubernetes/crd.yaml and the runtime_type CHECK constraints.
var Supported = []string{OpenCode, Hermes, QwenPaw, Custom}

// IsSupported reports whether value names a runtime adapter AgentHub can spawn.
func IsSupported(value string) bool {
	for _, item := range Supported {
		if item == value {
			return true
		}
	}
	return false
}

// Port is the container port the runtime's primary HTTP surface listens on.
func Port(value string) int32 {
	if value == Hermes || value == QwenPaw {
		return 8642
	}
	return 4096
}

// GatewayPort is the port the token-enforcing runtime proxy sidecar listens on.
// Runtimes without a built-in authenticator are only reachable through it.
const GatewayPort int32 = 9119

// UsesGatewayProxy reports whether the runtime is fronted by the runtime-proxy
// sidecar. Hermes serves its Dashboard on loopback and QwenPaw ships no
// authenticator at all, so both are published through the proxy instead of
// exposing the raw application port.
func UsesGatewayProxy(value string) bool {
	return value == Hermes || value == QwenPaw
}
