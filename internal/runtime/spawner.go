package runtime

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/hkjang/AgentHub/internal/buildinfo"
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

// DefaultBaseImage is the offline runtime image that ships alongside this
// control plane build. `make image-base` tags it from the same VERSION file, so
// deriving the tag here keeps a released control plane from requesting an image
// tag that was never built.
func DefaultBaseImage() string {
	if override := strings.TrimSpace(os.Getenv(EnvDefaultBaseImage)); override != "" {
		return override
	}
	return "agenthub-base:v" + strings.TrimSuffix(buildinfo.Version, "-dev")
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
	MCPServers                     []MCPBinding
	Security                       SecurityProfile
	Network                        NetworkProfile
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

type SnapshotSpec struct {
	Name      string
	PVCName   string
	Namespace string
}

type Spawner interface {
	Spawn(context.Context, Spec) error
	Start(context.Context, Spec) error
	Stop(context.Context, Spec) error
	Restart(context.Context, Spec) error
	Delete(context.Context, Spec) error
	Status(context.Context, Spec) (Status, error)
	Logs(context.Context, Spec, int64) ([]byte, error)
	Connection(context.Context, Spec) (Connection, error)
	Snapshot(context.Context, SnapshotSpec) error
	SnapshotStatus(context.Context, SnapshotSpec) (string, int64, error)
}

type DisabledSpawner struct{}

func (DisabledSpawner) Spawn(context.Context, Spec) error   { return ErrNotConfigured }
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
func (DisabledSpawner) Snapshot(context.Context, SnapshotSpec) error { return ErrNotConfigured }
func (DisabledSpawner) SnapshotStatus(context.Context, SnapshotSpec) (string, int64, error) {
	return "", 0, ErrNotConfigured
}
