# Build stage runs natively on the builder's architecture and cross-compiles to the target
# architecture. The Go toolchain lives only here and is discarded from the final image.
# buildx sets BUILDPLATFORM, TARGETOS, and TARGETARCH automatically for each requested
# --platform, so a single amd64 (or arm64) builder produces both release architectures without
# emulating the compiler.
FROM --platform=$BUILDPLATFORM golang:1.26.3-alpine3.22 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

# Resolve and cryptographically verify modules in a separate, cacheable layer before copying
# the source, so a source-only change does not re-download or re-verify dependencies.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

# CGO is disabled so the binary is fully static and cross-compilation needs no C toolchain or
# QEMU. -trimpath and -buildvcs=false keep the output independent of the build path and of any
# VCS metadata, which keeps the artifact reproducible across builders.
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/docify-repo ./cmd/docify-repo

# Runtime stage: only the binary, Git, and CA certificates. No language runtime, no package
# manager for target repositories, no cloud CLI, no GitHub CLI, and no shell script in the
# execution path. Pin this base image (and the golang image above) to an immutable @sha256
# digest before release: the runtime digest also freezes the Alpine package index, which makes
# the installed git and ca-certificates versions reproducible. See docs/releasing.md.
FROM alpine:3.22

# Populated by the release pipeline from the metadata action and surfaced as OCI labels so a
# pushed digest is traceable back to its version and source revision. They do not affect the
# binary, so identical source always produces an identical /usr/local/bin/docify-repo layer.
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=unknown

LABEL org.opencontainers.image.title="docify-repo" \
      org.opencontainers.image.description="Deterministic repository documentation generator for CI" \
      org.opencontainers.image.source="https://github.com/amit-upadhyay-IT/docify-repo" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"

# ca-certificates: TLS trust anchors for the LLM and GitHub HTTPS endpoints.
# git: native diff, rename detection, and object publishing. Nothing else is installed.
# A fixed numeric UID/GID lets image scanners and `--user` overrides reason about the runtime
# identity without a passwd lookup.
RUN apk add --no-cache ca-certificates git \
    && addgroup -S -g 10001 docify \
    && adduser -S -u 10001 -G docify -h /home/docify docify \
    && mkdir -p /workspace \
    && chown docify:docify /workspace

COPY --from=build --chown=root:root /out/docify-repo /usr/local/bin/docify-repo

# There is no long-running process to probe; declaring NONE stops orchestrators from scheduling
# health checks against a short-lived CI container.
HEALTHCHECK NONE

ENV HOME=/home/docify
USER 10001:10001
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/docify-repo"]
