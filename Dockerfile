# syntax=docker/dockerfile:1
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# The rgd-server image, built from this source tree:
#
#   docker build -t rigdepot-server .
#   docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
#
# Three stages: the web console (Node), then the server with the console in it
# and the command line for all six platforms (Go), then the image itself
# (Alpine). The first two always run on the building machine's own
# architecture and cross-compile, so a multi-architecture build never has to
# emulate another CPU to run a compiler.
#
# A release does not build its image here: it puts the files it has already
# built and checked into deploy/docker/Dockerfile, so that the image holds the
# same bytes as the archives and packages.
#
# The base images are pinned by digest; update tag and digest together.

ARG NODE_IMAGE=node:22.23.4-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202
ARG GO_IMAGE=golang:1.26.0-alpine@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5
ARG RUNTIME_IMAGE=alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# ---------------------------------------------------------------- web console
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
WORKDIR /src/web
# pnpm in the version web/package.json names, through corepack.
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 npm_config_update_notifier=false
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
# The console reads its messages from the repository's locales/.
COPY locales/ /src/locales/
COPY web/ ./
RUN pnpm build

# ------------------------------------------------------------------ programs
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src

# Dependencies first, so that this layer is reused until go.mod changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=web /src/web/dist/ web/dist/

ARG TARGETOS
ARG TARGETARCH
# Set by whoever builds a versioned image; a plain "docker build" gives a
# development build.
ARG VERSION=
ARG COMMIT=
ARG DATE=
ENV CGO_ENABLED=0 BUILDINFO=github.com/code2rich/rigdepot/internal/buildinfo
RUN ldflags="-s -w -X $BUILDINFO.Version=${VERSION} -X $BUILDINFO.Commit=${COMMIT} -X $BUILDINFO.Date=${DATE}" \
 && GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false -tags webdist -ldflags "$ldflags" \
      -o /out/rgd-server ./cmd/rgd-server

# The command line for every platform, packed the way a release packs it, for
# the server to hand out on the internal network (GET /install.sh).
RUN set -eu; \
    ldflags="-s -w -X $BUILDINFO.Version=${VERSION} -X $BUILDINFO.Commit=${COMMIT} -X $BUILDINFO.Date=${DATE}"; \
    mkdir -p /out/cli; \
    for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
      os=${target%/*}; arch=${target#*/}; bin=rgd; \
      if [ "$os" = windows ]; then bin=rgd.exe; fi; \
      dir=$(mktemp -d); \
      GOOS=$os GOARCH=$arch go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$dir/$bin" ./cmd/rgd; \
      cp LICENSE NOTICE "$dir/"; \
      tar -C "$dir" -czf "/out/cli/rgd_${VERSION:-0.0.0-dev}_${os}_${arch}.tar.gz" "$bin" LICENSE NOTICE; \
      rm -rf "$dir"; \
    done; \
    cd /out/cli && sha256sum rgd_* > checksums.txt

# --------------------------------------------------------------------- image
# Alpine rather than an empty image: the server calls git (and ssh) to fetch
# resource repositories and needs root certificates and time zone data. The
# programs themselves are statically linked.
FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates git openssh-client tzdata \
 && adduser -D -u 10001 -h /var/lib/rigdepot rigdepot \
 && chmod 0700 /var/lib/rigdepot
COPY --from=build /out/rgd-server /usr/bin/rgd-server
COPY --from=build /out/cli/ /usr/share/rigdepot/cli/
COPY LICENSE NOTICE /usr/share/licenses/rigdepot-server/

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
