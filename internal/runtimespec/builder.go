// Package runtimespec resolves an Agent definition into the Spec the Runtime
// Manager spawns from. Both the interactive control plane and the autonomous
// execution plane start Runtimes from the same definition, so this lives apart
// from either of them.
package runtimespec

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/store"
)

// Builder turns an Agent definition into a spawnable Spec.
type Builder struct {
	store  *store.Store
	logger *slog.Logger
}

func New(db *store.Store, logger *slog.Logger) *Builder { return &Builder{store: db, logger: logger} }

// resolveRuntimeImage honours the image the agent is pinned to so a definition
// keeps running the build it was created against; the catalog's current approved
// image is only a fallback for agents created before pinning existed. A pinned
// entry that has since been deprecated still wins — that is what makes rollback
// possible — but a pin whose row was deleted falls back rather than failing the
// spawn.
func (b *Builder) resolveRuntimeImage(ctx context.Context, agent store.Agent) string {
	if agent.RuntimeImageID != nil && *agent.RuntimeImageID != "" {
		if pinned, err := b.store.RuntimeImageByID(ctx, *agent.RuntimeImageID); err == nil {
			if reference := runtimeImageReference(pinned); reference != "" {
				return reference
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			b.logger.Warn("pinned runtime image lookup failed", "agent", agent.ID, "error", err)
		}
	}
	if approved, err := b.store.ApprovedRuntimeImage(ctx, agent.RuntimeType); err == nil {
		if reference := runtimeImageReference(approved); reference != "" {
			return reference
		}
	}
	return runtime.DefaultBaseImage()
}

// runtimeImageReference builds a pullable reference, preferring the digest so the
// deployment is reproducible even if the tag is later moved.
func runtimeImageReference(item store.RuntimeImage) string {
	if item.Image == "" || strings.HasPrefix(item.Image, "registry.local/") {
		// registry.local is the documentation placeholder, never a real mirror.
		return ""
	}
	reference := item.Image
	if item.Digest != "" {
		return reference + "@" + item.Digest
	}
	if item.Version != "" && !strings.Contains(reference[strings.LastIndex(reference, "/")+1:], ":") {
		reference += ":" + item.Version
	}
	return reference
}

// Build resolves everything a Runtime needs in order to be spawned for one
// agent: profile, workspace, pinned image, model binding, MCP bindings with
// their credentials, and the security and network profiles.
func (b *Builder) Build(ctx context.Context, rt store.Runtime, agent store.Agent) (runtime.Spec, error) {
	profiles, err := b.store.RuntimeProfiles(ctx)
	if err != nil {
		return runtime.Spec{}, err
	}
	profile := store.RuntimeProfile{ID: "rp-basic", Name: "Basic", CPUMillis: 2000, MemoryMB: 4096, StorageGB: 10, IdleTimeoutSeconds: 3600}
	if agent.RuntimeProfileID != nil {
		for _, p := range profiles {
			if p.ID == *agent.RuntimeProfileID {
				profile = p
				break
			}
		}
	}
	pvc, workspaceType, repositoryURL, branch, snapshotName, workspaceSize := "", "empty", "", "", "", profile.StorageGB
	gitCredentialKind, gitCredentialUsername, gitCredential := "", "", ""
	if agent.WorkspaceID != nil {
		workspace, workspaceErr := b.store.WorkspaceByID(ctx, *agent.WorkspaceID, agent.OwnerID, true)
		if workspaceErr != nil {
			return runtime.Spec{}, workspaceErr
		}
		pvc = workspace.PVCName
		workspaceType, repositoryURL, branch, workspaceSize = workspace.Type, workspace.RepositoryURL, workspace.Branch, workspace.SizeGB
		if workspace.GitCredentialSecretID != nil && *workspace.GitCredentialSecretID != "" {
			// Read from the owner's keyring, not the caller's: an administrator may
			// be starting this runtime on the owner's behalf.
			_, value, secretErr := b.store.RevealPersonalSecret(ctx, workspace.OwnerID, *workspace.GitCredentialSecretID)
			switch {
			case secretErr == nil:
				gitCredentialKind, gitCredentialUsername, gitCredential = workspace.GitCredentialKind, workspace.GitCredentialUsername, value
			case errors.Is(secretErr, store.ErrNotFound):
				// The secret was deleted; clone unauthenticated so the failure is a
				// clear git error rather than a spawn that never happens.
				b.logger.Warn("workspace git credential is missing", "workspace", workspace.ID)
			default:
				return runtime.Spec{}, secretErr
			}
		}
		if workspace.SourceSnapshotID != nil {
			snapshot, _, snapshotErr := b.store.WorkspaceSnapshotByID(ctx, agent.OwnerID, *workspace.SourceSnapshotID)
			if snapshotErr != nil {
				return runtime.Spec{}, snapshotErr
			}
			snapshotName = snapshot.StorageRef
		}
	}
	image := b.resolveRuntimeImage(ctx, agent)
	modelBaseURL, modelName, modelAPIKey := "", "", ""
	if agent.ModelEndpointID != nil {
		model, key, modelErr := b.store.ModelEndpointByID(ctx, *agent.ModelEndpointID)
		if modelErr != nil {
			return runtime.Spec{}, modelErr
		}
		modelBaseURL = model.BaseURL
		modelName = model.DefaultModel
		modelAPIKey = key
	} else {
		models, _ := b.store.ModelEndpoints(ctx)
		for _, m := range models {
			if m.Enabled && m.BaseURL != "" && m.DefaultModel != "" {
				_, key, _ := b.store.ModelEndpointByID(ctx, m.ID)
				modelBaseURL = m.BaseURL
				modelName = m.DefaultModel
				modelAPIKey = key
				break
			}
		}
	}
	bindings := []runtime.MCPBinding{}
	if agent.MCPBundleID != nil {
		servers, bundleErr := b.store.MCPServersForBundle(ctx, *agent.MCPBundleID)
		if bundleErr != nil {
			return runtime.Spec{}, bundleErr
		}
		policies, policyErr := b.store.MCPToolPolicies(ctx, agent.ID)
		if policyErr != nil {
			return runtime.Spec{}, policyErr
		}
		policyByServer := make(map[string]store.MCPToolPolicy, len(policies))
		for _, policy := range policies {
			policyByServer[policy.ServerID] = policy
		}
		for _, server := range servers {
			binding := runtime.MCPBinding{Name: server.Name, Mode: server.Mode, Endpoint: server.Endpoint, Image: server.Image, Port: server.Port, AuthType: server.AuthType, AuthHeader: server.AuthHeader}
			if binding.AuthType != "" && binding.AuthType != "none" {
				credential, credentialErr := b.store.MCPCredential(ctx, server, agent.OwnerID)
				switch {
				case credentialErr == nil:
					binding.Credential = credential
				case errors.Is(credentialErr, store.ErrNotFound):
					// Bind it anyway: the runtime reports a clear 401 from the MCP
					// server, which is easier to act on than the tool vanishing.
					b.logger.Warn("MCP credential is not configured", "server", server.Name, "agent", agent.ID, "perUser", server.PerUserCredential)
				default:
					return runtime.Spec{}, credentialErr
				}
			}
			if policy, ok := policyByServer[server.ID]; ok {
				binding.ToolPolicyMode, binding.ToolPolicyTools = policy.Mode, policy.Tools
			}
			bindings = append(bindings, binding)
		}
	}
	security := runtime.SecurityProfile{RunAsNonRoot: true, AllowPrivilegeEscalation: false, AutomountServiceAccountToken: false, SeccompProfile: "RuntimeDefault"}
	if agent.SecurityProfileID != nil {
		item, profileErr := b.store.PolicyProfileByID(ctx, "security", *agent.SecurityProfileID)
		if profileErr != nil {
			return runtime.Spec{}, profileErr
		}
		security.ReadOnlyRootFilesystem, _ = item.Spec["readOnlyRootFilesystem"].(bool)
		if seccomp, ok := item.Spec["seccompProfile"].(string); ok && seccomp != "" {
			security.SeccompProfile = seccomp
		}
	}
	network := runtime.NetworkProfile{DefaultDeny: true, AllowDNS: true}
	if agent.NetworkProfileID != nil {
		item, profileErr := b.store.PolicyProfileByID(ctx, "network", *agent.NetworkProfileID)
		if profileErr != nil {
			return runtime.Spec{}, profileErr
		}
		if value, ok := item.Spec["defaultDeny"].(bool); ok {
			network.DefaultDeny = value
		}
		if value, ok := item.Spec["allowDNS"].(bool); ok {
			network.AllowDNS = value
		}
		if values, ok := item.Spec["allowedDestinations"].([]any); ok {
			for _, value := range values {
				if destination, ok := value.(string); ok && destination != "" {
					network.AllowedDestinations = append(network.AllowedDestinations, destination)
				}
			}
		}
	}
	customCommand, customPort := agent.CustomRuntime()
	return runtime.Spec{Runtime: rt, Agent: agent, SidecarImage: runtime.SidecarImage(),
		CustomCommand: customCommand, CustomPort: int32(customPort), Profile: profile, Image: image, WorkspacePVC: pvc, WorkspaceType: workspaceType, WorkspaceRepositoryURL: repositoryURL, WorkspaceBranch: branch, WorkspaceSnapshot: snapshotName, WorkspaceSizeGB: workspaceSize, WorkspaceGitCredentialKind: gitCredentialKind, WorkspaceGitCredentialUsername: gitCredentialUsername, WorkspaceGitCredential: gitCredential, ModelBaseURL: modelBaseURL, ModelName: modelName, ModelAPIKey: modelAPIKey, MCPServers: bindings, Security: security, Network: network}, nil
}
