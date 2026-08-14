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
10. [관리자 콘솔 & Control Center](#10-관리자-콘솔--control-center)

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

## 10. 관리자 콘솔 & Control Center

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
