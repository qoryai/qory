# prebuild: the image a pool's prebuild runs in, behind the wall of its run. It is
# agent-docker with the reference devcontainer command, @devcontainers/cli at the version
# qory's prebuild runs, and Docker's buildx plugin, which the devcontainer command builds
# with. qory run prebuild-step, qory's helper, runs in it as the wall's user: it builds the
# target's development container in the nested Docker, pushes the images through the
# run's proxy and writes the result for qory run on the machine.
#
# Defined in wall.images with docker: true, under sysbox-runc, and named by
# wall.prebuild_image. No agent image has these programs: an agent with nested Docker
# does not get them.
#
# Build it FROM agent-docker, from a checkout of qory at the commit qory version prints:
#
#	docker build -t qory-prebuild --build-arg BASE=qory-agent-docker images/prebuild

ARG BASE=qory-agent-docker
# The Debian the downloads are checked on, on the machine that builds.
ARG FETCH_IMAGE=public.ecr.aws/docker/library/debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
# The devcontainer command: CLIVersion in internal/prebuild/prebuild.go, which qory
# compares with what the command reports. The sum is npm's dist.integrity, sha512 in
# hex. The package has no dependencies.
ARG DEVCONTAINER_CLI_VERSION=0.89.0
ARG DEVCONTAINER_CLI_SHA512=2f36a8386290fd9aa5e8fb226788552185596a0cd6903eb9686ff5a2ee7f2a5cb96353ed8cb835ca7eeabbe2d90b3568025e83659fe96f3f2a38ee112cd96a32
# Docker's buildx plugin; the sums are those of the release's checksums.txt.
ARG BUILDX_VERSION=0.38.0
ARG BUILDX_SHA256_AMD64=4fe4cc38adf48169132749b6ca22a990928db0118e3407584ee553723115d287
ARG BUILDX_SHA256_ARM64=38e890e1a162bfdbf32fbe91404991dc4fc28e55b0de901b34374a061bab4d2c

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS devcontainer
ARG DEVCONTAINER_CLI_VERSION
ARG DEVCONTAINER_CLI_SHA512
ADD https://registry.npmjs.org/@devcontainers/cli/-/cli-${DEVCONTAINER_CLI_VERSION}.tgz /out/devcontainers-cli.tgz
RUN echo "${DEVCONTAINER_CLI_SHA512}  /out/devcontainers-cli.tgz" | sha512sum -c -

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS buildx
ARG TARGETARCH
ARG BUILDX_VERSION
ARG BUILDX_SHA256_AMD64
ARG BUILDX_SHA256_ARM64
ADD https://github.com/docker/buildx/releases/download/v${BUILDX_VERSION}/buildx-v${BUILDX_VERSION}.linux-${TARGETARCH} /out/docker-buildx
RUN case "${TARGETARCH}" in \
      amd64) sum="${BUILDX_SHA256_AMD64}" ;; \
      arm64) sum="${BUILDX_SHA256_ARM64}" ;; \
      *) echo "no buildx sum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && echo "${sum}  /out/docker-buildx" | sha256sum -c - \
 && chmod 0755 /out/docker-buildx

FROM ${BASE}
ARG BASE
ARG DEVCONTAINER_CLI_VERSION
ARG BUILDX_VERSION

# From the checked archive alone, with no registry and no install script.
RUN --mount=type=bind,from=devcontainer,source=/out,target=/tmp/devcontainer \
    npm install --global --offline --ignore-scripts --no-fund --no-audit --no-update-notifier \
      /tmp/devcontainer/devcontainers-cli.tgz \
 && npm cache clean --force \
 && rm -rf /root/.npm \
 && test "$(devcontainer --version)" = "${DEVCONTAINER_CLI_VERSION}"

# Where the docker command looks for a plugin whatever DOCKER_CONFIG names.
COPY --from=buildx /out/docker-buildx /usr/local/lib/docker/cli-plugins/docker-buildx

RUN docker buildx version | grep -q "v${BUILDX_VERSION} " \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +

LABEL org.opencontainers.image.title="prebuild" \
      org.opencontainers.image.description="The image Qory's prebuild runs in behind the wall: agent-docker, the devcontainer command and buildx." \
      org.opencontainers.image.base.name="${BASE}"
