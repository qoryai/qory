# agent: the image the wall runs the agent in, the base every other agent image of
# Qory's is built FROM. It holds what qory's harness launches and what an agent needs to
# deliver its work: Claude Code at the version the runner's descriptor for it is written
# against, git, the GitHub command, and the system's authorities where the wall reads
# them. It runs as any user the machine gives it, with HOME a directory every user may
# write, and holds no setuid or setgid program. It sets no entry point: the wall starts
# the launch command, or qory's helper, itself.
#
# Every version and every sum is a build argument, with its default below. Build it from
# the checkout of the qory release you run, for linux/amd64 or linux/arm64:
#
#	docker build -t qory-agent images/agent
#	qory image check qory-agent

# node:24.21.0-trixie-slim, the index for every platform: Node 24 LTS on Debian 13.
ARG NODE_IMAGE=node:24.21.0-trixie-slim@sha256:8ec5d7557396cfe32d21c3f9c13072355ceab22b584578ca4bb28af31120cffe
# The Debian the downloads are checked on, on the machine that builds, never under
# emulation.
ARG FETCH_IMAGE=debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
# The Debian snapshot the packages come from: the one the base's Debian was built from,
# named in its /etc/apt/sources.list.d/debian.sources. Change it with the base.
ARG DEBIAN_SNAPSHOT=20260918T000000Z
# The GitHub command; the sums are those of gh_<version>_checksums.txt.
ARG GH_VERSION=2.101.0
ARG GH_SHA256_AMD64=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
ARG GH_SHA256_ARM64=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f
# Claude Code: the version contracts/runner/v1/runtimes/claude/descriptor.yaml names in
# the runner module qory's go.mod requires, which qory image check compares. The sums are
# npm's dist.integrity, sha512 in hex, of @anthropic-ai/claude-code and of its native
# build for each platform, @anthropic-ai/claude-code-linux-x64 and -linux-arm64.
ARG CLAUDE_CODE_VERSION=2.1.273
ARG CLAUDE_CODE_SHA512=ca6e36fd63517fa1f8dc541d20f8a0bf3cd35b40de43508faab4dd6270259165468fcc7c35acac084d13535e3c99bde5a67cb9ca8d7ff78fbb7502b951298111
ARG CLAUDE_CODE_SHA512_AMD64=22e9c65cda6c995d481e35bab6d648aa164354ece9f14660241181574572437242703598e1bdc0a2438bd1824ea6d97c1a1055ae092ce837241182d1fd035926
ARG CLAUDE_CODE_SHA512_ARM64=05ca1a2fa11b9d1a0ed60acdbcfdc971ed5bd90c9ca0cb004577341a58b2eb92e414f8a61bef6999247768e2d24c7820624421f855541d503f768e4181d45feb

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS gh
ARG TARGETARCH
ARG GH_VERSION
ARG GH_SHA256_AMD64
ARG GH_SHA256_ARM64
ADD https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz /tmp/gh.tar.gz
RUN case "${TARGETARCH}" in \
      amd64) sum="${GH_SHA256_AMD64}" ;; \
      arm64) sum="${GH_SHA256_ARM64}" ;; \
      *) echo "no gh sum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && echo "${sum}  /tmp/gh.tar.gz" | sha256sum -c - \
 && mkdir /out \
 && tar -xzf /tmp/gh.tar.gz -C /out --strip-components=1 --no-same-owner

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS claude-amd64
ARG CLAUDE_CODE_VERSION
ARG CLAUDE_CODE_SHA512
ARG CLAUDE_CODE_SHA512_AMD64
ADD https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-${CLAUDE_CODE_VERSION}.tgz /out/claude-code.tgz
ADD https://registry.npmjs.org/@anthropic-ai/claude-code-linux-x64/-/claude-code-linux-x64-${CLAUDE_CODE_VERSION}.tgz /out/claude-code-native.tgz
RUN printf '%s  %s\n' "${CLAUDE_CODE_SHA512}" /out/claude-code.tgz "${CLAUDE_CODE_SHA512_AMD64}" /out/claude-code-native.tgz \
    | sha512sum -c -

FROM --platform=$BUILDPLATFORM ${FETCH_IMAGE} AS claude-arm64
ARG CLAUDE_CODE_VERSION
ARG CLAUDE_CODE_SHA512
ARG CLAUDE_CODE_SHA512_ARM64
ADD https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-${CLAUDE_CODE_VERSION}.tgz /out/claude-code.tgz
ADD https://registry.npmjs.org/@anthropic-ai/claude-code-linux-arm64/-/claude-code-linux-arm64-${CLAUDE_CODE_VERSION}.tgz /out/claude-code-native.tgz
RUN printf '%s  %s\n' "${CLAUDE_CODE_SHA512}" /out/claude-code.tgz "${CLAUDE_CODE_SHA512_ARM64}" /out/claude-code-native.tgz \
    | sha512sum -c -

FROM claude-${TARGETARCH} AS claude

FROM ${NODE_IMAGE}
ARG NODE_IMAGE
ARG DEBIAN_SNAPSHOT
ARG CLAUDE_CODE_VERSION

# From the snapshot, through a copy of the base's sources that names it; the image keeps
# its own sources, so what is built FROM it installs from Debian as usual.
RUN apt="-o Dir::Etc::SourceList=/tmp/snapshot.sources -o Dir::Etc::SourceParts=- -o Acquire::Check-Valid-Until=false" \
 && sed -e "s|^URIs: http://deb.debian.org/debian-security\$|URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}|" \
        -e "s|^URIs: http://deb.debian.org/debian\$|URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}|" \
        /etc/apt/sources.list.d/debian.sources > /tmp/snapshot.sources \
 && ! grep -q deb.debian.org /tmp/snapshot.sources \
 && apt-get $apt update \
 && apt-get $apt install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/* /tmp/snapshot.sources

# From the checked archives alone, with no registry: the package's install script, the
# one script allowed, puts the native build in place of a stub. The version test fails a
# build that kept the stub.
RUN --mount=type=bind,from=claude,source=/out,target=/tmp/claude \
    npm install --global --offline --omit=optional --no-fund --no-audit --no-update-notifier \
      --strict-allow-scripts --allow-scripts=file:/tmp/claude/claude-code.tgz \
      /tmp/claude/claude-code.tgz /tmp/claude/claude-code-native.tgz \
 && npm cache clean --force \
 && rm -rf /root/.npm \
 && test "$(claude --version | cut -d' ' -f1)" = "${CLAUDE_CODE_VERSION}"

COPY --from=gh /out/bin/gh /usr/local/bin/gh

# HOME is one directory every user may write, with the sticky bit, because the wall runs
# the agent as the machine's user id, which has no entry in this image.
RUN gh --version \
 && git --version \
 && mkdir -p /home/agent \
 && chmod 1777 /home/agent \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +

# The agent's programs neither update themselves nor ask a person: under enforce a check
# for updates or a report is a denied connection, and nobody answers a prompt.
ENV HOME=/home/agent \
    DISABLE_AUTOUPDATER=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    GH_NO_UPDATE_NOTIFIER=1 \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

ENTRYPOINT []
CMD []

LABEL org.opencontainers.image.title="agent" \
      org.opencontainers.image.description="The image Qory's wall runs the agent in: Claude Code, git and gh, for any user." \
      org.opencontainers.image.source="https://github.com/qoryai/qory" \
      org.opencontainers.image.base.name="${NODE_IMAGE}"
