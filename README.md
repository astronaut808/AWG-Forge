# awg-forge

awg-forge is a small self-hosted Docker manager for AmneziaWG. It provides a simple Web UI and CLI for creating, disabling, deleting, downloading, and QR-sharing client configs. Plain `.conf` download is the recommended import path today; QR sharing is experimental.

Supported profiles today: AmneziaWG Legacy / 1.0, an AWG 1.5-oriented profile, and AWG 2.0. awg-forge does not implement AmneziaWG itself; it renders configs and runs the existing upstream `awg`, `awg-quick`, and `amneziawg-go` tools.

By default, awg-forge generates non-zero AmneziaWG obfuscation parameters for new tunnels. It avoids all-zero protocol settings because those move behavior toward plain WireGuard.

## Quick Start: Host Networking

```bash
cp .env.example .env
mkdir -p data
docker compose up -d
```

With host networking the UI binds to `127.0.0.1` by default. Open it through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Then open `http://127.0.0.1:51821`.

Host networking is the recommended production mode because tunnels created in the UI can use any free UDP listen port without changing Docker port mappings.

## Alternative: Bridge Networking

Bridge networking can work, but Docker ports must be published before the container starts. Because awg-forge lets you create tunnels in the UI, publish a fixed UDP range and only create tunnels inside that range.

```bash
cp .env.example .env
mkdir -p data
docker compose -f docker-compose.bridge.yml up -d
```

The bridge example publishes:

- Web UI: `127.0.0.1:51821:51821/tcp`
- Tunnel UDP range: `51820-51840:51820-51840/udp`

In bridge mode, keep tunnel ports inside `51820-51840` unless you edit `docker-compose.bridge.yml` and recreate the container. The UI binds to `0.0.0.0` inside the container so Docker can forward the port, but the host binding stays loopback-only for the UI.
Set `PUBLISHED_UDP_PORTS` to the same range so the UI and doctor can warn when a tunnel uses an unpublished port.

## Environment

See `.env.example`.

Important values:

- `SERVER_HOST`: public DNS name or IP clients connect to.
- `EXTERNAL_INTERFACE`: outbound interface on the server, often `eth0` or `ens3`.
  In bridge networking this is usually `eth0` inside the container.
- `PASSWORD`: required when binding the UI publicly; recommended always.
- `SESSION_SECRET`: optional. If omitted, awg-forge creates and persists one automatically in `state.json`.
- `MTU`: tunnel MTU. `0` means auto/omit from generated configs; common explicit values are `1280`, `1380`, `1400`, and `1420`.
- `PROTOCOL_PROFILE`: default profile for the first tunnel, usually `awg_legacy_1_0`.
- `APPLY_CONFIG`: when `true`, awg-forge applies changes with `awg-quick`/`awg`.
- `PUBLISHED_UDP_PORTS`: optional comma-separated Docker published UDP ports/ranges, e.g. `51820-51840,7443`. Empty means host networking or no range check.

## CLI

```bash
awg-forge init
awg-forge serve
awg-forge client add phone
awg-forge client add laptop awg15
awg-forge client disable <id>
awg-forge client enable <id>
awg-forge client remove <id>
awg-forge client config <id>
awg-forge render
awg-forge doctor
awg-forge tunnel restart
awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
```

The Web UI includes a multi-tunnel dashboard, client creation with tunnel selection, tunnel creation, protocol settings, tunnel status, last apply errors, and restart actions.

Design notes:

- [Frontend product plan](docs/frontend-spec.md)
- [Multi-profile and multi-tunnel architecture](docs/multi-profile-architecture.md)

## Creating Clients

Use the Web UI:

1. Open the UI through the SSH tunnel.
2. Log in.
3. Click `Create client`.
4. Select the target tunnel/profile.
5. Download the `.conf` and import it into AmneziaVPN.

QR import is available in the UI for testing, but treat it as experimental on iOS. If AmneziaVPN imports a QR profile but the iOS system VPN indicator does not appear, delete that profile and import the `.conf` file instead.

AWG 2.0 has been validated through `.conf` import on desktop and iOS clients with compatible AmneziaVPN builds. QR import is still experimental; use `.conf` for production client setup.

Or use the CLI:

```bash
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client config <id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
```

## Check IPv4 Egress

After importing the config into an AmneziaWG-compatible client:

```bash
curl -4 https://ifconfig.co
```

## Troubleshooting

Run:

```bash
docker exec awg-forge awg-forge doctor
```

Doctor checks host/container prerequisites, rendered configs, tunnel runtime state, `awg show` listen ports, enabled peers, stale client configs, latest handshakes, and transfer counters.

No internet usually means `EXTERNAL_INTERFACE` is wrong, IPv4 forwarding is disabled, or host firewall/NAT rules conflict.

If `doctor` warns about iptables, make sure `iptables -V` reports `nf_tables`.

If `/dev/net/tun` is missing, load the TUN device on the host and keep the compose `devices` entry.

With `network_mode: host`, do not add `ports:`.

With bridge networking, make sure every tunnel listen port is published in `docker-compose.bridge.yml`. If a tunnel is created on a port outside the published UDP range, clients will not reach it from the internet.

If the UI is unavailable, confirm the SSH tunnel and `WEBUI_HOST=127.0.0.1`, `WEBUI_PORT=51821`.
