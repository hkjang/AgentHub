package operator

import (
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// runtimeAdapter is everything the operator needs to know about one agent
// runtime. Adding a new adapter — Codex, Claude Code, anything else — means
// registering one of these rather than threading another branch through the
// StatefulSet builder.
//
// Every hook receives the same build context so an adapter can reach the runtime
// name, its image and the compiled configuration without the builder having to
// anticipate what it needs.
type runtimeAdapter struct {
	// Type is the value stored on the AgentRuntime spec.
	Type string

	// Command and Args start the agent process. Args may reference the config
	// files the operator writes to /etc/agenthub. ArgsFor replaces Args when the
	// arguments depend on which runtime this is.
	Command []string
	Args    []string
	ArgsFor func(build adapterBuild) []string

	// Env contributes adapter-specific variables on top of the shared set.
	Env func(build adapterBuild) []corev1.EnvVar

	// InitContainers prepare the agent's home directory before it starts.
	InitContainers func(build adapterBuild) []corev1.Container

	// Sidecars run alongside the agent, typically a UI and the token-enforcing
	// proxy in front of it.
	Sidecars func(build adapterBuild) []corev1.Container

	// Probes replace the default TCP ones. A runtime that binds to loopback needs
	// that: the kubelet probes the Pod IP, so nothing outside the container can
	// connect, and the default probe would fail forever on a runtime that is
	// working perfectly. They take the build because a runtime served under its
	// own base path answers on a URL that contains the runtime's id.
	Probes func(build adapterBuild) (readiness, liveness *corev1.Probe)
}

// adapterBuild is the context handed to every adapter hook.
type adapterBuild struct {
	// Name is the runtime's resource name, which is also its Secret name.
	Name string
	// Value is the parsed AgentRuntime spec.
	Value spec
	// Env is the shared environment every container receives.
	Env []corev1.EnvVar
}

func (b adapterBuild) image() string { return b.Value.Runtime.Image }

// runtimeID is the platform's id for this runtime, which is also the path prefix
// its UI is published under.
func (b adapterBuild) runtimeID() string { return b.Value.RuntimeRef.ID }

// sidecarImage is the control plane's own image, so a platform sidecar never
// runs the code of whatever runtime image the agent happens to be pinned to.
func (b adapterBuild) sidecarImage() string { return b.Value.sidecarImage() }

// secretEnv reads one key from the runtime Secret.
func (b adapterBuild) secretEnv(name, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: b.Name}, Key: key}}}
}

// initResources are deliberately small: these containers copy configuration and
// exit. QwenPaw's initialiser imports skills, so it is given more headroom.
func initResources(cpu, memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("32Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse(cpu), corev1.ResourceMemory: apiresource.MustParse(memory)},
	}
}

const (
	opencodeConfigInit = "mkdir -p /home/agent/.config/opencode\n" +
		"cp /etc/agenthub/opencode.json /home/agent/.config/opencode/opencode.json\n" +
		"cp /etc/agenthub/opencode.json /home/agent/.config/opencode/config.json\n" +
		"cp /etc/agenthub/opencode.json /home/agent/.opencode.json\n" +
		"if [ -n \"$OPENAI_BASE_URL\" ]; then\n" +
		"  printf 'OPENAI_BASE_URL=%s\\nOPENAI_API_KEY=%s\\nOLLAMA_HOST=%s\\nMODEL=%s\\nOPENAI_MODEL=%s\\n' \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$AGENTHUB_MODEL_NAME\" \"$AGENTHUB_MODEL_NAME\" > /home/agent/.config/opencode/.env\n" +
		"fi\n" +
		// The report reads the file that was just written, so what the platform
		// records is what the runtime will read rather than what it intended.
		"/usr/local/bin/agenthub-report-config /home/agent/.config/opencode/opencode.json || true"

	hermesConfigInit = "mkdir -p /home/agent/.hermes\n" +
		"cp /etc/agenthub/hermes-config.yaml /home/agent/.hermes/config.yaml\n" +
		"if [ -n \"$OPENAI_BASE_URL\" ] && [ -n \"$AGENTHUB_MODEL_NAME\" ]; then\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.default \"$AGENTHUB_MODEL_NAME\" || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.provider custom || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.base_url \"$OPENAI_BASE_URL\" || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.api_key \"$OPENAI_API_KEY\" || true\n" +
		"  printf 'OPENAI_BASE_URL=%s\\nOPENAI_API_KEY=%s\\nCUSTOM_BASE_URL=%s\\nCUSTOM_API_KEY=%s\\nHERMES_MODEL=%s\\nMODEL=%s\\n' \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$AGENTHUB_MODEL_NAME\" \"$AGENTHUB_MODEL_NAME\" > /home/agent/.hermes/.env\n" +
		"fi\n" +
		"/usr/local/bin/agenthub-report-config /home/agent/.hermes/config.yaml || true"

	hermesStart = "mkdir -p /home/agent/.hermes\n" +
		"if [ -f /etc/agenthub/hermes-config.yaml ]; then cp /etc/agenthub/hermes-config.yaml /home/agent/.hermes/config.yaml; fi\n" +
		"export API_SERVER_ENABLED=true\n" +
		"export API_SERVER_HOST=0.0.0.0\n" +
		"export API_SERVER_PORT=8642\n" +
		"export API_SERVER_KEY=\"${API_SERVER_KEY:-${AGENTHUB_RUNTIME_TOKEN:-}}\"\n" +
		"exec /opt/hermes/.venv/bin/hermes gateway run --no-supervise"

	qwenPawStart = "/usr/local/bin/agenthub-qwenpaw-configure || true\n" +
		"exec /opt/qwenpaw/.venv/bin/qwenpaw app --host 0.0.0.0 --port 8642"

	// Qwen Code is a terminal program, so what a person opens is the terminal
	// itself: ttyd serves it over a websocket, bound to loopback and published
	// only through the token-checking proxy. Everything the agent needs — its
	// settings with the bound MCP servers, its credentials, and the Python
	// environment `pip install` writes into — is prepared by the initialiser on
	// the home volume, because the default security profile mounts the root
	// filesystem read-only and the toolchain the image ships lives there.
	qwenCodeConfigInit = "/usr/local/bin/agenthub-qwencode-configure"

	// Langflow keeps everything it owns — the flows, the encryption key it
	// generates on first start, its database — under LANGFLOW_CONFIG_DIR, so that
	// directory has to be the persistent home rather than the image's /app. The
	// initialiser only has to create it and report; there is no configuration file
	// to merge because Langflow is configured entirely through the environment.
	langflowConfigInit = "mkdir -p " + langflowConfigDir + "\n" +
		"/usr/local/bin/agenthub-report-config || true"

	langflowStart = "mkdir -p " + langflowConfigDir + "\n" +
		"exec /app/.venv/bin/langflow run"
)

// nodeREDHome and n8nHome are where each keeps the work a person did in it: the
// flows, the credentials, the settings. Both are under /home/agent because that
// is the volume that survives the Pod.
const (
	nodeREDHome = "/home/agent/.node-red"
	n8nHome     = "/home/agent/.n8n"
)

// nodeREDStart runs the editor from the home volume rather than from the image's
// own /data, which is root-owned and is not a volume this platform mounts.
const nodeREDStart = "mkdir -p " + nodeREDHome + "\n" +
	"cp /etc/agenthub/node-red-settings.js " + nodeREDHome + "/settings.js\n" +
	"exec node /usr/src/node-red/node_modules/node-red/red.js --userDir " + nodeREDHome +
	" --settings " + nodeREDHome + "/settings.js"

// nodeREDConfigInit copies the generated settings and reports them. The settings
// file carries the base path, so it is per runtime rather than per image.
const nodeREDConfigInit = "mkdir -p " + nodeREDHome + "\n" +
	"cp /etc/agenthub/node-red-settings.js " + nodeREDHome + "/settings.js\n" +
	"/usr/local/bin/agenthub-report-config " + nodeREDHome + "/settings.js || true"

// n8n is configured entirely through the environment, so its initialiser only has
// to make the directory and say what arrived.
const n8nConfigInit = "mkdir -p " + n8nHome + "\n" +
	"/usr/local/bin/agenthub-report-config || true"

// nodeREDHealthCommand and n8nHealthCommand ask each product from inside the
// container, under the base path it is served at.
func nodeREDHealthCommand(build adapterBuild) []string {
	return []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:1880/" + build.runtimeID() + "/settings >/dev/null"}
}

func n8nHealthCommand(adapterBuild) []string {
	// The hardened n8n image has no curl; wget is what it does have. No base path
	// here: n8n is served from the root of its own origin.
	return []string{"/bin/sh", "-c", "wget -q -T 3 -O /dev/null http://127.0.0.1:5678/rest/settings"}
}

// jupyterStart serves the lab under the runtime's own path.
//
// base_url is not decoration here either: JupyterLab's client asks for its
// static assets and its kernel websockets relative to it, so served at the root
// behind a prefix the page loads and every kernel dies on connect.
//
// Token authentication is off because the proxy in front is the authenticator —
// the same arrangement every other loopback runtime here uses. The XSRF check
// goes with it: it rejects the proxied origin, and with no token to steal there
// is nothing for it to protect.
func jupyterStart(build adapterBuild) string {
	prefix := "/" + build.runtimeID() + "/"
	return "mkdir -p /tmp/jupyter-runtime\n" +
		"exec /opt/agenthub/venv/bin/jupyter lab --no-browser --ip=127.0.0.1 --port=8888 " +
		"--ServerApp.base_url=" + prefix + " " +
		"--IdentityProvider.token='' --ServerApp.password='' " +
		"--ServerApp.disable_check_xsrf=True --ServerApp.allow_remote_access=True " +
		"--ServerApp.root_dir=/workspace --ServerApp.open_browser=False"
}

// jupyterHealthCommand asks the lab's own API under the same base path.
func jupyterHealthCommand(build adapterBuild) []string {
	return []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:8888/" + build.runtimeID() + "/api/status >/dev/null"}
}

// gooseConfigHome is where Goose keeps its configuration. Its sessions and logs
// sit beside it under the home volume, so a conversation somebody had survives
// the Pod.
const gooseConfigHome = "/home/agent/.config/goose"

// qwenCodeHome is where Qwen Code keeps its settings and credentials. It is on
// the home volume so a person's own settings survive the Pod.
const qwenCodeHome = "/home/agent/.qwen"

// qwenCodeStart serves the terminal under the runtime's own id.
//
// The base path is not decoration: ttyd's browser client asks for /ws relative to
// the base path it was started with, not relative to the page it was loaded from.
// Served at the root behind a /{runtimeId}/ prefix it therefore asks the Portal
// for /ws — and a WebSocket handshake carries no Referer, so nothing can route
// that back to this runtime. The terminal renders and then says "press enter to
// reconnect". Telling ttyd the prefix it is already being served under is what
// makes the page and its socket agree.
func qwenCodeStart(build adapterBuild) string {
	return "exec /usr/local/bin/ttyd --port 7681 --interface 127.0.0.1 --writable " +
		"--base-path /" + build.runtimeID() + " " +
		"--client-option titleFixed=AgentHub " +
		"/usr/local/bin/agenthub-qwencode-shell"
}

// qwenCodeHealthCommand asks ttyd's own token endpoint, which answers as soon as
// the terminal is being served — under the same base path.
func qwenCodeHealthCommand(build adapterBuild) []string {
	return []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:7681/" + build.runtimeID() + "/token >/dev/null"}
}

// gooseStart serves the agent's terminal chat under the runtime's own path, for
// the same reason Qwen Code's is served that way: ttyd asks for its websocket
// relative to the base path it was started with.
func gooseStart(build adapterBuild) string {
	return "exec /usr/local/bin/ttyd --port 7681 --interface 127.0.0.1 --writable " +
		"--base-path /" + build.runtimeID() + " " +
		"--client-option titleFixed=AgentHub " +
		"/usr/local/bin/agenthub-goose-shell"
}

// gooseHealthCommand asks ttyd's own token endpoint under the same base path.
func gooseHealthCommand(build adapterBuild) []string {
	return []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:7681/" + build.runtimeID() + "/token >/dev/null"}
}

// gooseModelEnv is the model binding by the names Goose reads.
//
// It splits the platform's base URL rather than passing it whole, because Goose
// composes its endpoint from a host and a path it appends to it — give it the
// whole URL as the host and every request goes to /v1/v1/chat/completions. The
// path defaults to the OpenAI convention when the gateway's URL carries none.
func gooseModelEnv(baseURL string) []corev1.EnvVar {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return nil
	}
	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		path = "v1"
	}
	return []corev1.EnvVar{
		{Name: "OPENAI_HOST", Value: parsed.Scheme + "://" + parsed.Host},
		{Name: "OPENAI_BASE_PATH", Value: path + "/chat/completions"},
	}
}

// langflowHealthCommand is Langflow's own health endpoint, asked from inside the
// container. It answers `{"status":"ok"}` once the server is up.
var langflowHealthCommand = []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:7860/health >/dev/null"}

// langflowConfigDir is where Langflow's database, flows and generated secret key
// live. It is under /home/agent because that is the volume that survives a
// restart: on an emptyDir every flow a person drew would be gone with the Pod.
const langflowConfigDir = "/home/agent/.langflow"

var homeAndConfigMounts = []corev1.VolumeMount{
	{Name: "home", MountPath: "/home/agent"},
	{Name: "config", MountPath: "/etc/agenthub", ReadOnly: true},
	{Name: "tmp", MountPath: "/tmp"},
}

// runtimeAdapters is the registry. The zero adapter (an unknown type) leaves the
// agent container without a command, which surfaces as a clear image-entrypoint
// failure rather than a silently misconfigured Pod.
var runtimeAdapters = map[string]runtimeAdapter{
	runtimetype.OpenCode: {
		Type:    runtimetype.OpenCode,
		Command: []string{"opencode"},
		Args:    []string{"serve", "--hostname", "0.0.0.0", "--port", "4096"},
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{
				build.secretEnv("OPENCODE_SERVER_PASSWORD", "runtime-token"),
				{Name: "OLLAMA_HOST", Value: build.Value.Model.BaseURL},
				{Name: "OPENCODE_CONFIG_DIR", Value: "/home/agent/.config/opencode"},
			}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "opencode-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{opencodeConfigInit}, Env: build.Env,
				Resources:       initResources("200m", "256Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
	},
	runtimetype.Hermes: {
		Type:    runtimetype.Hermes,
		Command: []string{"/bin/sh", "-ec"},
		Args:    []string{hermesStart},
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{build.secretEnv("API_SERVER_KEY", "runtime-token")}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "hermes-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{hermesConfigInit}, Env: build.Env,
				Resources:       initResources("200m", "256Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// The Hermes Dashboard has no authenticator, so it binds to loopback
			// and is published only through the token-enforcing runtime proxy.
			dashboard := corev1.Container{
				Name: "hermes-dashboard", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/opt/hermes/.venv/bin/hermes"},
				Args:    []string{"dashboard", "--host", "127.0.0.1", "--port", "9120", "--no-open"},
				Env: []corev1.EnvVar{
					{Name: "HERMES_HOME", Value: "/home/agent/.hermes"},
					{Name: "API_SERVER_URL", Value: "http://127.0.0.1:8642"},
					build.secretEnv("API_SERVER_KEY", "runtime-token"),
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("128Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("1000m"), corev1.ResourceMemory: apiresource.MustParse("1024Mi")},
				},
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}
			return []corev1.Container{dashboard, runtimeProxyContainer("hermes-dashboard-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:9120")}
		},
	},
	runtimetype.QwenCode: {
		Type:    runtimetype.QwenCode,
		Command: []string{"/bin/sh", "-ec"},
		// Args are built per runtime because the terminal is served under the
		// runtime's own id; the builder calls Args when the adapter leaves it empty.
		ArgsFor: func(build adapterBuild) []string { return []string{qwenCodeStart(build)} },
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{
				{Name: "QWEN_CODE_HOME", Value: qwenCodeHome},
				{Name: "AGENTHUB_QWEN_SETTINGS", Value: "/etc/agenthub/qwen-settings.json"},
				// The model binding by the names Qwen Code reads. OPENAI_API_KEY and
				// OPENAI_BASE_URL are already in the shared environment; the model
				// name is not, because every other adapter takes it from its own
				// configuration file.
				{Name: "OPENAI_MODEL", Value: build.Value.Model.Name},
			}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "qwencode-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/usr/local/bin/agenthub-qwencode-configure"}, Env: build.Env,
				// Creating the agent's virtualenv is the expensive part; it happens
				// once per home volume and then never again.
				Resources:       initResources("500m", "512Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// A browser terminal with no authenticator in front of it is a shell
			// anyone who reaches the port can use.
			return []corev1.Container{runtimeProxyContainer("qwencode-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:7681")}
		},
		// Checked from inside the container: ttyd is on loopback, so a probe from
		// the kubelet could never connect.
		Probes: func(build adapterBuild) (*corev1.Probe, *corev1.Probe) {
			command := qwenCodeHealthCommand(build)
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 5, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 24,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 60, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
				}
		},
	},
	runtimetype.Goose: {
		Type:    runtimetype.Goose,
		Command: []string{"/bin/sh", "-ec"},
		ArgsFor: func(build adapterBuild) []string { return []string{gooseStart(build)} },
		Env: func(build adapterBuild) []corev1.EnvVar {
			env := []corev1.EnvVar{
				{Name: "GOOSE_CONFIG_HOME", Value: gooseConfigHome},
				{Name: "AGENTHUB_GOOSE_CONFIG", Value: "/etc/agenthub/goose-config.yaml"},
			}
			return append(env, gooseModelEnv(build.Value.Model.BaseURL)...)
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "goose-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/usr/local/bin/agenthub-goose-configure"}, Env: build.Env,
				// Creating the agent's virtualenv is the expensive part; it happens
				// once per home volume and then never again.
				Resources:       initResources("500m", "512Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// A browser terminal with no authenticator in front of it is a shell
			// anyone who reaches the port can use.
			return []corev1.Container{runtimeProxyContainer("goose-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:7681")}
		},
		Probes: func(build adapterBuild) (*corev1.Probe, *corev1.Probe) {
			command := gooseHealthCommand(build)
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 5, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 24,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 60, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
				}
		},
	},
	runtimetype.Jupyter: {
		Type:    runtimetype.Jupyter,
		Command: []string{"/bin/sh", "-ec"},
		ArgsFor: func(build adapterBuild) []string { return []string{jupyterStart(build)} },
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{
				{Name: "QWEN_CODE_HOME", Value: qwenCodeHome},
				{Name: "AGENTHUB_QWEN_SETTINGS", Value: "/etc/agenthub/qwen-settings.json"},
				{Name: "OPENAI_MODEL", Value: build.Value.Model.Name},
			}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			// The same initialiser as Qwen Code: this image is that image plus the
			// notebook toolchain, and the agent in it needs the same preparation.
			return []corev1.Container{{
				Name: "jupyter-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/usr/local/bin/agenthub-qwencode-configure"}, Env: build.Env,
				Resources:       initResources("500m", "512Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// A notebook server is arbitrary code execution with a file browser
			// attached; it is published through the token-checking proxy only.
			return []corev1.Container{runtimeProxyContainer("jupyter-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:8888")}
		},
		Probes: func(build adapterBuild) (*corev1.Probe, *corev1.Probe) {
			command := jupyterHealthCommand(build)
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 10, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 30,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 120, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
				}
		},
	},
	runtimetype.NodeRED: {
		Type:    runtimetype.NodeRED,
		Command: []string{"/bin/sh", "-ec"},
		Args:    []string{nodeREDStart},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "nodered-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{nodeREDConfigInit}, Env: build.Env,
				Resources:       initResources("200m", "256Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// The editor has no authenticator of its own here: anyone who reached
			// the port could deploy a flow, so only the proxy publishes it.
			return []corev1.Container{runtimeProxyContainer("nodered-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:1880")}
		},
		Probes: func(build adapterBuild) (*corev1.Probe, *corev1.Probe) {
			command := nodeREDHealthCommand(build)
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 10, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 24,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 90, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
				}
		},
	},
	runtimetype.N8N: {
		Type:    runtimetype.N8N,
		Command: []string{"/bin/sh", "-ec"},
		Args:    []string{"exec n8n start"},
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{
				// No N8N_PATH. It exists, and n8n does rewrite its HTML to use it,
				// but with it set the static assets and the REST API both fall
				// through to the index page — the browser is handed HTML where it
				// asked for JavaScript and the editor never starts. n8n is therefore
				// served at the root of its own origin, which is why its descriptor
				// says a Runtime Base Domain is required.
				{Name: "N8N_USER_FOLDER", Value: n8nHome},
				{Name: "N8N_LISTEN_ADDRESS", Value: "127.0.0.1"},
				{Name: "N8N_PORT", Value: "5678"},
				// The encryption key protects the credentials a person saves in it.
				// It is the runtime's own token, which is created once and kept for
				// the life of the runtime — the same lifetime as the volume those
				// credentials live on.
				build.secretEnv("N8N_ENCRYPTION_KEY", "runtime-token"),
			}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "n8n-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{n8nConfigInit}, Env: build.Env,
				Resources:       initResources("200m", "256Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{runtimeProxyContainer("n8n-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:5678")}
		},
		Probes: func(build adapterBuild) (*corev1.Probe, *corev1.Probe) {
			command := n8nHealthCommand(build)
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 15, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 36,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
					InitialDelaySeconds: 180, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
				}
		},
	},
	runtimetype.Langflow: {
		Type:    runtimetype.Langflow,
		Command: []string{"/bin/sh", "-ec"},
		Args:    []string{langflowStart},
		Env: func(build adapterBuild) []corev1.EnvVar {
			return []corev1.EnvVar{
				// Loopback only. Langflow starts with automatic login so that a
				// person arriving through the platform proxy is not asked for a
				// second password; that also means its port must not be reachable
				// any other way.
				{Name: "LANGFLOW_HOST", Value: "127.0.0.1"},
				{Name: "LANGFLOW_PORT", Value: "7860"},
				{Name: "LANGFLOW_AUTO_LOGIN", Value: "true"},
				{Name: "LANGFLOW_CONFIG_DIR", Value: langflowConfigDir},
				{Name: "LANGFLOW_SAVE_DB_IN_CONFIG_DIR", Value: "true"},
				{Name: "LANGFLOW_OPEN_BROWSER", Value: "false"},
				// The Langflow API stays authenticated even with automatic login
				// on, and the key it checks is the runtime's own token — the same
				// one the proxy in front of it checks. That is what lets the
				// execution plane run a saved flow without a second credential.
				{Name: "LANGFLOW_API_KEY_SOURCE", Value: "env"},
				build.secretEnv("LANGFLOW_API_KEY", "runtime-token"),
				// An offline site must not phone home, and Langflow's telemetry is
				// on unless this is set.
				{Name: "DO_NOT_TRACK", Value: "true"},
				// The platform's model binding, offered to flows as global
				// variables. Without this a person would have to retype the model
				// endpoint and its key into every flow they draw.
				{Name: "LANGFLOW_VARIABLES_TO_GET_FROM_ENVIRONMENT", Value: "OPENAI_API_KEY,OPENAI_BASE_URL,AGENTHUB_MODEL_NAME"},
				{Name: "LANGFLOW_FALLBACK_TO_ENV_VAR", Value: "true"},
			}
		},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "langflow-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-ec"}, Args: []string{langflowConfigInit}, Env: build.Env,
				Resources:       initResources("200m", "256Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    homeAndConfigMounts,
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// Langflow has no base-path setting, so the proxy publishes it from the
			// root of the runtime's own origin rather than under a prefix.
			return []corev1.Container{runtimeProxyContainer("langflow-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:7860")}
		},
		// Checked from inside the container, because that is the only place
		// 127.0.0.1:7860 exists. The grace is generous on purpose: Langflow builds
		// its component index and migrates its database on first start, which on a
		// cold volume takes far longer than the second start does.
		Probes: func(adapterBuild) (*corev1.Probe, *corev1.Probe) {
			return &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: langflowHealthCommand}},
					InitialDelaySeconds: 15, PeriodSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 30,
				}, &corev1.Probe{
					ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: langflowHealthCommand}},
					InitialDelaySeconds: 300, PeriodSeconds: 30, TimeoutSeconds: 5, FailureThreshold: 4,
				}
		},
	},
	runtimetype.QwenPaw: {
		Type:    runtimetype.QwenPaw,
		Command: []string{"/bin/sh", "-ec"},
		Args:    []string{qwenPawStart},
		InitContainers: func(build adapterBuild) []corev1.Container {
			return []corev1.Container{{
				Name: "qwenpaw-config-init", Image: build.image(), ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/usr/local/bin/agenthub-qwenpaw-configure"}, Env: build.Env,
				// The initialiser imports the skill pool, which needs more than the
				// other adapters' plain file copies.
				Resources:       initResources("500m", "512Mi"),
				SecurityContext: restrictedContainerSecurityContext(build.Value.Security.ReadOnlyRootFilesystem),
				VolumeMounts:    []corev1.VolumeMount{{Name: "home", MountPath: "/home/agent"}, {Name: "tmp", MountPath: "/tmp"}},
			}}
		},
		Sidecars: func(build adapterBuild) []corev1.Container {
			// `qwenpaw app` ships no authenticator either, so the same proxy fronts
			// it and every browser session still has to present the runtime token.
			return []corev1.Container{runtimeProxyContainer("qwenpaw-proxy", build.Name, build.sidecarImage(), "http://127.0.0.1:8642")}
		},
	},
}

func adapterFor(runtimeType string) runtimeAdapter { return runtimeAdapters[runtimeType] }

// adapterArgs resolves the start arguments, letting an adapter build them from
// the runtime it is starting.
func adapterArgs(adapter runtimeAdapter, build adapterBuild) []string {
	if adapter.ArgsFor != nil {
		return adapter.ArgsFor(build)
	}
	return adapter.Args
}
