package runtimecfg

import (
	"strings"

	"github.com/hkjang/AgentHub/internal/runtimetype"
)

// What sites usually want to set, and what the platform can honestly say about it.
//
// There are two kinds of entry here and the difference matters. A verified one
// names a key this platform already writes or a variable the operating system
// defines — we know it exists and what it does. An unverified one names a setting
// people ask for whose key belongs to the runtime's own version: we will inject
// whatever key an administrator gives us and report whether it landed in the file,
// but we will not guess the key on their behalf. Inventing one would produce a
// configuration that looks applied and does nothing, which is the exact failure
// this whole feature exists to remove.

// Target says where a setting goes.
const (
	// TargetConfig is a key inside the runtime's own configuration file, which the
	// platform generates and this overlays.
	TargetConfig = "config"
	// TargetEnv is an environment variable exported to every container.
	TargetEnv = "env"
)

// Suggestion is one setting the console offers.
type Suggestion struct {
	Target string `json:"target"`
	// Key is a dotted path for config entries (terminal.cwd) or a variable name for
	// env entries. Empty means the platform does not know the key for this runtime
	// version and the administrator has to supply it.
	Key   string `json:"key,omitempty"`
	Label string `json:"label"`
	// Description says what it does and, when the key is unknown, where to find it.
	Description string `json:"description"`
	// Example is a value that makes sense for a Korean offline deployment, as JSON.
	Example string `json:"example,omitempty"`
	// RuntimeTypes are the adapters this applies to. Empty means all of them.
	RuntimeTypes []string `json:"runtimeTypes,omitempty"`
	// Verified reports whether the platform knows this key is real. An unverified
	// suggestion is guidance, not a promise.
	Verified bool `json:"verified"`
}

var suggestions = []Suggestion{
	{
		Target: TargetEnv, Key: "LANG", Label: "언어·로케일", Verified: true,
		Description: "Pod 안 모든 컨테이너의 로케일입니다. 터미널과 CLI 도구의 메시지·정렬·인코딩에 적용됩니다. 런타임 제품 UI의 표시 언어는 제품 설정을 따릅니다.",
		Example:     `"ko_KR.UTF-8"`,
	},
	{
		Target: TargetEnv, Key: "LC_ALL", Label: "로케일 강제", Verified: true,
		Description: "LANG 을 무시하는 도구까지 로케일을 강제합니다. LANG 과 같은 값을 넣는 것이 보통입니다.",
		Example:     `"ko_KR.UTF-8"`,
	},
	{
		Target: TargetEnv, Key: "TZ", Label: "시간대", Verified: true,
		Description: "런타임 안에서 보이는 시간대입니다. 로그와 에이전트가 만드는 문서의 시각이 여기 따릅니다.",
		Example:     `"Asia/Seoul"`,
	},
	{
		Target: TargetEnv, Key: "HTTPS_PROXY", Label: "HTTPS 프록시", Verified: true,
		Description: "폐쇄망에서 외부로 나가는 요청이 통과할 프록시입니다. HTTP_PROXY·NO_PROXY 와 함께 설정하세요.",
		Example:     `"http://proxy.internal:3128"`,
	},
	{
		Target: TargetEnv, Key: "NO_PROXY", Label: "프록시 예외", Verified: true,
		Description: "프록시를 거치지 않을 대상입니다. 사내 모델 게이트웨이와 MCP 서버 주소를 반드시 넣으세요.",
		Example:     `"localhost,127.0.0.1,.svc,.cluster.local"`,
	},
	{
		Target: TargetConfig, Key: "autoupdate", Label: "자동 업데이트", Verified: true,
		RuntimeTypes: []string{runtimetype.OpenCode},
		Description:  "OpenCode 의 자동 업데이트입니다. 폐쇄망에서는 꺼 두는 것이 맞고, 플랫폼도 기본값을 false 로 생성합니다.",
		Example:      `false`,
	},
	{
		Target: TargetConfig, Key: "terminal.cwd", Label: "터미널 시작 디렉터리", Verified: true,
		RuntimeTypes: []string{runtimetype.Hermes},
		Description:  "Hermes 터미널이 열리는 경로입니다. 플랫폼은 작업공간(/workspace)으로 생성하므로, 하위 폴더에서 시작하고 싶을 때만 바꾸세요.",
		Example:      `"/workspace"`,
	},
	{
		Target: TargetEnv, Key: "LANGFLOW_LOG_LEVEL", Label: "Langflow 로그 수준", Verified: true,
		RuntimeTypes: []string{runtimetype.Langflow},
		Description:  "Langflow 서버의 로그 수준입니다. 기본값이 error 라 흐름이 왜 실패했는지 보이지 않는 경우가 많으므로, 도입 초기에는 info 를 권합니다.",
		Example:      `"info"`,
	},
	{
		Target: TargetEnv, Key: "LANGFLOW_WORKERS", Label: "Langflow 워커 수", Verified: true,
		RuntimeTypes: []string{runtimetype.Langflow},
		Description:  "흐름을 동시에 처리할 워커 프로세스 수입니다(기본 1). 늘리면 Runtime 프로파일의 메모리도 함께 올려야 합니다.",
		Example:      `"2"`,
	},
	{
		Target: TargetEnv, Key: "LANGFLOW_WORKER_TIMEOUT", Label: "Langflow 워커 타임아웃", Verified: true,
		RuntimeTypes: []string{runtimetype.Langflow},
		Description:  "한 요청이 이 시간(초)을 넘기면 워커가 끊습니다(기본 300). 오래 걸리는 흐름을 자동 실행할 때는 Goal 의 최대 실행 시간과 함께 올려 주세요.",
		Example:      `"600"`,
	},
	{
		Target: TargetEnv, Key: "LANGFLOW_DATABASE_URL", Label: "Langflow 데이터베이스", Verified: true,
		RuntimeTypes: []string{runtimetype.Langflow},
		Description:  "비우면 작업공간 볼륨의 SQLite 를 사용합니다. 사내 PostgreSQL 을 쓰면 Pod 를 다시 만들어도 흐름이 남습니다. 다만 이 값은 AgentRuntime 객체에 그대로 보이므로, 비밀번호가 들어간 접속 문자열은 권한을 좁힌 전용 계정으로 만드세요.",
		Example:      `"postgresql://langflow:***@postgres.agenthub.svc:5432/langflow"`,
	},
	{
		Target: TargetConfig, Key: "tools.approvalMode", Label: "자동 승인 모드", Verified: true,
		RuntimeTypes: []string{runtimetype.QwenCode},
		Description:  "사람이 런타임을 직접 쓸 때 이 에이전트가 어디까지 묻지 않고 진행할지 정합니다. plan(변경 없음) · default(매번 확인) · auto-edit(파일 편집만) · auto(편집과 안전한 명령) · yolo(모두). 자동 실행에서는 Goal의 승인 모드가 이 값을 대신하므로, 여기 값은 사람이 여는 터미널에만 적용됩니다.",
		Example:      `"auto-edit"`,
	},
	{
		Target: TargetConfig, Key: "model.sessionTokenLimit", Label: "세션 토큰 상한", Verified: true,
		RuntimeTypes: []string{runtimetype.QwenCode},
		Description:  "한 세션이 쓸 수 있는 토큰 상한입니다. 넘으면 대화가 자동으로 압축됩니다.",
		Example:      `120000`,
	},
	{
		Target: TargetConfig, Key: "telemetry.otlpEndpoint", Label: "OTLP 수집기 주소", Verified: true,
		RuntimeTypes: []string{runtimetype.QwenCode},
		Description:  "사내 OpenTelemetry 수집기로 이 에이전트의 실행 기록을 보냅니다. 플랫폼은 기본적으로 꺼 두므로(외부로 나가지 않도록), 켤 때는 telemetry.enabled 도 함께 넣으세요.",
		Example:      `"http://otel-collector.agenthub.svc:4317"`,
	},
	{
		Target: TargetConfig, Key: "ui.theme", Label: "터미널 테마", Verified: true,
		RuntimeTypes: []string{runtimetype.QwenCode},
		Description:  "브라우저 터미널에서 보이는 색 테마입니다.",
		Example:      `"Default Light"`,
	},
	// Below: what people ask for, whose key belongs to the runtime's own version.
	{
		Target: TargetConfig, Label: "자동 승인(YOLO) 모드", Verified: false,
		RuntimeTypes: []string{runtimetype.OpenCode, runtimetype.Hermes, runtimetype.QwenPaw},
		Description:  "도구 실행마다 묻지 않고 진행하게 하는 설정입니다. 키 이름은 런타임 버전마다 다르므로(권한·승인·approval 관련 항목) 해당 런타임 문서에서 확인해 입력하세요. 넣으면 설정 파일에 반영되고, 아래 주입 상태에서 반영 여부를 확인할 수 있습니다. 승인 없이 도구가 실행되므로 MCP 도구 정책과 정책 규칙으로 범위를 먼저 좁히는 것을 권합니다.",
	},
	{
		Target: TargetConfig, Label: "기본 모델·응답 옵션", Verified: false,
		Description: "모델 자체는 플랫폼이 Model Endpoint 로 주입하므로 덮어쓸 수 없습니다. 온도·최대 토큰처럼 런타임이 따로 갖는 응답 옵션이 있다면 그 키를 여기에 넣으세요.",
	},
	{
		Target: TargetConfig, Label: "스킬·확장 경로", Verified: false,
		RuntimeTypes: []string{runtimetype.QwenPaw},
		Description:  "Qwen Paw 는 초기화 시 스킬 풀을 가져옵니다. 스킬 목록이나 경로를 설정으로 지정할 수 있는 버전이라면 그 키를 여기에 넣으세요. 스킬 파일 자체는 작업공간이나 Runtime 공통 환경(파일)으로 배포하세요.",
	},
	{
		Target: TargetEnv, Label: "Langflow 그 밖의 설정", Verified: false,
		RuntimeTypes: []string{runtimetype.Langflow},
		Description:  "Langflow 는 모든 설정이 LANGFLOW_ 로 시작하는 환경변수입니다. 필요한 항목은 Langflow 문서의 Environment variables 에서 이름을 확인해 그대로 넣으세요. 플랫폼이 이미 정하는 접속·인증·저장 위치 관련 변수는 거부됩니다.",
	},
	{
		Target: TargetConfig, Label: "표시 테마·에디터 옵션", Verified: false,
		Description: "테마나 편집기 동작처럼 사람이 쓰는 화면의 설정입니다. 런타임 문서에서 키를 확인해 입력하면 모든 런타임에 같은 값이 적용됩니다.",
	},
}

// Suggestions lists what the console offers, optionally narrowed to one runtime.
//
// A configuration suggestion is dropped for a runtime that has no configuration
// file: offering a setting the API will then refuse is worse than not offering
// it, because the person reads the offer as a promise.
func Suggestions(runtimeType string) []Suggestion {
	items := make([]Suggestion, 0, len(suggestions))
	_, noConfigFile := configless[runtimeType]
	for _, item := range suggestions {
		if runtimeType != "" && len(item.RuntimeTypes) > 0 && !contains(item.RuntimeTypes, runtimeType) {
			continue
		}
		if noConfigFile && item.Target == TargetConfig {
			continue
		}
		items = append(items, item)
	}
	return items
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Expand turns a dotted key and a value into the nested object an overlay needs,
// so "terminal.cwd" sets that one field instead of replacing the whole section.
func Expand(dotted string, value any) map[string]any {
	parts := strings.Split(dotted, ".")
	result := map[string]any{}
	current := result
	for index, part := range parts {
		if index == len(parts)-1 {
			current[part] = value
			break
		}
		next := map[string]any{}
		current[part] = next
		current = next
	}
	return result
}
