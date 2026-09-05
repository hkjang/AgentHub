# Offline installation

## External PostgreSQL is required

PostgreSQL is deliberately **not** included in the AgentHub container, release
assets, offline downloader, Compose deployment, SBOM set, or provenance subject
list. Provision it as an independently operated service before installing
AgentHub. The release is tested with PostgreSQL major version 17.

The API and every worker must be able to reach the database. Configure database
authentication, TLS, backups and restore drills, monitoring, capacity and
upgrades in the database platform. Supply its connection string through
`AGENTHUB_POSTGRES_DSN`; installation fails closed when that value is absent.

## Select and fetch a release bundle

Every GitHub release publishes `offline-bundle.json` and the static
`agenthub-offline-linux-amd64` helper. The manifest records each archive's
original release, exact size and SHA-256. This matters because runtime images
have independent versions: an unchanged image may correctly come from an older
stable release instead of being republished under every control-plane release.

On an internet-connected staging host, download those two small assets from the
target release, make the helper executable, then choose exactly one runtime
selection mode:

```bash
chmod 0755 agenthub-offline-linux-amd64

# Inspect the exact files and total transfer size first.
./agenthub-offline-linux-amd64 plan \
  --manifest offline-bundle.json \
  --runtime openhands --runtime orca

# Use the same selection for the download.
./agenthub-offline-linux-amd64 fetch \
  --manifest offline-bundle.json \
  --output-dir ./agenthub-offline \
  --runtime openhands --runtime orca
```

Use `--all-runtimes` to prepare every platform runtime, or `--no-runtimes` for
the control plane only. `--runtime` may be repeated and accepts catalog runtime
types such as `opencode`, `hermes`, `qwenpaw`, `jupyter`, `openhands`, `orca` and
`pi`. A custom runtime image is administrator-supplied and cannot be selected
from this bundle. Dependencies and duplicate archives are resolved
automatically.

`fetch` verifies the declared size and SHA-256 before atomically accepting every
file. Existing verified files are reused. A GitHub asset larger than 2 GiB is
published as contiguous `.part-aa`, `.part-ab`, … files; all parts must come
from one release, and the helper never mixes or silently skips them.

## Verify checksums, provenance and SBOMs

Before moving files across the air gap, verify the target release on the
connected staging host. Download `SHA256SUMS` and the release's Sigstore bundle
in addition to the selected transfer set. `SHA256SUMS` lists the assets produced
before attestation, so it does not list itself or the Sigstore bundle. Starting
with releases that use this workflow, the release also includes an SPDX JSON
SBOM for the control image and every runtime image newly published in that
release. An archive reused from an older release may not have an SBOM; its exact
source, size and digest are still bound into the signed `offline-bundle.json`.

```bash
export AGENTHUB_VERSION=v0.234.0

# Use this form after downloading every asset listed in SHA256SUMS.
sha256sum -c SHA256SUMS

# Repeat for each asset accepted into the transfer set.
gh attestation verify offline-bundle.json \
  --repo hkjang/AgentHub \
  --signer-workflow hkjang/AgentHub/.github/workflows/release.yaml \
  --source-ref "refs/tags/${AGENTHUB_VERSION}"
```

For verification inside the disconnected environment, obtain the trusted root
on the connected host and transfer it with the release's
`agenthub-<version>.provenance.sigstore.json` bundle:

```bash
# Connected host
gh attestation trusted-root > trusted_root.jsonl

# Disconnected host; repeat for each transferred asset.
gh attestation verify offline-bundle.json \
  --repo hkjang/AgentHub \
  --bundle "agenthub-${AGENTHUB_VERSION}.provenance.sigstore.json" \
  --custom-trusted-root trusted_root.jsonl \
  --signer-workflow hkjang/AgentHub/.github/workflows/release.yaml \
  --source-ref "refs/tags/${AGENTHUB_VERSION}"
```

Use a current GitHub CLI with the `gh attestation` commands. Treat a checksum,
provenance, signer identity, source tag or manifest validation failure as a hard
stop; do not load that archive.

Transfer `offline-bundle.json` and the complete `agenthub-offline/` directory
through the approved media path. Do not add a PostgreSQL image to that media.

## Load and run with Docker Compose

On the offline Linux amd64 host, use the copied helper and exactly the same
selection. `load` verifies every file again and streams split archives directly
to `docker load`; it does not create another multi-gigabyte reassembled file.

```bash
cd agenthub-offline
chmod 0755 agenthub-offline-linux-amd64
./agenthub-offline-linux-amd64 load \
  --manifest ../offline-bundle.json \
  --input-dir . \
  --runtime openhands --runtime orca

install -m 0600 agenthub-offline.env.example .env
install -m 0644 agenthub-offline-compose.yaml compose.yaml
```

Edit `.env` and set the externally operated PostgreSQL DSN plus the bootstrap
values. Never commit or send the populated file back through the connected
staging host.

```dotenv
AGENTHUB_POSTGRES_DSN=postgres://agenthub:<password>@postgres.example.internal:5432/agenthub?sslmode=verify-full
AGENTHUB_BOOTSTRAP_ADMIN=admin
AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD=<a-long-unique-password>
AGENTHUB_ENCRYPTION_KEY=<base64-encoded-32-byte-key>
```

Then validate and start the deployment:

```bash
docker compose config --quiet
docker compose up -d
docker compose exec agenthub /app/agenthub version --json
```

The offline Compose file has no PostgreSQL service or image. The Portal listens
on host port 8080. Change only the host-side port mapping if that port is
reserved.

`AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD` is read once to create the first
administrator. Rotate it from the profile menu after first login; changing the
environment value later does not reset an existing account. Preserve
`AGENTHUB_ENCRYPTION_KEY` in enterprise secret escrow: losing or replacing it
makes encrypted settings and personal keyrings unrecoverable.

After the first login, configure General → Public URL, Authentication →
Keycloak, Kubernetes, Session Gateway, governance, logging, and offline policy
in the admin UI.

## Build archives locally (maintainers)

An internet-connected maintainer can still build every catalog image and create
`release/` locally:

```bash
make release-archives
```

Archives are compressed deterministically with `gzip -n`, split below GitHub's
per-asset limit, and listed in `release/SHA256SUMS`. The authoritative runtime
image/version/source/build/health metadata is `runtime-images.json`; the
individual `*_VERSION` files remain the published tag values. Changing a runtime
image source without bumping its version is rejected by the release workflow.

## Internal package mirrors

The runtime image ships `pip`, `conda`/`mamba` and npm, all of which default to
registries an offline site cannot reach. Point them at the internal mirrors once
in Administration → System Settings → Runtime Environment; every runtime is then
provisioned with the same files and variables:

```text
/etc/pip.conf            [global] index-url = https://nexus.company.local/repository/pypi-all/simple
/home/agent/.condarc     channels / default_channels / channel_alias → the internal conda mirror
/etc/npmrc               registry=https://nexus.company.local/repository/npm-all/
HTTPS_PROXY / NO_PROXY   only if the site reaches its mirrors through a proxy
```

The screen offers those three files as one-click samples. Content is delivered
through a ConfigMap, so put no credentials in it, and remember that the mirror
host must also be in the network profile's egress allow-list. A change applies
when a runtime next starts; a running runtime picks it up on restart.

## Tracing (optional)

AgentHub exports OpenTelemetry traces when Administration → System Settings →
Observability names an OTLP/HTTP collector, or when `AGENTHUB_OTLP_ENDPOINT` is
set. Leave it unset and tracing is off: no exporter is installed and no egress is
attempted, which is the right default for a site with no collector. The setting is
read at startup, so the API and the worker have to be restarted to pick it up, and
the collector has to be reachable from both.

## Runtime browser domain

An origin per runtime is the recommended way to open a workspace: it keeps a
runtime's UI out of the Portal's origin. It is not a prerequisite for getting
started — with no Runtime Base Domain configured, AgentHub serves the same
session from the Portal's own origin at `https://<portal>/{runtimeId}/`, using
the same one-use launch ticket. Nothing needs to be configured for that, and it
is worth moving to the domain below once wildcard DNS exists.

Create wildcard DNS for `*.agents.company.local`, issue a wildcard TLS
certificate for that domain, and route both the Portal hostname and wildcard
Runtime hostname to the AgentHub Service. Then set Administration → System
Settings → Session Gateway:

```text
Enabled: true
Scheme: https
Runtime Base Domain: agents.company.local
Session hours: 8
```

For a single-host local test only, `http` and `localhost:8080` are accepted;
modern browsers resolve `*.localhost` to loopback. Production validation rejects
plain HTTP. A sample wildcard Ingress is provided under `deploy/examples`.

Leaving the toggle off — or leaving Runtime Base Domain empty — is what selects
the path form. A runtime UI that requests assets from the origin root is matched
to its runtime by the `Referer` of the request, so a request that carries none
(a WebSocket handshake, for instance) is not proxied; that is the practical limit
of the shared origin.

## Kubernetes

1. Load the control image and every runtime image selected in the bundle into
   the offline registry, then update their image references in
   `deploy/kubernetes` and the AgentHub image catalog.
2. Replace every placeholder in `agenthub-bootstrap`, including the external
   `AGENTHUB_POSTGRES_DSN`, using the cluster's secret management process. Do
   not commit the populated Secret.
3. Apply `kubectl apply -k deploy/kubernetes`.
4. Configure OIDC and Runtime settings in AgentHub. In-cluster mode requires no
   Kubernetes token in the database.

When upgrading, apply the manifests again rather than only replacing the image.
`deploy/kubernetes/crd.yaml` is part of the kustomization, and a CRD silently
prunes fields its schema does not declare: a cluster left on an older
`AgentRuntime` definition accepts what the control plane writes and drops the
newer sections from it. The most visible symptom is the platform-wide runtime
environment — `/etc/pip.conf` and friends — never reaching any Pod. Saving that
setting detects it and names this step.

The control-plane namespace enforces the Kubernetes `restricted` Pod Security
level. The runtime namespace enforces `privileged`, because Kubernetes baseline
and restricted policies both reject `hostNetwork`; Runtime Pods still receive
restricted container security contexts, while Pod Security audit/warn report
drift. Administrators can turn host networking off in System Settings →
Kubernetes when the cluster does not require it. Review storage classes,
default-deny egress rules, ingress TLS, external PostgreSQL TLS and
backup/restore, and image registry trust before production use.
