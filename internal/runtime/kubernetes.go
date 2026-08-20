package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/hkjang/AgentHub/internal/runtimetype"
	"github.com/hkjang/AgentHub/internal/store"
)

var runtimeGVR = schema.GroupVersionResource{Group: "agenthub.io", Version: "v1alpha1", Resource: "agentruntimes"}
var volumeSnapshotGVR = schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}

type kubernetesSettings struct {
	Enabled    bool   `json:"enabled"`
	Namespace  string `json:"namespace"`
	Mode       string `json:"mode"`
	APIServer  string `json:"apiServer"`
	VerifyTLS  bool   `json:"verifyTls"`
	CRDEnabled bool   `json:"crdEnabled"`
}

type KubernetesSpawner struct {
	store *store.Store
	// log is where cluster-level surprises are reported. It is optional so the
	// spawner can be constructed in tests without one.
	log *slog.Logger
}

func NewKubernetesSpawner(db *store.Store) *KubernetesSpawner { return &KubernetesSpawner{store: db} }

// WithLogger attaches a logger, so a cluster that silently drops part of what the
// platform writes says so in the process log rather than nowhere.
func (k *KubernetesSpawner) WithLogger(logger *slog.Logger) *KubernetesSpawner {
	k.log = logger
	return k
}

func (k *KubernetesSpawner) logger() *slog.Logger {
	if k.log != nil {
		return k.log
	}
	return slog.Default()
}

func (k *KubernetesSpawner) clients(ctx context.Context) (dynamic.Interface, kubernetes.Interface, kubernetesSettings, error) {
	var settings kubernetesSettings
	if err := k.store.Setting(ctx, "kubernetes", &settings); err != nil {
		return nil, nil, settings, err
	}
	if !settings.Enabled {
		return nil, nil, settings, ErrNotConfigured
	}
	var config *rest.Config
	var err error
	if settings.Mode == "inCluster" || settings.Mode == "" {
		config, err = rest.InClusterConfig()
	} else {
		token, secretErr := k.store.SettingSecret(ctx, "kubernetes")
		if secretErr != nil {
			return nil, nil, settings, secretErr
		}
		config = &rest.Config{Host: settings.APIServer, BearerToken: token, TLSClientConfig: rest.TLSClientConfig{Insecure: !settings.VerifyTLS}, Timeout: 15 * time.Second}
	}
	if err != nil {
		return nil, nil, settings, fmt.Errorf("configure Kubernetes client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, settings, err
	}
	coreClient, err := kubernetes.NewForConfig(config)
	return dynamicClient, coreClient, settings, err
}

func (k *KubernetesSpawner) object(spec Spec) *unstructured.Unstructured {
	profile := map[string]any{"cpuMillis": int64(spec.Profile.CPUMillis), "memoryMb": int64(spec.Profile.MemoryMB), "storageGb": int64(spec.Profile.StorageGB), "gpuCount": int64(spec.Profile.GPUCount), "idleTimeoutSeconds": int64(spec.Profile.IdleTimeoutSeconds)}
	bindings := make([]any, 0, len(spec.MCPServers))
	for _, m := range spec.MCPServers {
		port := m.Port
		if port <= 0 {
			port = 8000
		}
		binding := map[string]any{"name": m.Name, "mode": m.Mode, "endpoint": m.Endpoint, "image": m.Image, "port": int64(port)}
		if m.ToolPolicyMode != "" || m.ApprovalAll || m.PolicyDenyAll || len(m.ApprovalTools) > 0 || len(m.PolicyDenied) > 0 || len(m.PolicyGated) > 0 {
			policy := map[string]any{
				"tools":            stringList(m.ToolPolicyTools),
				"approvalTools":    stringList(m.ApprovalTools),
				"approvalRequired": m.ApprovalAll,
			}
			if m.ToolPolicyMode != "" {
				policy["mode"] = m.ToolPolicyMode
			}
			// The central policy travels beside the per-agent list rather than
			// merged into it: they are different statements, and merging would make
			// "the platform forbids this" indistinguishable from "this agent was not
			// given it" in every screen that reads them back.
			if len(m.PolicyDenied) > 0 {
				policy["policyDenied"] = stringList(m.PolicyDenied)
			}
			if len(m.PolicyGated) > 0 {
				policy["policyGated"] = stringList(m.PolicyGated)
			}
			if m.PolicyDenyAll {
				policy["policyDenyAll"] = true
			}
			binding["toolPolicy"] = policy
		}
		if m.AuthType != "" && m.AuthType != "none" {
			// The credential itself goes to the Secret; the CRD only names the key,
			// because an AgentRuntime object is readable by anyone with RBAC on it.
			binding["authType"] = m.AuthType
			binding["authHeader"] = m.AuthHeader
			binding["credentialKey"] = mcpCredentialKey(m.Name)
		}
		bindings = append(bindings, binding)
	}
	image := spec.Image
	if image == "" {
		image = DefaultRuntimeImage(spec.Agent.RuntimeType)
	}
	workspaceSize := spec.WorkspaceSizeGB
	if workspaceSize <= 0 {
		workspaceSize = spec.Profile.StorageGB
	}
	object := map[string]any{
		"apiVersion": "agenthub.io/v1alpha1", "kind": "AgentRuntime",
		"metadata": map[string]any{"name": spec.Runtime.CRDName, "labels": map[string]any{"app.kubernetes.io/managed-by": "agenthub", "agenthub.io/owner": labelValue(spec.Agent.OwnerID), "agenthub.io/agent": labelValue(spec.Agent.ID)}},
		"spec": map[string]any{"owner": spec.Agent.OwnerID, "agentRef": map[string]any{"id": spec.Agent.ID, "version": int64(spec.Agent.Version)}, "runtimeRef": map[string]any{"id": spec.Runtime.ID}, "runtime": runtimeObject(spec, image), "profile": profile, "workspace": map[string]any{"type": spec.WorkspaceType, "pvcName": spec.WorkspacePVC, "sizeGb": int64(workspaceSize), "repositoryUrl": spec.WorkspaceRepositoryURL, "branch": spec.WorkspaceBranch, "snapshotName": spec.WorkspaceSnapshot, "gitCredentialKind": spec.WorkspaceGitCredentialKind, "gitCredentialUsername": spec.WorkspaceGitCredentialUsername}, "model": map[string]any{"baseUrl": spec.ModelBaseURL, "name": spec.ModelName, "secretRef": spec.Runtime.CRDName}, "mcp": bindings,
			"security":  map[string]any{"runAsNonRoot": spec.Security.RunAsNonRoot, "readOnlyRootFilesystem": spec.Security.ReadOnlyRootFilesystem, "allowPrivilegeEscalation": spec.Security.AllowPrivilegeEscalation, "automountServiceAccountToken": spec.Security.AutomountServiceAccountToken, "seccompProfile": spec.Security.SeccompProfile},
			"network":   map[string]any{"defaultDeny": spec.Network.DefaultDeny, "allowDNS": spec.Network.AllowDNS, "allowedDestinations": spec.Network.AllowedDestinations},
			"lifecycle": map[string]any{"desiredState": "Running", "autoRestart": true, "idleTimeoutSeconds": int64(spec.Profile.IdleTimeoutSeconds)}}}
	if provisioning := provisioningObject(spec); provisioning != nil {
		object["spec"].(map[string]any)["provisioning"] = provisioning
	}
	if scanner := dlpObject(spec); scanner != nil {
		object["spec"].(map[string]any)["dlp"] = scanner
	}
	if settings := runtimeSettingsObject(spec); settings != nil {
		object["spec"].(map[string]any)["runtimeSettings"] = settings
	}
	return &unstructured.Unstructured{Object: object}
}

// Object renders the AgentRuntime object a spec produces.
//
// It is exported so the operator's tests can take what this package writes and
// parse it with what the operator reads. That seam is where a break in the
// platform-wide runtime environment would be invisible: the setting saves, the
// object is written, the operator silently sees no files, and the feature looks
// like it does not exist.
func Object(spec Spec) *unstructured.Unstructured { return (&KubernetesSpawner{}).object(spec) }

// provisioningObject renders the platform-wide runtime environment. It is left
// off the object entirely when nothing is configured, so a site that never
// touches this setting keeps producing exactly the CRDs it did before.
func provisioningObject(spec Spec) map[string]any {
	if len(spec.ProvisionedFiles) == 0 && len(spec.ProvisionedVariables) == 0 {
		return nil
	}
	files := make([]any, 0, len(spec.ProvisionedFiles))
	for _, file := range spec.ProvisionedFiles {
		files = append(files, map[string]any{"path": file.Path, "content": file.Content, "mode": file.Mode})
	}
	variables := make([]any, 0, len(spec.ProvisionedVariables))
	for _, variable := range spec.ProvisionedVariables {
		variables = append(variables, map[string]any{"name": variable.Name, "value": variable.Value})
	}
	return map[string]any{"files": files, "env": variables}
}

// dlpObject renders the content scanner's configuration for the Pod. It is left
// off entirely when scanning is not configured, so a deployment that never turns
// it on keeps producing exactly the objects it did before.
func dlpObject(spec Spec) map[string]any {
	if !spec.DLP.Enabled || len(spec.DLP.Classes) == 0 {
		return nil
	}
	classes := make([]any, 0, len(spec.DLP.Classes))
	for _, name := range sortedClasses(spec.DLP.Classes) {
		classes = append(classes, map[string]any{"class": name, "action": spec.DLP.Classes[name]})
	}
	scanner := map[string]any{"enabled": true, "classes": classes, "scanResponses": spec.DLP.ScanResponses}
	if spec.DLP.MaxBytes > 0 {
		scanner["maxBytes"] = int64(spec.DLP.MaxBytes)
	}
	return scanner
}

// sortedClasses keeps the rendered object stable, so an unchanged configuration
// does not produce a new Pod template hash on every reconcile.
func sortedClasses(classes map[string]string) []string {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// runtimeSettingsObject renders the administrator's overlay for the Pod. It is
// left off entirely when nothing is configured, so a deployment that never sets
// one keeps producing exactly the objects it did before.
//
// The fingerprint travels with it: the Pod reports it back after applying the
// overlay, which is how "did my change reach the fleet" gets an answer that is not
// somebody's memory of clicking save.
func runtimeSettingsObject(spec Spec) map[string]any {
	profile := spec.RuntimeSettings
	if profile.Empty() {
		return nil
	}
	settings := map[string]any{"fingerprint": profile.Fingerprint()}
	if len(profile.Config) > 0 {
		settings["config"] = profile.Config
	}
	if len(profile.Env) > 0 {
		env := make([]any, 0, len(profile.Env))
		for _, name := range sortedEnvNames(profile.Env) {
			env = append(env, map[string]any{"name": name, "value": profile.Env[name]})
		}
		settings["env"] = env
	}
	return settings
}

// sortedEnvNames keeps the rendered object stable so an unchanged overlay does not
// produce a new Pod template hash on every reconcile.
func sortedEnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// stringList renders a []string as the []any an unstructured object needs.
func stringList(values []string) []any {
	rendered := make([]any, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, value)
	}
	return rendered
}

// runtimeObject renders the runtime section of the CRD. A custom runtime carries
// its own command and port, because there is no adapter to supply them.
func runtimeObject(spec Spec, image string) map[string]any {
	value := map[string]any{"type": spec.Agent.RuntimeType, "image": image, "sidecarImage": spec.SidecarImage}
	if len(spec.CustomCommand) > 0 {
		command := make([]any, 0, len(spec.CustomCommand))
		for _, part := range spec.CustomCommand {
			command = append(command, part)
		}
		value["command"] = command
	}
	if spec.CustomPort > 0 {
		value["port"] = int64(spec.CustomPort)
	}
	return value
}

// gitCredentialKey names the Secret entry holding the workspace clone credential.
const gitCredentialKey = "workspace-git-credential"

// mcpCredentialKey names the Secret entry holding one MCP server's credential.
// Secret keys allow alphanumerics, '-', '_' and '.', which labelValue already
// guarantees.
func mcpCredentialKey(serverName string) string {
	return "mcp-credential-" + labelValue(serverName)
}

// runtimeCredentialData collects every credential that belongs in the runtime
// Secret: the workspace clone credential and one entry per authenticated MCP
// server. None of these may appear in the CRD or the ConfigMap.
func runtimeCredentialData(spec Spec) map[string]string {
	data := map[string]string{}
	if spec.WorkspaceGitCredential != "" {
		data[gitCredentialKey] = spec.WorkspaceGitCredential
	}
	for _, binding := range spec.MCPServers {
		if binding.AuthType == "" || binding.AuthType == "none" || binding.Credential == "" {
			continue
		}
		data[mcpCredentialKey(binding.Name)] = binding.Credential
	}
	return data
}

func labelValue(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := strings.Trim(b.String(), "-.")
	if len(result) > 63 {
		result = result[:63]
	}
	if result == "" {
		return "unknown"
	}
	return result
}

func ensureCRDName(spec *Spec) {
	if spec.Runtime.CRDName == "" && spec.Agent.ID != "" {
		ownerPrefix := "owner"
		if len(spec.Agent.OwnerID) >= 8 {
			ownerPrefix = spec.Agent.OwnerID[:8]
		}
		agentPrefix := "agent"
		if len(spec.Agent.ID) >= 8 {
			agentPrefix = spec.Agent.ID[:8]
		}
		spec.Runtime.CRDName = "agent-" + strings.ToLower(strings.ReplaceAll(ownerPrefix+"-"+agentPrefix, "_", "-"))
	}
}

func (k *KubernetesSpawner) Spawn(ctx context.Context, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return errors.New("resource name may not be empty")
	}
	client, coreClient, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return err
	}
	runtimeToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	// The in-Pod gateway authenticates to the control plane with this token when it
	// has to ask for an approval, so the control plane keeps its hash. Only the
	// hash: the token itself belongs in the Pod's Secret.
	if err = k.store.SetRuntimeGatewayToken(ctx, spec.Runtime.ID, runtimeToken); err != nil {
		return err
	}
	secretData := map[string]string{"runtime-token": runtimeToken, "model-api-key": spec.ModelAPIKey}
	for key, value := range runtimeCredentialData(spec) {
		secretData[key] = value
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: spec.Runtime.CRDName, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "agenthub", "agenthub.io/runtime": spec.Runtime.CRDName}}, Type: corev1.SecretTypeOpaque, StringData: secretData}
	secretCreated := false
	if _, err = coreClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else {
		secretCreated = true
	}
	stored, err := client.Resource(runtimeGVR).Namespace(namespace).Create(ctx, k.object(spec), metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return k.setDesired(ctx, spec, "Running")
		}
		if secretCreated {
			_ = coreClient.CoreV1().Secrets(namespace).Delete(ctx, spec.Runtime.CRDName, metav1.DeleteOptions{})
		}
		return err
	}
	// A pruned environment is worth saying out loud, but not worth refusing to
	// start a runtime over: the agent runs, it just runs without the files an
	// administrator declared.
	if provisioningPruned(spec, stored) {
		k.logger().Warn("the AgentRuntime CRD dropped the runtime environment; apply deploy/kubernetes/crd.yaml",
			"runtime", spec.Runtime.CRDName)
	}
	return nil
}
func (k *KubernetesSpawner) ensureSecret(ctx context.Context, coreClient kubernetes.Interface, namespace string, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return nil
	}
	existing, err := coreClient.CoreV1().Secrets(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		tokenBytes := make([]byte, 32)
		if _, randErr := rand.Read(tokenBytes); randErr != nil {
			return randErr
		}
		runtimeToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
		if tokenErr := k.store.SetRuntimeGatewayToken(ctx, spec.Runtime.ID, runtimeToken); tokenErr != nil {
			return tokenErr
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: spec.Runtime.CRDName, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "agenthub", "agenthub.io/runtime": spec.Runtime.CRDName}}, Type: corev1.SecretTypeOpaque, StringData: map[string]string{"runtime-token": runtimeToken, "model-api-key": spec.ModelAPIKey}}
		_, err = coreClient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	if spec.ModelAPIKey != "" || existing.Data["model-api-key"] == nil {
		existing.Data["model-api-key"] = []byte(spec.ModelAPIKey)
	}
	// Drop stale MCP credentials so unbinding a server also revokes its secret.
	for key := range existing.Data {
		if strings.HasPrefix(key, "mcp-credential-") || key == gitCredentialKey {
			delete(existing.Data, key)
		}
	}
	for key, value := range runtimeCredentialData(spec) {
		existing.Data[key] = []byte(value)
	}
	_, err = coreClient.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}
func (k *KubernetesSpawner) Start(ctx context.Context, spec Spec) error {
	return k.setDesired(ctx, spec, "Running")
}
func (k *KubernetesSpawner) Stop(ctx context.Context, spec Spec) error {
	return k.setDesired(ctx, spec, "Stopped")
}
func (k *KubernetesSpawner) Restart(ctx context.Context, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return errors.New("resource name may not be empty")
	}
	client, coreClient, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	if coreClient != nil {
		_ = k.ensureSecret(ctx, coreClient, namespace, spec)
	}
	object, err := client.Resource(runtimeGVR).Namespace(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return k.Spawn(ctx, spec)
	}
	if err != nil {
		return err
	}
	fresh := k.object(spec)
	object.Object["spec"] = fresh.Object["spec"]
	if object.GetAnnotations() == nil {
		object.SetAnnotations(map[string]string{})
	}
	annotations := object.GetAnnotations()
	annotations["agenthub.io/restarted-at"] = time.Now().UTC().Format(time.RFC3339Nano)
	object.SetAnnotations(annotations)
	_, err = client.Resource(runtimeGVR).Namespace(namespace).Update(ctx, object, metav1.UpdateOptions{})
	return err
}

// Sync rewrites an existing runtime's object from the spec, leaving its desired
// state alone.
//
// This is how a change to the platform-wide runtime environment reaches Pods that
// are already running: the object carries a copy of the files and variables, so
// nothing an administrator saves takes effect until it is written again. A
// runtime that does not exist yet needs nothing — it will be created from the
// current settings whenever it is started.
func (k *KubernetesSpawner) Sync(ctx context.Context, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return nil
	}
	client, _, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	object, err := client.Resource(runtimeGVR).Namespace(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := syncSpec(object, k.object(spec)); err != nil {
		return err
	}
	stored, err := client.Resource(runtimeGVR).Namespace(namespace).Update(ctx, object, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	// The API server prunes fields a CRD's schema does not declare, without
	// saying so. Checking what came back costs nothing — Update already returns
	// the stored object — and it is the difference between "the cluster is a
	// version behind" and "this feature does not work".
	if provisioningPruned(spec, stored) {
		return ErrProvisioningUnsupported
	}
	return nil
}

// provisioningPruned reports whether the environment this spec carries survived
// being written.
func provisioningPruned(spec Spec, stored *unstructured.Unstructured) bool {
	if provisioningObject(spec) == nil || stored == nil {
		return false
	}
	_, found, err := unstructured.NestedMap(stored.Object, "spec", "provisioning")
	return err == nil && !found
}

// syncSpec replaces an object's spec with a freshly rendered one while keeping
// the desired state that is already on it.
//
// The distinction is the whole safety of a sync: pushing a setting to every
// runtime must not start the ones somebody stopped, and must not stop the ones
// that are running.
func syncSpec(existing, fresh *unstructured.Unstructured) error {
	desired, _, _ := unstructured.NestedString(existing.Object, "spec", "lifecycle", "desiredState")
	existing.Object["spec"] = fresh.Object["spec"]
	if desired == "" {
		return nil
	}
	return unstructured.SetNestedField(existing.Object, desired, "spec", "lifecycle", "desiredState")
}

func (k *KubernetesSpawner) Delete(ctx context.Context, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return nil
	}
	client, _, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	err = client.Resource(runtimeGVR).Namespace(namespace).Delete(ctx, spec.Runtime.CRDName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
func (k *KubernetesSpawner) setDesired(ctx context.Context, spec Spec, state string) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return errors.New("resource name may not be empty")
	}
	client, coreClient, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	if state == "Running" && coreClient != nil {
		_ = k.ensureSecret(ctx, coreClient, namespace, spec)
	}
	object, err := client.Resource(runtimeGVR).Namespace(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if state == "Running" {
			return k.Spawn(ctx, spec)
		}
		return nil
	}
	if err != nil {
		return err
	}
	fresh := k.object(spec)
	object.Object["spec"] = fresh.Object["spec"]
	if err := unstructured.SetNestedField(object.Object, state, "spec", "lifecycle", "desiredState"); err != nil {
		return err
	}
	_, err = client.Resource(runtimeGVR).Namespace(namespace).Update(ctx, object, metav1.UpdateOptions{})
	return err
}
func (k *KubernetesSpawner) Status(ctx context.Context, spec Spec) (Status, error) {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return Status{Phase: "Stopped"}, nil
	}
	client, _, settings, err := k.clients(ctx)
	if err != nil {
		return Status{}, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	object, err := client.Resource(runtimeGVR).Namespace(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return Status{Phase: "Stopped"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	raw, _, _ := unstructured.NestedMap(object.Object, "status")
	payload, _ := json.Marshal(raw)
	var status Status
	_ = json.Unmarshal(payload, &status)
	return status, nil
}
func (k *KubernetesSpawner) Logs(ctx context.Context, spec Spec, tail int64) ([]byte, error) {
	ensureCRDName(&spec)
	if spec.Runtime.PodName == "" {
		return []byte("Pod가 아직 시작되지 않았거나 대기 중입니다."), nil
	}
	_, client, settings, err := k.clients(ctx)
	if err != nil {
		return nil, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	if tail <= 0 {
		tail = 200
	}
	request := client.CoreV1().Pods(namespace).GetLogs(spec.Runtime.PodName, &corev1.PodLogOptions{Container: "agent", TailLines: &tail})
	return request.DoRaw(ctx)
}

func (k *KubernetesSpawner) Connection(ctx context.Context, spec Spec) (Connection, error) {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return Connection{}, errors.New("runtime is not spawned yet")
	}
	dynamicClient, coreClient, settings, err := k.clients(ctx)
	if err != nil {
		return Connection{}, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	object, err := dynamicClient.Resource(runtimeGVR).Namespace(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if err != nil {
		return Connection{}, err
	}
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	if phase != "Running" && phase != "Ready" {
		return Connection{}, fmt.Errorf("runtime is not ready (phase %s)", phase)
	}
	endpoint, _, _ := unstructured.NestedString(object.Object, "status", "endpoint")
	if endpoint == "" {
		return Connection{}, errors.New("runtime endpoint is not available")
	}
	if runtimetype.UsesGatewayProxy(spec.Agent.RuntimeType) {
		endpoint = strings.Replace(endpoint, fmt.Sprintf(":%d", runtimetype.Port(spec.Agent.RuntimeType)), fmt.Sprintf(":%d", runtimetype.GatewayPort), 1)
	}
	secret, err := coreClient.CoreV1().Secrets(namespace).Get(ctx, spec.Runtime.CRDName, metav1.GetOptions{})
	if err != nil {
		return Connection{}, err
	}
	token := string(secret.Data["runtime-token"])
	if token == "" {
		return Connection{}, errors.New("runtime access token is missing")
	}
	return Connection{Endpoint: endpoint, RuntimeType: spec.Agent.RuntimeType, Token: token}, nil
}

func (k *KubernetesSpawner) Snapshot(ctx context.Context, spec SnapshotSpec) error {
	client, _, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := spec.Namespace
	if namespace == "" {
		namespace = settings.Namespace
	}
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       "VolumeSnapshot",
		"metadata":   map[string]any{"name": spec.Name, "labels": map[string]any{"app.kubernetes.io/managed-by": "agenthub"}},
		"spec":       map[string]any{"source": map[string]any{"persistentVolumeClaimName": spec.PVCName}},
	}}
	_, err = client.Resource(volumeSnapshotGVR).Namespace(namespace).Create(ctx, object, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return snapshotSupportError(err)
}

// snapshotSupportError distinguishes "this cluster has no CSI snapshot support"
// from a real failure. The API server answers a request against a missing CRD
// with a plain 404, which is otherwise indistinguishable from a missing object.
func snapshotSupportError(err error) error {
	if err == nil {
		return nil
	}
	if meta.IsNoMatchError(err) || apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: %v", ErrSnapshotsUnsupported, err)
	}
	return err
}

func (k *KubernetesSpawner) SnapshotStatus(ctx context.Context, spec SnapshotSpec) (string, int64, error) {
	client, _, settings, err := k.clients(ctx)
	if err != nil {
		return "", 0, err
	}
	namespace := spec.Namespace
	if namespace == "" {
		namespace = settings.Namespace
	}
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	object, err := client.Resource(volumeSnapshotGVR).Namespace(namespace).Get(ctx, spec.Name, metav1.GetOptions{})
	if err != nil {
		return "", 0, snapshotSupportError(err)
	}
	ready, _, _ := unstructured.NestedBool(object.Object, "status", "readyToUse")
	if !ready {
		return "provisioning", 0, nil
	}
	sizeText, _, _ := unstructured.NestedString(object.Object, "status", "restoreSize")
	quantity, quantityErr := apiresource.ParseQuantity(sizeText)
	if quantityErr != nil {
		return "ready", 0, nil
	}
	return "ready", quantity.Value(), nil
}
