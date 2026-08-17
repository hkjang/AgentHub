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
		"fi"

	hermesConfigInit = "mkdir -p /home/agent/.hermes\n" +
		"cp /etc/agenthub/hermes-config.yaml /home/agent/.hermes/config.yaml\n" +
		"if [ -n \"$OPENAI_BASE_URL\" ] && [ -n \"$AGENTHUB_MODEL_NAME\" ]; then\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.default \"$AGENTHUB_MODEL_NAME\" || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.provider custom || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.base_url \"$OPENAI_BASE_URL\" || true\n" +
		"  /opt/hermes/.venv/bin/hermes config set model.api_key \"$OPENAI_API_KEY\" || true\n" +
		"  printf 'OPENAI_BASE_URL=%s\\nOPENAI_API_KEY=%s\\nCUSTOM_BASE_URL=%s\\nCUSTOM_API_KEY=%s\\nHERMES_MODEL=%s\\nMODEL=%s\\n' \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$AGENTHUB_MODEL_NAME\" \"$AGENTHUB_MODEL_NAME\" > /home/agent/.hermes/.env\n" +
		"fi"

	hermesStart = "mkdir -p /home/agent/.hermes\n" +
		"if [ -f /etc/agenthub/hermes-config.yaml ]; then cp /etc/agenthub/hermes-config.yaml /home/agent/.hermes/config.yaml; fi\n" +
		"export API_SERVER_ENABLED=true\n" +
		"export API_SERVER_HOST=0.0.0.0\n" +
		"export API_SERVER_PORT=8642\n" +
		"export API_SERVER_KEY=\"${API_SERVER_KEY:-${AGENTHUB_RUNTIME_TOKEN:-}}\"\n" +
		"exec /opt/hermes/.venv/bin/hermes gateway run --no-supervise"

	qwenPawStart = "/usr/local/bin/agenthub-qwenpaw-configure || true\n" +
		"exec /opt/qwenpaw/.venv/bin/qwenpaw app --host 0.0.0.0 --port 8642"
)

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
