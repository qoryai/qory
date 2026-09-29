# agent-docker: the agent image with a Docker of the agent's own. It is agent with
# Docker's daemon, its containerd and runc, and the docker command, from Docker's static
# release, and iptables for the daemon's networks.
#
# The image starts nothing. Under a runtime that runs a daemon without privileges, such as
# sysbox-runc, the runner's helper starts dockerd as the enclosure's root, on
# /var/run/docker.sock alone and with the system's directories alone as its PATH, then
# runs the agent as its user: so every program the daemon runs is in /usr/local/bin or
# /usr/sbin, and there is no entry point here, no daemon.json, nothing that listens on a
# port, and no volume for /var/lib/docker, which the wall gives a volume of the run's.
# qory image check reports the image as able to carry a Docker.
#
# Build it FROM agent, from the checkout of the qory release you run:
#
#	docker build -t qory-agent-docker --build-arg BASE=qory-agent images/agent-docker

ARG BASE=qory-agent
# The Debian the downloads are checked on, on the machine that builds.
ARG FETCH_IMAGE=debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
# The Debian snapshot the packages come from, agent's.
ARG DEBIAN_SNAPSHOT=20260918T000000Z
# Docker's static binaries. download.docker.com publishes no checksums for them, so the
# sums are of the archives as downloaded on 2026-09-29, one per architecture.
ARG DOCKER_VERSION=29.8.1
ARG DOCKER_SHA256_AMD64=d8db66739d2e28d4933786d73e918d9be643a67fbd835db1bf740d650a259e70
ARG DOCKER_SHA256_ARM64=667395fbffab52901b80181dfbb39ea76da2fbd7642c4fbddd24e42146b07b48

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS docker-amd64
ARG DOCKER_VERSION
ARG DOCKER_SHA256_AMD64
ADD https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz /tmp/docker.tgz
RUN echo "${DOCKER_SHA256_AMD64}  /tmp/docker.tgz" | sha256sum -c - \
 && mkdir /out \
 && tar -xzf /tmp/docker.tgz -C /out --strip-components=1 --no-same-owner \
      docker/dockerd docker/containerd docker/containerd-shim-runc-v2 docker/runc \
      docker/docker-init docker/docker-proxy docker/docker

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS docker-arm64
ARG DOCKER_VERSION
ARG DOCKER_SHA256_ARM64
ADD https://download.docker.com/linux/static/stable/aarch64/docker-${DOCKER_VERSION}.tgz /tmp/docker.tgz
RUN echo "${DOCKER_SHA256_ARM64}  /tmp/docker.tgz" | sha256sum -c - \
 && mkdir /out \
 && tar -xzf /tmp/docker.tgz -C /out --strip-components=1 --no-same-owner \
      docker/dockerd docker/containerd docker/containerd-shim-runc-v2 docker/runc \
      docker/docker-init docker/docker-proxy docker/docker

FROM docker-${TARGETARCH} AS docker

FROM ${BASE}
ARG BASE
ARG DEBIAN_SNAPSHOT

# From the snapshot, as in agent.
RUN apt="-o Dir::Etc::SourceList=/tmp/snapshot.sources -o Dir::Etc::SourceParts=- -o Acquire::Check-Valid-Until=false" \
 && sed -e "s|^URIs: http://deb.debian.org/debian-security\$|URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}|" \
        -e "s|^URIs: http://deb.debian.org/debian\$|URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}|" \
        /etc/apt/sources.list.d/debian.sources > /tmp/snapshot.sources \
 && ! grep -q deb.debian.org /tmp/snapshot.sources \
 && apt-get $apt update \
 && apt-get $apt install -y --no-install-recommends iptables \
 && rm -rf /var/lib/apt/lists/* /tmp/snapshot.sources \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +

COPY --from=docker /out/ /usr/local/bin/

# Runner 0.6.0 creates /run/qory as 0700 root when it writes the agent's docker
# configuration beneath it, which the agent then cannot read; this can go once the
# runner creates the directory itself.
RUN install -d -m 0755 /run/qory \
 && dockerd --version \
 && containerd --version \
 && runc --version \
 && docker --version \
 && iptables --version

LABEL org.opencontainers.image.title="agent-docker" \
      org.opencontainers.image.description="Qory's agent image with a Docker daemon of the agent's own, started by the runner under a nested runtime." \
      org.opencontainers.image.base.name="${BASE}"
