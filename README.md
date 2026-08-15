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

[🌐 웹 쇼케이스 둘러보기](docs/index.html) · [📖 사용자 매뉴얼](docs/user-guide.md) · [🚀 CRU 실전 워크스루](docs/cru-walkthrough.md) · [📐 시스템 아키텍처](docs/architecture.md)

</div>

---

## 🌟 핵심 가치 (Core Highlights)

- 🔒 **완전 폐쇄망 (Offline-Ready)**: 외부 인터넷 연결 없이 로컬 Docker 레지스트리와 내부 Kubernetes 클러스터 상에서 100% 결정론적으로 동작합니다.
- 📦 **영속 Workspace & CSI 스냅샷**: Agent Pod가 종료/재생성되어도 사용자의 소스 코드와 데이터는 PVC에 보존되며, CSI VolumeSnapshot을 통해 언제든 원하는 시점으로 복원할 수 있습니다.
- 🤖 **4대 엔터프라이즈 에이전트 런타임**: 인터랙티브 코딩 워크스페이스(**OpenCode** / **Qwen Code**) 및 자율 에이전트 어시스턴트(**Hermes** / [**Qwen Paw**](https://qwenpaw.agentscope.io/))를 사용자 전용 비루트(Non-root) Pod로 격리 기동합니다.
  - **Qwen Paw (AgentScope)**: 3계층 ReMe 메모리, 커널 샌드박스 보안 가드, 스킬/MCP 확장 및 Qwen 모델 자율 추론을 지원하는 개인 에이전트 워크스테이션
- 🔀 **Multi-Agent Workflow DAG**: 복수 에이전트 간의 순차/병렬/Supervisor 협업 그래프를 시각화하고, 실행 전 순환 참조(Cycle) 및 깊이 제한을 자동 검증합니다.
- 🔌 **MCP Fabric (Model Context Protocol)**: 사내 MCP 도구 레지스트리와 번들을 관리하고, Sidecar 또는 전용 StatefulSet 모드로 에이전트에 안전하게 주입합니다.
- 🔐 **Envelope Encryption Vault**: 사용자별 개인 키(AES-256-GCM)로 Credential을 암호화하며, Agent 정의에는 원문 대신 식별 참조값만 주입됩니다.
- 🌐 **Session Gateway**: One-Time Launch Ticket을 발급하여 네이티브 Web UI에 대한 안전한 쿠키 격리 및 감사 추적(Audit Trail)을 보장합니다.

---

---

## 🎬 3분 실전 워크플로우 데모 영상

https://github.com/user-attachments/assets/agenthub_demo.mp4

> 💡 **[3분 데모 비디오 파일 직접 다운로드 / 보기](docs/media/agenthub_demo.mp4)**  
> 로그인 → Agent Builder 템플릿 생성 → Kubernetes Pod/Node 즉시 스케줄링 → Hermes TUI 및 Ollama Gemma 4 모델 실시간 추론 대화까지 전 과정을 3분(180초) Full HD 영상으로 확인하실 수 있습니다.

---

## 📸 실제 UI 콘솔 및 라이브 런타임 갤러리

| Hermes Live Workspace (Ollama Gemma 4 실시간 추론) | 실시간 My Agents (Kubernetes Pod / Node 즉시 할당) |
| :---: | :---: |
| ![Hermes Live Workspace](docs/screenshots/07_hermes_workspace_chat.png) | ![실시간 My Agents](docs/screenshots/05_my_agents_live.png) |
| **Agent Builder 템플릿 생성 드로어** | **에이전트 상세 스펙 및 CRD 상태** |
| ![Agent Builder 생성 드로어](docs/screenshots/04_builder_template_selected.png) | ![에이전트 상세 드로어](docs/screenshots/06_agent_detail_drawer.png) |
| **OpenCode 영속 Workspace** | **Ollama 로컬 모델 카탈로그** |
| ![OpenCode Workspace](docs/screenshots/08_opencode_workspace.png) | ![Ollama 모델 카탈로그](docs/screenshots/10_models_catalog.png) |
| **MCP Fabric 도구 카탈로그 & 번들** | **Control Center 실시간 감사 로그** |
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
minikube image load agenthub:v0.1.0
minikube image load agenthub-base:v0.1.0

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
