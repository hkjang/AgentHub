# syntax=docker/dockerfile:1.7
FROM node:22-bookworm-slim AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-bookworm AS go-build
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/hkjang/AgentHub/internal/buildinfo.Version=${VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.Commit=${COMMIT} -X github.com/hkjang/AgentHub/internal/buildinfo.BuildTime=${BUILD_TIME}" -o /out/agenthub ./cmd/agenthub \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/hkjang/AgentHub/internal/buildinfo.Version=${VERSION} -X github.com/hkjang/AgentHub/internal/buildinfo.Commit=${COMMIT} -X github.com/hkjang/AgentHub/internal/buildinfo.BuildTime=${BUILD_TIME}" -o /out/agenthub-operator ./cmd/operator

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=go-build /out/agenthub /out/agenthub-operator /app/
COPY --from=web-build /src/web/dist /app/web/dist
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/app/agenthub"]
