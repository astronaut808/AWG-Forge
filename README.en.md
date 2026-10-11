# AWG-Forge

[README на русском](README.md)

Manage AmneziaWG on your own server: Web UI, CLI, and VPN runtime in one Docker image.

![AWG-Forge dashboard](docs/assets/awg-forge-dashboard.jpg)

## Features

- **Multiple tunnels on one VPS:** separate AmneziaWG profiles, ports, and subnets.
- **Routing per tunnel:** IPv4 traffic through the server or Cloudflare WARP.
- **Clients and imports:** access management, expiration, and traffic limits; `.conf`, QR codes, and `vpn://`.
- **Monitoring:** clients' last connection, traffic counters, and history.
- **Panel access:** SSH tunnel or HTTPS — your own certificates, automatic domain/IP certificates, or a reverse proxy.
- **Encrypted backups:** creation and verification in the panel, restoration through the CLI.
- **Diagnostics and maintenance:** Doctor, firewall repair, audit log, and support bundles without secrets.
- **Installation and upgrades:** an interactive script with dependency preparation and checks.
- **Controller and nodes (`develop`):** MFA login and read-only monitoring of connected servers.

AmneziaWG 2.0 is the default. AWG 3.x is experimental;
see the [profile matrix](docs/en/protocol-matrix.md) for compatibility and limitations.
Traffic history and limits require SQLite. The panel binds only to a local address by default.

## Quick Install

On a Linux/VPS server (x86_64):

```bash
curl -fsSL https://raw.githubusercontent.com/astronaut808/awg-forge/master/install.sh -o install.sh
chmod +x install.sh
sudo ./install.sh
```

The installer prepares dependencies, creates the first tunnel, and prints the password
and panel access command. See [requirements and next steps](docs/en/quick-install.md).

## Documentation

- [Setup and manual installation](docs/en/setup.md)
- [Upgrade](docs/en/quick-install.md#upgrade) · [Uninstall](docs/en/quick-install.md#uninstall)
- [Configuration, Web UI, CLI, and diagnostics](docs/en/README.md)
- [Development](docs/en/development.md) · [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md) · [Security policy](SECURITY.md)

## Support the project

- USDT (TRC20): `TBQcgJ9UoGEBXBwPMcf97t3uJiTCRnVmji`
- GRAM (ex TON): `UQCrUmIsUBgIJJJNKpvOO5dxpUH5r7xCz9-AJ2IHUTIckJhS`

## Project independence

AWG-Forge is an independent open-source project, not affiliated with Amnezia.org
or developed or supported by the Amnezia team. The AmneziaWG name describes
compatibility with the protocol and tooling.

## License

[MIT](LICENSE)
