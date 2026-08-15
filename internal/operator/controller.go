package operator

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var runtimeGVR = schema.GroupVersionResource{Group: "agenthub.io", Version: "v1alpha1", Resource: "agentruntimes"}

type Controller struct {
	dynamic dynamic.Interface
	client  kubernetes.Interface
	logger  *slog.Logger
}

func New(config *rest.Config, logger *slog.Logger) (*Controller, error) {
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Controller{dynamic: dynamicClient, client: client, logger: logger}, nil
}

func (c *Controller) Run(ctx context.Context) error {
	c.logger.Info("Agent Operator started")
	go c.periodicReconcile(ctx)
	for ctx.Err() == nil {
		watcher, err := c.dynamic.Resource(runtimeGVR).Namespace(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{})
		if err != nil {
			c.logger.Error("watch AgentRuntime", "error", err)
			if !wait(ctx, 5*time.Second) {
				return ctx.Err()
			}
			continue
		}
		if err := c.consume(ctx, watcher); err != nil && !errors.Is(err, context.Canceled) {
			c.logger.Warn("AgentRuntime watch restarted", "error", err)
		}
	}
	return ctx.Err()
}

func (c *Controller) periodicReconcile(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			items, err := c.dynamic.Resource(runtimeGVR).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
			if err != nil {
				c.logger.Warn("list AgentRuntime resources", "error", err)
				continue
			}
			for i := range items.Items {
				if err := c.Reconcile(ctx, &items.Items[i]); err != nil {
					c.logger.Warn("periodic reconcile failed", "name", items.Items[i].GetName(), "error", err)
				}
			}
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Controller) consume(ctx context.Context, watcher watch.Interface) error {
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return errors.New("watch channel closed")
			}
			if event.Type == watch.Deleted {
				continue
			}
			object, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if err := c.Reconcile(ctx, object); err != nil {
				c.logger.Error("reconcile AgentRuntime", "name", object.GetName(), "namespace", object.GetNamespace(), "error", err)
				_ = c.updateStatus(ctx, object, "Failed", "", "", 0, err.Error())
			}
		}
	}
}

type spec struct {
	Owner   string `json:"owner"`
	Runtime struct {
		Type  string `json:"type"`
		Image string `json:"image"`
	} `json:"runtime"`
	Profile struct {
		CPUMillis          int64 `json:"cpuMillis"`
		MemoryMB           int64 `json:"memoryMb"`
		StorageGB          int64 `json:"storageGb"`
		GPUCount           int64 `json:"gpuCount"`
		IdleTimeoutSeconds int64 `json:"idleTimeoutSeconds"`
	} `json:"profile"`
	Workspace struct {
		Type          string `json:"type"`
		PVCName       string `json:"pvcName"`
		SizeGB        int64  `json:"sizeGb"`
		RepositoryURL string `json:"repositoryUrl"`
		Branch        string `json:"branch"`
		SnapshotName  string `json:"snapshotName"`
	} `json:"workspace"`
	Model struct {
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		SecretRef string `json:"secretRef"`
	} `json:"model"`
	MCP []struct {
		Name     string `json:"name"`
		Mode     string `json:"mode"`
		Endpoint string `json:"endpoint"`
		Image    string `json:"image"`
		Port     int32  `json:"port"`
	} `json:"mcp"`
	Security struct {
		RunAsNonRoot                 bool   `json:"runAsNonRoot"`
		ReadOnlyRootFilesystem       bool   `json:"readOnlyRootFilesystem"`
		AllowPrivilegeEscalation     bool   `json:"allowPrivilegeEscalation"`
		AutomountServiceAccountToken bool   `json:"automountServiceAccountToken"`
		SeccompProfile               string `json:"seccompProfile"`
	} `json:"security"`
	Network struct {
		DefaultDeny         bool     `json:"defaultDeny"`
		AllowDNS            bool     `json:"allowDNS"`
		AllowedDestinations []string `json:"allowedDestinations"`
	} `json:"network"`
	Lifecycle struct {
		DesiredState       string `json:"desiredState"`
		AutoRestart        bool   `json:"autoRestart"`
		IdleTimeoutSeconds int64  `json:"idleTimeoutSeconds"`
	} `json:"lifecycle"`
}

func parseSpec(object *unstructured.Unstructured) (spec, error) {
	raw, found, err := unstructured.NestedMap(object.Object, "spec")
	if err != nil || !found {
		return spec{}, errors.New("spec is required")
	}
	data, _ := json.Marshal(raw)
	var value spec
	if err := json.Unmarshal(data, &value); err != nil {
		return spec{}, err
	}
	if value.Runtime.Type != "opencode" && value.Runtime.Type != "hermes" && value.Runtime.Type != "custom" {
		return spec{}, fmt.Errorf("unsupported runtime type %q", value.Runtime.Type)
	}
	if value.Runtime.Image == "" {
		return spec{}, errors.New("runtime image is required")
	}
	for _, item := range value.MCP {
		if item.Name == "" || (item.Mode != "shared" && item.Mode != "sidecar" && item.Mode != "dedicated") {
			return spec{}, errors.New("each MCP binding requires a name and valid mode")
		}
		if item.Mode == "shared" && item.Endpoint == "" {
			return spec{}, fmt.Errorf("shared MCP %q requires an endpoint", item.Name)
		}
		if item.Mode != "shared" && item.Image == "" {
			return spec{}, fmt.Errorf("%s MCP %q requires an image", item.Mode, item.Name)
		}
	}
	return value, nil
}

func mcpResourceName(runtimeName, serverName string) string {
	suffix := safeLabel(serverName)
	if len(suffix) > 24 {
		suffix = suffix[:24]
	}
	prefix := runtimeName
	if len(prefix) > 35 {
		prefix = prefix[:35]
	}
	return strings.Trim(prefix+"-mcp-"+suffix, "-.")
}

func mcpContainerName(serverName string) string {
	name := "mcp-" + safeLabel(serverName)
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.Trim(name, "-.")
}

func effectiveMCP(ns, runtimeName string, value spec) []map[string]any {
	result := make([]map[string]any, 0, len(value.MCP))
	sidecarIndex := int32(0)
	for _, item := range value.MCP {
		port := item.Port
		if port <= 0 {
			port = 8000
		}
		endpoint := item.Endpoint
		switch item.Mode {
		case "sidecar":
			endpoint = fmt.Sprintf("http://127.0.0.1:%d/mcp", port+sidecarIndex)
			sidecarIndex++
		case "dedicated":
			endpoint = fmt.Sprintf("http://%s.%s.svc:%d/mcp", mcpResourceName(runtimeName, item.Name), ns, port)
		}
		result = append(result, map[string]any{"name": item.Name, "mode": item.Mode, "endpoint": endpoint, "image": item.Image, "port": port})
	}
	return result
}

func runtimeConfigs(ns, runtimeName string, value spec) (string, string, string) {
	bindings := effectiveMCP(ns, runtimeName, value)
	runtimeValue := map[string]any{"owner": value.Owner, "runtime": value.Runtime, "profile": value.Profile, "workspace": value.Workspace, "model": value.Model, "mcp": bindings, "lifecycle": value.Lifecycle}
	runtimeRaw, _ := json.MarshalIndent(runtimeValue, "", "  ")
	opencode := map[string]any{"$schema": "https://opencode.ai/config.json", "autoupdate": false, "mcp": map[string]any{}}
	hermes := map[string]any{"terminal": map[string]any{"cwd": "/workspace", "home_mode": "profile"}, "mcp_servers": map[string]any{}}
	if value.Model.BaseURL != "" && value.Model.Name != "" {
		opencode["model"] = "agenthub/" + value.Model.Name
		opencode["provider"] = map[string]any{"agenthub": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "AgentHub Model Gateway", "options": map[string]any{"baseURL": value.Model.BaseURL, "apiKey": "{env:OPENAI_API_KEY}"}, "models": map[string]any{value.Model.Name: map[string]any{"name": value.Model.Name}}}}
		hermes["model"] = map[string]any{"provider": "custom", "default": value.Model.Name, "base_url": value.Model.BaseURL, "api_key": "${OPENAI_API_KEY}"}
	}
	openMCP := opencode["mcp"].(map[string]any)
	hermesMCP := hermes["mcp_servers"].(map[string]any)
	for _, item := range bindings {
		name, endpoint := safeLabel(fmt.Sprint(item["name"])), fmt.Sprint(item["endpoint"])
		if endpoint == "" {
			continue
		}
		openMCP[name] = map[string]any{"type": "remote", "url": endpoint, "enabled": true, "oauth": false}
		hermesMCP[name] = map[string]any{"url": endpoint, "enabled": true, "timeout": 120, "connect_timeout": 60}
	}
	openRaw, _ := json.MarshalIndent(opencode, "", "  ")
	hermesRaw, _ := json.MarshalIndent(hermes, "", "  ")
	return string(runtimeRaw), string(openRaw), string(hermesRaw)
}

func (c *Controller) Reconcile(ctx context.Context, object *unstructured.Unstructured) error {
	value, err := parseSpec(object)
	if err != nil {
		return err
	}
	namespace, name := object.GetNamespace(), object.GetName()
	if namespace == "" {
		return errors.New("namespace is required")
	}
	if strings.EqualFold(value.Lifecycle.DesiredState, "Stopped") {
		zero := int32(0)
		deployment, err := c.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			deployment.Spec.Replicas = &zero
			_, err = c.client.AppsV1().StatefulSets(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := c.scaleDedicatedMCPs(ctx, namespace, name, 0); err != nil {
			return err
		}
		return c.updateStatus(ctx, object, "Stopped", "", "", 0, "")
	}
	if err := c.ensureServiceAccount(ctx, namespace, name, object); err != nil {
		return err
	}
	if err := c.ensureSecret(ctx, namespace, name, object); err != nil {
		return err
	}
	if err := c.ensureConfigMap(ctx, namespace, name, value, object); err != nil {
		return err
	}
	pvcName := value.Workspace.PVCName
	if pvcName == "" {
		pvcName = name + "-workspace"
	}
	if err := c.ensurePVC(ctx, namespace, pvcName, value, object); err != nil {
		return err
	}
	if err := c.ensureService(ctx, namespace, name, value, object); err != nil {
		return err
	}
	if err := c.ensureNetworkPolicy(ctx, namespace, name, value, object); err != nil {
		return err
	}
	if err := c.ensureDedicatedMCPs(ctx, namespace, name, value, object); err != nil {
		return err
	}
	if err := c.ensureStatefulSet(ctx, namespace, name, pvcName, value, object); err != nil {
		return err
	}
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: fields.Set{"agenthub.io/runtime": name}.AsSelector().String()})
	if err != nil {
		return err
	}
	phase, podName, nodeName, restartCount := "Starting", "", "", int32(0)
	if len(pods.Items) > 0 {
		pod := pods.Items[0]
		podName = pod.Name
		nodeName = pod.Spec.NodeName
		for _, status := range pod.Status.ContainerStatuses {
			restartCount += status.RestartCount
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			phase = "Running"
		case corev1.PodFailed:
			phase = "Failed"
		case corev1.PodPending:
			phase = "Starting"
		}
	}
	return c.updateStatus(ctx, object, phase, podName, nodeName, restartCount, "")
}

func labels(name string, mapLabels map[string]string) map[string]string {
	result := map[string]string{"app.kubernetes.io/name": "agent-runtime", "app.kubernetes.io/managed-by": "agenthub-operator", "agenthub.io/runtime": name}
	for k, v := range mapLabels {
		result[k] = v
	}
	return result
}
func ownerRef(object *unstructured.Unstructured) []metav1.OwnerReference {
	controller, block := true, true
	return []metav1.OwnerReference{{APIVersion: object.GetAPIVersion(), Kind: object.GetKind(), Name: object.GetName(), UID: object.GetUID(), Controller: &controller, BlockOwnerDeletion: &block}}
}
func ptr[T any](value T) *T { return &value }

func (c *Controller) ensureServiceAccount(ctx context.Context, ns, name string, owner *unstructured.Unstructured) error {
	desired := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, AutomountServiceAccountToken: ptr(false)}
	_, err := c.client.CoreV1().ServiceAccounts(ns).Create(ctx, desired, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
func (c *Controller) ensureConfigMap(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	runtimeConfig, opencodeConfig, hermesConfig := runtimeConfigs(ns, name, value)
	desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Data: map[string]string{"runtime.json": runtimeConfig, "opencode.json": opencodeConfig, "hermes-config.yaml": hermesConfig}}
	existing, err := c.client.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.CoreV1().ConfigMaps(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	_, err = c.client.CoreV1().ConfigMaps(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}
func (c *Controller) ensureSecret(ctx context.Context, ns, name string, owner *unstructured.Unstructured) error {
	existing, err := c.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		existing.OwnerReferences = ownerRef(owner)
		existing.Labels = labels(name, nil)
		_, err = c.client.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
		return err
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	tokenText := base64.RawURLEncoding.EncodeToString(token)
	desired := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Type: corev1.SecretTypeOpaque, StringData: map[string]string{"runtime-token": tokenText}}
	_, err = c.client.CoreV1().Secrets(ns).Create(ctx, desired, metav1.CreateOptions{})
	return err
}
func (c *Controller) ensurePVC(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	_, err := c.client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	size := value.Workspace.SizeGB
	if size <= 0 {
		size = 10
	}
	desired := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(owner.GetName(), map[string]string{"agenthub.io/preserve": "true"})}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: apiresource.MustParse(strconv.FormatInt(size, 10) + "Gi")}}}}
	if value.Workspace.SnapshotName != "" {
		apiGroup := "snapshot.storage.k8s.io"
		desired.Spec.DataSource = &corev1.TypedLocalObjectReference{APIGroup: &apiGroup, Kind: "VolumeSnapshot", Name: value.Workspace.SnapshotName}
	}
	_, err = c.client.CoreV1().PersistentVolumeClaims(ns).Create(ctx, desired, metav1.CreateOptions{})
	return err
}
func runtimePort(runtimeType string) int32 {
	if runtimeType == "hermes" {
		return 8642
	}
	return 4096
}
func (c *Controller) ensureService(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	port := runtimePort(value.Runtime.Type)
	ports := []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstrFromInt32(port)}}
	if value.Runtime.Type == "hermes" {
		ports = append(ports, corev1.ServicePort{Name: "dashboard", Port: 9119, TargetPort: intstrFromInt32(9119)})
	}
	desired := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Spec: corev1.ServiceSpec{Selector: map[string]string{"agenthub.io/runtime": name}, Ports: ports}}
	existing, err := c.client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.CoreV1().Services(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	desired.Spec.ClusterIP = existing.Spec.ClusterIP
	desired.Spec.ClusterIPs = existing.Spec.ClusterIPs
	desired.Spec.IPFamilies = existing.Spec.IPFamilies
	desired.Spec.IPFamilyPolicy = existing.Spec.IPFamilyPolicy
	_, err = c.client.CoreV1().Services(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}
func intstrFromInt32(value int32) intstr.IntOrString { return intstr.FromInt32(value) }

func restrictedContainerSecurityContext(readOnly bool) *corev1.SecurityContext {
	nonRoot, allowPrivilegeEscalation := true, false
	return &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, ReadOnlyRootFilesystem: &readOnly, RunAsNonRoot: &nonRoot, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}

func sidecarContainers(value spec) []corev1.Container {
	containers := []corev1.Container{}
	index := int32(0)
	for _, item := range value.MCP {
		if item.Mode != "sidecar" {
			continue
		}
		port := item.Port
		if port <= 0 {
			port = 8000
		}
		port += index
		index++
		containers = append(containers, corev1.Container{
			Name:            mcpContainerName(item.Name),
			Image:           item.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports:           []corev1.ContainerPort{{ContainerPort: port}},
			Env: []corev1.EnvVar{
				{Name: "MCP_TRANSPORT", Value: "streamable-http"},
				{Name: "MCP_HOST", Value: "0.0.0.0"},
				{Name: "MCP_PORT", Value: strconv.FormatInt(int64(port), 10)},
				{Name: "PORT", Value: strconv.FormatInt(int64(port), 10)},
				{Name: "WORKSPACE", Value: "/workspace"},
			},
			Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("500m"), corev1.ResourceMemory: apiresource.MustParse("512Mi")}},
			SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem),
			VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}},
		})
	}
	return containers
}

func (c *Controller) ensureNetworkPolicy(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	if !value.Network.DefaultDeny {
		err := c.client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	port := intstr.FromInt32(runtimePort(value.Runtime.Type))
	ingressPorts := []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: &port}}
	if value.Runtime.Type == "hermes" {
		dashboardPort := intstr.FromInt32(9119)
		ingressPorts = append(ingressPorts, networkingv1.NetworkPolicyPort{Protocol: ptr(corev1.ProtocolTCP), Port: &dashboardPort})
	}
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	egress := []networkingv1.NetworkPolicyEgressRule{}
	if value.Network.AllowDNS {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: ptr(intstr.FromInt32(53))}, {Protocol: &tcp, Port: ptr(intstr.FromInt32(53))}}})
	}
	allowedPorts := map[int32]bool{}
	if p := endpointPort(value.Model.BaseURL); p > 0 {
		allowedPorts[p] = true
	}
	if p := endpointPort(value.Workspace.RepositoryURL); p > 0 {
		allowedPorts[p] = true
	}
	for _, item := range effectiveMCP(ns, name, value) {
		if p := endpointPort(fmt.Sprint(item["endpoint"])); p > 0 {
			allowedPorts[p] = true
		}
	}
	for allowed := range allowedPorts {
		p := intstr.FromInt32(allowed)
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &p}}})
	}
	for _, destination := range value.Network.AllowedDestinations {
		host, portText, err := net.SplitHostPort(destination)
		if err != nil {
			continue
		}
		host = strings.Trim(host, "[]")
		if _, _, err := net.ParseCIDR(host); err != nil {
			continue
		}
		portNumber, err := strconv.ParseInt(portText, 10, 32)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			continue
		}
		allowedPort := intstr.FromInt32(int32(portNumber))
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: host}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &allowedPort}}})
	}
	controlPlane := metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "agent-platform-system"}}
	desired := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Spec: networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/runtime": name}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &controlPlane}}, Ports: ingressPorts}},
		Egress:  egress,
	}}
	existing, err := c.client.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.NetworkingV1().NetworkPolicies(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	_, err = c.client.NetworkingV1().NetworkPolicies(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func endpointPort(raw string) int32 {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return 0
	}
	if rawPort := parsed.Port(); rawPort != "" {
		port, err := strconv.ParseInt(rawPort, 10, 32)
		if err == nil && port > 0 && port < 65536 {
			return int32(port)
		}
	}
	if parsed.Scheme == "https" {
		return 443
	}
	if parsed.Scheme == "http" {
		return 80
	}
	return 0
}

func dedicatedLabels(runtimeName, resourceName string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "agenthub-mcp", "app.kubernetes.io/managed-by": "agenthub-operator", "agenthub.io/parent-runtime": runtimeName, "agenthub.io/mcp": resourceName}
}

func (c *Controller) ensureDedicatedMCPs(ctx context.Context, ns, runtimeName string, value spec, owner *unstructured.Unstructured) error {
	desiredNames := map[string]bool{}
	for _, item := range value.MCP {
		if item.Mode != "dedicated" {
			continue
		}
		name := mcpResourceName(runtimeName, item.Name)
		desiredNames[name] = true
		port := item.Port
		if port <= 0 {
			port = 8000
		}
		if err := c.ensureDedicatedMCPService(ctx, ns, runtimeName, name, port, owner); err != nil {
			return err
		}
		if err := c.ensureDedicatedMCPStatefulSet(ctx, ns, runtimeName, name, port, item.Image, owner); err != nil {
			return err
		}
		if err := c.ensureDedicatedMCPNetworkPolicy(ctx, ns, runtimeName, name, port, owner); err != nil {
			return err
		}
	}
	selector := "agenthub.io/parent-runtime=" + runtimeName
	sets, err := c.client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for _, item := range sets.Items {
		if !desiredNames[item.Name] {
			if err := c.client.AppsV1().StatefulSets(ns).Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	services, err := c.client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for _, item := range services.Items {
		if !desiredNames[item.Name] {
			if err := c.client.CoreV1().Services(ns).Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	policies, err := c.client.NetworkingV1().NetworkPolicies(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	for _, item := range policies.Items {
		if !desiredNames[item.Name] {
			if err := c.client.NetworkingV1().NetworkPolicies(ns).Delete(ctx, item.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) ensureDedicatedMCPService(ctx context.Context, ns, runtimeName, name string, port int32, owner *unstructured.Unstructured) error {
	desired := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: dedicatedLabels(runtimeName, name), OwnerReferences: ownerRef(owner)}, Spec: corev1.ServiceSpec{Selector: map[string]string{"agenthub.io/mcp": name}, Ports: []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port)}}}}
	existing, err := c.client.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.CoreV1().Services(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	desired.Spec.ClusterIP = existing.Spec.ClusterIP
	desired.Spec.ClusterIPs = existing.Spec.ClusterIPs
	desired.Spec.IPFamilies = existing.Spec.IPFamilies
	desired.Spec.IPFamilyPolicy = existing.Spec.IPFamilyPolicy
	_, err = c.client.CoreV1().Services(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Controller) ensureDedicatedMCPStatefulSet(ctx context.Context, ns, runtimeName, name string, port int32, image string, owner *unstructured.Unstructured) error {
	replicas, runAs, nonRoot := int32(1), int64(10000), true
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("250m"), corev1.ResourceMemory: apiresource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("1000m"), corev1.ResourceMemory: apiresource.MustParse("1Gi")}}
	desired := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: dedicatedLabels(runtimeName, name), OwnerReferences: ownerRef(owner)}, Spec: appsv1.StatefulSetSpec{ServiceName: name, Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/mcp": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: dedicatedLabels(runtimeName, name)}, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &runAs, RunAsGroup: &runAs, FSGroup: &runAs, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "mcp", Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}}, Env: []corev1.EnvVar{{Name: "MCP_TRANSPORT", Value: "streamable-http"}, {Name: "MCP_HOST", Value: "0.0.0.0"}, {Name: "MCP_PORT", Value: strconv.FormatInt(int64(port), 10)}, {Name: "PORT", Value: strconv.FormatInt(int64(port), 10)}}, Resources: resources, SecurityContext: restrictedContainerSecurityContext(true), VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}}}}, Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}}}}
	existing, err := c.client.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.AppsV1().StatefulSets(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	_, err = c.client.AppsV1().StatefulSets(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Controller) ensureDedicatedMCPNetworkPolicy(ctx context.Context, ns, runtimeName, name string, port int32, owner *unstructured.Unstructured) error {
	tcp, udp, portValue := corev1.ProtocolTCP, corev1.ProtocolUDP, intstr.FromInt32(port)
	desired := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: dedicatedLabels(runtimeName, name), OwnerReferences: ownerRef(owner)}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/mcp": name}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/runtime": runtimeName}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &portValue}}}}, Egress: []networkingv1.NetworkPolicyEgressRule{{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: ptr(intstr.FromInt32(53))}, {Protocol: &tcp, Port: ptr(intstr.FromInt32(53))}}}}}}
	existing, err := c.client.NetworkingV1().NetworkPolicies(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.NetworkingV1().NetworkPolicies(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	_, err = c.client.NetworkingV1().NetworkPolicies(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}

func (c *Controller) scaleDedicatedMCPs(ctx context.Context, ns, runtimeName string, replicas int32) error {
	items, err := c.client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{LabelSelector: "agenthub.io/parent-runtime=" + runtimeName})
	if err != nil {
		return err
	}
	for i := range items.Items {
		item := &items.Items[i]
		if item.Spec.Replicas != nil && *item.Spec.Replicas == replicas {
			continue
		}
		item.Spec.Replicas = ptr(replicas)
		if _, err := c.client.AppsV1().StatefulSets(ns).Update(ctx, item, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) ensureStatefulSet(ctx context.Context, ns, name, pvcName string, value spec, owner *unstructured.Unstructured) error {
	port := runtimePort(value.Runtime.Type)
	replicas := int32(1)
	runAs := int64(10000)
	nonRoot := true
	cpu := value.Profile.CPUMillis
	if cpu <= 0 {
		cpu = 1000
	}
	memory := value.Profile.MemoryMB
	if memory <= 0 {
		memory = 2048
	}
	reqCPU := int64(100)
	if cpu < reqCPU {
		reqCPU = cpu
	}
	reqMemory := int64(256)
	if memory < reqMemory {
		reqMemory = memory
	}
	requests := corev1.ResourceList{corev1.ResourceCPU: *apiresource.NewMilliQuantity(reqCPU, apiresource.DecimalSI), corev1.ResourceMemory: *apiresource.NewQuantity(reqMemory*1024*1024, apiresource.BinarySI)}
	limits := corev1.ResourceList{corev1.ResourceCPU: *apiresource.NewMilliQuantity(cpu, apiresource.DecimalSI), corev1.ResourceMemory: *apiresource.NewQuantity(memory*1024*1024, apiresource.BinarySI)}
	env := []corev1.EnvVar{{Name: "AGENTHUB_RUNTIME_TYPE", Value: value.Runtime.Type}, {Name: "AGENTHUB_MODEL_BASE_URL", Value: value.Model.BaseURL}, {Name: "AGENTHUB_RUNTIME_CONFIG", Value: "/etc/agenthub/runtime.json"}, {Name: "OPENCODE_CONFIG", Value: "/etc/agenthub/opencode.json"}, {Name: "HERMES_CONFIG", Value: "/etc/agenthub/hermes-config.yaml"}, {Name: "AGENTHUB_RUNTIME_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}}}
	env = append(env, corev1.EnvVar{Name: "AGENTHUB_MODEL_NAME", Value: value.Model.Name}, corev1.EnvVar{Name: "OPENAI_BASE_URL", Value: value.Model.BaseURL}, corev1.EnvVar{Name: "OPENAI_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "model-api-key"}}})
	if value.Runtime.Type == "opencode" {
		env = append(env, corev1.EnvVar{Name: "OPENCODE_SERVER_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}})
	} else if value.Runtime.Type == "hermes" {
		env = append(env, corev1.EnvVar{Name: "API_SERVER_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}})
	}
	containers := []corev1.Container{{Name: "agent", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}}, Env: env, Resources: corev1.ResourceRequirements{Requests: requests, Limits: limits}, SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem), VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "home", MountPath: "/home/agent"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "config", MountPath: "/etc/agenthub", ReadOnly: true}}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 5, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 12}, LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 20, PeriodSeconds: 15, TimeoutSeconds: 3, FailureThreshold: 4}}}
	if value.Runtime.Type == "hermes" {
		dashboardResources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("1000m"), corev1.ResourceMemory: apiresource.MustParse("1024Mi")}}
		proxyResources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("200m"), corev1.ResourceMemory: apiresource.MustParse("256Mi")}}
		containers = append(containers,
			corev1.Container{Name: "hermes-dashboard", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/opt/hermes/.venv/bin/hermes"}, Args: []string{"dashboard", "--host", "127.0.0.1", "--port", "9120", "--no-open"}, Env: []corev1.EnvVar{{Name: "HERMES_HOME", Value: "/home/agent/.hermes"}, {Name: "API_SERVER_URL", Value: "http://127.0.0.1:8642"}, {Name: "API_SERVER_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}}}, Resources: dashboardResources, SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem), VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/agent"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "config", MountPath: "/etc/agenthub", ReadOnly: true}}},
			corev1.Container{Name: "hermes-dashboard-proxy", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/usr/local/bin/agenthub-runtime-proxy"}, Ports: []corev1.ContainerPort{{Name: "dashboard", ContainerPort: 9119}}, Env: []corev1.EnvVar{{Name: "AGENTHUB_RUNTIME_PROXY_LISTEN", Value: ":9119"}, {Name: "AGENTHUB_RUNTIME_PROXY_TARGET", Value: "http://127.0.0.1:9120"}, {Name: "AGENTHUB_RUNTIME_PROXY_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}}}, Resources: proxyResources, SecurityContext: restrictedContainerSecurityContext(true), ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(9119)}}, InitialDelaySeconds: 3, PeriodSeconds: 5, FailureThreshold: 12}, LivenessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/livez", Port: intstr.FromInt32(9119)}}, InitialDelaySeconds: 15, PeriodSeconds: 15, FailureThreshold: 4}},
		)
	}
	containers = append(containers, sidecarContainers(value)...)
	initContainers := []corev1.Container{}
	if value.Runtime.Type == "hermes" {
		initContainers = append(initContainers, corev1.Container{Name: "hermes-config-init", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ec"}, Args: []string{"mkdir -p /home/agent/.hermes\ncp /etc/agenthub/hermes-config.yaml /home/agent/.hermes/config.yaml\nif [ -n \"$OPENAI_BASE_URL\" ] && [ -n \"$AGENTHUB_MODEL_NAME\" ]; then\n  /opt/hermes/.venv/bin/hermes config set model.default \"$AGENTHUB_MODEL_NAME\" || true\n  /opt/hermes/.venv/bin/hermes config set model.provider custom || true\n  /opt/hermes/.venv/bin/hermes config set model.base_url \"$OPENAI_BASE_URL\" || true\n  /opt/hermes/.venv/bin/hermes config set model.api_key \"$OPENAI_API_KEY\" || true\n  printf 'OPENAI_BASE_URL=%s\\nOPENAI_API_KEY=%s\\nCUSTOM_BASE_URL=%s\\nCUSTOM_API_KEY=%s\\n' \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_API_KEY\" > /home/agent/.hermes/.env\nfi"}, Env: env, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("200m"), corev1.ResourceMemory: apiresource.MustParse("256Mi")}}, SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem), VolumeMounts: []corev1.VolumeMount{{Name: "home", MountPath: "/home/agent"}, {Name: "config", MountPath: "/etc/agenthub", ReadOnly: true}, {Name: "tmp", MountPath: "/tmp"}}})
	}
	if value.Workspace.Type == "git" && value.Workspace.RepositoryURL != "" {
		initContainers = append(initContainers, corev1.Container{Name: "workspace-git-clone", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ec"}, Args: []string{`if [ ! -d /workspace/.git ] && [ -z "$(ls -A /workspace 2>/dev/null)" ]; then
  if [ -n "$BRANCH" ]; then git clone --depth 1 --branch "$BRANCH" "$REPOSITORY_URL" /workspace; else git clone --depth 1 "$REPOSITORY_URL" /workspace; fi
fi`}, Env: []corev1.EnvVar{{Name: "REPOSITORY_URL", Value: value.Workspace.RepositoryURL}, {Name: "BRANCH", Value: value.Workspace.Branch}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("500m"), corev1.ResourceMemory: apiresource.MustParse("512Mi")}}, SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem), VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}}})
	}
	desired := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Spec: appsv1.StatefulSetSpec{ServiceName: name, Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/runtime": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(name, map[string]string{"agenthub.io/owner": safeLabel(value.Owner)})}, Spec: corev1.PodSpec{
		ServiceAccountName: name, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &runAs, RunAsGroup: &runAs, FSGroup: &runAs, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		InitContainers: initContainers, Containers: containers,
		Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}}, {Name: "home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}},
	}}}}
	existing, err := c.client.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = c.client.AppsV1().StatefulSets(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	desired.ResourceVersion = existing.ResourceVersion
	desired.Spec.VolumeClaimTemplates = existing.Spec.VolumeClaimTemplates
	_, err = c.client.AppsV1().StatefulSets(ns).Update(ctx, desired, metav1.UpdateOptions{})
	return err
}
func safeLabel(value string) string {
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

func (c *Controller) updateStatus(ctx context.Context, object *unstructured.Unstructured, phase, pod, node string, restartCount int32, reason string) error {
	currentPhase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	currentPod, _, _ := unstructured.NestedString(object.Object, "status", "podName")
	currentNode, _, _ := unstructured.NestedString(object.Object, "status", "nodeName")
	currentReason, _, _ := unstructured.NestedString(object.Object, "status", "failureReason")
	currentRestartCount, _, _ := unstructured.NestedInt64(object.Object, "status", "restartCount")
	observed, _, _ := unstructured.NestedInt64(object.Object, "status", "observedGeneration")
	if currentPhase == phase && currentPod == pod && currentNode == node && currentReason == reason && currentRestartCount == int64(restartCount) && observed == object.GetGeneration() {
		return nil
	}
	copy := object.DeepCopy()
	status := map[string]any{"phase": phase, "podName": pod, "nodeName": node, "endpoint": fmt.Sprintf("http://%s.%s.svc:%d", object.GetName(), object.GetNamespace(), runtimePortFromObject(object)), "restartCount": int64(restartCount), "failureReason": reason, "observedGeneration": object.GetGeneration(), "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	if err := unstructured.SetNestedMap(copy.Object, status, "status"); err != nil {
		return err
	}
	_, err := c.dynamic.Resource(runtimeGVR).Namespace(object.GetNamespace()).UpdateStatus(ctx, copy, metav1.UpdateOptions{})
	return err
}
func runtimePortFromObject(object *unstructured.Unstructured) int32 {
	kind, _, _ := unstructured.NestedString(object.Object, "spec", "runtime", "type")
	return runtimePort(kind)
}
