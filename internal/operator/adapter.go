package operator

import (
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
	// files the operator writes to /etc/agenthub.
	Command []string
	Args    []string

	// Env contributes adapter-specific variables on top of the shared set.
	Env func(build adapterBuild) []corev1.EnvVar

	// InitContainers prepare the agent's home directory before it starts.
	InitContainers func(build adapterBuild) []corev1.Container

	// Sidecars run alongside the agent, typically a UI and the token-enforcing
	// proxy in front of it.
	Sidecars func(build adapterBuild) []corev1.Container

	// Readiness and Liveness replace the default TCP probes. A runtime that binds
	// to loopback needs them: the kubelet probes the Pod IP, so nothing outside
	// the container can connect, and the default probe would fail forever on a
	// runtime that is working perfectly.
	Readiness *corev1.Probe
	Liveness  *corev1.Probe
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

	qwenCodeStart = "exec /usr/local/bin/ttyd --port 7681 --interface 127.0.0.1 --writable " +
		"--client-option titleFixed=AgentHub --client-option disableLeaveAlert=true " +
		"/usr/local/bin/agenthub-qwencode-shell"

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

// qwenCodeHome is where Qwen Code keeps its settings and credentials. It is on
// the home volume so a person's own settings survive the Pod.
const qwenCodeHome = "/home/agent/.qwen"

// qwenCodeHealthCommand asks ttyd's own token endpoint, which answers as soon as
// the terminal is being served.
var qwenCodeHealthCommand = []string{"/bin/sh", "-c", "curl -fsS -m 3 http://127.0.0.1:7681/token >/dev/null"}

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
		Args:    []string{qwenCodeStart},
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
		Readiness: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: qwenCodeHealthCommand}},
			InitialDelaySeconds: 5, PeriodSeconds: 5, TimeoutSeconds: 3, FailureThreshold: 24,
		},
		Liveness: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: qwenCodeHealthCommand}},
			InitialDelaySeconds: 60, PeriodSeconds: 30, TimeoutSeconds: 3, FailureThreshold: 4,
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
		Readiness: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: langflowHealthCommand}},
			InitialDelaySeconds: 15, PeriodSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 30,
		},
		Liveness: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: langflowHealthCommand}},
			InitialDelaySeconds: 300, PeriodSeconds: 30, TimeoutSeconds: 5, FailureThreshold: 4,
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
