package runtime

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestTheClusterRefusingIsNotThePlatformFailing(t *testing.T) {
	resource := schema.GroupResource{Group: "agenthub.io", Resource: "agentruntimes"}
	for _, one := range []struct {
		name  string
		err   error
		names string
	}{
		{"a token the cluster does not accept", apierrors.NewUnauthorized("Unauthorized"), "자격 증명"},
		{"a permission the account does not have", apierrors.NewForbidden(resource, "x", errors.New("nope")), "허용하지 않습니다"},
		{"a cluster that did not answer", apierrors.NewTimeoutError("too slow", 1), "제때 답하지"},
		{"an address that does not resolve", &net.DNSError{Err: "no such host", Name: "cluster.invalid"}, "cluster.invalid"},
		{"a connection nobody accepted", fmt.Errorf("dial tcp 10.0.0.1:6443: %w", errors.New("connect: connection refused")), "연결하지 못했습니다"},
	} {
		message, refused := ClusterRefusal(one.err)
		if !refused {
			t.Errorf("%s is reported as a platform fault; the person is sent looking for a bug that is not there", one.name)
			continue
		}
		if !strings.Contains(message, one.names) {
			t.Errorf("%s: the message does not say what happened: %s", one.name, message)
		}
		if !strings.Contains(message, "지금 확인") {
			t.Errorf("%s: the message does not say where to look: %s", one.name, message)
		}
		if strings.ContainsAny(message, "*") {
			t.Errorf("%s: the message carries emphasis marks into an error banner that renders them literally: %s", one.name, message)
		}
	}
}

// The important negative: a real fault has to keep reporting as a fault. A
// classifier that swallows everything turns every bug into "the cluster is
// having a moment".
func TestAnOrdinaryFailureIsStillAFailure(t *testing.T) {
	for _, err := range []error{
		errors.New("no rows in result set"),
		errors.New("json: cannot unmarshal number into Go value of type string"),
		apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "runtime-1"),
		apierrors.NewAlreadyExists(schema.GroupResource{Resource: "secrets"}, "runtime-1"),
		&apierrors.StatusError{ErrStatus: metav1.Status{Code: 422, Message: "invalid"}},
		nil,
	} {
		if message, refused := ClusterRefusal(err); refused {
			t.Errorf("%v was reported as the cluster refusing: %s", err, message)
		}
	}
}
