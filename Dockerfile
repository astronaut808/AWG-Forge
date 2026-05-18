# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26.3-bookworm AS forge-builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /out/awg-forge ./cmd/awg-forge

FROM golang:1.26.3-bookworm AS awg-go-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/amnezia-vpn/amneziawg-go .
RUN make && cp amneziawg-go /out-amneziawg-go

FROM debian:bookworm AS tools-builder
RUN apt-get update && apt-get install -y --no-install-recommends git make gcc libc6-dev pkg-config ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/amnezia-vpn/amneziawg-tools .
RUN make -C src WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no
RUN make -C src install WITH_WGQUICK=yes WITH_SYSTEMDUNITS=no WITH_BASHCOMPLETION=no DESTDIR=/out PREFIX=/usr

FROM debian:bookworm-slim
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
ENV CONFIG_DIR=/etc/awg-forge \
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
