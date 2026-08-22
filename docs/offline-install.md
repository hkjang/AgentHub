# Offline installation

## Docker Compose

On an internet-connected build host, create the release archives:

```bash
make release-archives
```

This produces `release/` containing the image archives and a `SHA256SUMS`
manifest. A GitHub release asset may not exceed 2 GiB, so any archive larger
than that is emitted as `<name>.tar.gz.part-aa`, `.part-ab`, … instead of a
single file; smaller archives stay a plain `.tar.gz`. Set `RELEASE_CHUNK` to
change the split size.

The control plane version lives in `VERSION`, the shared runtime base image has
its own `BASE_VERSION`, and the runtimes that do not boot from it have theirs:
`LANGFLOW_VERSION`, `QWENCODE_VERSION`, `JUPYTER_VERSION`, `GOOSE_VERSION`,
`HOLMES_VERSION`, `BROWSERCODE_VERSION`, `NODERED_VERSION` and `N8N_VERSION`.
Each is versioned separately because each is large, slow to build and only
rebuilt when something it is built from changes. A release whose notes say an
image is unchanged has no archive for it, and the notes name the tag it runs on:
keep using the archive you already loaded. All three files are read by
`make release-archives`, so pass overrides only when building something other
than the checked-out release.

Each runtime image is only needed by sites that run Agents of that type, and
nothing else depends on any of them: skipping one costs nothing, and an Agent of
that runtime type simply will not start until its image is loaded. The one
relationship worth knowing is that `agenthub-jupyter` is built from
`agenthub-qwencode` — it already contains it, so a site running only the
notebook runtime does not need both archives.

Transfer the whole `release/` directory, `compose.yaml`, and the Kubernetes
manifests through the approved media path. On the offline host, verify the media
first, then reassemble any split archive before loading it:

```bash
sha256sum -c SHA256SUMS

# Only needed for archives that were split; a plain .tar.gz loads directly.
for archive in *.tar.gz.part-aa; do
  name="${archive%.part-aa}"
  cat "${name}".part-* > "${name}"
done

docker load < agenthub-v0.89.0.tar.gz
docker load < agenthub-base-v0.13.0.tar.gz
# Only if this site runs Agents of that runtime type.
docker load < agenthub-langflow-v0.2.0.tar.gz
docker load < agenthub-qwencode-v0.2.0.tar.gz
docker load < agenthub-jupyter-v0.1.0.tar.gz
docker load < agenthub-goose-v0.1.0.tar.gz
docker load < agenthub-holmes-v0.2.0.tar.gz
docker load < agenthub-browsercode-v0.2.0.tar.gz
docker load < agenthub-nodered-v0.1.0.tar.gz
docker load < agenthub-n8n-v0.1.0.tar.gz
export AGENTHUB_BOOTSTRAP_ADMIN=admin
export AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD='a-long-unique-password'
export AGENTHUB_ENCRYPTION_KEY="$(openssl rand -base64 32)"
docker compose up -d
```

The Portal listens on host port 8080. Change the host-side Compose port mapping
in `compose.yaml` if the offline host reserves that port; AgentHub itself still
accepts only the four bootstrap environment variables shown above.

For an external PostgreSQL server, additionally set
`AGENTHUB_POSTGRES_DSN`. Preserve `AGENTHUB_ENCRYPTION_KEY` in the enterprise
secret escrow: losing or replacing it makes encrypted settings and personal
keyrings unrecoverable.

After the first login, configure General → Public URL, Authentication →
Keycloak, Kubernetes, Session Gateway, governance, logging, and offline policy
in the admin UI.

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

1. Load both images into the offline registry and update image references in
   `deploy/kubernetes`.
2. Replace every placeholder in `agenthub-bootstrap` using the cluster's secret
   management process. Do not commit the populated Secret.
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

The runtime namespace enforces the Kubernetes `restricted` Pod Security level.
Review storage classes, default-deny egress rules, ingress TLS, PostgreSQL TLS,
backup/restore, and image registry trust before production use.
