# syntax=docker/dockerfile:1.7
#
# The Goose runtime image.
#
# Goose is Block's open agent, and the reason it is here is that it speaks the
# Agent Client Protocol natively — `goose acp` runs it as a protocol peer on
# stdio, which is exactly the shape this platform's ACP backend drives. Adding it
# needed no new execution code at all: one line on the runtime descriptor says
# which command to start, and everything else — permissions, tool records,
# metering, the run timeline — is what the protocol already gives.
#
# Separate from agenthub-base for the same reasons Langflow and Qwen Code are: it
# carries its own agent, it moves on the vendor's schedule, and a site that does
# not run Goose agents should not have to load it.
#
# Two programs matter here. `goose` is the agent; `ttyd` is what puts its terminal
# chat in a browser, because an agent with no surface cannot be a workspace
# somebody opens. Both run as the unprivileged runtime user, and ttyd binds to
# loopback: the token-checking proxy sidecar is the only way in.
#
# An agent that cannot install a package is half an agent, so this image carries
# the same arrangement the others do: a virtualenv owned by the runtime user,
# first on PATH, so `pip install` works in a session without root and without
# fighting Debian's externally-managed marker.
FROM debian:bookworm-slim

# Pinned with their checksums: these are binaries downloaded at build time, and
# unpinned ones would make the image unreproducible in exactly the way an offline
# release must not be. The musl build is the static one — no glibc coupling to
# the base image, and less than half the size of the gnu build.
ARG GOOSE_VERSION=1.46.0
ARG GOOSE_SHA256_X86_64=80eca3f89b961baf6ab707b84610a2948108a16ffc7080ef97cb8f05e4500c7e
ARG GOOSE_SHA256_AARCH64=
ARG TTYD_VERSION=1.7.7
ARG TTYD_SHA256_X86_64=8a217c968aba172e0dbf3f34447218dc015bc4d5e59bf51db2f2cd12b7be4f55
ARG TTYD_SHA256_AARCH64=

USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git ca-certificates curl bzip2 openssh-client tini jq ripgrep less procps bash \
      python3 python3-venv \
 && arch="$(uname -m)"; \
    case "${arch}" in \
      x86_64) goose_asset="goose-x86_64-unknown-linux-musl.tar.bz2"; goose_sum="${GOOSE_SHA256_X86_64}"; \
              ttyd_asset="ttyd.x86_64"; ttyd_sum="${TTYD_SHA256_X86_64}" ;; \
      aarch64) goose_asset="goose-aarch64-unknown-linux-musl.tar.bz2"; goose_sum="${GOOSE_SHA256_AARCH64}"; \
              ttyd_asset="ttyd.aarch64"; ttyd_sum="${TTYD_SHA256_AARCH64}" ;; \
      *) echo "unsupported architecture ${arch}" >&2; exit 1 ;; \
    esac; \
    if [ -z "${goose_sum}" ] || [ -z "${ttyd_sum}" ]; then \
      echo "no checksum pinned for ${arch}; refusing to ship an unverified binary" >&2; exit 1; fi; \
    curl -fsSL -o /tmp/goose.tar.bz2 \
      "https://github.com/block/goose/releases/download/v${GOOSE_VERSION}/${goose_asset}"; \
    echo "${goose_sum}  /tmp/goose.tar.bz2" | sha256sum -c -; \
    tar xjf /tmp/goose.tar.bz2 -C /usr/local/bin ./goose; \
    rm -f /tmp/goose.tar.bz2; \
    curl -fsSL -o /usr/local/bin/ttyd "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/${ttyd_asset}"; \
    echo "${ttyd_sum}  /usr/local/bin/ttyd" | sha256sum -c - \
 && chmod 0755 /usr/local/bin/goose /usr/local/bin/ttyd \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /workspace /home/agent/.config/goose /home/agent/.local/share/goose /home/agent/.local/state/goose \
 && chown -R 10000:10000 /workspace /home/agent

# The toolchain an agent writing Python reaches for before it can run anything.
# Deliberately short: what is missing is one `pip install` away, and every
# megabyte here is a megabyte every site that runs a Goose agent has to carry
# through its media path.
ARG AGENT_TOOLCHAIN="pip setuptools wheel ruff black mypy pytest pytest-asyncio ipython rich typer httpx requests pyyaml python-dotenv jinja2 tqdm pydantic tabulate openai mcp"
RUN python3 -m venv /opt/agenthub/venv \
 && /opt/agenthub/venv/bin/pip install --no-cache-dir --upgrade ${AGENT_TOOLCHAIN} \
 && mkdir -p /home/agent/.cache \
 && chown -R 10000:10000 /opt/agenthub /home/agent

COPY deploy/runtime/agenthub-report-config.sh /usr/local/bin/agenthub-report-config
COPY deploy/runtime/goose-configure.sh /usr/local/bin/agenthub-goose-configure
COPY deploy/runtime/goose-shell.sh /usr/local/bin/agenthub-goose-shell
COPY deploy/runtime/goose-run.sh /usr/local/bin/agenthub-goose-run
COPY deploy/runtime/goose-toolchain.sh /etc/profile.d/agenthub-toolchain.sh
RUN chmod 0755 /usr/local/bin/agenthub-report-config /usr/local/bin/agenthub-goose-configure \
      /usr/local/bin/agenthub-goose-shell /usr/local/bin/agenthub-goose-run \
 && chmod 0644 /etc/profile.d/agenthub-toolchain.sh

USER 10000:10000
# The toolchain leads PATH so `python` and `pip` mean the agent's environment
# rather than the system interpreter.
ENV PATH=/home/agent/.venv/bin:/opt/agenthub/venv/bin:$PATH \
    HOME=/home/agent \
    AGENTHUB_RUNTIME_TYPE=goose \
    AGENTHUB_WORKSPACE=/workspace \
    GOOSE_CONFIG_HOME=/home/agent/.config/goose \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_CACHE_DIR=/home/agent/.cache/pip \
    PYTHONUNBUFFERED=1
WORKDIR /workspace
EXPOSE 7681
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/ttyd", "--port", "7681", "--interface", "127.0.0.1", "--writable", "/usr/local/bin/agenthub-goose-shell"]
