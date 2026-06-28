# AWG-Forge

[README на русском](README.md)

Self-hosted AmneziaWG control panel for Docker: Go backend, embedded Web UI, and CLI for tunnels, clients, diagnostics, backup/restore, and safe maintenance.

![awg-forge dashboard](docs/assets/awg-forge-dashboard.jpg)

## Why AWG-Forge

- Ready-to-run Docker-based setup: backend, Web UI, CLI, and runtime tools are shipped together.
- Safe default for the panel: the Web UI listens on `127.0.0.1`, not on the server's public interface.
- Multiple independent tunnels on one VPS: different profiles, UDP ports, and egress scenarios without manually editing Docker port mappings.
- Flexible IPv4 egress: a tunnel can exit directly through the server or through Cloudflare WARP.
- Management and maintenance in one place: daily actions through the Web UI, diagnostics and automation through the CLI.

## Supported

- AmneziaWG profiles: Legacy / 1.0, 1.5-oriented profile, and 2.0.
- Tunnels: separate profiles, UDP ports, subnets, endpoint settings, and IPv4 egress.
- IPv6 egress is not supported yet; generated client configs intentionally use `AllowedIPs = 0.0.0.0/0` without `::/0`.
- Egress: `Server WAN` or Cloudflare WARP per tunnel.
- Clients: create, download `.conf`, `vpn://` import key, enable/disable, expiration, delete.
- Diagnostics: Doctor, firewall repair, client status, last seen, received/sent counters.
- Maintenance Center: WARP, backup, restore verify, support bundle, live audit logs, updates, system info.

## Quick Start

Interactive install on Linux/VPS (Docker required):

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

The installer checks Docker before creating files, creates `/opt/awg-forge`, generates `.env`, password, and `SESSION_SECRET`, creates the first tunnel in `state.json`, starts Docker Compose, and prints the SSH tunnel command. New installs default to AmneziaWG 2.0 for the first tunnel.

By default the Web UI listens on `127.0.0.1:51821`. Open it through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Then open:

```text
http://127.0.0.1:51821
```

## Manual Start

```bash
git clone https://github.com/astronaut808/awg-forge.git
cd awg-forge
cp .env.example .env
mkdir -p data
docker compose up -d
```

Docker host networking is the recommended production mode. It lets tunnels created in the UI use different UDP ports without editing Docker port mappings.

## Important Settings

- `.env` stores container and Web UI runtime settings; tunnels are stored in `data/state.json`.
- `EXTERNAL_INTERFACE` is the server external interface for WAN egress.
- `WEBUI_HOST=127.0.0.1` is the safe default for SSH tunnel access.
- `APPLY_CONFIG=true` applies runtime tunnels and firewall rules.
- `SESSION_COOKIE_SECURE=auto|true|false` controls the Web UI Secure cookie policy.

Change a tunnel endpoint from `Tunnel settings` -> `Server host`. If an upgraded `.env` still contains old tunnel variables such as `SERVER_HOST`, `LISTEN_PORT`, or `IPV4_SUBNET`, you can remove them after verifying tunnel settings in the UI.

WARP can be selected while creating a tunnel or enabled later from `Tunnel settings` -> `Egress` -> `Cloudflare WARP`. If WARP is not configured yet, AWG-Forge registers the shared `warp0` automatically. See [Configuration](docs/en/configuration.md).

## Startup Check

1. Create a client in the UI.
2. Import the downloaded `.conf` into AmneziaVPN.
3. Check IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

Doctor:

```bash
docker exec awg-forge awg-forge doctor
```

## Maintenance

Uninstall an installed instance:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Without cloning the repository:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

Preview the uninstall plan:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh --dry-run --yes
```

Backup/restore, firewall repair, support bundle, logs, and update checks are available from `Maintenance Center` and CLI.

## Documentation

- [Russian README](README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [English documentation](docs/en/README.md)
- [Quick install](docs/en/quick-install.md)
- [Setup](docs/en/setup.md)
- [Configuration](docs/en/configuration.md)
- [Web UI and CLI](docs/en/usage.md)
- [Diagnostics and troubleshooting](docs/en/diagnostics.md)
- [AmneziaWG updates](docs/en/updates.md)
- [Development](docs/en/development.md)
- [Security](docs/en/security.md)
- [Changelog](CHANGELOG.md)

## Development

```bash
make ci
```

Run locally without applying runtime tunnel changes:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
go run ./cmd/awg-forge serve
```

Runtime and Docker image do not require Node/npm. The Web UI is built from `web/` with Vite/Preact/TypeScript and embedded into the Go binary as static files.

## Support the project

If AWG-Forge is useful to you, you can support development with a donation:

- USDT (TRC20): `TBQcgJ9UoGEBXBwPMcf97t3uJiTCRnVmji`
- GRAM (ex TON): `UQCrUmIsUBgIJJJNKpvOO5dxpUH5r7xCz9-AJ2IHUTIckJhS`

## Project independence

AWG-Forge is an independent open-source project for administering AmneziaWG infrastructure.

This project is not affiliated with Amnezia.org, and is not developed or supported by the Amnezia team. The AmneziaWG name is used only to describe compatibility with the corresponding protocol and tooling.

## License

[MIT](LICENSE)
