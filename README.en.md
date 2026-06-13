# awg-forge

[README на русском](README.md)

awg-forge is a self-hosted AmneziaWG manager for Docker. It provides a small Go backend, a static Web UI, and a CLI for running AmneziaWG tunnels and managing client `.conf` files.

awg-forge does not implement a custom VPN protocol. It renders AmneziaWG configs and manages the existing upstream `awg`, `awg-quick`, and `amneziawg-go` tools bundled in the Docker image.

## Status

Supported profiles:

- AmneziaWG Legacy / 1.0;
- AmneziaWG 1.5-oriented profile;
- AmneziaWG 2.0.

Supported client import path:

- `.conf` download.

Experimental import path:

- `Import key` in the Web UI generates a `vpn://` text key for AmneziaVPN / DefaultVPN compatibility testing.

QR import is not exposed. It was removed because `.conf` import is the most reliable path across current AmneziaVPN clients.

## Features

- Web UI with `1.0`, `1.5`, and `2.0` profile tabs.
- Multiple tunnels per profile.
- Client creation, disable, enable, delete, and config download.
- Automatic `.conf` download after successful client creation.
- Experimental `Import key` for AmneziaVPN / DefaultVPN.
- Tunnel settings: port, subnet, DNS, allowed IPs, keepalive, MTU, and enabled state.
- Protocol parameter generation and validation for Legacy / 1.0, 1.5, and 2.0.
- Safe non-zero obfuscation defaults for new tunnels.
- IPv4 egress with NAT/firewall reconciliation.
- Client health view with handshake and rx/tx counters.
- Maintenance Center for Doctor, firewall, backup, restore verify, support bundle, audit logs, updates, and system info.
- Doctor diagnostics for tools, runtime, firewall, ports, peers, handshakes, and stale configs.
- Manual managed firewall rule check and repair.
- Secret-free support bundle for safely sharing diagnostics.
- Encrypted backup/restore with a dedicated backup password and restore dry-run verification.
- State/config rollback when runtime config apply fails.
- Upstream AmneziaWG update checks without automatic system changes.
- Static HTML/CSS/JavaScript frontend with no Node/npm build pipeline.

## Quick Start

Interactive install on Linux/VPS:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

The script creates `/opt/awg-forge`, generates `.env`, password, `SESSION_SECRET`, detects the external interface, starts Docker Compose, and prints the SSH tunnel command.

Manual start:

```bash
git clone https://github.com/astronaut808/awg-forge.git
cd awg-forge
cp .env.example .env
mkdir -p data
docker compose up -d
```

By default the Web UI listens on `127.0.0.1:51821`. Open it through an SSH tunnel:

```bash
ssh -L 51821:127.0.0.1:51821 user@server
```

Then open:

```text
http://127.0.0.1:51821
```

Host networking is the recommended production mode because tunnels created in the UI can use any free UDP ports without changing Docker port mappings.

`SERVER_HOST` defines the default endpoint host for client configs. Individual tunnels can override it in the Web UI through `Tunnel settings` → `Server host`. See [Configuration](docs/en/configuration.md).

Uninstall:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh
```

Uninstall without cloning the repository:

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/uninstall.sh | sudo bash
```

Preview the uninstall plan without changing the host:

```bash
cd /opt/awg-forge
sudo ./uninstall.sh --dry-run --yes
```

Unknown AWG interfaces missing from `state.json` are intentionally preserved. Remove them only after review:

```bash
sudo ./uninstall.sh --remove-orphans
```

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
- [Frontend product plan](docs/frontend-spec.md)
- [Multi-profile / multi-tunnel architecture](docs/multi-profile-architecture.md)
- [Protocol matrix](docs/protocol-matrix.md)
- [AWG 2.0 design](docs/awg-2.0-design.md)
- [AmneziaVPN import and subscription research](docs/research/amnezia-import-subscriptions.md)
- [Changelog](CHANGELOG.md)

## Minimal Check After Startup

Create a client in the UI, import the downloaded `.conf` into AmneziaVPN, and check IPv4 egress:

```bash
curl -4 https://ifconfig.co
```

The response should show the server egress IP.

For AmneziaVPN / DefaultVPN, you can also try the `Import key` button. It shows an experimental `vpn://` key containing the same client config. It is not a subscription link and does not replace `.conf` for production fallback, routers, or the native AmneziaWG app.

## Development

```bash
make ci
```

`make ci` uses Deno only for linting the static JavaScript files. The runtime and Docker image do not require Deno, Node, or npm.

Run locally without applying runtime tunnel changes:

```bash
CONFIG_DIR=/private/tmp/awg-forge-dev \
WEBUI_HOST=127.0.0.1 \
WEBUI_PORT=51821 \
PASSWORD=test \
APPLY_CONFIG=false \
SERVER_HOST=127.0.0.1 \
go run ./cmd/awg-forge serve
```

More details: [Development](docs/en/development.md).

## License

[MIT](LICENSE)
