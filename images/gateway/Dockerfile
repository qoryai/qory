# gateway: the image that runs qory gateway, Forager's gateway as a service for the runs
# of other machines. It contains qory's static Linux build at /qory/qory, taken from the
# image built from the Dockerfile at the root, and what qory gateway needs at start beside
# it:
#
# - the system's authorities, /etc/ssl/certs/ca-certificates.crt, which the gateway
#   checks Qory Apiary's certificate and each upstream host's certificate against;
# - HOME, /home/gateway, mode 0700 and owned by the user 65532 it runs as, where qory
#   makes ~/.local/state/qory/gateway, the gateway's certificate authority and the runs'
#   records;
# - /tmp, mode 1777, where Forager makes the tools' sockets.
#
# It has no shell and no other program. qory gateway reads ~/.config/qory/forager.yaml,
# the access key's secret beside it or in QORY_ACCESS_KEY_SECRET, and the TLS certificate
# and key that gateway.tls is set to: mount them into the container, at
# /home/gateway/.config/qory. That folder must be writable, mode 0700 and owned by
# 65532, since qory writes instance-id there; the TLS key and access-key-secret must be
# owned by 65532 with mode 0600. qory stops with an error on a file another user owns.
# Give the container a fixed host name (docker run --hostname): the image has no
# /etc/machine-id, so the instance id follows the host name, and a new name registers a
# new instance.
# It listens on gateway.listen, such as 0.0.0.0:8443. A program listed in
# gateway.integrations is not in the image: build an image FROM this one with the
# program in it.
#
# Build it from the image of qory's build, of the same commit, for linux/amd64 and
# linux/arm64. The one RUN step runs on the platform of the machine that builds, so
# nothing runs under emulation:
#
#	docker buildx build --platform linux/amd64,linux/arm64 \
#	  --build-arg QORY_IMAGE=ghcr.io/qoryai/qory-prerelease@sha256:<digest> images/gateway

# The image built from the Dockerfile at the root, of the same commit: by digest, or
# qory, a local tag of it.
ARG QORY_IMAGE=qory
# Docker Hub's official golang image of Go 1.27.3, under the same digest as the root
# Dockerfile's default, for its authorities only: those of the Debian package
# ca-certificates.
ARG GO_IMAGE=public.ecr.aws/docker/library/golang:1.27.3@sha256:f24164268fc1e741bbdc0066dbadcf1c8426c5a8489f0729c02c3905a53032fd

FROM ${QORY_IMAGE} AS qory

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS files
RUN mkdir -p /out/root/etc/ssl/certs /out/root/tmp /out/home/gateway \
 && cp /etc/ssl/certs/ca-certificates.crt /out/root/etc/ssl/certs/ \
 && printf 'root:x:0:0::/:/sbin/nologin\ngateway:x:65532:65532::/home/gateway:/sbin/nologin\n' > /out/root/etc/passwd \
 && printf 'root:x:0:\ngateway:x:65532:\n' > /out/root/etc/group \
 && chmod 1777 /out/root/tmp \
 && chmod 0700 /out/home/gateway

FROM scratch

COPY --from=files /out/root/ /
COPY --from=files --chown=65532:65532 /out/home/ /home/
COPY --from=qory /qory/qory /qory/qory

USER 65532:65532
ENV HOME=/home/gateway
EXPOSE 8443

ENTRYPOINT ["/qory/qory", "gateway"]

LABEL org.opencontainers.image.title="gateway" \
      org.opencontainers.image.description="qory gateway, Forager's gateway as a service for the runs of other machines." \
      org.opencontainers.image.source="https://github.com/qoryai/qory" \
      org.opencontainers.image.licenses="Apache-2.0"
