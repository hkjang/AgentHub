package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

func TestRuntimeConfigsCompileModelAndMCPBindings(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.1.0"
	value.Model.BaseURL = "http://model-gateway.ai.svc:8000/v1"
	value.Model.Name = "qwen-coder"
	value.MCP = append(value.MCP,
		mcpBinding{Name: "jira", Mode: "shared", Endpoint: "https://mcp.example.test/mcp"},
		mcpBinding{Name: "filesystem", Mode: "sidecar", Image: "mcp/filesystem:1", Port: 8100},
		mcpBinding{Name: "database", Mode: "dedicated", Image: "mcp/database:1", Port: 8200},
	)

	runtimeRaw, openRaw, hermesRaw := runtimeConfigs("agent-runtime-dev", "agent-alice-1234", value)
	for _, expected := range []string{"http://127.0.0.1:8100/mcp", "agent-alice-1234-mcp-database.agent-runtime-dev.svc:8200", "https://mcp.example.test/mcp"} {
		if !strings.Contains(runtimeRaw, expected) || !strings.Contains(openRaw, expected) || !strings.Contains(hermesRaw, expected) {
			t.Fatalf("compiled configurations do not contain %q", expected)
		}
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(openRaw), &config); err != nil {
		t.Fatalf("OpenCode config is not JSON: %v", err)
	}
	if config["model"] != "agenthub/qwen-coder" {
		t.Fatalf("unexpected OpenCode model: %#v", config["model"])
	}
	// The model reference is useless if it names a provider the config never declares.
	providers, ok := config["provider"].(map[string]any)
	if !ok || providers["agenthub"] == nil {
		t.Fatalf("OpenCode config does not declare the referenced provider: %#v", config["provider"])
	}
}

func TestStatefulSetRollsPodWhenCompiledConfigChanges(t *testing.T) {
	newOwner := func() *unstructured.Unstructured {
		owner := &unstructured.Unstructured{}
		owner.SetAPIVersion("agenthub.io/v1alpha1")
		owner.SetKind("AgentRuntime")
		owner.SetName("cfg-runtime")
		owner.SetNamespace("agent-runtime-dev")
		owner.SetUID(types.UID("test-owner"))
		return owner
	}
	build := func(mutate func(*spec), annotations map[string]string) map[string]string {
		client := fake.NewSimpleClientset()
		controller := &Controller{client: client}
		owner := newOwner()
		if annotations != nil {
			owner.SetAnnotations(annotations)
		}
		var value spec
		value.Owner = "user-1"
		value.Runtime.Type = "opencode"
		value.Runtime.Image = "agenthub-base:v0.3.1"
		value.Model.BaseURL = "http://gateway.svc:8000/v1"
		value.Model.Name = "qwen"
		mutate(&value)
		if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "cfg-runtime", "cfg-workspace", value, owner); err != nil {
			t.Fatal(err)
		}
		statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "cfg-runtime", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return statefulSet.Spec.Template.ObjectMeta.Annotations
	}

	base := build(func(*spec) {}, nil)
	if base["agenthub.io/config-hash"] == "" {
		t.Fatal("Pod template must carry the compiled-config hash")
	}
	same := build(func(*spec) {}, nil)
	if same["agenthub.io/config-hash"] != base["agenthub.io/config-hash"] {
		t.Fatal("an unchanged configuration must not roll the Pod")
	}
	changed := build(func(value *spec) {
		value.MCP = append(value.MCP, mcpBinding{Name: "context7", Mode: "shared", Endpoint: "https://mcp.context7.com/mcp"})
	}, nil)
	if changed["agenthub.io/config-hash"] == base["agenthub.io/config-hash"] {
		t.Fatal("binding an MCP server must roll the Pod so the runtime picks it up")
	}
	restarted := build(func(*spec) {}, map[string]string{"agenthub.io/restarted-at": "2026-08-15T00:00:00Z"})
	if restarted["agenthub.io/restarted-at"] != "2026-08-15T00:00:00Z" {
		t.Fatal("an explicit restart must reach the Pod template")
	}
}

func TestHermesStatefulSetUsesLoopbackDashboardAndAuthenticatedProxy(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("hermes-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "hermes"
	value.Runtime.Image = "agenthub-base:v0.1.0"
	value.Security.ReadOnlyRootFilesystem = true

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "hermes-runtime", "hermes-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "hermes-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	containers := map[string]struct {
		command   []string
		args      []string
		readiness string
		liveness  string
	}{}
	for _, container := range statefulSet.Spec.Template.Spec.Containers {
		readiness, liveness := "", ""
		if container.ReadinessProbe != nil && container.ReadinessProbe.HTTPGet != nil {
			readiness = container.ReadinessProbe.HTTPGet.Path
		}
		if container.LivenessProbe != nil && container.LivenessProbe.HTTPGet != nil {
			liveness = container.LivenessProbe.HTTPGet.Path
		}
		containers[container.Name] = struct {
			command   []string
			args      []string
			readiness string
			liveness  string
		}{container.Command, container.Args, readiness, liveness}
	}
	dashboard, ok := containers["hermes-dashboard"]
	if !ok || !strings.Contains(strings.Join(dashboard.args, " "), "--host 127.0.0.1 --port 9120") {
		t.Fatalf("Hermes Dashboard is not loopback-only: %#v", dashboard)
	}
	proxy, ok := containers["hermes-dashboard-proxy"]
	if !ok || strings.Join(proxy.command, " ") != "/usr/local/bin/agenthub-runtime-proxy" || proxy.readiness != "/healthz" || proxy.liveness != "/livez" {
		t.Fatalf("authenticated Dashboard proxy is incomplete: %#v", proxy)
	}
	if statefulSet.Spec.Template.Spec.AutomountServiceAccountToken == nil || *statefulSet.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("Runtime Pod must not receive a Kubernetes service account token")
	}
}

func TestQwenPawStatefulSetIsFrontedByAuthenticatedProxy(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("qwenpaw-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "qwenpaw"
	value.Runtime.Image = "agenthub-base:v0.3.1"
	value.Security.ReadOnlyRootFilesystem = true

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "qwenpaw-runtime", "qwenpaw-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "qwenpaw-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var agent, proxy *corev1.Container
	for index, container := range statefulSet.Spec.Template.Spec.Containers {
		switch container.Name {
		case "agent":
			agent = &statefulSet.Spec.Template.Spec.Containers[index]
		case "qwenpaw-proxy":
			proxy = &statefulSet.Spec.Template.Spec.Containers[index]
		}
	}
	if agent == nil || !strings.Contains(strings.Join(agent.Args, " "), "qwenpaw app --host 0.0.0.0 --port 8642") {
		t.Fatalf("QwenPaw agent container does not start the app: %#v", agent)
	}
	if proxy == nil || strings.Join(proxy.Command, " ") != "/usr/local/bin/agenthub-runtime-proxy" {
		t.Fatalf("QwenPaw must be published through the authenticated proxy: %#v", proxy)
	}
	target := ""
	for _, item := range proxy.Env {
		if item.Name == "AGENTHUB_RUNTIME_PROXY_TARGET" {
			target = item.Value
		}
	}
	if target != "http://127.0.0.1:8642" {
		t.Fatalf("proxy target = %q, want the loopback QwenPaw app", target)
	}
	if err := controller.ensureService(context.Background(), "agent-runtime-dev", "qwenpaw-runtime", value, owner); err != nil {
		t.Fatal(err)
	}
	service, err := client.CoreV1().Services("agent-runtime-dev").Get(context.Background(), "qwenpaw-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	published := map[int32]bool{}
	for _, port := range service.Spec.Ports {
		published[port.Port] = true
	}
	if !published[runtimetype.GatewayPort] {
		t.Fatalf("QwenPaw Service does not publish the proxy port: %#v", service.Spec.Ports)
	}
}

func TestEndpointPort(t *testing.T) {
	tests := map[string]int32{
		"https://gateway.example/v1":      443,
		"http://gateway.example/v1":       80,
		"http://gateway.example:11434/v1": 11434,
		"not a url":                       0,
	}
	for input, expected := range tests {
		if got := endpointPort(input); got != expected {
			t.Errorf("endpointPort(%q)=%d, want %d", input, got, expected)
		}
	}
}

func TestSidecarsUseRestrictedSecurityContext(t *testing.T) {
	var value spec
	value.MCP = append(value.MCP, mcpBinding{Name: "git", Mode: "sidecar", Image: "mcp/git:1", Port: 8000})
	containers := sidecarContainers(value)
	if len(containers) != 1 {
		t.Fatalf("got %d sidecars, want 1", len(containers))
	}
	security := containers[0].SecurityContext
	if security == nil || security.AllowPrivilegeEscalation == nil || *security.AllowPrivilegeEscalation || security.RunAsNonRoot == nil || !*security.RunAsNonRoot {
		t.Fatal("sidecar security context is not restricted")
	}
	if len(security.Capabilities.Drop) != 1 || security.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("sidecar must drop all capabilities: %#v", security.Capabilities)
	}
}

func TestRuntimeHomeIsPersistedNotEphemeral(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("home-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "qwenpaw"
	value.Runtime.Image = "agenthub-base:v0.4.0"

	if err := controller.ensureHomePVC(context.Background(), "agent-runtime-dev", "home-runtime", owner); err != nil {
		t.Fatal(err)
	}
	claim, err := client.CoreV1().PersistentVolumeClaims("agent-runtime-dev").Get(context.Background(), "home-runtime-home", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("home volume was not provisioned: %v", err)
	}
	if len(claim.OwnerReferences) == 0 {
		t.Fatal("home volume must be owned by the runtime so it is collected with the agent")
	}

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "home-runtime", "home-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "home-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range statefulSet.Spec.Template.Spec.Volumes {
		if volume.Name != "home" {
			continue
		}
		if volume.EmptyDir != nil {
			t.Fatal("adapter state under /home/agent must not live on an emptyDir")
		}
		if volume.PersistentVolumeClaim == nil || volume.PersistentVolumeClaim.ClaimName != "home-runtime-home" {
			t.Fatalf("home volume is not backed by the runtime's claim: %#v", volume.VolumeSource)
		}
		return
	}
	t.Fatal("Pod template has no home volume")
}

func TestEgressPeersNarrowDestinationsBeyondThePort(t *testing.T) {
	tests := map[string]struct {
		endpoint  string
		resolved  bool
		cidr      string
		namespace string
		inPod     bool
	}{
		"literal IPv4 is pinned to a single host": {
			endpoint: "http://222.107.52.227:11300/v1", resolved: true, cidr: "222.107.52.227/32",
		},
		"literal IPv6 is pinned to a single host": {
			endpoint: "http://[2001:db8::1]:8000/v1", resolved: true, cidr: "2001:db8::1/128",
		},
		"cluster service is scoped to its namespace": {
			endpoint: "http://ollama.agent-platform-system.svc:11434/v1", resolved: true, namespace: "agent-platform-system",
		},
		"fully qualified cluster service is scoped too": {
			endpoint: "http://mcp.tools.svc.cluster.local:8000/mcp", resolved: true, namespace: "tools",
		},
		"loopback sidecars need no egress rule": {
			endpoint: "http://127.0.0.1:8100/mcp", resolved: true, inPod: true,
		},
		// NetworkPolicy cannot match a hostname, so this must stay honest rather
		// than silently pretending to be constrained.
		"public DNS name cannot be narrowed": {
			endpoint: "https://mcp.context7.com/mcp", resolved: false,
		},
	}
	for name, test := range tests {
		peers, resolved := egressPeers(test.endpoint)
		if resolved != test.resolved {
			t.Errorf("%s: resolved=%v, want %v", name, resolved, test.resolved)
			continue
		}
		if !resolved {
			continue
		}
		if test.inPod {
			if len(peers) != 0 {
				t.Errorf("%s: expected no peers, got %#v", name, peers)
			}
			continue
		}
		if len(peers) != 1 {
			t.Errorf("%s: expected one peer, got %#v", name, peers)
			continue
		}
		if test.cidr != "" && (peers[0].IPBlock == nil || peers[0].IPBlock.CIDR != test.cidr) {
			t.Errorf("%s: CIDR = %#v, want %q", name, peers[0].IPBlock, test.cidr)
		}
		if test.namespace != "" {
			if peers[0].NamespaceSelector == nil || peers[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != test.namespace {
				t.Errorf("%s: namespace selector = %#v, want %q", name, peers[0].NamespaceSelector, test.namespace)
			}
		}
	}
}

func TestNetworkPolicyBindsModelEgressToItsHost(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("np-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.Network.DefaultDeny = true
	value.Network.AllowDNS = true
	value.Model.BaseURL = "http://10.0.0.5:11300/v1"

	if err := controller.ensureNetworkPolicy(context.Background(), "agent-runtime-dev", "np-runtime", value, owner); err != nil {
		t.Fatal(err)
	}
	policy, err := client.NetworkingV1().NetworkPolicies("agent-runtime-dev").Get(context.Background(), "np-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range policy.Spec.Egress {
		for _, port := range rule.Ports {
			if port.Port == nil || port.Port.IntValue() != 11300 {
				continue
			}
			if len(rule.To) == 0 {
				t.Fatal("the model port is open to every destination, not just the model host")
			}
			if rule.To[0].IPBlock == nil || rule.To[0].IPBlock.CIDR != "10.0.0.5/32" {
				t.Fatalf("model egress is not pinned to its host: %#v", rule.To[0])
			}
			return
		}
	}
	t.Fatal("no egress rule was created for the model port")
}

func TestMCPCredentialsNeverReachTheConfigMap(t *testing.T) {
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.MCP = append(value.MCP,
		mcpBinding{Name: "context7", Mode: "shared", Endpoint: "https://mcp.context7.com/mcp", AuthType: "bearer", CredentialKey: "mcp-credential-context7"},
		mcpBinding{Name: "jira", Mode: "shared", Endpoint: "https://jira.test/mcp", AuthType: "header", AuthHeader: "X-Api-Key", CredentialKey: "mcp-credential-jira"},
		mcpBinding{Name: "public", Mode: "shared", Endpoint: "https://public.test/mcp"},
	)

	_, openRaw, hermesRaw := runtimeConfigs("agent-runtime-dev", "agent-1", value)
	for _, raw := range []string{openRaw, hermesRaw} {
		if !strings.Contains(raw, "Bearer ${AGENTHUB_MCP_CONTEXT7}") {
			t.Fatalf("bearer auth is not compiled as an env placeholder: %s", raw)
		}
		if !strings.Contains(raw, "X-Api-Key") || !strings.Contains(raw, "${AGENTHUB_MCP_JIRA}") {
			t.Fatalf("custom header auth is missing: %s", raw)
		}
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(openRaw), &config); err != nil {
		t.Fatal(err)
	}
	servers := config["mcp"].(map[string]any)
	if _, hasHeaders := servers["public"].(map[string]any)["headers"]; hasHeaders {
		t.Fatal("an unauthenticated server must not be given headers")
	}
}

func TestMCPCredentialEnvIsASafeVariableName(t *testing.T) {
	tests := map[string]string{
		"mcp-credential-context7":   "AGENTHUB_MCP_CONTEXT7",
		"mcp-credential-jira-cloud": "AGENTHUB_MCP_JIRA_CLOUD",
		"mcp-credential-team.tools": "AGENTHUB_MCP_TEAM_TOOLS",
	}
	for key, want := range tests {
		if got := mcpCredentialEnv(key); got != want {
			t.Errorf("mcpCredentialEnv(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestStatefulSetMountsMCPCredentialsFromTheRuntimeSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("mcp-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.MCP = append(value.MCP, mcpBinding{Name: "context7", Mode: "shared", Endpoint: "https://mcp.context7.com/mcp", AuthType: "bearer", CredentialKey: "mcp-credential-context7"})

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "mcp-runtime", "mcp-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "mcp-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range statefulSet.Spec.Template.Spec.Containers[0].Env {
		if item.Name != "AGENTHUB_MCP_CONTEXT7" {
			continue
		}
		if item.Value != "" {
			t.Fatal("the credential must come from the Secret, not a literal value")
		}
		if item.ValueFrom == nil || item.ValueFrom.SecretKeyRef == nil || item.ValueFrom.SecretKeyRef.Key != "mcp-credential-context7" {
			t.Fatalf("credential is not sourced from the runtime Secret: %#v", item.ValueFrom)
		}
		// Optional so a server whose credential is not configured yet still starts.
		if item.ValueFrom.SecretKeyRef.Optional == nil || !*item.ValueFrom.SecretKeyRef.Optional {
			t.Fatal("a missing credential must not block the Pod from starting")
		}
		return
	}
	t.Fatal("no MCP credential environment variable was injected")
}

func TestGitCloneUsesTheWorkspaceCredentialWithoutExposingIt(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("git-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.Workspace.Type = "git"
	value.Workspace.RepositoryURL = "https://bitbucket.corp/team/project.git"
	value.Workspace.GitCredentialKind = "token"
	value.Workspace.GitCredentialUsername = "build-bot"

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "git-runtime", "git-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, err := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "git-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var clone *corev1.Container
	for index, container := range statefulSet.Spec.Template.Spec.InitContainers {
		if container.Name == "workspace-git-clone" {
			clone = &statefulSet.Spec.Template.Spec.InitContainers[index]
		}
	}
	if clone == nil {
		t.Fatal("no clone init container was created")
	}
	script := strings.Join(clone.Args, " ")
	// The credential must never reach a command line or the remote URL, where it
	// would be visible in the process table and in git's own error output.
	if strings.Contains(script, "$GIT_CREDENTIAL@") || strings.Contains(script, "--password") {
		t.Fatalf("the clone script embeds the credential unsafely: %s", script)
	}
	if !strings.Contains(script, "credential.helper=store") || !strings.Contains(script, "GIT_SSH_COMMAND") {
		t.Fatalf("the clone script supports neither token nor key auth: %s", script)
	}
	var fromSecret bool
	for _, item := range clone.Env {
		if item.Name != "GIT_CREDENTIAL" {
			continue
		}
		if item.Value != "" {
			t.Fatal("the credential must come from the Secret, not a literal value")
		}
		if item.ValueFrom == nil || item.ValueFrom.SecretKeyRef == nil || item.ValueFrom.SecretKeyRef.Key != "workspace-git-credential" {
			t.Fatalf("credential is not sourced from the runtime Secret: %#v", item.ValueFrom)
		}
		fromSecret = true
	}
	if !fromSecret {
		t.Fatal("the clone container never receives the credential")
	}
}

func TestGitCloneOmitsTheCredentialForPublicRepositories(t *testing.T) {
	client := fake.NewSimpleClientset()
	controller := &Controller{client: client}
	owner := &unstructured.Unstructured{}
	owner.SetAPIVersion("agenthub.io/v1alpha1")
	owner.SetKind("AgentRuntime")
	owner.SetName("public-runtime")
	owner.SetNamespace("agent-runtime-dev")
	owner.SetUID(types.UID("test-owner"))
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.Workspace.Type = "git"
	value.Workspace.RepositoryURL = "https://github.com/example/public.git"

	if err := controller.ensureStatefulSet(context.Background(), "agent-runtime-dev", "public-runtime", "public-workspace", value, owner); err != nil {
		t.Fatal(err)
	}
	statefulSet, _ := client.AppsV1().StatefulSets("agent-runtime-dev").Get(context.Background(), "public-runtime", metav1.GetOptions{})
	for _, container := range statefulSet.Spec.Template.Spec.InitContainers {
		if container.Name != "workspace-git-clone" {
			continue
		}
		for _, item := range container.Env {
			if item.Name == "GIT_CREDENTIAL" {
				t.Fatal("an unbound workspace must not request a credential from the Secret")
			}
		}
	}
}

// Every supported runtime must be registered; a missing adapter would produce a
// Pod with no command rather than an obvious failure.
func TestEveryRuntimeTypeHasAnAdapter(t *testing.T) {
	for _, name := range runtimetype.Supported {
		if name == runtimetype.Custom {
			// Custom runtimes carry their own command through the image entrypoint.
			continue
		}
		adapter := adapterFor(name)
		if adapter.Type != name {
			t.Errorf("runtime %q has no adapter registered", name)
			continue
		}
		if len(adapter.Command) == 0 {
			t.Errorf("adapter %q does not say how to start the agent", name)
		}
	}
}

// Runtimes published through the token proxy must actually register that sidecar.
func TestProxiedRuntimesRegisterTheAuthenticatingSidecar(t *testing.T) {
	build := adapterBuild{Name: "runtime-1", Value: spec{}}
	build.Value.Runtime.Image = "agenthub-base:v0.4.0"
	for _, name := range runtimetype.Supported {
		if !runtimetype.UsesGatewayProxy(name) {
			continue
		}
		adapter := adapterFor(name)
		if adapter.Sidecars == nil {
			t.Errorf("%s is published through the proxy but registers no sidecars", name)
			continue
		}
		var proxied bool
		for _, container := range adapter.Sidecars(build) {
			for _, port := range container.Ports {
				if port.ContainerPort == runtimetype.GatewayPort {
					proxied = true
				}
			}
		}
		if !proxied {
			t.Errorf("%s registers no sidecar listening on the gateway port", name)
		}
	}
}

// A tool policy that the agent process could route around would not be a policy,
// so a policied binding must reach the agent as the loopback gateway address and
// the real upstream must only be known to the gateway container.
func TestPoliciedMCPBindingIsRoutedThroughTheGateway(t *testing.T) {
	var value spec
	value.Owner = "user-1"
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.7.0"
	value.MCP = append(value.MCP,
		mcpBinding{Name: "context7", Mode: "shared", Endpoint: "https://mcp.context7.test/mcp",
			AuthType: "bearer", CredentialKey: "mcp-credential-context7",
			ToolPolicy: &mcpToolPolicy{Mode: "allow", Tools: []string{"resolve-library-id"}}},
		mcpBinding{Name: "jira", Mode: "shared", Endpoint: "https://mcp.jira.test/mcp"},
	)

	bindings := effectiveMCP("agent-runtime-dev", "rt-1", value)
	policied, plain := bindings[0], bindings[1]
	if got := policied["endpoint"]; got != "http://127.0.0.1:9129/mcp/context7" {
		t.Fatalf("policied endpoint = %v, want the loopback gateway", got)
	}
	if got := policied["upstream"]; got != "https://mcp.context7.test/mcp" {
		t.Fatalf("the real upstream must be preserved for the gateway, got %v", got)
	}
	// A binding with no policy keeps talking to its server directly.
	if got := plain["endpoint"]; got != "https://mcp.jira.test/mcp" {
		t.Fatalf("unpolicied endpoint = %v, want the server itself", got)
	}
	if _, ok := plain["toolPolicyMode"]; ok {
		t.Fatal("a binding with no policy must not be marked as policied")
	}

	_, openRaw, hermesRaw := runtimeConfigs("agent-runtime-dev", "rt-1", value)
	for _, raw := range []string{openRaw, hermesRaw} {
		if strings.Contains(raw, "mcp.context7.test") {
			t.Fatalf("the agent config must not learn the upstream address:\n%s", raw)
		}
		// The credential belongs to the gateway now, not the agent process.
		if strings.Contains(raw, "AGENTHUB_MCP_CONTEXT7") {
			t.Fatalf("a policied binding must not hand the credential to the agent:\n%s", raw)
		}
		if !strings.Contains(raw, "127.0.0.1:9129/mcp/context7") {
			t.Fatalf("the agent should be pointed at the gateway:\n%s", raw)
		}
	}

	gateway, ok := mcpGatewayContainer(value.Runtime.Image, "rt-1", bindings, value.MCP)
	if !ok {
		t.Fatal("a policied binding must produce a gateway container")
	}
	var config, credential string
	for _, env := range gateway.Env {
		switch env.Name {
		case "AGENTHUB_MCP_GATEWAY":
			config = env.Value
		case "AGENTHUB_MCP_CONTEXT7":
			if env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
				t.Fatal("the credential must come from the runtime Secret, not a literal")
			}
			credential = env.ValueFrom.SecretKeyRef.Key
		}
	}
	if credential != "mcp-credential-context7" {
		t.Fatalf("gateway credential key = %q", credential)
	}
	var upstreams []struct {
		Name         string   `json:"name"`
		Upstream     string   `json:"upstream"`
		AuthTemplate string   `json:"authTemplate"`
		Mode         string   `json:"mode"`
		Tools        []string `json:"tools"`
	}
	if err := json.Unmarshal([]byte(config), &upstreams); err != nil {
		t.Fatalf("gateway config is not valid JSON: %v (%s)", err, config)
	}
	if len(upstreams) != 1 || upstreams[0].Upstream != "https://mcp.context7.test/mcp" {
		t.Fatalf("only the policied server belongs in the gateway: %#v", upstreams)
	}
	if upstreams[0].Mode != "allow" || len(upstreams[0].Tools) != 1 {
		t.Fatalf("policy was not carried through: %#v", upstreams[0])
	}
	// The template is what lets the gateway substitute the secret it holds.
	if upstreams[0].AuthTemplate != "Bearer %s" {
		t.Fatalf("auth template = %q, want a substitutable template", upstreams[0].AuthTemplate)
	}
}

func TestNoGatewayContainerWithoutAPolicy(t *testing.T) {
	var value spec
	value.Runtime.Type = "opencode"
	value.MCP = append(value.MCP, mcpBinding{Name: "jira", Mode: "shared", Endpoint: "https://mcp.jira.test/mcp"})
	if _, ok := mcpGatewayContainer("image", "rt-1", effectiveMCP("ns", "rt-1", value), value.MCP); ok {
		t.Fatal("a runtime with no tool policy must not carry the gateway sidecar")
	}
}

// Pinning an agent to an older runtime image must not pin the platform's own
// sidecars with it, or a policy shipped in this release would silently run last
// release's code — or, as it did once, crash-loop.
func TestPlatformSidecarsRunTheControlPlaneImage(t *testing.T) {
	var value spec
	value.Runtime.Type = "opencode"
	value.Runtime.Image = "agenthub-base:v0.4.0"
	value.Runtime.SidecarImage = "agenthub:v0.7.0"
	value.MCP = append(value.MCP, mcpBinding{Name: "context7", Mode: "shared", Endpoint: "https://mcp.context7.test/mcp",
		ToolPolicy: &mcpToolPolicy{Mode: "allow", Tools: []string{"resolve-library-id"}}})

	gateway, ok := mcpGatewayContainer(value.sidecarImage(), "rt-1", effectiveMCP("ns", "rt-1", value), value.MCP)
	if !ok || gateway.Image != "agenthub:v0.7.0" {
		t.Fatalf("gateway image = %q, want the control plane image", gateway.Image)
	}
	// An object written before sidecarImage existed still has to work.
	value.Runtime.SidecarImage = ""
	if got := value.sidecarImage(); got != "agenthub-base:v0.4.0" {
		t.Fatalf("fallback image = %q, want the runtime image", got)
	}
}
