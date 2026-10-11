# AWG-Forge

[README на русском](README.md)

Manage AmneziaWG on your own server: Web UI, CLI, and VPN runtime in one Docker image.

![AWG-Forge dashboard](docs/assets/awg-forge-dashboard.jpg)

## Why AWG-Forge

- Interactive installation on a VPS.
- Multiple independent tunnels with separate ports, subnets, and profiles.
- Manage clients, access expiration, configurations, and QR codes from the Web UI.
- Route each tunnel's IPv4 traffic through the server or Cloudflare WARP.
- Diagnostics, backups, and recovery from the panel and CLI.
- The panel is local by default, accessed through an SSH tunnel.

AmneziaWG 2.0 is the default. AWG 3.x is experimental;
see the [profile matrix](docs/en/protocol-matrix.md) for compatibility and limitations.

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
