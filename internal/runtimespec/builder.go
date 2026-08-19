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

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/policy"
	"github.com/hkjang/AgentHub/internal/runtime"
	"github.com/hkjang/AgentHub/internal/runtimecfg"
	"github.com/hkjang/AgentHub/internal/runtimeenv"
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
	highRiskApproval := b.highRiskApprovalEnabled(ctx)
	// The policy and the agent's owner are read once for the whole binding set: a
	// rule can name a role or a user, and neither is on the agent row.
	document := b.policyDocument(ctx)
	owner, ownerErr := b.store.UserByID(ctx, agent.OwnerID)
	if ownerErr != nil {
		b.logger.Warn("agent owner is unreadable; policy rules naming a user or role will not match",
			"agent", agent.ID, "error", ownerErr)
	}
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
			if bound, ok := policyByServer[server.ID]; ok {
				binding.ToolPolicyMode, binding.ToolPolicyTools = bound.Mode, bound.Tools
				binding.ApprovalTools = bound.ApprovalTools
			}
			// The platform-wide policy is compiled in beside the agent's own list.
			// It is compiled rather than consulted per call because the gateway is a
			// separate process in the Pod with no database, and a rule that has to
			// ask the control plane on every tool call would be a rule nobody could
			// afford to write.
			if rules := policy.CompileServer(document, policy.Request{
				Agent: agent.Name, AgentID: agent.ID, Server: server.Name,
				User: owner.Username, UserID: agent.OwnerID, Role: owner.Role,
			}); !rules.Empty() {
				binding.PolicyDenied, binding.PolicyGated, binding.PolicyDenyAll = rules.Denied, rules.Gated, rules.DenyAll
			}
			// The catalogue's own switches finally mean something: a server marked
			// "approval required", or a high-risk server while the governance switch
			// asks for it, gates every tool on it rather than trusting the agent to
			// declare what it is about to do.
			if server.ApprovalRequired || (highRiskApproval && strings.EqualFold(server.RiskLevel, "high")) {
				binding.ApprovalAll = true
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
	provisionedFiles, provisionedVariables := b.runtimeEnvironment(ctx)
	scanner := b.contentScanner(ctx)
	settings := b.runtimeSettings(ctx, agent.RuntimeType)
	customCommand, customPort := agent.CustomRuntime()
	return runtime.Spec{DLP: scanner, RuntimeSettings: settings, ProvisionedFiles: provisionedFiles, ProvisionedVariables: provisionedVariables, Runtime: rt, Agent: agent, SidecarImage: runtime.SidecarImage(),
		CustomCommand: customCommand, CustomPort: int32(customPort), Profile: profile, Image: image, WorkspacePVC: pvc, WorkspaceType: workspaceType, WorkspaceRepositoryURL: repositoryURL, WorkspaceBranch: branch, WorkspaceSnapshot: snapshotName, WorkspaceSizeGB: workspaceSize, WorkspaceGitCredentialKind: gitCredentialKind, WorkspaceGitCredentialUsername: gitCredentialUsername, WorkspaceGitCredential: gitCredential, ModelBaseURL: modelBaseURL, ModelName: modelName, ModelAPIKey: modelAPIKey, MCPServers: bindings, Security: security, Network: network}, nil
}

// runtimeSettings resolves the administrator's overlay for this runtime type. As
// with the policy and the scanner, an unreadable setting is logged and skipped:
// starting a runtime without an overlay is recoverable, and refusing to start
// anything because one setting row no longer parses is not.
func (b *Builder) runtimeSettings(ctx context.Context, runtimeType string) runtimecfg.Profile {
	var settings runtimecfg.Settings
	switch err := b.store.Setting(ctx, runtimecfg.SettingKey, &settings); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return runtimecfg.Profile{}
	default:
		b.logger.Error("runtime settings are unusable; runtimes are provisioned without them", "error", err)
		return runtimecfg.Profile{}
	}
	return settings.For(runtimeType)
}

// contentScanner reads what the in-Pod gateway should inspect on tool calls. As
// with the policy, an unreadable setting is logged and skipped rather than
// blocking every spawn on the cluster.
func (b *Builder) contentScanner(ctx context.Context) dlp.Settings {
	var settings dlp.Settings
	switch err := b.store.Setting(ctx, dlp.SettingKey, &settings); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		b.logger.Error("DLP settings are unusable; runtimes are provisioned without content scanning", "error", err)
		return dlp.Settings{}
	}
	return settings
}

// policyDocument reads the platform-wide policy.
//
// A site that never wrote one has no setting row, which is not a failure. A
// stored document that no longer parses is logged and skipped rather than
// blocking every spawn: the API validates it on the way in, so only a hand-edited
// row can get here, and refusing to start anything would be a worse outcome than
// running without a restriction nobody could read anyway.
func (b *Builder) policyDocument(ctx context.Context) policy.Document {
	var document policy.Document
	switch err := b.store.Setting(ctx, policy.SettingKey, &document); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
	default:
		b.logger.Error("policy document is unusable; runtimes are provisioned without it", "error", err)
	}
	return document
}

// runtimeEnvironment resolves the platform-wide files and variables every
// runtime is provisioned with. A site that never configured any has no setting
// row, which is not a failure; a stored value that no longer parses is logged and
// skipped rather than blocking every spawn on the cluster, because the API
// validates this setting on the way in and only a hand-edited row can get here.
func (b *Builder) runtimeEnvironment(ctx context.Context) ([]runtimeenv.File, []runtimeenv.Variable) {
	var settings runtimeenv.Settings
	switch err := b.store.Setting(ctx, runtimeenv.SettingKey, &settings); {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	default:
		b.logger.Error("runtime environment setting is unusable", "error", err)
		return nil, nil
	}
	return settings.Effective()
}

// highRiskApprovalEnabled reads the governance switch that decides whether a
// high-risk MCP server needs a decision per call. It defaults to on, matching the
// seeded setting: a switch that failed open would be worse than one that asks for
// an approval nobody expected.
func (b *Builder) highRiskApprovalEnabled(ctx context.Context) bool {
	var governance struct {
		HighRiskToolApproval *bool `json:"highRiskToolApproval"`
	}
	if err := b.store.Setting(ctx, "governance", &governance); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			b.logger.Warn("governance setting is unreadable; high-risk tools stay gated", "error", err)
		}
		return true
	}
	return governance.HighRiskToolApproval == nil || *governance.HighRiskToolApproval
}
