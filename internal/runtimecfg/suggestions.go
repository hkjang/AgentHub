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
	// Below: what people ask for, whose key belongs to the runtime's own version.
	{
		Target: TargetConfig, Label: "자동 승인(YOLO) 모드", Verified: false,
		Description: "도구 실행마다 묻지 않고 진행하게 하는 설정입니다. 키 이름은 런타임 버전마다 다르므로(권한·승인·approval 관련 항목) 해당 런타임 문서에서 확인해 입력하세요. 넣으면 설정 파일에 반영되고, 아래 주입 상태에서 반영 여부를 확인할 수 있습니다. 승인 없이 도구가 실행되므로 MCP 도구 정책과 정책 규칙으로 범위를 먼저 좁히는 것을 권합니다.",
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
		Target: TargetConfig, Label: "표시 테마·에디터 옵션", Verified: false,
		Description: "테마나 편집기 동작처럼 사람이 쓰는 화면의 설정입니다. 런타임 문서에서 키를 확인해 입력하면 모든 런타임에 같은 값이 적용됩니다.",
	},
}

// Suggestions lists what the console offers, optionally narrowed to one runtime.
func Suggestions(runtimeType string) []Suggestion {
	items := make([]Suggestion, 0, len(suggestions))
	for _, item := range suggestions {
		if runtimeType == "" || len(item.RuntimeTypes) == 0 || contains(item.RuntimeTypes, runtimeType) {
			items = append(items, item)
		}
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
