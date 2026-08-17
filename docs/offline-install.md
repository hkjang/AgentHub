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

The control plane version lives in `VERSION` and the runtime base image has its
own `BASE_VERSION`, because the base image is large and slow to build and is
only rebuilt when something it is built from changes. A release whose notes say
the base image is unchanged has no `agenthub-base-*.tar.gz` asset, and its notes
name the base tag it runs on: keep using the archive you already loaded. Both
files are read by `make release-archives`, so pass overrides only when building
something other than the checked-out release.

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

docker load < agenthub-v0.7.0.tar.gz
docker load < agenthub-base-v0.7.0.tar.gz
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

## Runtime browser domain

Native OpenCode/Hermes browser sessions require a Runtime-specific origin.
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

## Kubernetes

1. Load both images into the offline registry and update image references in
   `deploy/kubernetes`.
2. Replace every placeholder in `agenthub-bootstrap` using the cluster's secret
   management process. Do not commit the populated Secret.
3. Apply `kubectl apply -k deploy/kubernetes`.
4. Configure OIDC and Runtime settings in AgentHub. In-cluster mode requires no
   Kubernetes token in the database.

The runtime namespace enforces the Kubernetes `restricted` Pod Security level.
Review storage classes, default-deny egress rules, ingress TLS, PostgreSQL TLS,
backup/restore, and image registry trust before production use.
