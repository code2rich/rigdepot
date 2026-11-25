# syntax=docker/dockerfile:1
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Rigdepot Authors
#
# The rgd-server image.
#
#   docker build -t rigdepot-server .
#   docker buildx build --platform linux/amd64,linux/arm64 -t rigdepot-server .
#
# The build stage always runs on the machine's own architecture and
# cross-compiles (no cgo), so a multi-architecture build never has to emulate
# another CPU to run the compiler.

ARG GO_VERSION=1.26.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Dependencies first, so that this layer is reused until go.mod changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
# Set by the release build; a plain "docker build" gives a development build.
ARG VERSION=
ARG COMMIT=
ARG DATE=
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
      -ldflags "-s -w \
        -X github.com/code2rich/rigdepot/internal/buildinfo.Version=${VERSION} \
        -X github.com/code2rich/rigdepot/internal/buildinfo.Commit=${COMMIT} \
        -X github.com/code2rich/rigdepot/internal/buildinfo.Date=${DATE}" \
      -o /out/rgd-server ./cmd/rgd-server

# Alpine rather than an empty image: the server calls git (and ssh) to fetch
# resource repositories and needs root certificates and time zone data. The
# binary itself is statically linked.
FROM alpine:3
RUN apk add --no-cache ca-certificates git openssh-client tzdata \
 && adduser -D -u 10001 -h /var/lib/rigdepot rigdepot
COPY --from=build /out/rgd-server /usr/bin/rgd-server
COPY LICENSE NOTICE /usr/share/licenses/rigdepot-server/

ENV RGD_LISTEN=0.0.0.0:8080 \
    RGD_DATA_DIR=/var/lib/rigdepot
# Not root; all state lives in one volume.
USER 10001
VOLUME /var/lib/rigdepot
EXPOSE 8080
# The health check is the server's own subcommand; the image has no curl.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD ["rgd-server", "healthcheck"]
ENTRYPOINT ["rgd-server"]
CMD ["serve"]
