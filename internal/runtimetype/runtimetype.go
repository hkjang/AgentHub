// Package runtimetype is the single source of truth for the agent runtime
// adapters AgentHub supports. It is a dependency-free leaf package so the
// control plane, the store and the operator can all agree on the same list.
package runtimetype

const (
	OpenCode = "opencode"
	Hermes   = "hermes"
	QwenPaw  = "qwenpaw"
	QwenCode = "qwencode"
	Goose    = "goose"
	Langflow = "langflow"
	NodeRED  = "nodered"
	N8N      = "n8n"
	Jupyter  = "jupyter"
	Custom   = "custom"
)

// Supported lists every runtime type accepted by the API, the database check
// constraints and the AgentRuntime CRD enum. Keep this in sync with
// deploy/kubernetes/crd.yaml and the runtime_type CHECK constraints.
var Supported = []string{OpenCode, Hermes, QwenPaw, QwenCode, Goose, Jupyter, Langflow, NodeRED, N8N, Custom}

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
	switch value {
	case Hermes, QwenPaw:
		return 8642
	case Langflow:
		return 7860
	case QwenCode, Goose:
		// ttyd, which is what puts the agent's terminal in a browser.
		return 7681
	case NodeRED:
		return 1880
	case N8N:
		return 5678
	case Jupyter:
		return 8888
	}
	return 4096
}

// GatewayPort is the port the token-enforcing runtime proxy sidecar listens on.
// Runtimes without a built-in authenticator are only reachable through it.
const GatewayPort int32 = 9119

// UsesGatewayProxy reports whether the runtime is fronted by the runtime-proxy
// sidecar. Hermes serves its Dashboard on loopback and QwenPaw ships no
// authenticator at all, so both are published through the proxy instead of
// exposing the raw application port. Langflow joins them because AgentHub starts
// it with automatic login — its visual editor would otherwise be open to anyone
// who reached the port.
func UsesGatewayProxy(value string) bool {
	switch value {
	case Hermes, QwenPaw, Langflow, QwenCode, Goose, NodeRED, N8N, Jupyter:
		return true
	}
	return false
}

// HostSessionOnly reports that a browser session for this runtime needs an
// origin of its own and cannot be served from the Portal under /{runtimeId}/.
//
// Langflow has no base-path setting: its frontend requests /assets and /api from
// the root of whatever origin it was loaded from, so under a path prefix the
// editor loads a blank page. Refusing the session with an explanation is better
// than handing somebody that page.
//
// n8n is here for a subtler reason. It has a base-path setting and it rewrites
// its HTML to use it — but with the setting on, its static assets and its REST
// API both fall through to the index page, so the browser is handed HTML where it
// asked for JavaScript and the editor never starts. Served at the root of its own
// origin it is correct, so that is where the platform puts it.
func HostSessionOnly(value string) bool {
	switch value {
	case Langflow, N8N:
		return true
	}
	return false
}

// ServesUnderRuntimePath reports that this runtime is started with its base path
// set to the runtime's own id, and must therefore be reached at /{runtimeId}/ in
// both session modes.
//
// It exists because of where a browser terminal asks for its websocket. ttyd
// addresses /ws relative to the base path it was started with, not to the page's
// URL — so served at the origin root under a /{runtimeId}/ prefix, the browser
// asks the Portal for /ws, and a WebSocket handshake carries no Referer for the
// path gateway to route by. The terminal renders and then says "press enter to
// reconnect", which is a confusing way to learn that half the protocol never
// arrived. Giving ttyd the prefix it is already being served under makes the two
// agree, and keeping the prefix in host mode too means the answer does not change
// when a site configures a Runtime Base Domain.
func ServesUnderRuntimePath(value string) bool {
	switch value {
	case QwenCode, Goose, NodeRED, Jupyter:
		return true
	}
	return false
}
