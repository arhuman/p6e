# syntax=docker/dockerfile:1.7
# The syntax line enables BuildKit features (the cache mounts below). Build with
# DOCKER_BUILDKIT=1, which is the default in modern Docker and `docker buildx`.

# ---- build stage ----
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache ca-certificates
WORKDIR /build

# Dependencies before source, so editing a pipeline node does not re-run
# `go mod download`. p6e has one dependency, but the ordering still saves the
# module-graph work on every rebuild.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Only the packages the binary compiles from: never `COPY . .`. Keep this list
# in sync with .dockerignore.
COPY cmd ./cmd
COPY internal ./internal

# Passed by compose from the same git metadata the Makefile uses, so an image
# and a local build of the same commit report the same version.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux \
    go build -trimpath \
      -ldflags="-s -w \
        -X main.Version=${VERSION} \
        -X main.GitCommit=${COMMIT} \
        -X main.BuildDate=${BUILD_DATE}" \
      -o /out/p6e ./cmd/p6e

# ---- runtime stage ----
FROM alpine:3.21
# ca-certificates so an http.request step can reach an HTTPS endpoint, tzdata
# for time zones, wget for the healthcheck below.
RUN apk add --no-cache ca-certificates tzdata wget

# Non-root with a numeric UID, which lets Kubernetes verify runAsNonRoot without
# a lookup. Group 0 plus g=u keeps it OpenShift-compatible.
RUN addgroup -S p6e && adduser -S -u 1001 -G p6e p6e
WORKDIR /app

COPY --from=builder /out/p6e /usr/local/bin/p6e

# Pipelines are mounted here rather than baked in: the whole point of `serve` is
# a directory an operator controls, and an image rebuild per pipeline edit would
# defeat it. Created empty so the container starts even with nothing mounted,
# where it exits reporting that there is nothing to serve.
RUN mkdir -p /pipelines && chown -R 1001:0 /app /pipelines && chmod -R g=u /app /pipelines

USER 1001
# Never bake a local timezone into a shared image; override per deployment.
ENV TZ=UTC

# 8080 answers webhooks. 8081 is health, readiness and metrics: it holds
# operational detail about every pipeline in the process, so publish it
# deliberately rather than by habit.
EXPOSE 8080 8081

# The admin listener defaults to loopback, which is exactly right here: this
# probe runs inside the container. Publishing metrics to a scraper needs
# --admin-listen :8081, which the compose overlays do.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -q --spider http://127.0.0.1:8081/healthz || exit 1

LABEL org.opencontainers.image.source="https://github.com/arhuman/p6e" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.title="p6e" \
      org.opencontainers.image.description="Typed, low-latency pipeline execution engine"

ENTRYPOINT ["p6e"]
CMD ["serve", "/pipelines"]
