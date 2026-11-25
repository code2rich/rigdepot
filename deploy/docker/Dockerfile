# syntax=docker/dockerfile:1
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# The release image of rgd-server, built by goreleaser (dockers_v2 in
# .goreleaser.yaml) from the Debian package it has just made for each
# architecture: the image holds the same rgd-server and the same six rgd
# archives as the package, byte for byte, and nothing is compiled here.
# goreleaser puts the packages of each platform into <os>/<arch>/ of the build
# context.
#
# To build an image from source instead, use the Dockerfile at the root of the
# repository.

ARG RUNTIME_IMAGE=alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# Unpack the package on the building machine's own architecture.
FROM --platform=$BUILDPLATFORM ${RUNTIME_IMAGE} AS unpack
RUN apk add --no-cache dpkg
ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/ /packages/
RUN set -eu; set -- /packages/*.deb; [ "$#" -eq 1 ] && [ -f "$1" ]; dpkg-deb --extract "$1" /pkg

# Alpine rather than an empty image: the server calls git (and ssh) to fetch
# resource repositories and needs root certificates and time zone data. The
# programs themselves are statically linked.
FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates git openssh-client tzdata \
 && adduser -D -u 10001 -h /var/lib/rigdepot rigdepot \
 && chmod 0700 /var/lib/rigdepot
COPY --from=unpack /pkg/usr/bin/rgd-server /usr/bin/rgd-server
COPY --from=unpack /pkg/usr/share/rigdepot/cli/ /usr/share/rigdepot/cli/
COPY --from=unpack /pkg/usr/share/doc/rigdepot-server/ /usr/share/licenses/rigdepot-server/

# Not root. Everything the server writes is in the data volume; the rest of
# the file system can be mounted read-only (docker run --read-only, with a
# tmpfs on /tmp for git).
ENV RGD_LISTEN=0.0.0.0:8080 \
    RGD_DATA_DIR=/var/lib/rigdepot \
    HOME=/var/lib/rigdepot
USER 10001
VOLUME /var/lib/rigdepot
EXPOSE 8080
# The health check is the server's own subcommand; the image has no curl.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["rgd-server", "healthcheck"]
ENTRYPOINT ["rgd-server"]
CMD ["serve"]
