# AgentHub 사용자 및 관리자 가이드

AgentHub는 폐쇄망(Offline-Ready) 환경에 최적화된 **엔터프라이즈 AI Agent 런타임 플랫폼**입니다.  
사용자별 영속 워크스페이스(PVC)와 격리된 Kubernetes Agent Pod(OpenCode, Hermes)를 손쉽게 배포하고, MCP(Model Context Protocol) 도구 및 LLM 모델을 중앙에서 통제합니다.

---

## 📑 목차
1. [시스템 접근 및 인증](#1-시스템-접근-및-인증)
2. [운영 대시보드 & 커맨드 팔레트](#2-운영-대시보드--커맨드-팔레트)
3. [Agent 마켓플레이스 & Builder](#3-agent-마켓플레이스--builder)
4. [영속 Workspace & CSI 스냅샷](#4-영속-workspace--csi-스냅샷)
5. [Agent 런타임 라이프사이클 & 세션 게이트웨이](#5-agent-런타임-라이프사이클--세션-게이트웨이)
6. [Multi-Agent 워크플로우 DAG](#6-multi-agent-워크플로우-dag)
7. [MCP Fabric & 번들 바인딩](#7-mcp-fabric--번들-바인딩)
8. [평가 및 벤치마크 (Evaluation)](#8-평가-및-벤치마크-evaluation)
9. [개인 보안 Vault & API 토큰](#9-개인-보안-vault--api-토큰)
10. [자율 실행 & 작업 승인](#10-자율-실행--작업-승인)
11. [관리자 콘솔 & Control Center](#11-관리자-콘솔--control-center)

---

## 1. 시스템 접근 및 인증

AgentHub는 오프라인 로컬 관리자 인증 및 Keycloak OIDC SSO 엔터프라이즈 인증을 지원합니다.

![AgentHub 로그인 콘솔](screenshots/01_login_page.png)

- **보안 세션**: HTTP-Only, SameSite=Lax 쿠키 및 CSRF 토큰 기반 보호
- **OIDC SSO**: Keycloak 연동 시 PKCE 및 State 검증을 통한 안전한 싱글 사인온

---

## 2. 운영 대시보드 & 커맨드 팔레트

로그인 후 접속하는 대시보드에서는 현재 활성화된 에이전트 Pod, 스토리지 사용량, 런타임 상태 메트릭을 실시간으로 확인합니다.

![AgentHub 메인 대시보드](screenshots/02_dashboard_overview.png)

키보드 단축키 `⌘ K` (또는 `Ctrl + K`)를 누르면 언제 어디서나 원하는 메뉴나 에이전트로 즉시 이동할 수 있는 **커맨드 팔레트**가 열립니다.

![커맨드 팔레트 빠른 이동](screenshots/03_command_palette.png)

---

## 3. Agent 마켓플레이스 & Builder

### 3.1 Agent Catalog
사전 검증된 템플릿(Verified Templates)을 탐색하여 원클릭으로 나만의 AI 에이전트를 구성할 수 있습니다.

![Agent Catalog 마켓플레이스](screenshots/04_catalog_marketplace.png)

### 3.2 Agent Builder 드로어
템플릿을 기반으로 에이전트 이름, 런타임 프로필(CPU/메모리 사양), 영속 워크스페이스, LLM 모델 및 MCP 번들을 연결합니다.

![Agent Builder 드로어](screenshots/05_agent_builder_drawer.png)

### 3.3 내 에이전트 목록 및 상세 제어
등록된 에이전트의 런타임 상태를 확인하고, 디테일 드로어를 통해 시작/중지 및 설정을 변경할 수 있습니다.

![생성된 내 에이전트 목록](screenshots/10_my_agents_created.png)

![에이전트 상세 드로어](screenshots/11_agent_detail_drawer.png)

---

## 4. 영속 Workspace & CSI 스냅샷

AgentHub의 핵심 가치는 **런타임 Pod가 재시작되거나 삭제되어도 작업 코드와 파일이 영구히 보존**되는 데 있습니다.

### 4.1 워크스페이스 목록 & 생성
빈 작업공간(Empty) 또는 사내 Git Repository를 자동 복제하는 영속 PVC 워크스페이스를 생성합니다.

![워크스페이스 목록](screenshots/06_workspaces_list.png)

![새 워크스페이스 생성 드로어](screenshots/07_workspace_create_drawer.png)

![생성된 영속 워크스페이스 상세](screenshots/08_workspace_created_detail.png)

### 4.2 CSI 볼륨 스냅샷 & 복구
작업 상태를 특정 시점으로 동결하는 CSI VolumeSnapshot을 생성하고, 필요 시 언제든 새 워크스페이스로 복원할 수 있습니다.

![CSI 볼륨 스냅샷 관리](screenshots/09_workspaces_snapshots.png)

---

## 5. Agent 런타임 라이프사이클 & 세션 게이트웨이

### 5.1 Runtimes 관리
Kubernetes 클러스터 상에서 구동되는 각 에이전트 Pod의 파드 상태, CPU/메모리 점유율, 헬스체크를 통합 모니터링합니다.

![런타임 라이프사이클 관리](screenshots/12_runtimes_management.png)

### 5.2 Session Gateway
OpenCode나 Hermes와 같은 네이티브 Web UI에 접근할 때 1회용 런치 티켓(One-Time Launch Ticket)을 발급하여 안전한 격리 세션을 생성합니다.

![세션 게이트웨이 콘솔](screenshots/13_sessions_gateway.png)

---

## 6. Multi-Agent 워크플로우 DAG

복수의 전문 에이전트(개발, 리뷰, 배포, 데이터분석 등) 간의 협업 그래프(DAG)를 설계합니다.

![Multi-Agent Workflow 빌더](screenshots/14_workflows_builder.png)

- **실행 모드**: Sequential(순차), Parallel(병렬), Router, Supervisor, Reviewer, Consensus
- **가드레일 검증**: 최대 깊이(Max Depth), 최대 호출 횟수(Max Calls), 순환 참조(Cycle)를 자동 검사합니다.

![새 워크플로우 생성 드로어](screenshots/15_workflow_create_drawer.png)

![검증 완료된 워크플로우 목록](screenshots/16_workflow_created_list.png)

---

### 합의(Consensus) 모드

여러 에이전트에게 **같은 질문**을 독립적으로 던지고 표결로 결론을 내립니다. 다른 모드와 달리 단계 간 연결은 무시되므로, 어떤 에이전트도 다른 에이전트의 답을 먼저 보지 않습니다.

- 각 참여자는 근거를 설명한 뒤 마지막 줄에 `VOTE: <결론>` 을 적습니다. 이 안내는 플랫폼이 자동으로 붙입니다.
- 집계는 모델이 아니라 플랫폼이 직접 계산하므로 결과가 재현 가능합니다. 대소문자·띄어쓰기·문장부호 차이는 같은 표로 봅니다.
- 결과는 **만장일치 / 다수결 / 동률 / 합의 없음** 중 하나로 표시되며, 소수 의견과 기권도 그대로 남습니다. 동률은 합의로 처리하지 않습니다.
- `VOTE` 를 적지 않은 참여자는 기권으로 기록되고 분모에서 빠집니다.

---

## 7. MCP Fabric & 번들 바인딩

Model Context Protocol(MCP)을 엔터프라이즈 환경에 맞게 중앙 집중형으로 관리합니다.

### 7.1 MCP Catalog
사내 DB 조회, Git 조작, K8s 배포 등 등록된 MCP 도구들의 스키마와 기능을 탐색합니다.

![MCP Fabric 도구 카탈로그](screenshots/17_mcp_catalog.png)

### 7.2 MCP Bundles
도구들을 역할별(예: DevOps Pack, QA Tester Pack)로 묶어 에이전트에 일괄 바인딩합니다.

![MCP 번들 관리](screenshots/18_mcp_bundles.png)

---

## 8. 평가 및 벤치마크 (Evaluation)

에이전트의 응답 정확도, 지연 시간, 가드레일 준수율을 벤치마크 데이터셋으로 정량 평가합니다.

![Agent 평가 및 벤치마크](screenshots/19_evaluation_benchmarks.png)

---

## 9. 개인 보안 Vault & API 토큰

### 9.1 Personal Secret Vault
각 사용자별 Envelope Encryption 키로 암호화되는 개인 시크릿 저장소입니다. 에이전트 정의에는 평문 대신 참조 ID만 저장됩니다.

![개인 Secret 보관함](screenshots/20_developer_secrets.png)

![새 시크릿 등록 드로어](screenshots/21_developer_secret_drawer.png)

### 9.2 Scoped API Keys
Cursor, Claude Desktop, Antigravity CLI 등 외부 AI 도구와 연동할 수 있는 최소 권한 API 토큰을 발급합니다.

![API 키 목록](screenshots/22_developer_api_keys.png)

![새 API Key 발급 드로어](screenshots/23_developer_new_token_drawer.png)

![1회성 API Key 발급 완료](screenshots/24_developer_token_created.png)

---

## 10. 자율 실행 & 작업 승인

Agent에 **목표(Goal)** 를 설정하면 사람이 지켜보지 않아도 스스로 작업을 수행합니다. 대화형 사용 방식은 그대로 유지되며, 두 방식을 동시에 사용할 수 있습니다.

- **목표 설정**: 에이전트 상세 화면의 *목표* 패널에서 성공/실패 조건, 최대 단계 수, 완료 판정 방식(에이전트 선언 · 규칙 · 심판 모델 · 복합)을 정의합니다.
- **실행 계획**: 계획 수립 방식을 `플랫폼`으로 두면 작업 시작 전에 단계별 계획을 세우고, 실행 기록 화면에서 계획과 실제 수행 내역을 나란히 확인할 수 있습니다. `런타임 위임`은 OpenCode·Hermes 자체 에이전트 루프에 계획을 맡깁니다.
- **승인 게이트**: *상태 변경 시 승인 필요* 를 켜면, 에이전트가 재시작·배포 같은 조치를 수행하기 전에 작업이 **승인 대기** 상태로 멈추고 검토자에게 알림이 갑니다. `검토` 화면에서 승인하면 승인 사유와 함께 작업이 이어서 진행되고, 거절하면 그 사유가 기록된 채 작업이 종료됩니다. 승인을 기다린 시간은 재시도 횟수로 계산되지 않습니다.
- **기억(Memory)**: 에이전트가 학습한 사실은 Pod가 재생성되어도 유지되며, 에이전트 상세 화면에서 확인하고 삭제할 수 있습니다.
- **위임(Delegation)**: 권한 밖의 일은 다른 에이전트에게 위임할 수 있습니다. 위임은 항상 작업 대기열을 거치므로 권한·쿼터·감사 기록이 그대로 적용되며, *최대 위임 깊이* 로 연쇄를 제한하고 순환 위임(A → B → A)은 자동으로 차단됩니다.
- **이벤트 Trigger**: 플랫폼에서 일어난 일에 반응해 작업을 시작할 수 있습니다. 구독 가능한 이벤트는 작업 완료·작업 실패·재시도 소진·승인 처리·런타임 장애·산출물 생성입니다. 이벤트 필터에 `{"agentId":"…"}` 같은 JSON 객체를 넣으면 해당 값이 일치하는 이벤트에만 반응하므로, 특정 에이전트나 런타임만 감시할 수 있습니다. 어떤 이벤트가 실제로 발행되고 있는지는 `작업` 화면의 *최근 플랫폼 이벤트* 에서 확인할 수 있습니다.
  - 이 Trigger가 만든 작업이 다시 같은 Trigger를 깨우지는 않으므로, 무한 반복 걱정 없이 연쇄를 구성할 수 있습니다.
- **MCP 도구 정책**: 에이전트 상세 화면의 *MCP 도구 정책* 에서, 연결된 MCP 서버마다 호출 가능한 도구를 지정할 수 있습니다. *이 도구만 허용* 또는 *이 도구만 차단* 중에서 고르고 도구 이름을 쉼표로 나열하면 됩니다. 정책이 있는 서버는 Pod 안의 게이트웨이를 거쳐서만 호출되므로 에이전트가 우회할 수 없고, 차단된 도구는 도구 목록에도 나타나지 않으며, 자격 증명도 에이전트가 아닌 게이트웨이가 보관합니다. 정책 변경은 Runtime 재시작 후 적용됩니다.
  - *이 도구만 허용* 을 고르고 목록을 비워 두면 해당 서버의 모든 도구가 차단됩니다.
- **대화 작업을 백그라운드로 전환**: `런타임 세션` 화면에서 *백그라운드로* 를 누르면 지금 하던 일을 에이전트에게 맡길 수 있습니다. 이미 실행 중인 런타임을 그대로 사용하므로 작업이 도는 동안에도 같은 작업공간을 열어 볼 수 있습니다.
- **작업 추적**: `작업` 화면에서 대기열·진행 상황·재시도·산출물을 확인하고, 실패한 작업은 다시 실행할 수 있습니다.
- **토큰 사용량과 비용**: `작업` 화면의 *최근 30일 토큰 사용량* 에서 에이전트별 입력·출력 토큰과 금액을 확인할 수 있습니다. 금액은 `관리자 · 리소스 · 모델 엔드포인트` 에 입력한 100만 토큰당 단가로 계산되며, 단가를 입력하지 않은 모델의 토큰은 금액에 포함하지 않고 **미산정** 으로 따로 표시합니다. 자율 실행은 사람이 보고 있지 않을 때 도는 만큼, 수렴하지 못하는 작업이 조용히 비용을 쓰고 있지 않은지 여기서 확인하세요.

### 사내 자체 런타임(custom) 연결

OpenCode·Hermes·Qwen Paw 외에 사내에서 직접 만든 에이전트를 올릴 때는 런타임 유형 `custom` 을 사용합니다. 이 유형에는 내장 어댑터가 없으므로 **시작 명령** 을 반드시 입력해야 합니다.

- **시작 명령**: 컨테이너에서 실행할 명령을 한 줄에 하나씩 입력합니다(예: `/usr/local/bin/my-agent`, `serve`, `--port`, `9000`). 쉘을 거치지 않으므로 따옴표·파이프·환경변수 치환은 동작하지 않습니다. 필요하면 이미지의 진입 스크립트에 넣으세요.
- **서비스 포트**: 런타임이 실제로 듣는 포트를 입력합니다. 비워 두면 기본 포트를 사용하며, 값이 맞지 않으면 Pod는 떠도 준비(Ready) 상태가 되지 않습니다.
- 시작 명령이 없으면 저장 단계에서 거절됩니다. 예전에는 이 경우 Pod가 이미지 기본 진입점으로 떠서 원인 없이 CrashLoopBackOff에 빠졌습니다.

---

## 11. 관리자 콘솔 & Control Center

### 10.1 Control Center (실시간 운영 & 감사 로그)
시스템 링버퍼 로그 스트림 및 불변 감사 추적(Audit Trail)을 제공합니다.

![Control Center 운영 콘솔](screenshots/26_admin_operations_control_center.png)

### 10.2 리소스 및 엔드포인트 관리
- **Runtime Profiles**: Pod에 할당할 CPU, Memory, GPU 스펙 정의
- **Runtime Images**: 검증된 오프라인 Base 이미지 등록
- **Models**: 사내 vLLM, Ollama, OpenAI 호환 엔드포인트 등록
- **MCP Servers**: 전사 MCP 서버 레지스트리 관리

![관리자 Runtime Profiles](screenshots/27_admin_runtime_profiles.png)

![관리자 Runtime Images](screenshots/28_admin_runtime_images.png)

![관리자 Model Endpoints](screenshots/29_admin_models.png)

![관리자 MCP Servers](screenshots/30_admin_mcp_servers.png)

### 10.3 보안 및 전역 설정
- **Users & Teams**: RBAC 역할(Admin, Manager, User) 및 팀별 접근 제어
- **Security & Network**: Pod Security Standards, NetworkPolicy, 마스터 키 회전
- **System Settings**: OIDC SSO, 승인 거버넌스 및 클러스터 설정

![사용자 및 팀 관리](screenshots/31_admin_users_and_teams.png)

![보안 정책 및 네트워크 제어](screenshots/32_admin_security_policies.png)

![시스템 전역 설정](screenshots/33_admin_system_settings.png)
