# ghcr.io/qoryai/agent: the image the wall runs the agent in, the base every other agent
# image of Qory's is built FROM. It holds what qory's harness launches and what an agent
# needs to deliver its work: Claude Code at the version the runner's descriptor for it is
# written against, git, the GitHub command, and the system's authorities where the wall
# reads them. It runs as any user the machine gives it, with HOME a directory every user
# may write, and holds no setuid or setgid program. It sets no entry point: the wall starts
# the launch command, or qory's helper, itself.
#
# Every version is a build argument, with its default here. The release workflow builds
# it for linux/amd64 and linux/arm64 with each qory release; to build it yourself:
#
#	docker build -t qory-agent images/agent
#	qory image check qory-agent

# node:24.21.0-trixie-slim, the index for every platform: Node 24 LTS on Debian 13.
ARG NODE_IMAGE=node:24.21.0-trixie-slim@sha256:8ec5d7557396cfe32d21c3f9c13072355ceab22b584578ca4bb28af31120cffe

# The GitHub command, from its release; the sums are gh_<version>_checksums.txt's.
FROM ${NODE_IMAGE} AS gh
ARG TARGETARCH
ARG GH_VERSION=2.101.0
ARG GH_SHA256_AMD64=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
ARG GH_SHA256_ARM64=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f
ADD https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz /tmp/gh.tar.gz
RUN case "${TARGETARCH}" in \
      amd64) sum="${GH_SHA256_AMD64}" ;; \
      arm64) sum="${GH_SHA256_ARM64}" ;; \
      *) echo "no gh sum for ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && echo "${sum}  /tmp/gh.tar.gz" | sha256sum -c - \
 && mkdir /out \
 && tar -xzf /tmp/gh.tar.gz -C /out --strip-components=1 --no-same-owner

FROM ${NODE_IMAGE}
ARG NODE_IMAGE
# The version contracts/runner/v1/runtimes/claude/descriptor.yaml names in the runner
# module qory's go.mod requires. qory image check compares the two.
ARG CLAUDE_CODE_VERSION=2.1.273

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git \
 && rm -rf /var/lib/apt/lists/*

# Its install script puts the native build for the platform in place of a stub.
RUN npm install --global --no-fund --no-audit --no-update-notifier \
      --allow-scripts=@anthropic-ai/claude-code "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force \
 && rm -rf /root/.npm

COPY --from=gh /out/bin/gh /usr/local/bin/gh

# HOME is one directory every user may write, with the sticky bit, because the wall runs
# the agent as the machine's user id, which has no entry in this image.
RUN mkdir -p /home/agent \
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
