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
	// Runners are the ways the platform can hand this runtime an autonomous task
	// beyond the prose loop every agent has: "flow" runs something somebody drew,
	// "cli" drives the runtime's own agent headlessly, "acp" speaks the Agent
	// Client Protocol to it.
	//
	// It is a list rather than one boolean per backend because the list is the
	// question every caller actually asks — can this runtime do this — and a
	// boolean per backend is a field to add, thread through the console, and
	// forget to check every time a backend is added.
	Runners []string `json:"runners,omitempty"`
	// CoarseToolKinds says this runtime's agent does not tell the platform what
	// kind of action a tool call is — it labels them all "other", or labels the
	// ones that matter that way.
	//
	// It matters because the ACP backend answers permission requests by the kind
	// the agent declares. With nothing to judge by, the fine-grained approval
	// modes refuse everything, and an operator sees a runtime that starts and
	// then does nothing. The platform will not guess a kind from a tool's name —
	// that would be inventing a fact the agent declined to state — so it says this
	// instead, and the console warns before the Goal is saved.
	CoarseToolKinds bool `json:"coarseToolKinds,omitempty"`
	// Commands are the argv the platform executes inside this runtime for each
	// backend it supports, keyed by runner name.
	//
	// Every one of them is a wrapper the image ships rather than the agent binary
	// itself, because an exec has no shell, no working directory and no profile:
	// the wrapper supplies all three, and the platform passes plain arguments so a
	// task title with a quote in it cannot become a command.
	//
	// They live here rather than beside each runner because they are all the same
	// fact — what the platform runs in this image — and a constant in each runner
	// package is a place for one of them to drift from the Dockerfile that ships
	// it. TestEveryCommandExistsInItsImage reads both and fails when they part.
	// Not sent to the console: a person chooses a runtime, not a command line.
	Commands map[string][]string `json:"-"`
	// BestFor is one sentence: when to choose this one.
	BestFor string `json:"bestFor"`
}

// qwenCodeACP starts Qwen Code as a protocol peer rather than as a terminal. It
// goes through the same wrapper the headless runner uses, because an exec has no
// working directory and no PATH of its own, and an agent that cannot see what the
// person installed in this workspace is not working in it.
//
// `--approval-mode default` is not a default: it is the whole point. Started
// without it this agent approves its own tool calls and never asks — verified
// against the real binary, which wrote a file without a word. With it, every
// tool call becomes a `session/request_permission` the platform answers and
// records. It stays `default` regardless of what the Goal chose, because the
// Goal's mode decides what the platform answers, not whether it is asked; a
// permissive Goal still leaves a record of what it permitted.
//
// JupyterLab shares it: that image is built on this one, and the agent beside the
// notebooks is the same agent.
var qwenCodeCommands = map[string][]string{
	// Headless: the agent's own budgets and output format are appended by the
	// runner, which knows this agent's flags.
	RunnerCLI: {"/usr/local/bin/agenthub-qwencode-run"},
	RunnerACP: {"/usr/local/bin/agenthub-qwencode-run", "--acp", "--approval-mode", "default"},
}

// gooseACP starts Goose as a protocol peer. The mode that makes it ask before
// acting is an environment variable rather than a flag, so it lives in the
// wrapper this points at — the same wrapper that supplies the working directory
// and the PATH an exec does not have.
var gooseCommands = map[string][]string{RunnerACP: {"/usr/local/bin/agenthub-goose-run", "acp"}}

// browserCodeACP starts BrowserCode as a protocol peer. It asks before using a
// tool without being told to, which is the third of three agents and the first
// that needed no persuading.
var browserCodeCommands = map[string][]string{RunnerACP: {"/usr/local/bin/agenthub-browsercode-run", "acp"}}

// holmesCommands starts one investigation. The wrapper is what makes stdout
// carry the machine-readable record and nothing else.
var holmesCommands = map[string][]string{RunnerInvestigate: {"/usr/local/bin/agenthub-holmes-run"}}

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
		Strengths: []string{"자체 도구 루프로 파일을 고치고 명령을 실행함", "자동 실행에서도 같은 도구 루프를 그대로 사용함", "ACP로 실행하면 도구 요청마다 플랫폼이 승인·거절을 판단하고 기록함", "MCP 도구가 설정에 자동 등록됨", "pip·npm 설치가 되는 작업 도구모음 포함"},
		Watchouts: []string{"자체 인증이 없는 브라우저 터미널이라 플랫폼 프록시로만 공개됩니다", "무인 실행은 승인 모드에 따라 파일을 실제로 바꿉니다 — 승인 모드를 먼저 정하세요", "흐름 편집기 같은 화면은 없습니다 — 터미널이 전부입니다"},
		Workspace: "/workspace", Port: 7681,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		Runners: []string{RunnerCLI, RunnerACP}, Commands: qwenCodeCommands,
	},
	Langflow: {
		Type: Langflow, Code: "LF", Label: "Langflow",
		Summary:   "흐름을 그려서 만드는 시각적 에이전트 빌더. 저장한 흐름을 AgentHub가 그대로 실행합니다.",
		BestFor:   "코드 대신 그림으로 조립하는 파이프라인 — 문서 처리, RAG, 여러 단계를 잇는 프롬프트 흐름",
		Strengths: []string{"드래그로 조립하는 흐름 편집기", "저장한 흐름을 자동 실행 백엔드로 쓸 수 있음", "모델 자격증명이 흐름의 전역 변수로 주입됨", "프로젝트를 MCP 서버로 노출할 수 있음"},
		Watchouts: []string{"자체 로그인을 끈 상태로 뜨므로 플랫폼 프록시로만 공개됩니다", "하위 경로로 서비스할 수 없어 Runtime Base Domain이 설정된 사이트에서만 UI를 열 수 있습니다", "MCP 도구는 이 런타임의 설정으로 전달되지 않습니다 — 흐름 안에서 직접 연결해야 합니다", "터미널이 없습니다"},
		Workspace: "/workspace", Port: 7860,
		BrowserUI: true, Terminal: false, ToolLoop: true, MCPConfigured: false, ProxiedUI: true,
		HostSessionOnly: true, Runners: []string{RunnerFlow},
	},
	Goose: {
		Type: Goose, Code: "GO", Label: "Goose",
		Summary:   "프로토콜로 대화하는 오픈소스 에이전트. 도구를 쓰기 전마다 플랫폼에 묻습니다.",
		BestFor:   "무엇을 바꿨는지 남아야 하는 무인 작업 — 승인·거절이 실행 기록으로 필요한 일",
		Strengths: []string{"Agent Client Protocol을 직접 지원해 도구 요청마다 플랫폼이 승인·거절을 판단", "터미널 대화를 브라우저에서 그대로 사용", "MCP 서버가 확장(extension)으로 자동 등록됨", "대화·설정이 홈 볼륨에 남아 재기동해도 유지", "pip 설치가 되는 작업 도구모음 포함"},
		Watchouts: []string{"자체 인증이 없는 브라우저 터미널이라 플랫폼 프록시로만 공개됩니다",
			// Learned by driving the real agent: every tool it announces is kind
			// "other", including reads. The platform judges by the kind an agent
			// declares, so the fine-grained modes have nothing to work with here.
			"도구 종류를 모두 other 로 알려주기 때문에, ACP 실행의 승인 모드는 사실상 auto·yolo(전부 승인) 아니면 거절입니다 — 무인 실행에는 auto 를 고르세요",
			"사용 토큰을 알려주지 않아 ACP 실행이 계량되지 않습니다"},
		Workspace: "/workspace", Port: 7681,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		Runners: []string{RunnerACP}, Commands: gooseCommands, CoarseToolKinds: true,
	},
	Holmes: {
		Type: Holmes, Code: "HG", Label: "HolmesGPT",
		Summary:   "장애를 조사하는 SRE 에이전트. 결론과 함께 그 근거로 쓴 조회를 그대로 남깁니다.",
		BestFor:   "왜 죽었는지 물어보는 일 — 알림·메트릭·로그를 훑어 근본 원인과 근거를 정리",
		Strengths: []string{"Prometheus·Grafana·Loki·Alertmanager 등 관측 도구를 조회하는 내장 툴셋", "조사에 쓴 조회 하나하나가 실행 기록의 단계로 남음", "토큰 사용량을 실제 값으로 보고해 그대로 계량됨", "MCP 서버가 툴셋으로 자동 등록됨", "터미널에서 사람이 직접 이어서 물어볼 수 있음"},
		Watchouts: []string{"자체 인증이 없는 브라우저 터미널이라 플랫폼 프록시로만 공개됩니다",
			// The privilege is off unless an administrator turns it on, and saying
			// which switch it is saves an hour of wondering why the investigator
			// keeps answering "I could not look".
			"Kubernetes 툴셋은 기본으로 꺼져 있습니다 — 관리자가 보안 프로파일에서 '클러스터 읽기'를 켠 Agent에서만 클러스터를 조회할 수 있습니다(읽기 전용이며 Secret은 볼 수 없습니다)",
			"조사 중 셸 실행은 승인 모드가 auto·yolo 일 때만 허용됩니다",
			"조회 결과가 크면 컨텍스트를 많이 쓰므로 Runtime 프로파일과 토큰 예산을 넉넉히 잡으세요"},
		Workspace: "/workspace", Port: 7681,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		Runners: []string{RunnerInvestigate}, Commands: holmesCommands,
	},
	BrowserCode: {
		Type: BrowserCode, Code: "BC", Label: "BrowserCode",
		Summary:   "진짜 브라우저를 직접 몰아 일하는 에이전트. 웹에서 해야 하는 일을 맡깁니다.",
		BestFor:   "사람이 브라우저로 하던 일 — 로그인이 필요한 사이트 조회, 웹 UI 확인, 화면을 통한 데이터 수집",
		Strengths: []string{"컨테이너 안의 Chromium을 DevTools 프로토콜로 직접 제어", "고정된 동작 목록이 아니라 필요한 JavaScript를 그때그때 작성해 실행", "ACP를 직접 지원해 도구 요청마다 플랫폼이 승인·거절을 판단하고 기록", "브라우저 프로필이 홈 볼륨에 남아 로그인 세션이 재기동 후에도 유지", "MCP 도구가 설정에 자동 등록됨"},
		Watchouts: []string{"자체 인증이 없는 브라우저 터미널이라 플랫폼 프록시로만 공개됩니다",
			// The trade this runtime makes, said before it is made rather than
			// found in a log: Chromium cannot start with its own sandbox inside an
			// unprivileged container, so the container is the boundary.
			"컨테이너 안에서는 Chromium 자체 샌드박스를 켤 수 없어 끄고 실행합니다 — 격리는 Pod가 담당하므로, 이 런타임의 네트워크 정책을 좁게 잡고 신뢰할 수 없는 페이지를 여는 용도로 쓰세요",
			"에이전트가 여는 페이지에 자격증명을 남기면 홈 볼륨의 브라우저 프로필에 남습니다",
			"브라우저가 메모리를 많이 쓰므로 Runtime 프로파일을 넉넉히 잡으세요"},
		Workspace: "/workspace", Port: 7681,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		// Its file tools declare a kind, but the browser tool — the reason to run
		// this runtime — is labelled "other", so the strict modes refuse the one
		// thing it is for.
		Runners: []string{RunnerACP}, Commands: browserCodeCommands, CoarseToolKinds: true,
	},
	Jupyter: {
		Type: Jupyter, Code: "JL", Label: "JupyterLab",
		Summary:   "노트북으로 데이터를 다루는 작업대. 옆 터미널에 Qwen Code 에이전트가 함께 있습니다.",
		BestFor:   "데이터 분석과 리포트 — 표를 읽고 그림을 그리고, 지루한 부분은 에이전트에게 넘기는 일",
		Strengths: []string{"노트북·파일 브라우저·터미널이 한 화면에", "pandas·matplotlib·scikit-learn 등 분석 도구 기본 포함", "같은 작업공간에서 Qwen Code 에이전트를 그대로 사용", "자동 실행에서도 그 에이전트가 직접 수행", "ACP 실행이면 그 에이전트의 도구 요청이 기록으로 남음", "pip 설치가 홈 볼륨에 남아 재기동해도 유지"},
		Watchouts: []string{"자체 토큰 로그인을 끈 상태로 뜨므로 플랫폼 프록시로만 공개됩니다", "MCP 도구는 노트북이 아니라 에이전트 설정으로 전달됩니다", "커널이 메모리를 많이 쓰므로 Runtime 프로파일을 넉넉히 잡으세요"},
		Workspace: "/workspace", Port: 8888,
		BrowserUI: true, Terminal: true, ToolLoop: true, MCPConfigured: true, ProxiedUI: true,
		Runners: []string{RunnerCLI, RunnerACP}, Commands: qwenCodeCommands,
	},
	NodeRED: {
		Type: NodeRED, Code: "NR", Label: "Node-RED",
		Summary:   "노드를 선으로 이어 만드는 배선 도구. 만들어 두면 계속 도는 자동화입니다.",
		BestFor:   "시스템과 시스템을 잇는 일 — 이벤트 수신, 변환, 호출, 주기 실행",
		Strengths: []string{"드래그로 잇는 흐름 편집기", "HTTP·MQTT·파일·스케줄 노드가 기본 제공", "만든 흐름이 Runtime 안에서 계속 실행됨", "흐름과 설정이 홈 볼륨에 남음"},
		Watchouts: []string{"자체 로그인을 켜지 않은 상태로 뜨므로 플랫폼 프록시로만 공개됩니다", "MCP 도구는 이 런타임의 설정으로 전달되지 않습니다", "자동 실행(작업 대기열)에서는 흐름을 대신 실행하지 않습니다 — 사람이 만들고 런타임이 돌립니다"},
		Workspace: "/workspace", Port: 1880,
		BrowserUI: true, Terminal: false, ToolLoop: false, MCPConfigured: false, ProxiedUI: true,
	},
	N8N: {
		Type: N8N, Code: "N8", Label: "n8n",
		Summary:   "수백 가지 연동을 가진 업무 자동화 도구. 트리거와 노드로 업무를 잇습니다.",
		BestFor:   "사내 업무 연결 — 메일·메신저·DB·HTTP·스케줄을 엮는 자동화",
		Strengths: []string{"바로 쓰는 연동 노드가 매우 많음", "웹훅·스케줄 트리거 내장", "자격증명과 흐름이 홈 볼륨에 남음", "폐쇄망을 위해 텔레메트리·버전 확인·템플릿 갤러리를 꺼서 제공"},
		Watchouts: []string{"첫 접속에서 소유자 계정을 만들어야 합니다 — 그 전까지는 플랫폼 프록시가 유일한 문지기입니다", "하위 경로로 서비스할 수 없어 Runtime Base Domain이 설정된 사이트에서만 UI를 열 수 있습니다", "MCP 도구는 이 런타임의 설정으로 전달되지 않습니다", "자동 실행(작업 대기열)에서는 워크플로를 대신 실행하지 않습니다"},
		Workspace: "/workspace", Port: 5678,
		BrowserUI: true, Terminal: false, ToolLoop: false, MCPConfigured: false, ProxiedUI: true,
		HostSessionOnly: true,
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

// The autonomous backends a runtime can offer. They are named here rather than in
// the store because the descriptor is what says which runtime supports which, and
// a name that exists in one place and not the other is the drift this avoids.
const (
	// RunnerFlow runs a flow the runtime holds.
	RunnerFlow = "flow"
	// RunnerCLI drives the runtime's own command-line agent headlessly.
	RunnerCLI = "cli"
	// RunnerInvestigate hands the task to an incident investigator and keeps the
	// evidence it gathered as well as its conclusion.
	RunnerInvestigate = "investigate"
	// RunnerACP speaks the Agent Client Protocol to whatever agent the runtime
	// was given, which is how one adapter serves many agents.
	RunnerACP = "acp"
)

// RunnerCommand is the argv the platform executes in this runtime for a backend,
// or nothing when the runtime does not support it. The copy is defensive: the
// descriptors are package state, and a caller appending its own flags to the
// returned slice would otherwise append them to every later run.
func RunnerCommand(runtimeType, runner string) []string {
	command := Describe(runtimeType).Commands[runner]
	if len(command) == 0 {
		return nil
	}
	return append([]string(nil), command...)
}

// SupportsRunner reports whether this runtime can be handed a task that way.
func SupportsRunner(runtimeType, runner string) bool {
	for _, item := range Describe(runtimeType).Runners {
		if item == runner {
			return true
		}
	}
	return false
}

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
