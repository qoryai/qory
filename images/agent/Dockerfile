# agent: the image the wall runs the agent in, the base every other agent image of
# Qory's is built FROM. It contains no agent runtime, such as Claude Code or Codex: the
# machine installs the runtime at run time, and Forager mounts it read-only at
# /qory/runtime/<runtime>. It contains the tools a run may need to deliver its work:
# git, the GitHub command, Chromium with Noto's fonts for Latin and other scripts, CJK
# and color emoji, ffmpeg and ffprobe, ImageMagick, poppler's pdftoppm (Claude Code
# renders a PDF's pages with it), qpdf, rsvg-convert, Graphviz, Tesseract OCR with
# 28 European languages, pandoc, Python 3 with PyYAML, Requests, Beautiful Soup, lxml,
# cssselect, Pillow, openpyxl and python-docx, jq, yq, curl, ripgrep, SQLite, zip,
# unzip, xz, file, tree, less, bc, envsubst, rsync, patch, and the system's authorities
# where the wall reads them. It runs as any user the machine gives it, with HOME a
# directory every user may write, and contains no setuid or setgid program. It sets no
# entry point: the wall starts the launch command, or qory's helper, itself.
#
# Every version and every sum is a build argument, with its default below. Build it from
# a checkout of qory at the commit qory version prints, for linux/amd64 or linux/arm64:
#
#	docker build -t qory-agent images/agent
#	qory image check qory-agent

# node:24.21.0-trixie-slim, the index for every platform: Node 24 LTS on Debian 13. Docker
# Hub's official images, here and below, are pulled from Amazon's public copy of them,
# the same images under the same digests, which needs no login and is out of reach of
# Docker Hub's pull limit for anonymous users.
ARG NODE_IMAGE=public.ecr.aws/docker/library/node:24.21.0-trixie-slim@sha256:8ec5d7557396cfe32d21c3f9c13072355ceab22b584578ca4bb28af31120cffe
# The Debian the downloads are checked on, on the machine that builds, never under
# emulation.
ARG FETCH_IMAGE=public.ecr.aws/docker/library/debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a
# The Debian snapshot the packages come from: the one the base's Debian was built from,
# named in its /etc/apt/sources.list.d/debian.sources. Change it with the base, and in
# images/agent-docker/Dockerfile, which has a copy of it; a test in internal/image fails
# when the two differ.
ARG DEBIAN_SNAPSHOT=20260918T000000Z
# The GitHub command; the sums are those of gh_<version>_checksums.txt.
ARG GH_VERSION=2.101.0
ARG GH_SHA256_AMD64=9bca2d1c16825f109907a23307628a2f0698fbf99662b73a5cf0b020293072b8
ARG GH_SHA256_ARM64=b57e8063f18862647c9d22727c32e9da1b963f8bf9db648fe123a6975695640f

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

FROM ${NODE_IMAGE}
ARG NODE_IMAGE
ARG DEBIAN_SNAPSHOT

# From the snapshot, through a copy of the base's sources that names it; the image keeps
# the base's apt sources, so what is built FROM it installs from Debian as usual. Retries
# have no delay because, with the default delay, apt-get 3.0.3 grows after a hung
# download until the kernel kills it: at 11 GB with no limit, and in 4 of 4 runs under a
# limit of 3 GiB.
#
# Debian's packages, one a line, in alphabetical order. To add a tool, add a line with
# its package below, and a line that runs the tool to the check after gh is copied. To
# add a Tesseract language, add tesseract-ocr-<code> below and <code> to the check's
# loop. To add a font, add its package below and a line to the check that finds one of
# its fonts in fc-list.
RUN apt="-o Dir::Etc::SourceList=/tmp/snapshot.sources -o Dir::Etc::SourceParts=- -o Acquire::Check-Valid-Until=false -o Acquire::Retries::Delay=false" \
 && sed -e "s|^URIs: http://deb.debian.org/debian-security\$|URIs: http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}|" \
        -e "s|^URIs: http://deb.debian.org/debian\$|URIs: http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}|" \
        /etc/apt/sources.list.d/debian.sources > /tmp/snapshot.sources \
 && ! grep -q deb.debian.org /tmp/snapshot.sources \
 && apt-get $apt update \
 && apt-get $apt install -y --no-install-recommends \
      bc \
      ca-certificates \
      chromium \
      curl \
      ffmpeg \
      file \
      fonts-noto-cjk \
      fonts-noto-color-emoji \
      fonts-noto-core \
      gettext-base \
      git \
      graphviz \
      imagemagick \
      jq \
      less \
      librsvg2-bin \
      pandoc \
      patch \
      poppler-utils \
      python3 \
      python3-bs4 \
      python3-cssselect \
      python3-docx \
      python3-lxml \
      python3-openpyxl \
      python3-pil \
      python3-requests \
      python3-yaml \
      qpdf \
      ripgrep \
      rsync \
      sqlite3 \
      tesseract-ocr \
      tesseract-ocr-bul \
      tesseract-ocr-ces \
      tesseract-ocr-dan \
      tesseract-ocr-deu \
      tesseract-ocr-ell \
      tesseract-ocr-est \
      tesseract-ocr-fin \
      tesseract-ocr-fra \
      tesseract-ocr-gle \
      tesseract-ocr-hrv \
      tesseract-ocr-hun \
      tesseract-ocr-ita \
      tesseract-ocr-lav \
      tesseract-ocr-lit \
      tesseract-ocr-mlt \
      tesseract-ocr-nld \
      tesseract-ocr-nor \
      tesseract-ocr-pol \
      tesseract-ocr-por \
      tesseract-ocr-ron \
      tesseract-ocr-rus \
      tesseract-ocr-slk \
      tesseract-ocr-slv \
      tesseract-ocr-spa \
      tesseract-ocr-srp \
      tesseract-ocr-swe \
      tesseract-ocr-ukr \
      tree \
      unzip \
      xz-utils \
      yq \
      zip \
 && rm -rf /var/lib/apt/lists/* /tmp/snapshot.sources

COPY --from=gh /out/bin/gh /usr/local/bin/gh

# Each tool runs, Chromium from /usr/bin, Tesseract lists each language installed, and
# fontconfig lists a font of each Noto package; a check that fails prints its fix. HOME
# is one directory every user may write, with the sticky bit, because the wall runs the
# agent as the machine's user id, which has no entry in this image.
RUN gh --version \
 && git --version \
 && pdftoppm -v \
 && rg --version \
 && { test "$(command -v chromium)" = /usr/bin/chromium \
      || { echo "Install chromium at /usr/bin/chromium: command -v chromium prints '$(command -v chromium)'" >&2; exit 1; }; } \
 && chromium --version \
 && ffmpeg -version \
 && ffprobe -version \
 && magick -version \
 && python3 -c 'import yaml, requests, bs4, lxml, cssselect, PIL, openpyxl, docx' \
 && jq --version \
 && curl --version \
 && zip -v \
 && unzip -v \
 && xz --version \
 && file --version \
 && pandoc --version \
 && sqlite3 --version \
 && yq --version \
 && tesseract --version \
 && langs="$(tesseract --list-langs 2>&1)" \
 && for lang in eng osd bul ces dan deu ell est fin fra gle hrv \
      hun ita lav lit mlt nld nor pol por ron rus slk slv spa srp swe ukr; do \
      printf '%s\n' "$langs" | grep -qx "$lang" || { echo "Add tesseract-ocr-$lang: tesseract lists no $lang" >&2; exit 1; }; \
    done \
 && qpdf --version \
 && rsvg-convert --version \
 && dot -V \
 && tree --version \
 && less --version \
 && bc --version \
 && envsubst --version \
 && rsync --version \
 && patch --version \
 && { fc-list | grep -q 'Noto Sans Arabic' || { echo "Add fonts-noto-core: fontconfig lists no Noto Sans Arabic" >&2; exit 1; }; } \
 && { fc-list | grep -q 'Noto Sans CJK' || { echo "Add fonts-noto-cjk: fontconfig lists no Noto Sans CJK" >&2; exit 1; }; } \
 && { fc-list | grep -q 'Noto Color Emoji' || { echo "Add fonts-noto-color-emoji: fontconfig lists no Noto Color Emoji" >&2; exit 1; }; } \
 && mkdir -p /home/agent \
 && chmod 1777 /home/agent \
 && find / -xdev -type f \( -perm -4000 -o -perm -2000 \) -exec chmod ug-s {} +

# The image's tools, gh, git and npm, neither check for updates nor ask a person: under
# enforce a check for updates is a denied connection, and nobody answers a prompt.
ENV HOME=/home/agent \
    GH_NO_UPDATE_NOTIFIER=1 \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0 \
    NPM_CONFIG_UPDATE_NOTIFIER=false

ENTRYPOINT []
CMD []

LABEL org.opencontainers.image.title="agent" \
      org.opencontainers.image.description="The image Qory's wall runs the agent in: git, gh, Chromium and the tools a run may need, for any user, with no agent runtime." \
      org.opencontainers.image.source="https://github.com/qoryai/qory" \
      org.opencontainers.image.base.name="${NODE_IMAGE}"
