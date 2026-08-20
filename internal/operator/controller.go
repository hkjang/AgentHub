package operator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
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

	"github.com/hkjang/AgentHub/internal/dlp"
	"github.com/hkjang/AgentHub/internal/runtimecfg"
	"github.com/hkjang/AgentHub/internal/runtimeenv"
	"github.com/hkjang/AgentHub/internal/runtimetype"
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

// mcpBinding is one MCP server attached to a runtime. AuthType/AuthHeader
// describe the scheme; CredentialKey names the entry in the runtime Secret that
// holds the value, so the credential never appears in the CRD.
type mcpBinding struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	Endpoint      string `json:"endpoint"`
	Image         string `json:"image"`
	Port          int32  `json:"port"`
	AuthType      string `json:"authType"`
	AuthHeader    string `json:"authHeader"`
	CredentialKey string `json:"credentialKey"`
	// ToolPolicy, when present, routes this server through the in-Pod egress
	// gateway so the agent process cannot reach it directly.
	ToolPolicy *mcpToolPolicy `json:"toolPolicy,omitempty"`
}

type mcpToolPolicy struct {
	Mode  string   `json:"mode"`
	Tools []string `json:"tools"`
	// ApprovalTools need a person's decision before they run, and
	// ApprovalRequired gates every tool on the server. The gateway holds the call
	// open while it asks the control plane, which is the one place an agent cannot
	// route around.
	ApprovalTools    []string `json:"approvalTools"`
	ApprovalRequired bool     `json:"approvalRequired"`
	// PolicyDenied and PolicyGated are patterns compiled from the platform-wide
	// policy. They travel beside the per-agent list rather than merged into it:
	// "the platform forbids this" and "this agent was not given it" are different
	// statements, and only one of them is the agent owner's to change.
	PolicyDenied  []string `json:"policyDenied,omitempty"`
	PolicyGated   []string `json:"policyGated,omitempty"`
	PolicyDenyAll bool     `json:"policyDenyAll,omitempty"`
}

// gated reports whether this policy needs a decision for at least one tool.
func (p *mcpToolPolicy) gated() bool {
	return p != nil && (p.ApprovalRequired || len(p.ApprovalTools) > 0 || len(p.PolicyGated) > 0)
}

type spec struct {
	Owner string `json:"owner"`
	// RuntimeRef is the control plane's id for this runtime. The gateway sends it
	// with an approval request so the request can be tied back to an agent, its
	// owner and a reviewer.
	RuntimeRef struct {
		ID string `json:"id"`
	} `json:"runtimeRef"`
	Runtime struct {
		Type  string `json:"type"`
		Image string `json:"image"`
		// SidecarImage runs AgentHub's own sidecars. It is the control plane's
		// image, so an agent pinned to an older runtime image still gets the
		// session proxy and MCP gateway from this release. Empty falls back to
		// the runtime image, which is what pre-0.7 objects carry.
		SidecarImage string `json:"sidecarImage"`
		// Command and Port start a 'custom' runtime, which has no adapter of its
		// own. Without them the container would run its image's default
		// entrypoint, which is what used to leave every custom runtime in
		// CrashLoopBackOff.
		Command []string `json:"command"`
		Port    int32    `json:"port"`
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
		// How to authenticate the clone. The credential itself is read from the
		// runtime Secret at clone time.
		GitCredentialKind     string `json:"gitCredentialKind"`
		GitCredentialUsername string `json:"gitCredentialUsername"`
	} `json:"workspace"`
	Model struct {
		BaseURL   string `json:"baseUrl"`
		Name      string `json:"name"`
		SecretRef string `json:"secretRef"`
	} `json:"model"`
	MCP []mcpBinding `json:"mcp"`
	// Provisioning is the platform-wide runtime environment: the files every
	// container in this Pod is given and the variables all of them export.
	Provisioning struct {
		Files []runtimeenv.File     `json:"files"`
		Env   []runtimeenv.Variable `json:"env"`
	} `json:"provisioning"`
	// RuntimeSettings is the administrator's overlay for this runtime type.
	RuntimeSettings struct {
		Fingerprint string         `json:"fingerprint"`
		Config      map[string]any `json:"config"`
		Env         []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"env"`
	} `json:"runtimeSettings"`
	// DLP is what the in-Pod gateway inspects on tool calls.
	DLP struct {
		Enabled       bool `json:"enabled"`
		ScanResponses bool `json:"scanResponses"`
		MaxBytes      int  `json:"maxBytes"`
		Classes       []struct {
			Class  string `json:"class"`
			Action string `json:"action"`
		} `json:"classes"`
	} `json:"dlp"`
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
	if !runtimetype.IsSupported(value.Runtime.Type) {
		return spec{}, fmt.Errorf("unsupported runtime type %q", value.Runtime.Type)
	}
	if value.Runtime.Image == "" {
		return spec{}, errors.New("runtime image is required")
	}
	// A custom runtime has no adapter to start it. Without a command the
	// container would run its image's default entrypoint, which is what used to
	// leave every custom runtime in CrashLoopBackOff with nothing explaining why.
	if value.Runtime.Type == runtimetype.Custom && len(value.Runtime.Command) == 0 {
		return spec{}, errors.New("custom runtime requires a start command")
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
		binding := map[string]any{"name": item.Name, "mode": item.Mode, "endpoint": endpoint, "image": item.Image, "port": port, "authType": item.AuthType, "authHeader": item.AuthHeader, "credentialKey": item.CredentialKey}
		if item.ToolPolicy != nil {
			// The adapter config must point at the gateway, never the server: a
			// policy the agent process could route around would not be a policy.
			binding["upstream"] = endpoint
			binding["endpoint"] = fmt.Sprintf("http://127.0.0.1:%d/mcp/%s", mcpGatewayPort, safeLabel(item.Name))
			binding["toolPolicyMode"] = item.ToolPolicy.Mode
		}
		result = append(result, binding)
	}
	return result
}

// sidecarImage picks the image AgentHub's own sidecars run.
func (v spec) sidecarImage() string {
	if strings.TrimSpace(v.Runtime.SidecarImage) != "" {
		return v.Runtime.SidecarImage
	}
	return v.Runtime.Image
}

// envControlPlaneURL is where a Pod's gateway reaches the control plane to ask for
// a tool approval. It defaults to the in-cluster Service the manifests create.
const envControlPlaneURL = "AGENTHUB_CONTROL_PLANE_URL"

const defaultControlPlaneURL = "http://agenthub.agent-platform-system.svc:8080"

func controlPlaneURL() string {
	if value := strings.TrimSpace(os.Getenv(envControlPlaneURL)); value != "" {
		return value
	}
	return defaultControlPlaneURL
}

// dlpConfig renders the scanner settings for the gateway, in the same shape the
// control plane's own scanner reads, so both ends are configured by one document.
func dlpConfig(value spec) (string, bool) {
	if !value.DLP.Enabled || len(value.DLP.Classes) == 0 {
		return "", false
	}
	settings := dlp.Settings{Enabled: true, ScanResponses: value.DLP.ScanResponses, MaxBytes: value.DLP.MaxBytes, Classes: map[string]string{}}
	for _, class := range value.DLP.Classes {
		settings.Classes[class.Class] = class.Action
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// gatesApproval reports whether any binding needs a decision before a call.
func gatesApproval(policies []mcpBinding) bool {
	for _, item := range policies {
		if item.ToolPolicy.gated() {
			return true
		}
	}
	return false
}

// declaredEnvNames is the comma-separated list of overlay variable names, sorted
// so the value is stable across reconciles and does not roll Pods on its own.
func declaredEnvNames(variables []struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}) string {
	names := make([]string, 0, len(variables))
	for _, variable := range variables {
		if strings.TrimSpace(variable.Name) != "" {
			names = append(names, variable.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// mcpGatewayPort is where the in-Pod MCP tool policy gateway listens. It binds
// to loopback only, so nothing outside the Pod can reach it.
const mcpGatewayPort int32 = 9129

// mcpGatewayContainer runs the egress gateway for every policied binding.
//
// It holds the credentials rather than the agent container, so a tool the agent
// may not call is one it cannot authenticate to either.
func mcpGatewayContainer(image, runtimeName, runtimeRef string, bindings []map[string]any, policies []mcpBinding, value spec) (corev1.Container, bool) {
	byName := make(map[string]*mcpToolPolicy, len(policies))
	for _, item := range policies {
		if item.ToolPolicy != nil {
			byName[item.Name] = item.ToolPolicy
		}
	}
	type upstreamConfig struct {
		Name          string   `json:"name"`
		Upstream      string   `json:"upstream"`
		AuthHeader    string   `json:"authHeader,omitempty"`
		AuthTemplate  string   `json:"authTemplate,omitempty"`
		CredentialEnv string   `json:"credentialEnv,omitempty"`
		Mode          string   `json:"mode"`
		Tools         []string `json:"tools"`
		// Tools that need a person's decision, and whether every tool on this
		// server does.
		ApprovalTools    []string `json:"approvalTools,omitempty"`
		ApprovalRequired bool     `json:"approvalRequired,omitempty"`
		PolicyDenied     []string `json:"policyDenied,omitempty"`
		PolicyGated      []string `json:"policyGated,omitempty"`
		PolicyDenyAll    bool     `json:"policyDenyAll,omitempty"`
	}
	configs := []upstreamConfig{}
	env := []corev1.EnvVar{}
	for _, binding := range bindings {
		policy, ok := byName[fmt.Sprint(binding["name"])]
		if !ok {
			continue
		}
		entry := upstreamConfig{
			Name: safeLabel(fmt.Sprint(binding["name"])), Upstream: fmt.Sprint(binding["upstream"]),
			Mode: policy.Mode, Tools: policy.Tools,
			ApprovalTools: policy.ApprovalTools, ApprovalRequired: policy.ApprovalRequired,
			PolicyDenied: policy.PolicyDenied, PolicyGated: policy.PolicyGated, PolicyDenyAll: policy.PolicyDenyAll,
		}
		if entry.Tools == nil {
			entry.Tools = []string{}
		}
		if headerName, headerValue := mcpAuthHeader(binding); headerName != "" {
			credentialKey := fmt.Sprint(binding["credentialKey"])
			variable := mcpCredentialEnv(credentialKey)
			entry.AuthHeader = headerName
			// The rendered value is an ${ENV} placeholder for the adapter; the
			// gateway substitutes the secret itself, so it needs the template.
			entry.AuthTemplate = strings.ReplaceAll(headerValue, "${"+variable+"}", "%s")
			entry.CredentialEnv = variable
			env = append(env, corev1.EnvVar{Name: variable, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: runtimeName}, Key: credentialKey, Optional: ptr(true)}}})
		}
		configs = append(configs, entry)
	}
	if len(configs) == 0 {
		return corev1.Container{}, false
	}
	encoded, err := json.Marshal(configs)
	if err != nil {
		return corev1.Container{}, false
	}
	env = append(env,
		corev1.EnvVar{Name: "AGENTHUB_MCP_GATEWAY", Value: string(encoded)},
		corev1.EnvVar{Name: "AGENTHUB_MCP_GATEWAY_LISTEN", Value: fmt.Sprintf("127.0.0.1:%d", mcpGatewayPort)})
	// The content scanner runs in the gateway for the same reason the tool policy
	// does: it is the only place the agent process cannot route around.
	if scanner, ok := dlpConfig(value); ok {
		env = append(env, corev1.EnvVar{Name: "AGENTHUB_DLP", Value: scanner})
	}
	// A gated tool needs a decision from the control plane, so the gateway is told
	// where to ask, who it is, and how to authenticate — the runtime's own token,
	// read from the Pod's Secret, whose hash the control plane holds.
	if gatesApproval(policies) {
		env = append(env,
			corev1.EnvVar{Name: "AGENTHUB_APPROVAL_URL", Value: controlPlaneURL()},
			corev1.EnvVar{Name: "AGENTHUB_RUNTIME_ID", Value: runtimeRef},
			corev1.EnvVar{Name: "AGENTHUB_RUNTIME_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: runtimeName}, Key: "runtime-token"}}})
	}
	return corev1.Container{
		Name:            "agenthub-mcp-gateway",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/usr/local/bin/agenthub-runtime-proxy"},
		Env:             env,
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("200m"), corev1.ResourceMemory: apiresource.MustParse("256Mi")}},
		SecurityContext: restrictedContainerSecurityContext(true),
		// No probe: the gateway binds to loopback so nothing outside the Pod can
		// reach it, and the restricted Pod Security standard forbids pointing a
		// probe at 127.0.0.1. A gateway that is down fails MCP calls closed,
		// which is the behaviour a tool policy should have anyway.
	}, true
}

// openCodeProvider is the provider key the generated OpenCode config declares
// and the model reference points at. They must stay identical or OpenCode
// cannot resolve the bound model.
const openCodeProvider = "agenthub"

// mcpCredentialEnv is the environment variable a Pod reads one MCP credential
// from. Environment names allow only letters, digits and underscore.
func mcpCredentialEnv(credentialKey string) string {
	var b strings.Builder
	b.WriteString("AGENTHUB_MCP_")
	for _, r := range strings.ToUpper(strings.TrimPrefix(credentialKey, "mcp-credential-")) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// mcpAuthHeader renders the header an adapter should send. The value is an
// ${ENV} placeholder so the credential stays out of the generated configuration.
func mcpAuthHeader(binding map[string]any) (string, string) {
	authType := fmt.Sprint(binding["authType"])
	credentialKey := fmt.Sprint(binding["credentialKey"])
	if credentialKey == "" || credentialKey == "<nil>" || authType == "" || authType == "none" || authType == "<nil>" {
		return "", ""
	}
	placeholder := "${" + mcpCredentialEnv(credentialKey) + "}"
	switch authType {
	case "bearer":
		return "Authorization", "Bearer " + placeholder
	case "basic":
		return "Authorization", "Basic " + placeholder
	case "header":
		name := fmt.Sprint(binding["authHeader"])
		if name == "" || name == "<nil>" {
			name = "Authorization"
		}
		return name, placeholder
	}
	return "", ""
}

func runtimeConfigs(ns, runtimeName string, value spec) (string, string, string, string) {
	bindings := effectiveMCP(ns, runtimeName, value)
	runtimeValue := map[string]any{"owner": value.Owner, "runtime": value.Runtime, "profile": value.Profile, "workspace": value.Workspace, "model": value.Model, "mcp": bindings, "lifecycle": value.Lifecycle}
	runtimeRaw, _ := json.MarshalIndent(runtimeValue, "", "  ")
	opencode := map[string]any{"$schema": "https://opencode.ai/config.json", "autoupdate": false, "mcp": map[string]any{}}
	hermes := map[string]any{"terminal": map[string]any{"cwd": "/workspace", "home_mode": "profile"}, "mcp_servers": map[string]any{}}
	// Qwen Code reads one settings file for both the model it uses and the tools
	// it may call. Telemetry and usage statistics are off because an offline site
	// must not report outwards; an administrator who runs their own collector can
	// turn telemetry back on through the settings overlay.
	qwen := map[string]any{
		"mcpServers": map[string]any{},
		"privacy":    map[string]any{"usageStatisticsEnabled": false},
		"telemetry":  map[string]any{"enabled": false},
	}
	if value.Model.BaseURL != "" && value.Model.Name != "" {
		// One provider entry named after the platform, not after whichever backend
		// happens to serve it: the same binding fronts Ollama, vLLM or any other
		// OpenAI-compatible gateway an administrator registers.
		opencode["model"] = openCodeProvider + "/" + value.Model.Name
		opencode["provider"] = map[string]any{
			openCodeProvider: map[string]any{
				"npm":  "@ai-sdk/openai-compatible",
				"name": "AgentHub Model Gateway",
				"options": map[string]any{
					"baseURL": value.Model.BaseURL,
					"apiKey":  "{env:OPENAI_API_KEY}",
				},
				"models": map[string]any{
					value.Model.Name: map[string]any{"name": value.Model.Name},
				},
			},
		}
		hermes["model"] = map[string]any{"provider": "custom", "default": value.Model.Name, "base_url": value.Model.BaseURL, "api_key": "${OPENAI_API_KEY}"}
		// The key and the endpoint reach Qwen Code through its own .env, which the
		// initialiser writes; the settings file names the model so that a session
		// somebody opens by hand starts on the same one a task would use.
		qwen["model"] = map[string]any{"name": value.Model.Name}
	}
	// The administrator's overlay lands here, on the configuration the platform
	// just generated, so the runtime still reads one file and the platform's own
	// keys survive.
	if overlay := value.RuntimeSettings.Config; len(overlay) > 0 {
		switch value.Runtime.Type {
		case runtimetype.OpenCode:
			opencode, _ = runtimecfg.Merge(runtimetype.OpenCode, opencode, overlay)
		case runtimetype.Hermes:
			hermes, _ = runtimecfg.Merge(runtimetype.Hermes, hermes, overlay)
		case runtimetype.QwenCode:
			qwen, _ = runtimecfg.Merge(runtimetype.QwenCode, qwen, overlay)
		}
	}
	openMCP := opencode["mcp"].(map[string]any)
	hermesMCP := hermes["mcp_servers"].(map[string]any)
	qwenMCP := qwen["mcpServers"].(map[string]any)
	for _, item := range bindings {
		name, endpoint := safeLabel(fmt.Sprint(item["name"])), fmt.Sprint(item["endpoint"])
		if endpoint == "" {
			continue
		}
		open := map[string]any{"type": "remote", "url": endpoint, "enabled": true, "oauth": false}
		hermesEntry := map[string]any{"url": endpoint, "enabled": true, "timeout": 120, "connect_timeout": 60}
		// httpUrl, not url: in Qwen Code the second one means SSE, and a streamable
		// HTTP server declared under it never connects.
		qwenEntry := map[string]any{"httpUrl": endpoint, "timeout": 120000}
		// The ConfigMap is not a Secret, so the credential is referenced by the
		// environment variable the Pod reads from the runtime Secret.
		// A policied binding is authenticated by the gateway, so the credential
		// must not be handed to the agent process at all.
		if headerName, headerValue := mcpAuthHeader(item); headerName != "" && item["toolPolicyMode"] == nil {
			open["headers"] = map[string]any{headerName: headerValue}
			hermesEntry["headers"] = map[string]any{headerName: headerValue}
			qwenEntry["headers"] = map[string]any{headerName: headerValue}
		}
		openMCP[name] = open
		hermesMCP[name] = hermesEntry
		qwenMCP[name] = qwenEntry
	}
	openRaw, _ := json.MarshalIndent(opencode, "", "  ")
	hermesRaw, _ := json.MarshalIndent(hermes, "", "  ")
	qwenRaw, _ := json.MarshalIndent(qwen, "", "  ")
	return string(runtimeRaw), string(openRaw), string(hermesRaw), string(qwenRaw)
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
	if err := c.ensureProvisionedConfigMap(ctx, namespace, name, value, object); err != nil {
		return err
	}
	pvcName := value.Workspace.PVCName
	if pvcName == "" {
		pvcName = name + "-workspace"
	}
	if err := c.ensurePVC(ctx, namespace, pvcName, value, object); err != nil {
		return err
	}
	if err := c.ensureHomePVC(ctx, namespace, name, object); err != nil {
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
	runtimeConfig, opencodeConfig, hermesConfig, qwenConfig := runtimeConfigs(ns, name, value)
	data := map[string]string{"runtime.json": runtimeConfig, "opencode.json": opencodeConfig, "hermes-config.yaml": hermesConfig, "qwen-settings.json": qwenConfig}
	// Qwen Paw writes its own configuration during initialisation, so its overlay
	// cannot be merged here — it is delivered as a patch the initialiser applies
	// after `qwenpaw init` has created the file.
	if value.Runtime.Type == runtimetype.QwenPaw && len(value.RuntimeSettings.Config) > 0 {
		if patch, err := json.MarshalIndent(value.RuntimeSettings.Config, "", "  "); err == nil {
			data["qwenpaw-overlay.json"] = string(patch)
		}
	}
	desired := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Data: data}
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

// homePVCName is the per-runtime volume backing /home/agent. The adapters keep
// their state there — QwenPaw's provider registry and skills, Hermes' memory,
// OpenCode's auth and settings — so an emptyDir discards the user's setup every
// time the Pod is recreated, which now happens on any configuration change.
func homePVCName(runtimeName string) string { return runtimeName + "-home" }

// homeSizeGB is deliberately modest: this volume holds adapter state and caches,
// not project files, which live on the workspace volume.
const homeSizeGB = 5

func (c *Controller) ensureHomePVC(ctx context.Context, ns, runtimeName string, owner *unstructured.Unstructured) error {
	name := homePVCName(runtimeName)
	_, err := c.client.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	// Unlike the workspace volume this one is owned by the runtime: it holds
	// adapter state rather than the user's project files, so it should be
	// collected when the agent is deleted instead of leaving an orphan behind.
	desired := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(runtimeName, map[string]string{"agenthub.io/volume": "home"}), OwnerReferences: ownerRef(owner)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: apiresource.MustParse(strconv.Itoa(homeSizeGB) + "Gi")}},
		},
	}
	_, err = c.client.CoreV1().PersistentVolumeClaims(ns).Create(ctx, desired, metav1.CreateOptions{})
	return err
}

// gitCloneScript clones the workspace repository, authenticating when the
// workspace is bound to a credential. The credential is never placed in the
// remote URL or on a command line: a token goes into a 0600 credential file that
// git reads through the store helper, and an SSH key into a 0600 file referenced
// by GIT_SSH_COMMAND. Both live on the tmpfs and are removed before the clone
// container exits.
const gitCloneScript = `
if [ -d /workspace/.git ] || [ -n "$(ls -A /workspace 2>/dev/null)" ]; then
  echo "workspace is already populated; skipping clone"
  exit 0
fi
umask 077
CREDENTIAL_FILE=/tmp/.git-credentials
KEY_FILE=/tmp/.git-ssh-key
cleanup() { rm -f "$CREDENTIAL_FILE" "$KEY_FILE"; }
trap cleanup EXIT

set --
if [ -n "${GIT_CREDENTIAL:-}" ]; then
  case "${GIT_CREDENTIAL_KIND:-}" in
    token)
      user="${GIT_CREDENTIAL_USERNAME:-git}"
      host="$(printf '%s' "$REPOSITORY_URL" | sed -e 's#^\([a-z+]*\)://##' -e 's#/.*##' -e 's#^.*@##')"
      scheme="$(printf '%s' "$REPOSITORY_URL" | sed -n 's#^\([a-z+]*\)://.*#\1#p')"
      [ -n "$scheme" ] || scheme=https
      printf '%s://%s:%s@%s\n' "$scheme" "$user" "$GIT_CREDENTIAL" "$host" > "$CREDENTIAL_FILE"
      chmod 600 "$CREDENTIAL_FILE"
      set -- -c credential.helper= -c "credential.helper=store --file=$CREDENTIAL_FILE"
      ;;
    ssh-key)
      printf '%s\n' "$GIT_CREDENTIAL" > "$KEY_FILE"
      chmod 600 "$KEY_FILE"
      GIT_SSH_COMMAND="ssh -i $KEY_FILE -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/tmp/.git-known-hosts"
      export GIT_SSH_COMMAND
      ;;
  esac
elif [ -n "${GIT_CREDENTIAL_KIND:-}" ]; then
  echo "workspace is bound to a git credential but none was provided; attempting an unauthenticated clone" >&2
fi

if [ -n "${BRANCH:-}" ]; then
  git "$@" clone --depth 1 --branch "$BRANCH" "$REPOSITORY_URL" /workspace
else
  git "$@" clone --depth 1 "$REPOSITORY_URL" /workspace
fi
`

func runtimePort(rt string) int32 { return runtimetype.Port(rt) }

// specPort is the port this runtime actually serves on. A custom runtime may
// declare its own; everything else is decided by its adapter.
func specPort(value spec) int32 {
	if value.Runtime.Type == runtimetype.Custom && value.Runtime.Port > 0 {
		return value.Runtime.Port
	}
	return runtimePort(value.Runtime.Type)
}

func (c *Controller) ensureService(ctx context.Context, ns, name string, value spec, owner *unstructured.Unstructured) error {
	port := specPort(value)
	ports := []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstrFromInt32(port)}}
	if runtimetype.UsesGatewayProxy(value.Runtime.Type) {
		ports = append(ports, corev1.ServicePort{Name: "dashboard", Port: runtimetype.GatewayPort, TargetPort: intstrFromInt32(runtimetype.GatewayPort)})
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

// runtimeProxyContainer fronts a loopback-only runtime UI with the sidecar that
// enforces Basic auth against the per-runtime token, so unauthenticated traffic
// never reaches the agent application itself.
func runtimeProxyContainer(containerName, runtimeName, image, target string) corev1.Container {
	port := runtimetype.GatewayPort
	return corev1.Container{
		Name:            containerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/usr/local/bin/agenthub-runtime-proxy"},
		Ports:           []corev1.ContainerPort{{Name: "dashboard", ContainerPort: port}},
		Env: []corev1.EnvVar{
			{Name: "AGENTHUB_RUNTIME_PROXY_LISTEN", Value: fmt.Sprintf(":%d", port)},
			{Name: "AGENTHUB_RUNTIME_PROXY_TARGET", Value: target},
			{Name: "AGENTHUB_RUNTIME_PROXY_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: runtimeName}, Key: "runtime-token"}}},
		},
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("32Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("200m"), corev1.ResourceMemory: apiresource.MustParse("256Mi")}},
		SecurityContext: restrictedContainerSecurityContext(true),
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 3, PeriodSeconds: 5, FailureThreshold: 12},
		LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/livez", Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 15, PeriodSeconds: 15, FailureThreshold: 4},
	}
}

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
	port := intstr.FromInt32(specPort(value))
	ingressPorts := []networkingv1.NetworkPolicyPort{{Protocol: ptr(corev1.ProtocolTCP), Port: &port}}
	if runtimetype.UsesGatewayProxy(value.Runtime.Type) {
		dashboardPort := intstr.FromInt32(runtimetype.GatewayPort)
		ingressPorts = append(ingressPorts, networkingv1.NetworkPolicyPort{Protocol: ptr(corev1.ProtocolTCP), Port: &dashboardPort})
	}
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	egress := []networkingv1.NetworkPolicyEgressRule{}
	if value.Network.AllowDNS {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: ptr(intstr.FromInt32(53))}, {Protocol: &tcp, Port: ptr(intstr.FromInt32(53))}}})
	}
	// Each dependency is allowed to its own destination rather than to every host
	// that happens to listen on the same port. Literal addresses become a /32
	// IPBlock and in-cluster services are scoped to their namespace; only public
	// DNS names, which NetworkPolicy cannot express, fall back to port-only.
	endpoints := []string{value.Model.BaseURL, value.Workspace.RepositoryURL}
	for _, item := range effectiveMCP(ns, name, value) {
		endpoints = append(endpoints, fmt.Sprint(item["endpoint"]))
	}
	// A gated tool call is decided by the control plane, so the gateway has to be
	// able to reach it. Without this rule the default-deny policy would turn every
	// approval into a refusal.
	if gatesApproval(value.MCP) {
		endpoints = append(endpoints, controlPlaneURL())
	}
	unresolved := map[int32]bool{}
	seen := map[string]bool{}
	for _, raw := range endpoints {
		port := endpointPort(raw)
		if port <= 0 {
			continue
		}
		peers, resolved := egressPeers(raw)
		if !resolved {
			unresolved[port] = true
			continue
		}
		if len(peers) == 0 {
			// Loopback: a sidecar in this very Pod, which egress never governs.
			continue
		}
		key := fmt.Sprintf("%d/%s", port, raw)
		if seen[key] {
			continue
		}
		seen[key] = true
		p := intstr.FromInt32(port)
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{To: peers, Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &p}}})
	}
	for port := range unresolved {
		p := intstr.FromInt32(port)
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

// egressPeers narrows an endpoint URL to the NetworkPolicy peers that can serve
// it. It reports resolved=false for public DNS names: NetworkPolicy matches on
// addresses and selectors, not hostnames, so those still need a port-only rule
// and are better constrained through an explicit allowed-destination CIDR.
func egressPeers(raw string) ([]networkingv1.NetworkPolicyPeer, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return nil, false
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil, true
		}
		suffix := "/32"
		if ip.To4() == nil {
			suffix = "/128"
		}
		return []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: ip.String() + suffix}}}, true
	}
	if host == "localhost" {
		return nil, true
	}
	// Cluster DNS: <service>.<namespace>.svc[.cluster.local]. The namespace is the
	// tightest selector available without resolving the Service's Pod labels.
	labelsOfHost := strings.Split(strings.TrimSuffix(strings.TrimSuffix(host, ".cluster.local"), "."), ".")
	for index, part := range labelsOfHost {
		if part != "svc" || index < 2 {
			continue
		}
		namespace := labelsOfHost[index-1]
		return []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}}}}, true
	}
	return nil, false
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
	port := specPort(value)
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
	env := []corev1.EnvVar{{Name: "AGENTHUB_RUNTIME_TYPE", Value: value.Runtime.Type}, {Name: "AGENTHUB_MODEL_BASE_URL", Value: value.Model.BaseURL}, {Name: "AGENTHUB_RUNTIME_CONFIG", Value: "/etc/agenthub/runtime.json"}, {Name: "OPENCODE_CONFIG", Value: "/etc/agenthub/opencode.json"}, {Name: "HERMES_CONFIG", Value: "/etc/agenthub/hermes-config.yaml"}, {Name: "HOME", Value: "/home/agent"}, {Name: "QWENPAW_HOME", Value: "/home/agent/.qwenpaw"}, {Name: "AGENTHUB_RUNTIME_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "runtime-token"}}}}
	env = append(env, corev1.EnvVar{Name: "AGENTHUB_MODEL_NAME", Value: value.Model.Name}, corev1.EnvVar{Name: "OPENAI_BASE_URL", Value: value.Model.BaseURL}, corev1.EnvVar{Name: "OPENAI_API_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "model-api-key"}}})
	// The administrator's overlay for this runtime type. It goes in before the
	// per-server credentials and after the platform's own variables, so a value the
	// platform sets keeps winning — those are refused at the edge anyway, and a
	// second line of defence here costs nothing.
	for _, variable := range value.RuntimeSettings.Env {
		if variable.Name == "" {
			continue
		}
		env = append(env, corev1.EnvVar{Name: variable.Name, Value: variable.Value})
	}
	// The names — never the values — of what the overlay declared, so the report
	// each Pod sends can say which of them actually reached the container. For a
	// runtime with no configuration file this is the only evidence there is.
	if names := declaredEnvNames(value.RuntimeSettings.Env); names != "" {
		env = append(env, corev1.EnvVar{Name: "AGENTHUB_REPORT_ENV_KEYS", Value: names})
	}
	if fingerprint := value.RuntimeSettings.Fingerprint; fingerprint != "" {
		// The Pod reports this back after applying the overlay, which is what turns
		// "I saved the setting" into "the fleet is running it".
		env = append(env, corev1.EnvVar{Name: "AGENTHUB_RUNTIME_SETTINGS_FINGERPRINT", Value: fingerprint})
	}
	// Every initialiser reports the configuration it wrote, so it needs to know
	// where to report and who it is. The token is already in the shared set above.
	env = append(env,
		corev1.EnvVar{Name: "AGENTHUB_CONTROL_PLANE_URL", Value: controlPlaneURL()},
		corev1.EnvVar{Name: "AGENTHUB_RUNTIME_ID", Value: value.RuntimeRef.ID})
	// Each authenticated MCP server contributes one optional Secret key; optional
	// so a server whose credential is not configured yet does not block start-up.
	for _, item := range effectiveMCP(ns, name, value) {
		credentialKey := fmt.Sprint(item["credentialKey"])
		if credentialKey == "" || credentialKey == "<nil>" {
			continue
		}
		env = append(env, corev1.EnvVar{Name: mcpCredentialEnv(credentialKey), ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: credentialKey, Optional: ptr(true)}}})
	}
	adapter := adapterFor(value.Runtime.Type)
	if value.Runtime.Type == runtimetype.Custom {
		adapter.Command = value.Runtime.Command
	}
	build := adapterBuild{Name: name, Value: value}
	if adapter.Env != nil {
		env = append(env, adapter.Env(build)...)
	}
	// The adapter's own init containers need the completed environment, so the
	// build context is only finalised once every variable has been added.
	build.Env = env

	agentContainer := corev1.Container{
		Name:            "agent",
		Image:           value.Runtime.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         adapter.Command,
		Args:            adapterArgs(adapter, build),
		Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
		Env:             env,
		Resources:       corev1.ResourceRequirements{Requests: requests, Limits: limits},
		SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem),
		VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "home", MountPath: "/home/agent"}, {Name: "tmp", MountPath: "/tmp"}, {Name: "config", MountPath: "/etc/agenthub", ReadOnly: true}},
		ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 5, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 12},
		LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 20, PeriodSeconds: 15, TimeoutSeconds: 3, FailureThreshold: 4},
	}
	// An adapter that binds to loopback says how to check it instead, because a
	// TCP probe from the kubelet can never reach 127.0.0.1 inside the container.
	if adapter.Probes != nil {
		if readiness, liveness := adapter.Probes(build); readiness != nil {
			agentContainer.ReadinessProbe, agentContainer.LivenessProbe = readiness, liveness
		}
	}
	containers := []corev1.Container{agentContainer}
	if adapter.Sidecars != nil {
		containers = append(containers, adapter.Sidecars(build)...)
	}
	containers = append(containers, sidecarContainers(value)...)
	if gateway, ok := mcpGatewayContainer(value.sidecarImage(), name, value.RuntimeRef.ID, effectiveMCP(ns, name, value), value.MCP, value); ok {
		containers = append(containers, gateway)
	}
	initContainers := []corev1.Container{}
	if adapter.InitContainers != nil {
		initContainers = append(initContainers, adapter.InitContainers(build)...)
	}
	if value.Workspace.Type == "git" && value.Workspace.RepositoryURL != "" {
		cloneEnv := []corev1.EnvVar{{Name: "REPOSITORY_URL", Value: value.Workspace.RepositoryURL}, {Name: "BRANCH", Value: value.Workspace.Branch}, {Name: "GIT_CREDENTIAL_KIND", Value: value.Workspace.GitCredentialKind}, {Name: "GIT_CREDENTIAL_USERNAME", Value: value.Workspace.GitCredentialUsername}}
		if value.Workspace.GitCredentialKind != "" {
			// Optional: a deleted secret must produce a clear git failure rather than
			// a Pod that never starts.
			cloneEnv = append(cloneEnv, corev1.EnvVar{Name: "GIT_CREDENTIAL", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "workspace-git-credential", Optional: ptr(true)}}})
		}
		initContainers = append(initContainers, corev1.Container{Name: "workspace-git-clone", Image: value.Runtime.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/bin/sh", "-ec"}, Args: []string{gitCloneScript}, Env: cloneEnv, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("500m"), corev1.ResourceMemory: apiresource.MustParse("512Mi")}}, SecurityContext: restrictedContainerSecurityContext(value.Security.ReadOnlyRootFilesystem), VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "tmp", MountPath: "/tmp"}}})
	}
	// The generated configuration is delivered through a ConfigMap and copied into
	// the agent's home directory by an init container, so a config-only change is
	// invisible to a running Pod. Stamping its hash (and the explicit restart
	// marker) into the Pod template makes the StatefulSet roll the Pod instead.
	podAnnotations := map[string]string{"agenthub.io/config-hash": configHash(ns, name, value)}
	if restartedAt := owner.GetAnnotations()["agenthub.io/restarted-at"]; restartedAt != "" {
		podAnnotations["agenthub.io/restarted-at"] = restartedAt
	}
	desired := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels(name, nil), OwnerReferences: ownerRef(owner)}, Spec: appsv1.StatefulSetSpec{ServiceName: name, Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"agenthub.io/runtime": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(name, map[string]string{"agenthub.io/owner": safeLabel(value.Owner)}), Annotations: podAnnotations}, Spec: corev1.PodSpec{
		ServiceAccountName: name, AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &runAs, RunAsGroup: &runAs, FSGroup: &runAs, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		InitContainers: initContainers, Containers: containers,
		Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}}, {Name: "home", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: homePVCName(name)}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}},
	}}}}
	// Last, so the administrator's common files and variables reach every
	// container the adapters contributed as well as the agent's own.
	applyProvisioning(&desired.Spec.Template.Spec, name, value)
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

// configHash fingerprints everything the ConfigMaps carry so that any change to
// the model binding, MCP bundle, system prompt or the administrator's common
// files and variables produces a new Pod template.
func configHash(ns, name string, value spec) string {
	runtimeRaw, openRaw, hermesRaw, qwenRaw := runtimeConfigs(ns, name, value)
	// The overlay's fingerprint is folded in so that changing a setting rolls the
	// Pod. Qwen Paw's overlay never reaches the generated configs above, and an
	// environment variable is not part of them either, so without this a saved
	// setting would sit in the ConfigMap while the running Pod kept its old one.
	sum := sha256.Sum256([]byte(runtimeRaw + "\x00" + openRaw + "\x00" + hermesRaw + "\x00" + qwenRaw + "\x00" +
		provisioningHash(value) + "\x00" + value.RuntimeSettings.Fingerprint))
	return hex.EncodeToString(sum[:])
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
