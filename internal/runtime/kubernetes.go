package runtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

type KubernetesSpawner struct{ store *store.Store }

func NewKubernetesSpawner(db *store.Store) *KubernetesSpawner { return &KubernetesSpawner{store: db} }

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
		if m.ToolPolicyMode != "" || m.ApprovalAll || len(m.ApprovalTools) > 0 {
			tools := make([]any, 0, len(m.ToolPolicyTools))
			for _, tool := range m.ToolPolicyTools {
				tools = append(tools, tool)
			}
			approval := make([]any, 0, len(m.ApprovalTools))
			for _, tool := range m.ApprovalTools {
				approval = append(approval, tool)
			}
			policy := map[string]any{"tools": tools, "approvalTools": approval, "approvalRequired": m.ApprovalAll}
			if m.ToolPolicyMode != "" {
				policy["mode"] = m.ToolPolicyMode
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
		image = DefaultBaseImage()
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
	return &unstructured.Unstructured{Object: object}
}

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
	if _, err = client.Resource(runtimeGVR).Namespace(namespace).Create(ctx, k.object(spec), metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return k.setDesired(ctx, spec, "Running")
		}
		if secretCreated {
			_ = coreClient.CoreV1().Secrets(namespace).Delete(ctx, spec.Runtime.CRDName, metav1.DeleteOptions{})
		}
		return err
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
