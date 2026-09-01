# syntax=docker/dockerfile:1.7
FROM node:22-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-bookworm AS go-build
ARG VERSION
ARG BASE_VERSION=0.14.0-dev
ARG LANGFLOW_VERSION=0.2.0-dev
ARG QWENCODE_VERSION=0.2.0-dev
ARG JUPYTER_VERSION=0.1.0-dev
ARG NODERED_VERSION=0.1.0-dev
ARG N8N_VERSION=0.2.0-dev
ARG GOOSE_VERSION=0.1.0-dev
ARG HOLMES_VERSION=0.2.0-dev
ARG BROWSERCODE_VERSION=0.2.0-dev
ARG OPENCODEREVIEW_VERSION=0.1.0-dev
ARG ORCA_VERSION=0.5.0-dev
ARG OPENHANDS_VERSION=1.43.1-dev
ARG PI_VERSION=0.1.0-dev
ARG PRIMEAGENT_VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/hkjang/AgentHub/internal/buildinfo.Version=${VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.Commit=${COMMIT} -X github.com/hkjang/AgentHub/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/hkjang/AgentHub/internal/buildinfo.BaseVersion=${BASE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.LangflowVersion=${LANGFLOW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.QwenCodeVersion=${QWENCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.JupyterVersion=${JUPYTER_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.NodeREDVersion=${NODERED_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.N8NVersion=${N8N_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.GooseVersion=${GOOSE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.HolmesVersion=${HOLMES_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.BrowserCodeVersion=${BROWSERCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenCodeReviewVersion=${OPENCODEREVIEW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OrcaVersion=${ORCA_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenHandsVersion=${OPENHANDS_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PiVersion=${PI_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PrimeAgentVersion=${PRIMEAGENT_VERSION}" -o /out/agenthub ./cmd/agenthub \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/hkjang/AgentHub/internal/buildinfo.Version=${VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.Commit=${COMMIT} -X github.com/hkjang/AgentHub/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/hkjang/AgentHub/internal/buildinfo.BaseVersion=${BASE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.LangflowVersion=${LANGFLOW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.QwenCodeVersion=${QWENCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.JupyterVersion=${JUPYTER_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.NodeREDVersion=${NODERED_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.N8NVersion=${N8N_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.GooseVersion=${GOOSE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.HolmesVersion=${HOLMES_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.BrowserCodeVersion=${BROWSERCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenCodeReviewVersion=${OPENCODEREVIEW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OrcaVersion=${ORCA_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenHandsVersion=${OPENHANDS_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PiVersion=${PI_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PrimeAgentVersion=${PRIMEAGENT_VERSION}" -o /out/agenthub-operator ./cmd/operator \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/hkjang/AgentHub/internal/buildinfo.Version=${VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.Commit=${COMMIT} -X github.com/hkjang/AgentHub/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/hkjang/AgentHub/internal/buildinfo.BaseVersion=${BASE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.LangflowVersion=${LANGFLOW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.QwenCodeVersion=${QWENCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.JupyterVersion=${JUPYTER_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.NodeREDVersion=${NODERED_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.N8NVersion=${N8N_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.GooseVersion=${GOOSE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.HolmesVersion=${HOLMES_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.BrowserCodeVersion=${BROWSERCODE_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenCodeReviewVersion=${OPENCODEREVIEW_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OrcaVersion=${ORCA_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.OpenHandsVersion=${OPENHANDS_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PiVersion=${PI_VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.PrimeAgentVersion=${PRIMEAGENT_VERSION}" -o /out/agenthub-worker ./cmd/worker \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/agenthub-runtime-proxy ./cmd/runtime-proxy

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=go-build /out/agenthub /out/agenthub-operator /out/agenthub-worker /app/
# Platform sidecars run this image, so the binary has to live where the
# generated Pod spec expects it.
COPY --from=go-build /out/agenthub-runtime-proxy /usr/local/bin/agenthub-runtime-proxy
COPY --from=web-build /src/web/dist /app/web/dist
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/agenthub"]
