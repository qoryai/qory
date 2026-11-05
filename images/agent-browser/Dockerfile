# agent-browser: the agent image with Chromium, for a run that renders a page in a
# browser. It adds Chromium and the packages Chromium depends on, and nothing else, so
# the agent image itself stays without a browser.
#
# Build it FROM agent, from a checkout of qory at the commit qory version prints:
#
#	docker build -t qory-agent-browser --build-arg BASE=qory-agent images/agent-browser
#	qory image check qory-agent-browser

ARG BASE=qory-agent
# The Debian snapshot the packages come from, agent's.
ARG DEBIAN_SNAPSHOT=20260918T000000Z

FROM ${BASE}
ARG BASE
ARG DEBIAN_SNAPSHOT

# From the snapshot, as in agent. Retries have no delay because, with the default delay,
# apt-get 3.0.3 grows after a hung download until the kernel kills it: at 11 GB with no
# limit, and in 4 of 4 runs under a limit of 3 GiB.
RUN apt="-o Dir::Etc::SourceList=/tmp/snapshot.sources -o Dir::Etc::SourceParts=- -o Acquire::Check-Valid-Until=false -o Acquire::Retries::Delay=false" \
 && sed -e "s|^URIs: http://deb.debian.org/debian-security\$|URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}|" \
        -e "s|^URIs: http://deb.debian.org/debian\$|URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}|" \
        /etc/apt/sources.list.d/debian.sources > /tmp/snapshot.sources \
 && ! grep -q deb.debian.org /tmp/snapshot.sources \
 && apt-get $apt update \
 && apt-get $apt install -y --no-install-recommends chromium \
 && rm -rf /var/lib/apt/lists/* /tmp/snapshot.sources \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} + \
 && chromium --version

LABEL org.opencontainers.image.title="agent-browser" \
      org.opencontainers.image.description="Qory's agent image with Chromium." \
      org.opencontainers.image.base.name="${BASE}"
