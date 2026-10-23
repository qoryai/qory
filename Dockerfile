# The image of qory's static Linux build, and nothing else: the file /qory/qory, mode 0555
# and owned by root, so any user a container runs as can read and execute it. The mode
# does not stop root in a container from writing it; a read-only mount does, and the wall
# mounts it read-only. It is the build the wall mounts at /qory/qory, and an image that
# is copied from, not run:
#
#   COPY --from=<image>@sha256:<digest> /qory/qory /qory/qory
#
# in a Dockerfile, or, for the file on this machine:
#
#   id=$(docker create --platform linux/arm64 <image>@sha256:<digest>)
#   docker cp "$id:/qory/qory" ./qory
#
# Build from the repository's root, from a checkout whose .git is a directory: go stamps
# the commit into the binary, and qory version prints it. scripts/build-image.sh builds
# it from a commit, for linux/amd64 and linux/arm64:
#
#   docker buildx build --platform linux/amd64,linux/arm64 .
#
# The build stage runs on the platform of the machine that builds, BUILDPLATFORM, and Go
# cross-compiles for the platform asked for, TARGETOS and TARGETARCH, so nothing runs
# under emulation.

# Docker Hub's official golang image of Go 1.27.3, the release go.mod requires, by the
# digest of its image index, which lists an image for each platform. The image sets
# GOTOOLCHAIN=local, so go builds only with the Go 1.27.3 in it.
ARG GO_IMAGE=public.ecr.aws/docker/library/golang:1.27.3@sha256:f24164268fc1e741bbdc0066dbadcf1c8426c5a8489f0729c02c3905a53032fd

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY . .

# Static, as the wall mounts it into an image that may hold no loader, and with the
# release's -trimpath and -s -w, without its -X version flags, so qory version reports
# the commit go stamped. No -buildvcs=false: go reads the commit from .git.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOWORK=off CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags "-s -w" -o /out/qory/qory . \
 && chmod 0555 /out/qory/qory

FROM scratch

# /out/ is copied whole, so /qory/qory keeps its mode.
COPY --from=build /out/ /

# Never run: docker create refuses an image with no command, and the entrypoint gives it
# one, so docker create and then docker cp take the binary out.
ENTRYPOINT ["/qory/qory"]

LABEL org.opencontainers.image.title="qory" \
      org.opencontainers.image.description="qory's static Linux build at /qory/qory, to copy from." \
      org.opencontainers.image.source="https://github.com/qoryai/qory" \
      org.opencontainers.image.licenses="Apache-2.0"
