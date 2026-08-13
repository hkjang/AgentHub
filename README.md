# AgentHub

AgentHub is an offline-ready enterprise Agent Runtime Platform: a JupyterHub-like
control plane that spawns an isolated OpenCode or Hermes runtime for each user
and persistent Workspace.

## Included

- Agent Catalog, Builder, definition/runtime separation, Runtime lifecycle UI
- Kubernetes `AgentRuntime` CRD and reconciling Agent Operator
- Persistent Workspace PVCs with stop/start preservation semantics
- Git initialization, CSI VolumeSnapshot requests and snapshot-based restore
- Keycloak OIDC discovery with simple issuer/client/secret administration
- Local bootstrap administrator and encrypted administration settings
- Personal Secret vault, transactional per-user key rotation, scoped API keys
- Optional approval flow that disappears when governance disables it
- Server log console, runtime log API, audit trail and Control Center
- Shared, sidecar and dedicated MCP bindings; scoped REST/OpenAPI and MCP APIs
- Multi-Agent Workflow DAG validation/guardrails and configuration Evaluation
- Runtime-origin Session Gateway with one-time launch tickets for native Web UIs
- Responsive Korean-first UI, keyboard quick navigation, accessible forms, and
  independently scrolling sidebar/detail drawer
- Fully local frontend assets and deterministic offline Docker release workflow

## Required environment variables

Only four AgentHub bootstrap values are accepted:

```text
AGENTHUB_POSTGRES_DSN
AGENTHUB_BOOTSTRAP_ADMIN
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD
AGENTHUB_ENCRYPTION_KEY
```

The encryption key must decode to exactly 32 bytes. All other service settings
are maintained in Administration → System Settings.

## Development

```bash
cp .env.example .env
# edit the four values
docker compose up -d postgres
cd web && npm ci && npm run build && cd ..
set -a && . ./.env && set +a
go run ./cmd/agenthub
```

Open <http://localhost:8080>. Run checks with `make test`, validate Kubernetes
manifests with `make validate`, and run the PostgreSQL-backed smoke suite with
`scripts/integration-smoke.sh` against an isolated test deployment.

See [architecture](docs/architecture.md) and
[offline installation](docs/offline-install.md).
