package runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/exec"
	"k8s.io/client-go/util/retry"

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

// clients builds everything a call needs to reach the cluster, including the
// REST configuration itself: an exec stream is built from that rather than from
// a typed client, and rebuilding it at the call site would mean a second place
// that has to know how this deployment authenticates.
func (k *KubernetesSpawner) clients(ctx context.Context) (dynamic.Interface, kubernetes.Interface, *rest.Config, kubernetesSettings, error) {
	var settings kubernetesSettings
	if err := k.store.Setting(ctx, "kubernetes", &settings); err != nil {
		return nil, nil, nil, settings, err
	}
	if !settings.Enabled {
		return nil, nil, nil, settings, ErrNotConfigured
	}
	var config *rest.Config
	var err error
	if settings.Mode == "inCluster" || settings.Mode == "" {
		config, err = rest.InClusterConfig()
	} else {
		token, secretErr := k.store.SettingSecret(ctx, "kubernetes")
		if secretErr != nil {
			return nil, nil, nil, settings, secretErr
		}
		config = &rest.Config{Host: settings.APIServer, BearerToken: token, TLSClientConfig: rest.TLSClientConfig{Insecure: !settings.VerifyTLS}, Timeout: 15 * time.Second}
	}
	if err != nil {
		return nil, nil, nil, settings, fmt.Errorf("configure Kubernetes client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, nil, settings, err
	}
	coreClient, err := kubernetes.NewForConfig(config)
	return dynamicClient, coreClient, config, settings, err
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
		"spec": map[string]any{"owner": spec.Agent.OwnerID, "agentRef": map[string]any{"id": spec.Agent.ID, "version": int64(spec.Agent.Version)}, "runtimeRef": map[string]any{"id": spec.Runtime.ID}, "runtime": runtimeObject(spec, image), "profile": profile, "workspace": map[string]any{"type": spec.WorkspaceType, "pvcName": spec.WorkspacePVC, "sizeGb": int64(workspaceSize), "repositoryUrl": spec.WorkspaceRepositoryURL, "branch": spec.WorkspaceBranch, "snapshotName": spec.WorkspaceSnapshot, "gitCredentialKind": spec.WorkspaceGitCredentialKind, "gitCredentialUsername": spec.WorkspaceGitCredentialUsername}, "model": map[string]any{"baseUrl": spec.ModelBaseURL, "name": spec.ModelName, "secretRef": spec.Runtime.CRDName, "credentialsFingerprint": credentialFingerprint(spec)}, "mcp": bindings,
			"security":  map[string]any{"runAsNonRoot": spec.Security.RunAsNonRoot, "readOnlyRootFilesystem": spec.Security.ReadOnlyRootFilesystem, "allowPrivilegeEscalation": spec.Security.AllowPrivilegeEscalation, "automountServiceAccountToken": spec.Security.AutomountServiceAccountToken, "seccompProfile": spec.Security.SeccompProfile, "clusterRead": spec.Security.ClusterRead},
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
// credentialFingerprint identifies the credentials this runtime is meant to be
// using, without putting any of them in the CRD.
//
// The Secret is updated in place when a model key is rotated or an MCP
// credential changes, and nothing about the Pod changes with it: the values
// arrive as environment variables and files that the agent process read once at
// start. So the platform reported the rotation done while every running runtime
// went on using the credential that had just been revoked — and the new one only
// took effect whenever the Pod next happened to restart, which might be never.
//
// The fingerprint travels in the spec, the operator folds it into the Pod's
// config hash, and a changed credential rolls the Pod exactly as a changed
// setting does. Only the hash crosses the boundary; the credentials stay in the
// Secret where they belong.
func credentialFingerprint(spec Spec) string {
	data := runtimeCredentialData(spec)
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sum := sha256.New()
	sum.Write([]byte(spec.ModelAPIKey))
	for _, key := range keys {
		sum.Write([]byte{0})
		sum.Write([]byte(key))
		sum.Write([]byte{0})
		sum.Write([]byte(data[key]))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

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
	client, coreClient, _, settings, err := k.clients(ctx)
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
	sent := k.object(spec)
	stored, err := client.Resource(runtimeGVR).Namespace(namespace).Create(ctx, sent, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			return k.setDesired(ctx, spec, "Running")
		}
		if secretCreated {
			_ = coreClient.CoreV1().Secrets(namespace).Delete(ctx, spec.Runtime.CRDName, metav1.DeleteOptions{})
		}
		return err
	}
	// A pruned field is worth saying out loud, but not worth refusing to start a
	// runtime over: the agent runs, it just runs without whatever was dropped.
	k.reportPruned(spec, sent, stored)
	if credentialFingerprintPruned(spec, stored) {
		// Named separately because this one's symptom is a rotation that looks like
		// it worked, which nobody goes looking for.
		k.logger().Warn("a rotated model key or MCP credential will not reach a running runtime until the CRD is reapplied",
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
	// Whatever token the Pod actually holds is the one the control plane has to
	// recognise.
	//
	// The operator mints a runtime-token of its own when it reconciles a runtime
	// whose Secret is missing — it has to, because a CRD applied without this
	// control plane still needs one. That token is never shown to the control
	// plane, so its hash is not the one on file, and every request from that
	// Pod's gateway is answered 401: no tool approval can be asked for, no
	// content-scanner finding reported, no configuration report delivered. The
	// runtime looks healthy and its approval gate is simply off.
	//
	// Reading the token back out of the Secret settles it in whichever direction
	// the disagreement went, and costs one statement on a path that already
	// touches the database.
	if token := string(existing.Data["runtime-token"]); token != "" {
		if err := k.store.SetRuntimeGatewayToken(ctx, spec.Runtime.ID, token); err != nil {
			return err
		}
	}
	// The credentials are this platform's to write; the operator owns the same
	// Secret's metadata and writes it on every reconcile. Both used to read the
	// object and send the whole thing back, so whichever wrote second was refused
	// with "the object has been modified" — which reached whoever had just pressed
	// start. A patch names only the keys being set and cleared, carries no resource
	// version, and cannot collide with the other writer.
	data := map[string]any{}
	if spec.ModelAPIKey != "" || existing.Data["model-api-key"] == nil {
		data["model-api-key"] = []byte(spec.ModelAPIKey)
	}
	// Unbinding a server revokes its credential: a key that is no longer wanted is
	// set to null, which is how a merge patch deletes one.
	for key := range existing.Data {
		if strings.HasPrefix(key, "mcp-credential-") || key == gitCredentialKey {
			data[key] = nil
		}
	}
	for key, value := range runtimeCredentialData(spec) {
		data[key] = []byte(value)
	}
	if len(data) == 0 {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return err
	}
	_, err = coreClient.CoreV1().Secrets(namespace).Patch(ctx, spec.Runtime.CRDName, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}
func (k *KubernetesSpawner) Start(ctx context.Context, spec Spec) error {
	return k.setDesired(ctx, spec, "Running")
}
func (k *KubernetesSpawner) Stop(ctx context.Context, spec Spec) error {
	return k.setDesired(ctx, spec, "Stopped")
}

// updateRuntimeObject rewrites a runtime's object and does not give up because
// somebody else touched it first.
//
// The object has two writers. The control plane owns the spec — start, stop,
// restart, and the platform-wide environment push — and the operator owns the
// status, which it rewrites whenever a Pod changes phase. Kubernetes counts a
// status write as a change to the object, so a spec write carrying the version
// that was read a moment earlier is refused:
//
//	both read resourceVersion 2409596
//	  operator writes status: ok
//	  control plane writes the spec: 409 the object has been modified
//
// That was checked against a real API server, and it is not a rare corner: the
// environment push walks every runtime at once, and a person pressing start is
// most likely to do it while the phase is moving. A refusal cost them the whole
// action — the setting reported as failed for that runtime and silently never
// reached the Pod, or the start button returned an error.
//
// A conflict means the read was stale, nothing more, so the read is taken again
// and the change reapplied to what is there now.
func updateRuntimeObject(ctx context.Context, client dynamic.Interface, namespace, name string, apply func(*unstructured.Unstructured) error) (*unstructured.Unstructured, error) {
	var stored *unstructured.Unstructured
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		object, err := client.Resource(runtimeGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if err := apply(object); err != nil {
			return err
		}
		stored, err = client.Resource(runtimeGVR).Namespace(namespace).Update(ctx, object, metav1.UpdateOptions{})
		return err
	})
	return stored, err
}

func (k *KubernetesSpawner) Restart(ctx context.Context, spec Spec) error {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return errors.New("resource name may not be empty")
	}
	client, coreClient, _, settings, err := k.clients(ctx)
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
	_, err = updateRuntimeObject(ctx, client, namespace, spec.Runtime.CRDName, func(object *unstructured.Unstructured) error {
		fresh := k.object(spec)
		object.Object["spec"] = fresh.Object["spec"]
		if object.GetAnnotations() == nil {
			object.SetAnnotations(map[string]string{})
		}
		annotations := object.GetAnnotations()
		annotations["agenthub.io/restarted-at"] = time.Now().UTC().Format(time.RFC3339Nano)
		object.SetAnnotations(annotations)
		return nil
	})
	if apierrors.IsNotFound(err) {
		return k.Spawn(ctx, spec)
	}
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
	client, _, _, settings, err := k.clients(ctx)
	if err != nil {
		return err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	stored, err := updateRuntimeObject(ctx, client, namespace, spec.Runtime.CRDName, func(object *unstructured.Unstructured) error {
		return syncSpec(object, k.object(spec))
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	sent := k.object(spec)
	k.reportPruned(spec, sent, stored)
	// The API server prunes fields a CRD's schema does not declare, without
	// saying so. Checking what came back costs nothing — Update already returns
	// the stored object — and it is the difference between "the cluster is a
	// version behind" and "this feature does not work".
	if provisioningPruned(spec, stored) {
		return ErrProvisioningUnsupported
	}
	return nil
}

// credentialFingerprintPruned reports that this cluster's CRD predates the
// credential fingerprint, so the field was dropped on the way in.
//
// It matters more than the other pruned fields, because the symptom is not a
// missing feature: a rotation appears to succeed and the running runtime keeps
// the revoked credential. An operator who upgrades the control plane without
// reapplying the CRD deserves to be told that in words rather than to find out
// from a provider's audit log.
func credentialFingerprintPruned(spec Spec, stored *unstructured.Unstructured) bool {
	if credentialFingerprint(spec) == "" || stored == nil {
		return false
	}
	value, found, err := unstructured.NestedString(stored.Object, "spec", "model", "credentialsFingerprint")
	return err == nil && (!found || value == "")
}

// prunedFields lists everything the control plane wrote that did not come back.
//
// The API server removes a field the CRD does not declare, silently, on a write
// that otherwise succeeds. Two of those had already been found the hard way — the
// runtime environment, and then the credential fingerprint, where the symptom was
// a key rotation that appeared to work while every running runtime kept the
// revoked credential — and each was answered with a check for that one field.
//
// This is the rule those two were examples of. What came back is compared with
// what was sent, so a field added later is covered on the day it is added rather
// than after somebody spends an afternoon on why a setting does nothing.
func prunedFields(sent, stored *unstructured.Unstructured) []string {
	if sent == nil || stored == nil {
		return nil
	}
	var missing []string
	comparePrunedPaths(sent.Object["spec"], stored.Object["spec"], "spec", &missing)
	sort.Strings(missing)
	return missing
}

// comparePrunedPaths walks what was sent and records the leaves that did not come
// back. Only absence counts: the API server may add defaults of its own, and a
// value it normalised is still a value it kept.
func comparePrunedPaths(sent, stored any, path string, missing *[]string) {
	switch value := sent.(type) {
	case map[string]any:
		storedMap, ok := stored.(map[string]any)
		if !ok {
			*missing = append(*missing, path)
			return
		}
		for key, child := range value {
			next, found := storedMap[key]
			if !found {
				*missing = append(*missing, path+"."+key)
				continue
			}
			comparePrunedPaths(child, next, path+"."+key, missing)
		}
	case []any:
		storedList, ok := stored.([]any)
		if !ok || len(storedList) != len(value) {
			*missing = append(*missing, path)
			return
		}
		for index, child := range value {
			comparePrunedPaths(child, storedList[index], path+"[]", missing)
		}
	}
}

// reportPruned says what this cluster's CRD dropped, once per write, naming the
// paths and the file that fixes it.
func (k *KubernetesSpawner) reportPruned(spec Spec, sent, stored *unstructured.Unstructured) {
	missing := prunedFields(sent, stored)
	if len(missing) == 0 {
		return
	}
	k.logger().Warn("this cluster's AgentRuntime CRD dropped part of what the platform wrote; apply deploy/kubernetes/crd.yaml — whatever these fields configure is being ignored",
		"runtime", spec.Runtime.CRDName, "fields", strings.Join(missing, ", "))
}

// provisioningPruned reports whether the environment this spec carries survived
// being written. It stays as its own check because a sync refuses over it rather
// than only warning.
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
	client, _, _, settings, err := k.clients(ctx)
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
	client, coreClient, _, settings, err := k.clients(ctx)
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
	_, err = updateRuntimeObject(ctx, client, namespace, spec.Runtime.CRDName, func(object *unstructured.Unstructured) error {
		fresh := k.object(spec)
		object.Object["spec"] = fresh.Object["spec"]
		return unstructured.SetNestedField(object.Object, state, "spec", "lifecycle", "desiredState")
	})
	if apierrors.IsNotFound(err) {
		if state == "Running" {
			return k.Spawn(ctx, spec)
		}
		return nil
	}
	return err
}
func (k *KubernetesSpawner) Status(ctx context.Context, spec Spec) (Status, error) {
	ensureCRDName(&spec)
	if spec.Runtime.CRDName == "" {
		return Status{Phase: "Stopped"}, nil
	}
	client, _, _, settings, err := k.clients(ctx)
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

// StatusAll reads every runtime's status in the namespace at once.
//
// The console asks for the agent list constantly, and answering it used to cost
// one settings read, one client construction and one API request per agent —
// thirty agents meant ninety round trips to render one table. The information is
// the same either way: these objects all live in one namespace and the API server
// will list them in a single call.
func (k *KubernetesSpawner) StatusAll(ctx context.Context) (map[string]Status, error) {
	client, _, _, settings, err := k.clients(ctx)
	if err != nil {
		return nil, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	list, err := client.Resource(runtimeGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Status, len(list.Items))
	for _, object := range list.Items {
		raw, _, _ := unstructured.NestedMap(object.Object, "status")
		payload, _ := json.Marshal(raw)
		var status Status
		_ = json.Unmarshal(payload, &status)
		out[object.GetName()] = status
	}
	return out, nil
}

func (k *KubernetesSpawner) Logs(ctx context.Context, spec Spec, tail int64) ([]byte, error) {
	ensureCRDName(&spec)
	if spec.Runtime.PodName == "" {
		return []byte("Pod가 아직 시작되지 않았거나 대기 중입니다."), nil
	}
	_, client, _, settings, err := k.clients(ctx)
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
	dynamicClient, coreClient, _, settings, err := k.clients(ctx)
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
	client, _, _, settings, err := k.clients(ctx)
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
// snapshotSupportError separates two answers a 404 can carry.
//
// A cluster with no snapshot CRD and a snapshot somebody deleted both come back
// as "not found", and both used to be reported as the cluster lacking support —
// which sends an operator to install something already installed while the real
// news is that what they were about to restore from is gone.
//
// They are told apart by what the answer names. Kubernetes reports a missing
// object with its name in the status details; a missing resource type has no
// object to name.
func snapshotSupportError(err error) error {
	if err == nil {
		return nil
	}
	if meta.IsNoMatchError(err) {
		return fmt.Errorf("%w: %v", ErrSnapshotsUnsupported, err)
	}
	if apierrors.IsNotFound(err) {
		if named(err) {
			return fmt.Errorf("%w: %v", ErrSnapshotMissing, err)
		}
		return fmt.Errorf("%w: %v", ErrSnapshotsUnsupported, err)
	}
	return err
}

// named says whether a Kubernetes error is about one object rather than a whole
// resource type.
func named(err error) bool {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	details := status.Status().Details
	return details != nil && details.Name != ""
}

func (k *KubernetesSpawner) SnapshotStatus(ctx context.Context, spec SnapshotSpec) (string, int64, error) {
	client, _, _, settings, err := k.clients(ctx)
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

// Exec runs a command inside the agent container of a running runtime.
//
// This is how an autonomous task drives a runtime whose agent is a terminal
// program rather than a server: Qwen Code has no API to call, it has a command
// line, and the platform reaches it the same way a person with kubectl would.
//
// It is deliberately narrow. The container is always the agent's, the command
// comes from the platform rather than from anything a model produced, and the
// output is captured rather than streamed to a terminal — a task's evidence has
// to end up in the run record, not on somebody's screen.
func (k *KubernetesSpawner) Exec(ctx context.Context, spec Spec, request ExecRequest) (ExecResult, error) {
	ensureCRDName(&spec)
	if spec.Runtime.PodName == "" {
		return ExecResult{}, errors.New("runtime pod is not known yet")
	}
	if len(request.Command) == 0 {
		return ExecResult{}, errors.New("no command to run")
	}
	_, coreClient, config, settings, err := k.clients(ctx)
	if err != nil {
		return ExecResult{}, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	// The shared configuration carries a 15 second timeout, which is right for an
	// API call and wrong for a command that may run for minutes. The deadline for
	// this one belongs to the caller's context.
	streamConfig := rest.CopyConfig(config)
	streamConfig.Timeout = 0

	container := request.Container
	if container == "" {
		container = "agent"
	}
	options := &corev1.PodExecOptions{
		Container: container,
		Command:   request.Command,
		Stdin:     request.Stdin != "",
		Stdout:    true,
		Stderr:    true,
	}
	url := coreClient.CoreV1().RESTClient().Post().
		Resource("pods").Name(spec.Runtime.PodName).Namespace(namespace).SubResource("exec").
		VersionedParams(options, scheme.ParameterCodec).URL()

	executor, err := remotecommand.NewSPDYExecutor(streamConfig, http.MethodPost, url)
	if err != nil {
		return ExecResult{}, err
	}
	var stdout, stderr bytes.Buffer
	streams := remotecommand.StreamOptions{
		Stdout: &limitedWriter{limit: execOutputLimit, buffer: &stdout},
		Stderr: &limitedWriter{limit: execOutputLimit, buffer: &stderr},
	}
	if request.Stdin != "" {
		streams.Stdin = strings.NewReader(request.Stdin)
	}
	err = executor.StreamWithContext(ctx, streams)
	result := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	// A non-zero exit is the command's answer, not a failure to run it: the CLI
	// agents this drives use exit codes to say which guardrail stopped them, and
	// collapsing that into an error would throw the distinction away.
	var exitErr exec.CodeExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.Code
		return result, nil
	}
	return result, err
}

// execOutputLimit bounds what one command may return. A CLI agent's JSON answer
// is kilobytes; anything approaching this is a runaway that would otherwise land
// in the worker's memory and then in the run record.
const execOutputLimit = 4 << 20

// limitedWriter keeps the first execOutputLimit bytes and silently drops the
// rest, so a flood cannot end the stream early — the exit code still matters.
type limitedWriter struct {
	limit  int
	buffer *bytes.Buffer
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if remaining := w.limit - w.buffer.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buffer.Write(p[:remaining])
		} else {
			w.buffer.Write(p)
		}
	}
	return len(p), nil
}

// ExecStream opens a command inside the agent container and hands back its pipes.
//
// The one-shot Exec is enough for a command that takes a prompt and prints an
// answer. A protocol is not that: the Agent Client Protocol is a JSON-RPC
// conversation over stdin and stdout, where the agent asks the platform for
// permission mid-turn and the platform answers before the turn can continue. That
// needs both directions open at once, which is what this is for.
func (k *KubernetesSpawner) ExecStream(ctx context.Context, spec Spec, request ExecRequest) (*Session, error) {
	ensureCRDName(&spec)
	if spec.Runtime.PodName == "" {
		return nil, errors.New("runtime pod is not known yet")
	}
	if len(request.Command) == 0 {
		return nil, errors.New("no command to run")
	}
	_, coreClient, config, settings, err := k.clients(ctx)
	if err != nil {
		return nil, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	streamConfig := rest.CopyConfig(config)
	streamConfig.Timeout = 0

	container := request.Container
	if container == "" {
		container = "agent"
	}
	options := &corev1.PodExecOptions{
		Container: container, Command: request.Command,
		Stdin: true, Stdout: true, Stderr: true,
	}
	url := coreClient.CoreV1().RESTClient().Post().
		Resource("pods").Name(spec.Runtime.PodName).Namespace(namespace).SubResource("exec").
		VersionedParams(options, scheme.ParameterCodec).URL()
	executor, err := remotecommand.NewSPDYExecutor(streamConfig, http.MethodPost, url)
	if err != nil {
		return nil, err
	}

	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	var stderr bytes.Buffer
	session := &Session{
		Stdin: inWriter, Stdout: outReader,
		stderr: &stderr,
		done:   make(chan error, 1),
		cancel: func() { _ = inWriter.Close() },
	}
	go func() {
		streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin: inReader, Stdout: outWriter, Stderr: &limitedWriter{limit: execOutputLimit, buffer: &stderr},
		})
		// Closing the read end is what tells the client the conversation is over;
		// without it a caller waiting for the next message waits forever.
		_ = outWriter.CloseWithError(io.EOF)
		var exitErr exec.CodeExitError
		if errors.As(streamErr, &exitErr) && exitErr.Code == 0 {
			streamErr = nil
		}
		session.done <- streamErr
		close(session.done)
	}()
	return session, nil
}
