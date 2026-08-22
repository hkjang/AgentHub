package runtime

import (
	"crypto/x509"
	"errors"
	"net"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ClusterRefusal names what the cluster said, in words the person can act on.
//
// Pressing start on a deployment whose cluster token is no longer accepted
// answered 500 "요청을 처리하지 못했습니다: Unauthorized" — the platform reporting
// itself broken, in a mix of two languages, for something that is neither the
// platform's fault nor the person's. The deployment check, asked about the same
// cluster at the same moment, said it plainly:
//
//	{"reachable": false, "detail": "the server has asked for the client to provide credentials"}
//
// One path explained it and its siblings dumped the upstream error. This is the
// explanation, put where every cluster-touching route already goes.
//
// The second return is false for anything that is not the cluster refusing, so
// a real fault still reports as a fault.
func ClusterRefusal(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	// Plain text: this reaches an error banner that renders exactly what it is
	// given, so the emphasis marks the documentation uses would show as asterisks.
	const check = "시스템 설정 ▸ Kubernetes 의 [지금 확인] 으로 점검할 수 있습니다."
	switch {
	case apierrors.IsUnauthorized(err):
		return "클러스터가 이 배포의 자격 증명을 받아들이지 않습니다. 토큰이 만료됐거나 잘못됐습니다 — " + check, true
	case apierrors.IsForbidden(err):
		return "클러스터가 이 작업을 이 배포의 계정에 허용하지 않습니다. ServiceAccount 권한을 확인해 주세요 — " + check, true
	case apierrors.IsServiceUnavailable(err), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return "클러스터가 제때 답하지 않았습니다. 잠시 뒤 다시 시도하거나, " + check, true
	}
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &authority) || errors.As(err, &hostname) {
		return "클러스터의 인증서를 확인하지 못했습니다. CA 인증서나 TLS 검증 설정을 확인해 주세요 — " + check, true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "클러스터 주소를 찾지 못했습니다(" + dns.Name + "). 주소를 확인해 주세요 — " + check, true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "클러스터에 연결하지 못했습니다. 주소와 네트워크 경로를 확인해 주세요 — " + check, true
	}
	// A transport failure that arrives as text rather than as a typed error still
	// has to read as the cluster's answer, not as a platform fault.
	text := strings.ToLower(err.Error())
	for _, phrase := range []string{"connection refused", "no such host", "i/o timeout", "tls handshake", "certificate signed by unknown authority", "context deadline exceeded"} {
		if strings.Contains(text, phrase) {
			return "클러스터에 연결하지 못했습니다: " + err.Error() + " — " + check, true
		}
	}
	return "", false
}
