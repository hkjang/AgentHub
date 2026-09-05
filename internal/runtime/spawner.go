package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// ErrSnapshotMissing means this cluster does snapshots and does not have this
// one. It used to be reported as the cluster lacking snapshot support, which
// sent an operator to install a CRD that was already installed — while the real
// answer was that the thing they were about to restore from is gone.
var ErrSnapshotMissing = errors.New("snapshot no longer exists in the cluster")

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
	// EnvDefaultOpenCodeReviewImage overrides the review engine image.
	EnvDefaultOpenCodeReviewImage = "AGENTHUB_DEFAULT_OPENCODEREVIEW_IMAGE"
	// EnvDefaultOrcaImage overrides the execution fabric image.
	EnvDefaultOrcaImage = "AGENTHUB_DEFAULT_ORCA_IMAGE"
	// EnvDefaultPiImage overrides the RPC-driven coding agent image.
	EnvDefaultPiImage = "AGENTHUB_DEFAULT_PI_IMAGE"
	// EnvDefaultPrimeAgentImage overrides the Prime Agent image.
	EnvDefaultPrimeAgentImage = "AGENTHUB_DEFAULT_PRIMEAGENT_IMAGE"
	// EnvDefaultOpenHandsImage overrides the agent server image.
	EnvDefaultOpenHandsImage = "AGENTHUB_DEFAULT_OPENHANDS_IMAGE"
	EnvDefaultJupyterImage   = "AGENTHUB_DEFAULT_JUPYTER_IMAGE"
	EnvDefaultNodeREDImage   = "AGENTHUB_DEFAULT_NODERED_IMAGE"
	EnvDefaultN8NImage       = "AGENTHUB_DEFAULT_N8N_IMAGE"
	EnvDefaultGooseImage     = "AGENTHUB_DEFAULT_GOOSE_IMAGE"
	EnvDefaultHolmesImage    = "AGENTHUB_DEFAULT_HOLMES_IMAGE"
	EnvDefaultBrowserCode    = "AGENTHUB_DEFAULT_BROWSERCODE_IMAGE"
)

// DefaultRuntimeImage is the image a runtime of this type starts from when no
// administrator has approved one.
//
// Several runtimes do not boot from the shared base image: each is published as
// its own archive with its own version, because each carries a runtime tree no
// other adapter needs. Sending one of their agents to agenthub-base leaves it
// looking for a binary that image never contained — the Pod starts, the command
// is not there, and the runtime fails with nothing an operator can act on.
//
// That is not hypothetical: Goose, HolmesGPT and BrowserCode each shipped with
// their own image and no entry here, so an agent created from the catalog went
// to agenthub-base and could not start. TestEveryRuntimeImageHasADefault now
// reads the Dockerfiles in the repository and fails when one of them has no
// case below.
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
	case runtimetype.OpenCodeReview:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultOpenCodeReviewImage)); override != "" {
			return override
		}
		return "agenthub-opencodereview:v" + strings.TrimSuffix(buildinfo.OpenCodeReviewVersion, "-dev")
	case runtimetype.Pi:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultPiImage)); override != "" {
			return override
		}
		return "agenthub-pi:v" + strings.TrimSuffix(buildinfo.PiVersion, "-dev")
	case runtimetype.PrimeAgent:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultPrimeAgentImage)); override != "" {
			return override
		}
		return "agenthub-primeagent:v" + strings.TrimSuffix(buildinfo.PrimeAgentVersion, "-dev")
	case runtimetype.Orca:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultOrcaImage)); override != "" {
			return override
		}
		return "agenthub-orca:v" + strings.TrimSuffix(buildinfo.OrcaVersion, "-dev")
	case runtimetype.OpenHands:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultOpenHandsImage)); override != "" {
			return override
		}
		return "agenthub-openhands:v" + strings.TrimSuffix(buildinfo.OpenHandsVersion, "-dev")
	case runtimetype.Jupyter:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultJupyterImage)); override != "" {
			return override
		}
		return "agenthub-jupyter:v" + strings.TrimSuffix(buildinfo.JupyterVersion, "-dev")
	case runtimetype.NodeRED:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultNodeREDImage)); override != "" {
			return override
		}
		return "agenthub-nodered:v" + strings.TrimSuffix(buildinfo.NodeREDVersion, "-dev")
	case runtimetype.N8N:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultN8NImage)); override != "" {
			return override
		}
		return "agenthub-n8n:v" + strings.TrimSuffix(buildinfo.N8NVersion, "-dev")
	case runtimetype.Goose:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultGooseImage)); override != "" {
			return override
		}
		return "agenthub-goose:v" + strings.TrimSuffix(buildinfo.GooseVersion, "-dev")
	case runtimetype.Holmes:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultHolmesImage)); override != "" {
			return override
		}
		return "agenthub-holmes:v" + strings.TrimSuffix(buildinfo.HolmesVersion, "-dev")
	case runtimetype.BrowserCode:
		if override := strings.TrimSpace(os.Getenv(EnvDefaultBrowserCode)); override != "" {
			return override
		}
		return "agenthub-browsercode:v" + strings.TrimSuffix(buildinfo.BrowserCodeVersion, "-dev")
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
	Runtime store.Runtime
	Agent   store.Agent
	Profile store.RuntimeProfile
	// HostNetwork is copied into the AgentRuntime object and ultimately the
	// Runtime PodSpec. The Kubernetes administrator setting supplies it at each
	// write so changing the setting is honoured on the next start or restart.
	HostNetwork            bool
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
	// has to cover the tools nobody has seen yet. PolicyDenyAll and PolicyGateAll
	// are a rule that named no tool at all. PolicyAllowed are the exceptions an
	// allow rule named above those restrictions, which the gateway checks first.
	PolicyDenied  []string
	PolicyGated   []string
	PolicyAllowed []string
	PolicyDenyAll bool
	PolicyGateAll bool
}

type SecurityProfile struct {
	RunAsNonRoot                 bool
	ReadOnlyRootFilesystem       bool
	AllowPrivilegeEscalation     bool
	AutomountServiceAccountToken bool
	SeccompProfile               string
	// ClusterRead lets this runtime read the cluster it runs in. It exists for
	// one kind of agent — an investigator, which cannot say why a Pod restarted
	// without looking at it — and it is off unless an administrator turns it on
	// in a security profile, because it is a privilege rather than a setting.
	//
	// It does not mount a service account token: that stays forbidden at every
	// layer. The runtime is given a short-lived, audience-scoped token through a
	// projected volume the kubelet refreshes, and a kubeconfig naming it, which
	// is the same way every other credential reaches a Pod here. What it grants
	// is Kubernetes' own `view` role, which excludes Secrets.
	ClusterRead bool
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

// Session is a command running inside a runtime with its pipes still open.
//
// It exists for protocols: something that has to be spoken, where the answer to
// one message decides the next. Closing Stdin ends the conversation; Wait then
// reports how the command finished and what it said on stderr.
type Session struct {
	Stdin  io.WriteCloser
	Stdout io.Reader

	stderr *bytes.Buffer
	done   chan error
	cancel func()
}

// Wait blocks until the command exits and returns why, if it failed.
func (s *Session) Wait() error { return <-s.done }

// Stderr is whatever the command complained about, which is usually where an
// agent that could not start says so.
func (s *Session) Stderr() string {
	if s.stderr == nil {
		return ""
	}
	return s.stderr.String()
}

// Close ends the conversation from this side.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
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
	// ExecStream opens one with both directions held open, for an agent that
	// speaks a protocol rather than answering a single prompt.
	ExecStream(context.Context, Spec, ExecRequest) (*Session, error)
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
func (DisabledSpawner) ExecStream(context.Context, Spec, ExecRequest) (*Session, error) {
	return nil, ErrNotConfigured
}
func (DisabledSpawner) Snapshot(context.Context, SnapshotSpec) error { return ErrNotConfigured }
func (DisabledSpawner) SnapshotStatus(context.Context, SnapshotSpec) (string, int64, error) {
	return "", 0, ErrNotConfigured
}

// BatchStatus is implemented by a spawner that can report every runtime's status
// in one request instead of one per runtime.
//
// It is separate from Spawner so that a deployment without Kubernetes keeps a
// spawner that cannot pretend to have it, and so the caller falls back to asking
// one at a time rather than failing when it is absent.
type BatchStatus interface {
	StatusAll(ctx context.Context) (map[string]Status, error)
}
