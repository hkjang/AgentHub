package runtime

import (
	"context"
	"errors"

	"github.com/hkjang/AgentHub/internal/store"
)

var ErrNotConfigured = errors.New("Kubernetes runtime is not configured")

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
	ModelBaseURL           string
	ModelName              string
	ModelAPIKey            string
	MCPServers             []MCPBinding
	Security               SecurityProfile
	Network                NetworkProfile
}

type MCPBinding struct {
	Name     string
	Mode     string
	Endpoint string
	Image    string
	Port     int
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
