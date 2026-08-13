# AgentHub architecture

AgentHub is the control plane. Agent processes never execute inside the Portal container.

```text
Browser ──> AgentHub API / Session Gateway ──> PostgreSQL
                 │
                 ├──> AgentRuntime CRD
                 │          │
                 │          v
                 └──> Agent Operator ──> StatefulSet + Service + PVC
                                                │
                              ┌─────────────────┴─────────────────┐
                              v                                   v
                     OpenCode :4096           Hermes API :8642 + UI :9119
```

The persistent `agent_definitions` record is intentionally separate from an
`agent_runtimes` instance. Stopping a runtime scales its StatefulSet to zero;
the Agent definition and Workspace PVC remain.

## Security boundaries

- Browser sessions are random, hashed in PostgreSQL, `HttpOnly`, `SameSite=Lax`, and protected by a separate CSRF token.
- Local passwords use bcrypt. Keycloak tokens are verified using OIDC discovery, issuer, audience, signature, expiry, state, and PKCE.
- The only bootstrap configuration read from environment variables is the PostgreSQL DSN, bootstrap administrator identity/password, and 32-byte encryption key.
- OIDC client secrets and external Kubernetes tokens use AES-256-GCM with authenticated context binding.
- Each user owns a random data-encryption key wrapped by the deployment encryption key. Rotation re-encrypts all personal secrets transactionally.
- Runtime Pods are non-root, use RuntimeDefault seccomp, drop all Linux capabilities, disallow privilege escalation, have a read-only root filesystem, and never receive a Kubernetes ServiceAccount token.
- A default-deny NetworkPolicy permits only the runtime ports from the control-plane namespace. DNS, selected Model/MCP endpoint ports, and administrator-approved CIDR/port destinations form the generated egress allow-list.
- API keys are shown once and only their SHA-256 digest is persisted.
- Audit records never contain secret plaintext.

## Browser session gateway

OpenCode's native Web UI uses origin-root assets and APIs, so it cannot be
reliably mounted under a Portal subpath. AgentHub issues a two-minute, one-use
launch ticket and sends the browser to a Runtime-specific origin such as
`<runtime-id>.agents.company.local`. The ticket is exchanged for an encrypted,
host-only, `HttpOnly` Runtime cookie. Portal cookies and upstream cookies are
never forwarded across this boundary; the gateway injects the per-Runtime
credential server-side and audits the launch.

Hermes Dashboard runs only on Pod loopback (`127.0.0.1:9120`). A small
`agenthub-runtime-proxy` sidecar exposes port 9119, checks the per-Runtime token,
and reports ready only after the Dashboard responds. NetworkPolicy then limits
that port to AgentHub's control-plane namespace.

## Runtime adapter contract

The Portal implements a common `Spawner` interface. Kubernetes is the default
implementation and creates an `AgentRuntime` resource. Runtime-specific launch
behavior lives in the offline `agenthub-base` image:

- `opencode`: `opencode serve --hostname 0.0.0.0 --port 4096`
- `hermes`: `hermes gateway run --no-supervise` with its authenticated API on 8642 and the loopback Dashboard/proxy pair on 9120/9119
- `custom`: an administrator-approved image and command

The Operator compiles each selected MCP Bundle into native runtime configuration:

- Shared endpoints are injected as remote MCP servers.
- Sidecar mode adds a restricted container to the Agent Pod and routes the
  runtime to its loopback Streamable HTTP endpoint.
- Dedicated mode creates a separately isolated StatefulSet, Service, and
  NetworkPolicy owned by the same `AgentRuntime` CRD.

OpenCode receives the generated configuration through `OPENCODE_CONFIG`;
Hermes receives a generated `config.yaml` under its isolated `HERMES_HOME`.
