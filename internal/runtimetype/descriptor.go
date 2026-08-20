package runtimetype

// What each runtime actually is, in one place.
//
// The adapters differ in ways that matter to the person choosing one and
// to the platform driving one — whether there is a browser workspace, whether
// the agent has its own tool loop, whether MCP servers reach it through its
// configuration, where its work lives. That knowledge was spread across the
// operator's adapter table, the console's label map and several Korean strings
// written twice. Choosing a runtime was therefore guesswork, and describing a
// run's environment to the model was impossible without repeating all of it.

// Descriptor is everything the platform and its users need to know about one
// runtime adapter that is not a Kubernetes detail.
type Descriptor struct {
	Type string `json:"type"`
	// Code is the two-or-three letter badge the console renders.
	Code string `json:"code"`
	// Label and Summary are what a person reads when picking one.
	Label   string `json:"label"`
	Summary string `json:"summary"`
	// Strengths and Watchouts are the honest comparison: what this runtime is
	// good at, and what it will not do for you.
	Strengths []string `json:"strengths"`
	Watchouts []string `json:"watchouts"`
	// Workspace is where the agent's files live inside the Pod. It is the same
	// path a person sees when they open the runtime, which is what makes handing
	// work between the two meaningful.
	Workspace string `json:"workspace"`
	// Port is the container port the runtime's own surface listens on.
	Port int32 `json:"port"`
	// BrowserUI reports whether a person can open and work in it.
	BrowserUI bool `json:"browserUi"`
	// Terminal reports whether that surface includes a shell.
	Terminal bool `json:"terminal"`
	// ToolLoop reports whether the runtime itself calls tools and edits files
	// when a person drives it. Autonomous task execution never uses this: it is
	// a prose loop against the model gateway, and saying so is the difference
	// between an agent that hands work over and one that claims it did it.
	ToolLoop bool `json:"toolLoop"`
	// MCPConfigured reports whether the platform writes bound MCP servers into
	// this runtime's own configuration.
	MCPConfigured bool `json:"mcpConfigured"`
	// ProxiedUI reports that the surface is published through the platform's
	// authenticating proxy rather than directly.
	ProxiedUI bool `json:"proxiedUi"`
	// HostSessionOnly reports that this runtime's UI needs an origin of its own,
	// so a site without a Runtime Base Domain cannot open it at all. The console
	// says so before the button is pressed instead of after.
	HostSessionOnly bool `json:"hostSessionOnly"`
	// FlowExecution reports that the platform can run this runtime's own saved
	// flows as an autonomous task, rather than only reasoning at the model
	// gateway. It is what makes a Langflow agent do work unattended.
	FlowExecution bool `json:"flowExecution"`
	// CLIExecution reports that the platform can drive this runtime's own agent
	// headlessly for an autonomous task — the runtime's tool loop, running in its
	// own workspace, rather than a prose loop that can only describe what it would
	// have done.
	CLIExecution bool `json:"cliExecution"`
	// BestFor is one sentence: when to choose this one.
	BestFor string `json:"bestFor"`
}

var descriptors = map[string]Descriptor{
	OpenCode: {
		Type: OpenCode, Code: "OC", Label: "OpenCode",
		Summary:   "브라우저에서 여는 코딩 워크스페이스. 파일 편집과 터미널을 갖춘 코딩 에이전트입니다.",
		BestFor:   "리포지터리를 읽고 고치는 일 — 코드 수정, 테스트 실행, 스크립트 작성",
		Strengths: []string{"파일 편집기와 터미널이 있는 웹 IDE", "작업공간 볼륨에 결과가 그대로 남음", "MCP 도구가 설정에 자동 등록됨"},
		Watchouts: []string{"자동 실행(작업 대기열)에서는 파일을 직접 고치지 않습니다 — 사람이 이어받거나 런타임에서 직접 작업해야 합니다"},
		Workspace: "/workspace", Port: 4096,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true,
	},
	Hermes: {
		Type: Hermes, Code: "H", Label: "Hermes",
		Summary:   "장기 기억과 도구 실행을 갖춘 자율 에이전트. 대시보드와 자체 API를 함께 제공합니다.",
		BestFor:   "여러 단계를 스스로 이어가는 조사·정리 작업, 기억이 쌓여야 하는 반복 업무",
		Strengths: []string{"자체 에이전트 루프와 장기 기억", "터미널이 포함된 대시보드", "MCP 도구가 설정에 자동 등록됨", "인증된 자체 API(8642)"},
		Watchouts: []string{"대시보드는 루프백에만 열리므로 플랫폼 프록시를 통해서만 접근합니다"},
		Workspace: "/workspace", Port: 8642,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
	},
	QwenPaw: {
		Type: QwenPaw, Code: "QP", Label: "Qwen Paw",
		Summary:   "AgentScope 기반 개인 에이전트 워크스테이션. 스킬을 조합해 개인 업무를 자동화합니다.",
		BestFor:   "개인 업무 자동화와 스킬 실험 — 정해진 스킬을 사람이 직접 돌려 보는 용도",
		Strengths: []string{"스킬 중심의 개인 워크스테이션", "모델 공급자가 플랫폼 엔드포인트로 자동 설정됨"},
		Watchouts: []string{"자체 인증이 없어 플랫폼 프록시로만 공개됩니다", "MCP 도구는 이 런타임의 설정으로 전달되지 않습니다"},
		Workspace: "/workspace", Port: 8642,
		BrowserUI: true, Terminal: false, ToolLoop: true, MCPConfigured: false, ProxiedUI: true,
	},
	QwenCode: {
		Type: QwenCode, Code: "QC", Label: "Qwen Code",
		Summary:   "터미널에서 사는 코딩 에이전트. 브라우저로 열면 그 터미널이 그대로 열립니다.",
		BestFor:   "리포지터리를 직접 고치는 일 — 사람이 옆에서 보며 시키거나, 작업을 맡겨 무인으로 돌리거나",
		Strengths: []string{"자체 도구 루프로 파일을 고치고 명령을 실행함", "자동 실행에서도 같은 도구 루프를 그대로 사용함", "MCP 도구가 설정에 자동 등록됨", "pip·npm 설치가 되는 작업 도구모음 포함"},
		Watchouts: []string{"자체 인증이 없는 브라우저 터미널이라 플랫폼 프록시로만 공개됩니다", "무인 실행은 승인 모드에 따라 파일을 실제로 바꿉니다 — 승인 모드를 먼저 정하세요", "흐름 편집기 같은 화면은 없습니다 — 터미널이 전부입니다"},
		Workspace: "/workspace", Port: 7681,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		CLIExecution: true,
	},
	Langflow: {
		Type: Langflow, Code: "LF", Label: "Langflow",
		Summary:   "흐름을 그려서 만드는 시각적 에이전트 빌더. 저장한 흐름을 AgentHub가 그대로 실행합니다.",
		BestFor:   "코드 대신 그림으로 조립하는 파이프라인 — 문서 처리, RAG, 여러 단계를 잇는 프롬프트 흐름",
		Strengths: []string{"드래그로 조립하는 흐름 편집기", "저장한 흐름을 자동 실행 백엔드로 쓸 수 있음", "모델 자격증명이 흐름의 전역 변수로 주입됨", "프로젝트를 MCP 서버로 노출할 수 있음"},
		Watchouts: []string{"자체 로그인을 끈 상태로 뜨므로 플랫폼 프록시로만 공개됩니다", "하위 경로로 서비스할 수 없어 Runtime Base Domain이 설정된 사이트에서만 UI를 열 수 있습니다", "MCP 도구는 이 런타임의 설정으로 전달되지 않습니다 — 흐름 안에서 직접 연결해야 합니다", "터미널이 없습니다"},
		Workspace: "/workspace", Port: 7860,
		BrowserUI: true, Terminal: false, ToolLoop: true, MCPConfigured: false, ProxiedUI: true,
		HostSessionOnly: true, FlowExecution: true,
	},
	Custom: {
		Type: Custom, Code: "A", Label: "Custom",
		Summary:   "직접 정의한 컨테이너 실행 명령. 플랫폼은 수명주기와 네트워크만 관리합니다.",
		BestFor:   "사내 자체 에이전트나 기존 도구를 그대로 올릴 때",
		Strengths: []string{"어떤 이미지든 실행 명령과 포트만 지정하면 됨"},
		Watchouts: []string{"플랫폼은 이 런타임의 내부를 모릅니다 — 작업공간 경로와 준비 상태는 이미지가 책임집니다"},
		Workspace: "/workspace",
		BrowserUI: false, Terminal: false, ToolLoop: false, MCPConfigured: false,
	},
}

// unknown is what an unrecognised type resolves to. It exists so every caller
// can render something honest instead of branching on a missing key.
var unknown = Descriptor{Code: "A", Label: "Unknown", Summary: "알 수 없는 Runtime 유형", Workspace: "/workspace"}

// Describe returns the descriptor for a runtime type.
func Describe(value string) Descriptor {
	if item, ok := descriptors[value]; ok {
		return item
	}
	item := unknown
	item.Type = value
	return item
}

// Descriptors lists every supported runtime in the order the console offers
// them.
func Descriptors() []Descriptor {
	items := make([]Descriptor, 0, len(Supported))
	for _, name := range Supported {
		items = append(items, Describe(name))
	}
	return items
}
