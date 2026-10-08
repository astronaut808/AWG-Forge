# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26.7-bookworm AS forge-builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG AWG_FORGE_VERSION=dev
ARG AWG_FORGE_COMMIT=unknown
RUN . ./build/amneziawg.refs \
  && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
  -ldflags="-s -w -X github.com/astronaut808/awg-forge/internal/buildinfo.Version=$AWG_FORGE_VERSION -X github.com/astronaut808/awg-forge/internal/buildinfo.Commit=$AWG_FORGE_COMMIT -X github.com/astronaut808/awg-forge/internal/buildinfo.AmneziaWGGoRef=$AMNEZIAWG_GO_REF -X github.com/astronaut808/awg-forge/internal/buildinfo.AmneziaWGToolsRef=$AMNEZIAWG_TOOLS_REF -X github.com/astronaut808/awg-forge/internal/buildinfo.AWG3Runtime=true" \
  -o /out/awg-forge ./cmd/awg-forge

FROM golang:1.26.7-bookworm AS awg-go-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY build/amneziawg.refs /tmp/amneziawg.refs
RUN . /tmp/amneziawg.refs \
  && git init . && git remote add origin https://github.com/amnezia-vpn/amneziawg-go \
  && git fetch --depth=1 origin "$AMNEZIAWG_GO_REF" \
  && git checkout --detach FETCH_HEAD
RUN make && cp amneziawg-go /out-amneziawg-go

FROM debian:bookworm AS tools-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make gcc libc6-dev pkg-config ca-certificates patch && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY build/amneziawg.refs /tmp/amneziawg.refs
RUN . /tmp/amneziawg.refs \
  && git init . && git remote add origin https://github.com/amnezia-vpn/amneziawg-tools \
  && git fetch --depth=1 origin "$AMNEZIAWG_TOOLS_REF" \
  && git checkout --detach FETCH_HEAD
COPY build/patches/awg-quick-force-userspace.patch /tmp/awg-quick-force-userspace.patch
RUN patch -p1 < /tmp/awg-quick-force-userspace.patch \
  && make -C src WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no \
  && make -C src install WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no DESTDIR=/out PREFIX=/usr

FROM debian:bookworm-slim
ARG AWG_FORGE_VERSION=dev
ARG AWG_FORGE_COMMIT=unknown
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates dumb-init iproute2 iptables nftables procps openresolv perl-base \
  && rm -rf /var/lib/apt/lists/*

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

COPY --from=forge-builder /out/awg-forge /usr/local/bin/awg-forge
COPY --from=awg-go-builder /out-amneziawg-go /usr/local/bin/amneziawg-go
COPY --from=tools-builder /out/usr/bin/awg /usr/local/bin/awg
COPY --from=tools-builder /out/usr/bin/awg-quick /usr/local/bin/awg-quick
COPY scripts/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh \
  && iptables -V | grep -q nf_tables
LABEL org.opencontainers.image.title="awg-forge" \
      org.opencontainers.image.version=$AWG_FORGE_VERSION \
      org.opencontainers.image.revision=$AWG_FORGE_COMMIT \
      org.awg-forge.amneziawg-update-mode="manual" \
      org.awg-forge.awg3-runtime="true"
ENV CONFIG_DIR=/etc/awg-forge \
    AWG_FORGE_VERSION=$AWG_FORGE_VERSION \
    AWG_FORGE_COMMIT=$AWG_FORGE_COMMIT \
    AMNEZIAWG_UPDATE_MODE=manual \
    WEBUI_HOST=127.0.0.1 \
    WEBUI_PORT=51821 \
    EXTERNAL_INTERFACE=eth0 \
    APPLY_CONFIG=true
VOLUME ["/etc/awg-forge"]
# The service intentionally runs as root to manage host-network TUN interfaces,
# routes, and firewall rules with the capabilities granted by Compose.
# nosemgrep: dockerfile.security.missing-user-entrypoint.missing-user-entrypoint
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/docker-entrypoint.sh"]
# nosemgrep: dockerfile.security.missing-user.missing-user
CMD ["serve"]
