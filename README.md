<div align="center">

<img src="docs/assets/logo.png" width="128" height="128" alt="AgentHub Logo" />

# AgentHub

### Offline-Ready Enterprise AI Agent Runtime Platform

**JupyterHub처럼 각 사용자와 영속 Workspace마다 격리된 OpenCode 및 Hermes 런타임을 제공하는 엔터프라이즈 제어면**

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org)
[![React Version](https://img.shields.io/badge/React-19-61DAFB?style=flat&logo=react)](https://reactjs.org)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-CRD%20%26%20Operator-326CE5?style=flat&logo=kubernetes)](https://kubernetes.io)
[![MCP Ready](https://img.shields.io/badge/MCP-Streamable%20HTTP-FF6B6B?style=flat)](https://modelcontextprotocol.io)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

[🌐 웹 쇼케이스 둘러보기](docs/index.html) · [📘 공식 사용자 가이드 (PDF)](docs/AgentHub_User_Guide.pdf) · [📗 CRU 매뉴얼 (PDF)](docs/AgentHub_CRU_Operations_Manual.pdf) · [📙 아키텍처 백서 (PDF)](docs/AgentHub_Architecture_and_MCP_Whitepaper.pdf)

</div>

---

## 🌟 핵심 가치 (Core Highlights)

- 🔒 **완전 폐쇄망 (Offline-Ready)**: 외부 인터넷 연결 없이 로컬 Docker 레지스트리와 내부 Kubernetes 클러스터 상에서 100% 결정론적으로 동작합니다.
- 📦 **영속 Workspace & CSI 스냅샷**: Agent Pod가 종료/재생성되어도 사용자의 소스 코드와 데이터는 PVC에 보존되며, CSI VolumeSnapshot을 통해 언제든 원하는 시점으로 복원할 수 있습니다.
- 🤖 **3대 엔터프라이즈 에이전트 런타임**: 인터랙티브 코딩 워크스페이스(**OpenCode**) 및 자율 에이전트 어시스턴트(**Hermes** / [**Qwen Paw**](https://qwenpaw.agentscope.io/))를 사용자 전용 비루트(Non-root) Pod로 격리 기동합니다.
  - **Qwen Paw (AgentScope)**: 3계층 ReMe 메모리, 커널 샌드박스 보안 가드, 스킬/MCP 확장 및 Qwen/Ollama 모델 자율 추론을 지원하는 개인 에이전트 워크스테이션
  - **OpenCode**: 브라우저 기반 풀스택 코딩 IDE 및 실시간 파일/터미널 워크스페이스
  - **Hermes Agent**: 장기 기억(Long-term Memory) 및 도구 실행 자율 에이전트
  - **custom**: 사내에서 직접 만든 에이전트도 시작 명령과 포트만 지정하면 동일한 격리·정책·실행 플레인 위에서 운영할 수 있습니다.
- 🤖 **자율 실행 플레인**: Agent에 Goal과 Trigger를 주면 예약·Webhook·수동으로 스스로 Task를 수행하고, 완료 조건을 플랫폼이 검증한 뒤 산출물과 실행 타임라인을 남깁니다. 기존 Interactive 방식은 그대로 유지됩니다.
- 📄 **GitOps 정의 내보내기/가져오기**: 에이전트 정의를 YAML로 주고받아 형상 관리와 클러스터 간 이관이 가능합니다. 참조는 이름으로 기록되며, 대상 클러스터에 없는 항목은 이름을 알려주고 거절합니다.
- 🔭 **OpenTelemetry 분산 추적**: 수집기 주소만 넣으면 작업 시도 → 추론 단계 → 모델 호출 → 완료 판정 → 런타임 확보가 하나의 Trace로 남습니다. 단계별 토큰과 소요 시간이 스팬에 붙어 어디서 느려지고 어디서 비용이 나는지 Task 단위로 추적할 수 있고, 화면·로그의 Trace ID가 수집기의 Trace ID와 동일합니다. 수집기가 없으면 추적은 꺼진 채로 아무 비용도 들지 않습니다.
- 💰 **토큰 사용량·비용 가시화**: 실행 기록에 이미 남는 토큰 수를 그대로 집계해 에이전트별 입력·출력 토큰과 금액을 보여줍니다. 단가가 없는 모델은 금액 0이 아니라 '미산정'으로 구분해 합계를 왜곡하지 않습니다.
- 🛡️ **도구 실행 직전 승인 강제**: 승인이 필요한 도구는 호출되는 순간 Pod 안의 게이트웨이가 붙잡아 두고, 검토자가 어떤 인자로 무엇을 실행하려는지 확인한 뒤 승인할 때까지 실행되지 않습니다. 에이전트가 승인을 요청하지 않아도 우회할 수 없고, 승인 경로에 문제가 있으면 실행 대신 차단(fail-closed)됩니다.
- 🛡️ **MCP 도구 정책**: MCP 서버 단위가 아니라 도구 단위로 호출 권한을 제한합니다. Pod 내부 게이트웨이가 강제하므로 에이전트가 우회할 수 없고, 차단된 도구는 목록에서도 숨겨지며, 자격 증명은 에이전트 컨테이너에 들어가지 않습니다.
- 📡 **이벤트 Trigger (전달 보장)**: 작업 실패·런타임 장애·승인 처리·산출물 생성 같은 플랫폼 이벤트에 다른 Agent가 반응해 후속 작업을 시작합니다. 이벤트는 DB 아웃박스에 적재되고 **전달이 끝난 뒤에야 완료로 기록**되므로 워커가 죽어도 유실되지 않습니다. 실패하면 백오프로 재시도하고 5회 후에는 전달 실패로 남겨 알림을 보내며, 구독자별 전달 원장으로 재전달 시 중복 작업이 생기지 않습니다.
- 🧭 **자율 제어(Planner · Approval · Memory · Delegation)**: 실행 전 계획을 수립하고, 상태 변경 작업은 사람의 승인을 받은 뒤 재개하며, 학습한 사실을 Agent 범위로 영속화하고, 권한 밖의 일은 순환·깊이 제한 아래 다른 Agent에게 위임합니다.
- 🧭 **화면 안에 있는 사용법**: 워크플로와 작업 대기열처럼 개념 설명이 필요한 화면은 상단의 *사용법 안내* 에 4단계 절차와 상태·실행 방식의 뜻을 함께 제공하고, 예시 버튼으로 첫 실행을 바로 만들어 볼 수 있습니다. 한 번 닫으면 기억합니다.
- 🇰🇷 **한국어 우선 관리 콘솔**: 메뉴, 상태, 안내 문구를 한국어로 제공하며 빠른 이동(⌘K)은 한글·영문 키워드를 모두 검색합니다. 열려 있는 화면은 항상 현재 메뉴 하나만 강조되고, `ESC`로 드로어·확인창을 닫을 수 있으며 세션이 만료되면 자동으로 로그인 화면으로 돌아갑니다.
- 🔀 **Multi-Agent Workflow DAG**: 복수 에이전트 간의 순차/병렬/Supervisor 협업 그래프를 시각화하고, 실행 전 순환 참조(Cycle) 및 깊이 제한을 자동 검증합니다.
  - **구조화된 판단**: 라우터의 분기 선택과 LLM 완료 판정은 JSON Schema로 제한된 응답으로 받습니다. 문장에 이름이 언급됐다는 이유로 분기가 선택되거나, 설정하지 않은 완료 조건으로 실패 처리되는 일이 없고, 스키마를 지원하지 않는 게이트웨이에서는 프롬프트 방식으로 자동 대체하되 응답을 반드시 검증합니다.
  - **합의(Consensus) 모드**: 같은 질문을 독립적으로 물어 표결로 결론을 냅니다. 집계는 플랫폼이 직접 계산하며 만장일치·다수결·동률을 구분하고, 소수 의견과 기권까지 기록에 남깁니다.
  - **감독자(Supervisor) 모드**: 감독 에이전트가 결과를 검토해 보완이 필요한 에이전트만 다시 실행시키고, 개정된 결과를 재검토해 승인합니다. 검토 횟수는 제한되며 승인 여부가 실행 기록에 남습니다.
- ♻️ **체크포인트 재시도**: 작업이 실패하면 처음부터 다시 하지 않고 이미 완료한 단계 다음부터 이어서 실행합니다. 같은 추론을 두 번 결제하지 않고, 배포·위임처럼 이미 수행된 부수효과를 반복하지도 않으며, 승인 후 재개도 그때까지의 맥락을 그대로 이어받습니다. 앞선 전제가 바뀐 경우에는 *처음부터* 를 선택할 수 있습니다.
- ⚡ **Runtime 예열 & 워커 자동 확장**: 예약 실행 전에 Runtime을 미리 띄워 콜드 스타트를 없애고, 대기열이 밀리면 워커가 동시 실행 수를 스스로 늘렸다가 한가해지면 되돌립니다. 예열된 Runtime은 사람이 손대는 순간 사용자 소유가 됩니다.
- 🧰 **Runtime 공통 환경 & 바이브코딩 툴체인**: `/etc/pip.conf`, `.condarc`, `/etc/npmrc`, 프록시 변수처럼 모든 Runtime에 공통으로 필요한 설정을 관리자 화면에서 한 번만 정의하면 전체 Pod의 모든 컨테이너에 읽기 전용으로 배포됩니다. Runtime 이미지에는 `python`·`pip`·`conda`·`mamba`와 ruff·pytest·httpx·pandas·openai·typescript 같은 기본 라이브러리가 포함되어, 별도 설치 없이 바이브코딩 에이전트로 바로 쓸 수 있습니다.
- 🔌 **MCP Fabric (Model Context Protocol)**: 사내 MCP 도구 레지스트리와 번들을 관리하고, Sidecar 또는 전용 StatefulSet 모드로 에이전트에 안전하게 주입합니다.
- 🔐 **Envelope Encryption Vault**: 사용자별 개인 키(AES-256-GCM)로 Credential을 암호화하며, Agent 정의에는 원문 대신 식별 참조값만 주입됩니다.
- 🌐 **Session Gateway**: One-Time Launch Ticket을 발급하여 네이티브 Web UI에 대한 안전한 쿠키 격리 및 감사 추적(Audit Trail)을 보장합니다. Runtime Base Domain을 설정하면 Runtime별 전용 Origin(권장)을 사용하고, 설정하지 않으면 Wildcard DNS 없이도 `포털주소/{runtimeId}/` 경로로 같은 세션을 바로 열 수 있습니다.

---

---

## 🎬 3분 실전 워크플로우 데모 영상

https://github.com/user-attachments/assets/agenthub_demo.mp4

> 💡 **[3분 데모 비디오 파일 직접 다운로드 / 보기](docs/media/agenthub_demo.mp4)**  
> 로그인 → Agent Builder 템플릿 생성 → Kubernetes Pod/Node 즉시 스케줄링 → Hermes TUI 및 Ollama Gemma 4 모델 실시간 추론 대화까지 전 과정을 3분(180초) Full HD 영상으로 확인하실 수 있습니다.

---

## 📸 실제 UI 콘솔 및 라이브 런타임 갤러리

| Hermes Live Workspace (Ollama Gemma 4 실시간 추론) | 실시간 내 에이전트 (Kubernetes Pod / Node 즉시 할당) |
| :---: | :---: |
| ![Hermes Live Workspace](docs/screenshots/07_hermes_workspace_chat.png) | ![실시간 내 에이전트](docs/screenshots/05_my_agents_live.png) |
| **Agent Builder 템플릿 생성 드로어** | **에이전트 상세 스펙 및 CRD 상태** |
| ![Agent Builder 생성 드로어](docs/screenshots/04_builder_template_selected.png) | ![에이전트 상세 드로어](docs/screenshots/06_agent_detail_drawer.png) |
| **OpenCode 영속 Workspace** | **Ollama 로컬 모델 카탈로그** |
| ![OpenCode Workspace](docs/screenshots/08_opencode_workspace.png) | ![Ollama 모델 카탈로그](docs/screenshots/10_models_catalog.png) |
| **MCP Fabric 도구 카탈로그 & 번들** | **운영 센터 실시간 감사 로그** |
| ![MCP 도구 카탈로그](docs/screenshots/09_mcp_fabric.png) | ![운영 콘솔](docs/screenshots/11_admin_operations.png) |

> 📌 **전체 33개 스크린샷과 상세 설명은 [사용자 가이드](docs/user-guide.md) 및 [인터랙티브 쇼케이스 페이지](docs/index.html)에서 확인하실 수 있습니다.**

---

## 🏗️ 시스템 아키텍처

```text
Browser ──> AgentHub Portal / Session Gateway ──> PostgreSQL
                 │
                 ├──> AgentRuntime CRD (Kubernetes)
                 │          │
                 │          v
                 └──> Agent Operator ──> StatefulSet + Service + Persistent PVC
                                                │
                               ┌────────────────┴────────────────┐
                               v                                 v
                      OpenCode :4096 (Web IDE)          Hermes API :8642 + UI :9119
```

- **제어면(Control Plane)과 런타임(Data Plane)의 완전한 분리**: AgentHub 포털 컨테이너 내부에서는 어떠한 사용자 코드나 에이전트 프로세스도 직접 실행되지 않습니다.
- **Agent Definition vs Runtime Instance**: Agent 설정 정의는 PostgreSQL에 안전하게 저장되며, 런타임 종료 시 Pod만 스케일 다운(0)되고 PVC와 설정은 영구 보존됩니다.

---

## ⚡ 5분 빠른 시작 (Quickstart)

### 1. 로컬 개발 환경 기동

AgentHub는 단 4개의 필수 환경변수만으로 동작합니다:

```bash
# 1. 환경 설정 파일 생성
cp .env.example .env

# 2. PostgreSQL 데이터베이스 기동
docker compose up -d postgres

# 3. 프론트엔드 및 백엔드 빌드
cd web && npm ci && npm run build && cd ..
make build

# 4. AgentHub 서버 실행
set -a && . ./.env && set +a
./bin/agenthub
```

웹 브라우저에서 `http://localhost:8080`에 접속합니다.  
- **초기 관리자 ID**: `admin`
- **초기 비밀번호**: `local-development-password` (또는 `.env`에 설정한 값)

---

### 2. Minikube / Kubernetes 클러스터 배포

```bash
# 1. Minikube 클러스터 시작
minikube start --driver=docker

# 2. Kubernetes CRD, RBAC 및 Operator 매니페스트 배포
kubectl apply -k deploy/kubernetes

# 3. AgentHub 런타임 베이스 이미지 빌드 및 로드
make image image-base
# base 이미지는 BASE_VERSION 파일을 따르며, 변경이 없으면 이전 태그를 그대로 사용합니다.
minikube image load agenthub:v0.15.0
minikube image load agenthub-base:v0.8.1

# 4. 파드 상태 확인
kubectl get pods -n agent-platform-system
```

---

## 📚 상세 문서 모음

- 📖 **[사용자 & 관리자 매뉴얼](docs/user-guide.md)**: 33개 스크린샷 기반의 전 메뉴 상세 기능 가이드
- 🚀 **[CRU 실전 운영 워크스루](docs/cru-walkthrough.md)**: Workspace, Agent, Workflow 생성/수정 10단계 튜토리얼
- 🌐 **[인터랙티브 웹 쇼케이스](docs/index.html)**: 반응형 다크 테마 기반의 프로젝트 홍보 랜딩 페이지
- 📐 **[시스템 아키텍처 상세](docs/architecture.md)**: 보안 경계, 세션 게이트웨이, 어댑터 명세
- 📦 **[오프라인 설치 가이드](docs/offline-install.md)**: 에어갭(Air-Gapped) 환경에서의 릴리스 아카이브 및 배포

---

## 📄 라이선스 (License)

Apache License 2.0. Copyright (c) 2026 AgentHub Contributors.
