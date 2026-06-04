# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26.4-bookworm AS forge-builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG AWG_FORGE_VERSION=dev
ARG AWG_FORGE_COMMIT=unknown
ARG AMNEZIAWG_GO_REF_OVERRIDE=
ARG AMNEZIAWG_TOOLS_REF_OVERRIDE=
RUN . ./build/amneziawg.refs \
  && if [ -n "$AMNEZIAWG_GO_REF_OVERRIDE" ]; then AMNEZIAWG_GO_REF="$AMNEZIAWG_GO_REF_OVERRIDE"; fi \
  && if [ -n "$AMNEZIAWG_TOOLS_REF_OVERRIDE" ]; then AMNEZIAWG_TOOLS_REF="$AMNEZIAWG_TOOLS_REF_OVERRIDE"; fi \
  && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
  -ldflags="-s -w -X github.com/astronaut808/awg-forge/internal/buildinfo.Version=$AWG_FORGE_VERSION -X github.com/astronaut808/awg-forge/internal/buildinfo.Commit=$AWG_FORGE_COMMIT -X github.com/astronaut808/awg-forge/internal/buildinfo.AmneziaWGGoRef=$AMNEZIAWG_GO_REF -X github.com/astronaut808/awg-forge/internal/buildinfo.AmneziaWGToolsRef=$AMNEZIAWG_TOOLS_REF" \
  -o /out/awg-forge ./cmd/awg-forge

FROM golang:1.26.4-bookworm AS awg-go-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY build/amneziawg.refs /tmp/amneziawg.refs
ARG AMNEZIAWG_GO_REF_OVERRIDE=
RUN . /tmp/amneziawg.refs \
  && if [ -n "$AMNEZIAWG_GO_REF_OVERRIDE" ]; then AMNEZIAWG_GO_REF="$AMNEZIAWG_GO_REF_OVERRIDE"; fi \
  && git init . && git remote add origin https://github.com/amnezia-vpn/amneziawg-go \
  && git fetch --depth=1 origin "$AMNEZIAWG_GO_REF" \
  && git checkout --detach FETCH_HEAD
RUN make && cp amneziawg-go /out-amneziawg-go

FROM debian:bookworm AS tools-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make gcc libc6-dev pkg-config ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY build/amneziawg.refs /tmp/amneziawg.refs
ARG AMNEZIAWG_TOOLS_REF_OVERRIDE=
RUN . /tmp/amneziawg.refs \
  && if [ -n "$AMNEZIAWG_TOOLS_REF_OVERRIDE" ]; then AMNEZIAWG_TOOLS_REF="$AMNEZIAWG_TOOLS_REF_OVERRIDE"; fi \
  && git init . && git remote add origin https://github.com/amnezia-vpn/amneziawg-tools \
  && git fetch --depth=1 origin "$AMNEZIAWG_TOOLS_REF" \
  && git checkout --detach FETCH_HEAD
RUN make -C src WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no
RUN make -C src install WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no DESTDIR=/out PREFIX=/usr

FROM debian:bookworm-slim
ARG AWG_FORGE_VERSION=dev
ARG AWG_FORGE_COMMIT=unknown
RUN apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates dumb-init iproute2 iptables nftables procps openresolv \
  && rm -rf /var/lib/apt/lists/*
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
      org.awg-forge.amneziawg-update-mode="manual"
ENV CONFIG_DIR=/etc/awg-forge \
    AWG_FORGE_VERSION=$AWG_FORGE_VERSION \
    AWG_FORGE_COMMIT=$AWG_FORGE_COMMIT \
    AMNEZIAWG_UPDATE_MODE=manual \
    WEBUI_HOST=127.0.0.1 \
    WEBUI_PORT=51821 \
    LISTEN_PORT=51820 \
    EXTERNAL_INTERFACE=eth0 \
    IPV4_SUBNET=10.8.0.0/24 \
    DNS=1.1.1.1 \
    ALLOWED_IPS=0.0.0.0/0 \
    PERSISTENT_KEEPALIVE=0 \
    MTU=0 \
    PROTOCOL_PROFILE=awg_legacy_1_0 \
    APPLY_CONFIG=true
VOLUME ["/etc/awg-forge"]
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/docker-entrypoint.sh"]
CMD ["serve"]
