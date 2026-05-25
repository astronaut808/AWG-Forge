# Web UI and CLI

## Web UI

Main workflow:

1. Open the UI through an SSH tunnel or a protected admin endpoint.
2. Log in.
3. Select profile tab `1.0`, `1.5`, or `2.0`.
4. Create a tunnel if needed.
5. Create a client inside the selected tunnel.
6. After successful creation, the `.conf` file downloads automatically.
7. Import the `.conf` into a compatible AmneziaVPN client.

## UI Actions

Tunnel actions:

- `Create tunnel`: create a new tunnel inside the selected profile.
- `Create client`: create a client inside a specific tunnel.
- `Config`: download an existing client's `.conf`.
- `Import key`: generate an experimental `vpn://` key for AmneziaVPN / DefaultVPN testing.
- `Edit`: rename a client or store admin-only notes without changing VPN config.
- `Settings`: tunnel settings, including optional per-tunnel `Server host` endpoint override.
- `Protocol`: protocol params and regenerate.
- `Health`: handshake and runtime traffic counters for clients.
- `Restart`: restart a tunnel.
- `Delete`: delete a tunnel or client.

Maintenance actions are available through the `Maintenance` button:

- `Overview`: overall runtime, clients, firewall, and recovery status.
- `Doctor`: system and runtime diagnostics grouped by OK/WARN/FAIL.
- `Firewall`: managed firewall rule status per tunnel and repair action.
- `Backup`: download an encrypted backup with a dedicated password.
- `Restore`: verify an `.afbackup` through a dry-run without writing to `CONFIG_DIR`; actual restore remains CLI-only.
- `Updates`: check whether bundled AmneziaWG upstream refs are behind.
- `Support`: download a support bundle without secrets.
- `System`: current mode, server host, tunnels, profiles, and useful commands.

## Stale Configs

Changing tunnel settings or protocol params can make old client configs stale.

After such changes, affected clients show a `stale` badge until a fresh `.conf` is downloaded.

Client rename and notes are metadata-only changes and do not make configs stale.

## CLI In Docker

```bash
docker exec awg-forge awg-forge doctor
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge backup /tmp/awg-forge.afbackup
docker cp awg-forge:/tmp/awg-forge.afbackup ./awg-forge-backup-YYYYMMDD-HHMMSS.afbackup
docker cp ./<backup-file>.afbackup awg-forge:/tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore verify /tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='long-random-backup-password' awg-forge awg-forge restore /tmp/backup.afbackup
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge firewall check
docker exec awg-forge awg-forge support-bundle
docker exec awg-forge awg-forge updates
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config <client-id>
docker exec awg-forge awg-forge client disable <client-id>
docker exec awg-forge awg-forge client enable <client-id>
docker exec awg-forge awg-forge client remove <client-id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
docker exec awg-forge awg-forge tunnel restart
```

## Local CLI

```bash
awg-forge init
awg-forge serve
awg-forge render
awg-forge doctor
BACKUP_PASSWORD='long-random-backup-password' awg-forge backup ./awg-forge.afbackup
BACKUP_PASSWORD='long-random-backup-password' awg-forge restore verify ./awg-forge.afbackup
BACKUP_PASSWORD='long-random-backup-password' awg-forge restore ./awg-forge.afbackup
awg-forge firewall check
awg-forge firewall repair
awg-forge support-bundle
awg-forge updates
```

## Client Config Import

The supported path is `.conf` file import.

The `Import key` action is experimental. It returns a `vpn://` key that contains the same rendered client config encoded for AmneziaVPN-style text import. It has been checked on iOS, but the format is not iOS-specific. Use it only for compatibility testing with AmneziaVPN or DefaultVPN. Routers, the AmneziaWG native app, and production fallback should continue to use `.conf`.

QR import is not shown in the UI and is not supported as a product path.
