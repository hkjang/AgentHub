package runtime

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/hkjang/AgentHub/internal/buildinfo"
	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/runtimecfg"
	"github.com/hkjang/AgentHub/internal/runtimeenv"
	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

var ErrNotConfigured = errors.New("Kubernetes runtime is not configured")

// ErrSnapshotsUnsupported means the cluster has no CSI snapshot support: the
// snapshot.storage.k8s.io CRDs are not installed. Workspace snapshots are an
// optional capability, so this is reported separately from a genuine failure.
var ErrSnapshotsUnsupported = errors.New("cluster does not provide CSI VolumeSnapshot support")

// EnvDefaultBaseImage overrides the runtime base image used when an
// administrator has not approved one in Admin ▸ Resources ▸ Images. Offline
// sites that retag the bundled image need this escape hatch.
const EnvDefaultBaseImage = "AGENTHUB_DEFAULT_RUNTIME_IMAGE"

// DefaultBaseImage is the offline runtime image this control plane build runs
// on. It follows BASE_VERSION rather than VERSION: the base image is only
// rebuilt when something it is built from changes, so a control plane release
// that derived the tag from its own version would ask for an image tag that was
// never published.
func DefaultBaseImage() string {
	if override := strings.TrimSpace(os.Getenv(EnvDefaultBaseImage)); override != "" {
		return override
	}
	return "agenthub-base:v" + strings.TrimSuffix(buildinfo.BaseVersion, "-dev")
}

// EnvDefaultLangflowImage and EnvDefaultQwenCodeImage override the images of the
// runtimes that do not boot from the shared one, the same way
// EnvDefaultBaseImage overrides it.
const (
	EnvDefaultLangflowImage = "AGENTHUB_DEFAULT_LANGFLOW_IMAGE"
	EnvDefaultQwenCodeImage = "AGENTHUB_DEFAULT_QWENCODE_IMAGE"
)

// DefaultRuntimeImage is the image a runtime of this type starts from when no
// administrator has approved one.
//
// Langflow and Qwen Code do not boot from the shared base image: each is
// published as its own archive with its own version, because each carries a
// runtime tree no other adapter needs. Sending one of their agents to
// agenthub-base would leave it looking for a binary that image never contained.
func DefaultRuntimeImage(runtimeType string) string {
	switch runtimeType {
	case runtimetype.Langflow:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultLangflowImage)); override != "" {
			return override
		}
		return "agenthub-langflow:v" + strings.TrimSuffix(buildinfo.LangflowVersion, "-dev")
	case runtimetype.QwenCode:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultQwenCodeImage)); override != "" {
			return override
		}
		return "agenthub-qwencode:v" + strings.TrimSuffix(buildinfo.QwenCodeVersion, "-dev")
	}
	return DefaultBaseImage()
}

// EnvSidecarImage names the image AgentHub's own sidecars run. Deployments that
// pull from a private registry set it to the same reference the control plane
// itself was deployed with.
const EnvSidecarImage = "AGENTHUB_SIDECAR_IMAGE"

// SidecarImage is the image for the session proxy and the MCP tool policy
// gateway. It follows the control plane's version rather than the runtime's, so
// an agent pinned to an older runtime image still gets this release's sidecars.
func SidecarImage() string {
	if override := strings.TrimSpace(os.Getenv(EnvSidecarImage)); override != "" {
		return override
	}
	return "agenthub:v" + strings.TrimSuffix(buildinfo.Version, "-dev")
}

type Spec struct {
	Runtime                store.Runtime
	Agent                  store.Agent
	Profile                store.RuntimeProfile
	Image                  string
	Namespace              string
	WorkspacePVC           string
	WorkspaceType          string
	WorkspaceRepositoryURL string
	WorkspaceBranch        string
	WorkspaceSnapshot      string
	WorkspaceSizeGB        int
	// Git credential for a private repository clone. Kind is "token" or
	// "ssh-key"; the value travels in the runtime Secret, never the CRD.
	WorkspaceGitCredentialKind     string
	WorkspaceGitCredentialUsername string
	WorkspaceGitCredential         string
	ModelBaseURL                   string
	ModelName                      string
	ModelAPIKey                    string
	// CustomCommand and CustomPort start a 'custom' runtime, which has no adapter
	// of its own. Every other runtime ignores them.
	CustomCommand []string
	CustomPort    int32
	// SidecarImage runs AgentHub's own sidecars — the session proxy and the MCP
	// tool policy gateway. It is the control plane's image rather than the
	// runtime's, so pinning an agent to an older runtime image cannot leave it
	// running platform code from that older release.
	SidecarImage string
	MCPServers   []MCPBinding
	Security     SecurityProfile
	Network      NetworkProfile
	// ProvisionedFiles and ProvisionedVariables are the platform-wide runtime
	// environment an administrator configures once in Admin ▸ Settings ▸ Runtime
	// Environment. Every runtime gets the same set, which is the point: an
	// offline site's /etc/pip.conf should not have to be repeated per agent.
	ProvisionedFiles     []runtimeenv.File
	ProvisionedVariables []runtimeenv.Variable
	// DLP is the content scanner's configuration for tool calls, which are
	// inspected inside the Pod for the same reason tool policy is enforced there:
	// it is the one place the agent process cannot route around.
	DLP dlp.Settings
	// RuntimeSettings is the administrator's overlay for this runtime type — its
	// locale, its own product options, whatever knob that adapter exposes. It is
	// merged into the configuration the platform generates rather than delivered
	// beside it, because the runtime reads exactly one file.
	RuntimeSettings runtimecfg.Profile
}

type MCPBinding struct {
	Name     string
	Mode     string
	Endpoint string
	Image    string
	Port     int
	// AuthType is none, bearer, header or basic. AuthHeader names the header for
	// auth_type=header. Credential is the resolved plaintext for the requesting
	// user; it is delivered through the runtime Secret, never the CRD spec.
	AuthType   string
	AuthHeader string
	Credential string
	// ToolPolicyMode is allow, deny, or empty for no policy. ToolPolicyTools is
	// the list it applies to. The policy is enforced by the egress gateway in the
	// Pod rather than the agent process, so it holds whatever the model tries.
	ToolPolicyMode  string
	ToolPolicyTools []string
	// ApprovalTools need a person's decision before they run; ApprovalAll gates
	// every tool on the server, which is what an approval-required or high-risk
	// entry in the admin catalogue means. The gateway holds the call open while it
	// waits, so an agent that never asks for approval is gated anyway.
	ApprovalTools []string
	ApprovalAll   bool
	// PolicyDenied and PolicyGated are patterns compiled from the platform-wide
	// policy for this agent and this server. They are patterns rather than tool
	// names because the tool list is not known until the server runs, and a rule
	// has to cover the tools nobody has seen yet. PolicyDenyAll is a rule that
	// named no tool at all.
	PolicyDenied  []string
	PolicyGated   []string
	PolicyDenyAll bool
}

type SecurityProfile struct {
	RunAsNonRoot                 bool
	ReadOnlyRootFilesystem       bool
	AllowPrivilegeEscalation     bool
	AutomountServiceAccountToken bool
	SeccompProfile               string
}

type NetworkProfile struct {
	DefaultDeny         bool
	AllowDNS            bool
	AllowedDestinations []string
}

type Status struct {
	Phase         string
	PodName       string
	NodeName      string
	Endpoint      string
	RestartCount  int
	FailureReason string
}

type Connection struct {
	Endpoint    string
	RuntimeType string
	Token       string
}

// ExecRequest is one command to run inside a runtime.
type ExecRequest struct {
	// Command is argv. It is never a shell string: the platform builds it from
	// its own settings, and a shell would invite quoting bugs where a task title
	// becomes a command.
	Command []string
	// Container defaults to the agent container.
	Container string
	// Stdin is fed to the command when it is not empty.
	Stdin string
}

// ExecResult is what the command said and how it ended.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type SnapshotSpec struct {
	Name      string
	PVCName   string
	Namespace string
}

type Spawner interface {
	Spawn(context.Context, Spec) error
	// Sync re-renders a runtime's object from the spec without changing whether
	// it is meant to be running. It exists because the platform-wide runtime
	// environment is copied into every runtime's object when it is written: an
	// administrator who adds /etc/pip.conf changes the setting, and until the
	// object is written again the Pods carry the old one.
	Sync(context.Context, Spec) error
	Start(context.Context, Spec) error
	Stop(context.Context, Spec) error
	Restart(context.Context, Spec) error
	Delete(context.Context, Spec) error
	Status(context.Context, Spec) (Status, error)
	Logs(context.Context, Spec, int64) ([]byte, error)
	Connection(context.Context, Spec) (Connection, error)
	// Exec runs a command inside a running runtime's agent container. It is what
	// lets the execution plane drive a runtime whose agent is a command-line
	// program with no API of its own.
	Exec(context.Context, Spec, ExecRequest) (ExecResult, error)
	Snapshot(context.Context, SnapshotSpec) error
	SnapshotStatus(context.Context, SnapshotSpec) (string, int64, error)
}

// ErrProvisioningUnsupported means the cluster's AgentRuntime definition is
// older than this control plane: the API server accepted the object and silently
// dropped the platform-wide runtime environment from it, because a CRD prunes
// fields its schema does not declare.
//
// It has its own error because the symptom is otherwise indistinguishable from
// the feature not working — the setting saves, the object is written, and no file
// ever reaches a Pod.
var ErrProvisioningUnsupported = errors.New("cluster의 AgentRuntime CRD가 오래되어 런타임 환경 설정이 저장되지 않습니다. deploy/kubernetes/crd.yaml을 다시 적용해 주세요")

type DisabledSpawner struct{}

func (DisabledSpawner) Spawn(context.Context, Spec) error   { return ErrNotConfigured }
func (DisabledSpawner) Sync(context.Context, Spec) error    { return ErrNotConfigured }
func (DisabledSpawner) Start(context.Context, Spec) error   { return ErrNotConfigured }
func (DisabledSpawner) Stop(context.Context, Spec) error    { return ErrNotConfigured }
func (DisabledSpawner) Restart(context.Context, Spec) error { return ErrNotConfigured }
func (DisabledSpawner) Delete(context.Context, Spec) error  { return ErrNotConfigured }
func (DisabledSpawner) Status(context.Context, Spec) (Status, error) {
	return Status{}, ErrNotConfigured
}
func (DisabledSpawner) Logs(context.Context, Spec, int64) ([]byte, error) {
	return nil, ErrNotConfigured
}
func (DisabledSpawner) Connection(context.Context, Spec) (Connection, error) {
	return Connection{}, ErrNotConfigured
}
func (DisabledSpawner) Exec(context.Context, Spec, ExecRequest) (ExecResult, error) {
	return ExecResult{}, ErrNotConfigured
}
func (DisabledSpawner) Snapshot(context.Context, SnapshotSpec) error { return ErrNotConfigured }
func (DisabledSpawner) SnapshotStatus(context.Context, SnapshotSpec) (string, int64, error) {
	return "", 0, ErrNotConfigured
}
