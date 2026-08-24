package runtime

import (
	"context"
	"fmt"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// Whether this deployment can actually do the things it is about to promise.
//
// The Kubernetes settings are a form: a mode, an address, a token, a namespace.
// Saving it proves the form was filled in. Whether the address answers, whether
// the token is accepted, whether the namespace exists, whether the CRD is
// installed and whether this service account may do each of the things the
// platform does are five separate questions, and every one of them was answered
// the same way — by a runtime that failed to start, hours later, for somebody
// else.
//
// The permission checks matter most. Kubernetes refuses a verb it was not
// granted with an error that names the verb, the resource and the account, which
// is perfectly clear and arrives at three in the morning in a log nobody is
// reading. The API server will answer the same question in advance, and it is
// the only component entitled to: SelfSubjectAccessReview is the cluster's own
// opinion about this account, not the platform's guess.

// Checker is implemented by a spawner that can be asked about its cluster.
// It is separate from Spawner so that a deployment running without Kubernetes
// keeps a spawner that cannot pretend to check one.
type Checker interface {
	CheckCluster(ctx context.Context) (ClusterCheck, error)
}

// ClusterCheck is what the cluster said about itself.
type ClusterCheck struct {
	ServerVersion  string `json:"serverVersion"`
	Namespace      string `json:"namespace"`
	NamespaceFound bool   `json:"namespaceFound"`
	CRDExpected    bool   `json:"crdExpected"`
	CRDInstalled   bool   `json:"crdInstalled"`
	// SnapshotsInstalled reports whether the cluster has the VolumeSnapshot API.
	// Without it workspace snapshots cannot work, and the platform offers the
	// button either way — so it is worth saying here rather than at the moment
	// somebody tries.
	SnapshotsInstalled bool `json:"snapshotsInstalled"`
	// CRDRuntimeTypes is what the installed definition will accept. Upgrading the
	// control plane does not upgrade the definition, so a build that knows about
	// a runtime type the cluster has never heard of accepts the agent, accepts
	// the image, and fails at spawn with a Kubernetes validation error — the one
	// place nobody was looking. Empty when the definition could not be read,
	// which is not the same as a definition that accepts nothing.
	CRDRuntimeTypes []string     `json:"crdRuntimeTypes,omitempty"`
	Permissions     []Permission `json:"permissions"`
	// Scope names whose permissions these are. The operator runs under its own
	// account and writes the Pods, volumes and network policies; the cluster will
	// only answer about the caller, so saying nothing here would let somebody read
	// this as a report on the whole deployment.
	Scope string `json:"scope"`
}

// Permission is one thing the platform does, and whether this account may do it.
type Permission struct {
	// What names the action in the words an operator uses, not in the API's.
	What     string `json:"what"`
	Verb     string `json:"verb"`
	Resource string `json:"resource"`
	Allowed  bool   `json:"allowed"`
	// Reason is the cluster's own explanation when it refuses, which usually
	// names the role that would have to grant it.
	Reason string `json:"reason,omitempty"`
}

// clusterActions is what the control plane itself calls, and nothing else.
//
// The first draft of this list asked about ConfigMaps, volumes and network
// policies, and every correctly configured deployment would have been told three
// permissions were missing. Those objects are written by the operator, which runs
// under its own account: the control plane writes AgentRuntime objects and the
// operator turns them into Pods and everything around them. A check that reports
// a healthy deployment as broken is worse than no check, because the next real
// warning is read as another false one.
//
// So each entry below corresponds to a call in this package. SelfSubjectAccessReview
// answers only for the caller, which is why this check covers the control plane
// and says so rather than guessing about the operator.
var clusterActions = []struct {
	what     string
	verb     string
	group    string
	resource string
}{
	{"Runtime 정의 생성", "create", "agenthub.io", "agentruntimes"},
	{"Runtime 정의 조회", "list", "agenthub.io", "agentruntimes"},
	{"Runtime 정의 수정", "update", "agenthub.io", "agentruntimes"},
	{"Runtime 정의 삭제", "delete", "agenthub.io", "agentruntimes"},
	{"Pod 상태 조회", "get", "", "pods"},
	{"Pod 로그 조회", "get", "", "pods/log"},
	{"Runtime 안에서 명령 실행", "create", "", "pods/exec"},
	{"Runtime Secret 생성", "create", "", "secrets"},
	{"Runtime Secret 삭제", "delete", "", "secrets"},
	{"작업공간 스냅샷 생성", "create", "snapshot.storage.k8s.io", "volumesnapshots"},
}

// CheckCluster asks the cluster the five questions in order. A failure to reach
// it at all stops there, because every later answer would be the same failure
// repeated.
func (k *KubernetesSpawner) CheckCluster(ctx context.Context) (ClusterCheck, error) {
	dynamicClient, coreClient, _, settings, err := k.clients(ctx)
	if err != nil {
		return ClusterCheck{}, err
	}
	namespace := settings.Namespace
	if namespace == "" {
		namespace = "agent-runtime-dev"
	}
	out := ClusterCheck{Namespace: namespace, CRDExpected: settings.CRDEnabled}

	version, err := coreClient.Discovery().ServerVersion()
	if err != nil {
		return out, fmt.Errorf("API 서버에 연결하지 못했습니다: %w", err)
	}
	out.ServerVersion = version.GitVersion

	if _, err := coreClient.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err == nil {
		out.NamespaceFound = true
	} else if !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return out, fmt.Errorf("네임스페이스를 확인하지 못했습니다: %w", err)
	} else if apierrors.IsForbidden(err) {
		// Reading namespaces is not something this platform needs; being refused
		// here says nothing about whether the namespace is usable.
		out.NamespaceFound = true
	}

	if settings.CRDEnabled {
		_, listErr := dynamicClient.Resource(runtimeGVR).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
		out.CRDInstalled = listErr == nil || apierrors.IsForbidden(listErr)
		out.CRDRuntimeTypes = definitionRuntimeTypes(ctx, dynamicClient)
	}
	// A cluster without the snapshot API refuses the list with NotFound, which is
	// a different answer from being refused permission to read it.
	_, snapshotErr := dynamicClient.Resource(volumeSnapshotGVR).Namespace(namespace).List(ctx, metav1.ListOptions{Limit: 1})
	out.SnapshotsInstalled = snapshotErr == nil || apierrors.IsForbidden(snapshotErr)
	out.Scope = "컨트롤 플레인"

	for _, action := range clusterActions {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Namespace: namespace, Verb: action.verb, Group: action.group,
					Resource: strings.SplitN(action.resource, "/", 2)[0],
				},
			},
		}
		if at := strings.Index(action.resource, "/"); at >= 0 {
			review.Spec.ResourceAttributes.Subresource = action.resource[at+1:]
		}
		answer, reviewErr := coreClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		permission := Permission{What: action.what, Verb: action.verb, Resource: action.resource}
		switch {
		case reviewErr != nil:
			// The cluster would not answer the question. Reporting that as "not
			// allowed" would send somebody to fix a role that is probably fine.
			permission.Reason = "확인하지 못했습니다: " + reviewErr.Error()
		default:
			permission.Allowed = answer.Status.Allowed
			permission.Reason = answer.Status.Reason
		}
		out.Permissions = append(out.Permissions, permission)
	}
	return out, nil
}

// definitionRuntimeTypes reads the runtime types the installed definition
// accepts.
//
// Silent about every failure on purpose: reading a cluster-scoped definition is
// a permission many deployments will not have granted, and a readiness report
// that turns "I may not look" into "your cluster is wrong" is worse than one
// that says nothing about it.
func definitionRuntimeTypes(ctx context.Context, client dynamic.Interface) []string {
	definition, err := client.Resource(definitionGVR).Get(ctx, "agentruntimes.agenthub.io", metav1.GetOptions{})
	if err != nil {
		return nil
	}
	versions, found, err := unstructured.NestedSlice(definition.Object, "spec", "versions")
	if err != nil || !found {
		return nil
	}
	for _, version := range versions {
		entry, ok := version.(map[string]any)
		if !ok {
			continue
		}
		values, found, err := unstructured.NestedStringSlice(entry,
			"schema", "openAPIV3Schema", "properties", "spec", "properties",
			"runtime", "properties", "type", "enum")
		if err == nil && found && len(values) > 0 {
			return values
		}
	}
	return nil
}
