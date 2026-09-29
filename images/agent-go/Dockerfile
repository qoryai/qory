# ghcr.io/qoryai/agent-go and ghcr.io/qoryai/agent-go-docker: an agent image with Go.
# They are the target's images, not Qory's: the images of a repository written in Go,
# built here because the first repository Qory's factory works on is Qory's own. One
# Dockerfile makes both, from a different BASE: agent-go FROM ghcr.io/qoryai/agent, and
# agent-go-docker FROM ghcr.io/qoryai/agent-docker.
#
# Go is at the version the target pins, and GOTOOLCHAIN=local keeps it there: a go.mod
# that asks for a newer toolchain fails instead of downloading one. GOPATH and the caches
# are under HOME, which any user may write. There is no C compiler, so cgo is off.
#
# The release workflow builds both FROM the images it just pushed, by digest; to build
# them yourself, after images/agent and images/agent-docker:
#
#	docker build -t qory-agent-go --build-arg BASE=qory-agent images/agent-go
#	docker build -t qory-agent-go-docker --build-arg BASE=qory-agent-docker images/agent-go

ARG BASE=qory-agent

# Go's release; the sums are the ones go.dev/dl lists.
FROM ${BASE} AS go
ARG TARGETARCH
ARG GO_VERSION=1.27.1
ARG GO_SHA256_AMD64=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
ARG GO_SHA256_ARM64=3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec
ADD https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz /tmp/go.tar.gz
RUN case "${TARGETARCH}" in \
      amd64) sum="${GO_SHA256_AMD64}" ;; \
      arm64) sum="${GO_SHA256_ARM64}" ;; \
      *) echo "no Go sum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && echo "${sum}  /tmp/go.tar.gz" | sha256sum -c - \
 && mkdir /out \
 && tar -xzf /tmp/go.tar.gz -C /out --no-same-owner

FROM ${BASE}
ARG BASE
ARG TITLE=agent-go

COPY --from=go /out/go /usr/local/go

ENV GOTOOLCHAIN=local \
    GOPATH=${HOME}/go \
    GOMODCACHE=${HOME}/go/pkg/mod \
    GOCACHE=${HOME}/.cache/go-build \
    PATH=/usr/local/go/bin:${HOME}/go/bin:${PATH}

LABEL org.opencontainers.image.title="${TITLE}" \
      org.opencontainers.image.description="Qory's agent image with Go, for a repository written in Go." \
      org.opencontainers.image.base.name="${BASE}"
