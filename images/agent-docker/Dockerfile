# ghcr.io/qoryai/agent-docker: the agent image with a Docker of the agent's own. It is
# ghcr.io/qoryai/agent with Docker's daemon, its containerd and runc, and the docker
# command, from Docker's static release, and iptables for the daemon's networks.
#
# The image starts nothing. Under a runtime that runs a daemon without privileges, such as
# sysbox-runc, the runner's helper starts dockerd as the enclosure's root, on
# /var/run/docker.sock alone, then runs the agent as its user: so there is no entry point
# here, no daemon.json, nothing that listens on a port, and no volume for
# /var/lib/docker, which the wall gives a volume of the run's. qory image check reports
# the image as able to carry a Docker when dockerd is in a system directory.
#
# The release workflow builds it FROM the agent image it just pushed, by digest; to build
# it yourself, after images/agent:
#
#	docker build -t qory-agent-docker --build-arg BASE=qory-agent images/agent-docker

ARG BASE=qory-agent

# Docker's static binaries. download.docker.com publishes no checksums for them, so the
# sums are of the archives as downloaded on 2026-09-29, one per architecture.
FROM ${BASE} AS fetch-amd64
ARG DOCKER_VERSION=29.8.1
ARG DOCKER_SHA256_AMD64=d8db66739d2e28d4933786d73e918d9be643a67fbd835db1bf740d650a259e70
ADD https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}.tgz /tmp/docker.tgz
RUN echo "${DOCKER_SHA256_AMD64}  /tmp/docker.tgz" | sha256sum -c -

FROM ${BASE} AS fetch-arm64
ARG DOCKER_VERSION=29.8.1
ARG DOCKER_SHA256_ARM64=667395fbffab52901b80181dfbb39ea76da2fbd7642c4fbddd24e42146b07b48
ADD https://download.docker.com/linux/static/stable/aarch64/docker-${DOCKER_VERSION}.tgz /tmp/docker.tgz
RUN echo "${DOCKER_SHA256_ARM64}  /tmp/docker.tgz" | sha256sum -c -

FROM fetch-${TARGETARCH} AS fetch
RUN mkdir /out \
 && tar -xzf /tmp/docker.tgz -C /out --strip-components=1 --no-same-owner \
      docker/dockerd docker/containerd docker/containerd-shim-runc-v2 docker/runc \
      docker/docker-init docker/docker-proxy docker/docker

FROM ${BASE}
ARG BASE

RUN apt-get update \
 && apt-get install -y --no-install-recommends iptables \
 && rm -rf /var/lib/apt/lists/* \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +

# Runner 0.6.0 creates /run/qory as 0700 root when it writes the agent's docker
# configuration beneath it, which the agent then cannot read; this can go once the
# runner creates the directory itself.
RUN install -d -m 0755 /run/qory

COPY --from=fetch /out/ /usr/local/bin/

LABEL org.opencontainers.image.title="agent-docker" \
      org.opencontainers.image.description="Qory's agent image with a Docker daemon of the agent's own, started by the runner under a nested runtime." \
      org.opencontainers.image.base.name="${BASE}"
