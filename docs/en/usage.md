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
- `Logs`: inspect recent safe audit events. The panel auto-refreshes while Maintenance is open and shows newest events first.
- `System`: current mode, server host, tunnels, profiles, and useful commands.

## Stale Configs

Changing tunnel settings or protocol params can make old client configs stale.

After such changes, affected clients show a `stale` badge until a fresh `.conf` is downloaded.

Client rename and notes are metadata-only changes and do not make configs stale.

## Client Runtime Status

The client list shows two different kinds of status:

- `enabled` / `disabled`: whether the client is allowed in awg-forge config.
- `active now`, `seen recently`, `offline`, `never seen`: approximate runtime status from `awg show` and persisted `last_seen_at`.

AmneziaWG/WireGuard does not keep a permanent TCP-like connection, so `active now` is only an approximate online indicator, not a strict online/offline status. In the dashboard, active means the latest handshake is younger than about 3 minutes. The UI also shows `received` / `sent` counters when runtime exposes them.

When runtime reports a handshake, awg-forge persists that the client has connected before and stores the latest handshake time in `state.json`. After an interface restart, the client may show `last seen` until a fresh runtime handshake appears.

Doctor may warn about clients with no handshake yet. This is useful for spotting unused or wrongly imported configs, but it does not mean the whole tunnel is broken when other clients on the same tunnel work.

## Client Expiration

When creating or editing a client, you can choose an expiration:

- `Never expires`;
- `1 hour`;
- `1 day`;
- `7 days`;
- `30 days`;
- custom date and time.

When the expiration passes, the client remains visible in the UI and `state.json`, but becomes `expired` and is no longer rendered into the server config as a peer. This is safer than deletion because name, notes, last seen, and support bundle history are preserved. The UI shows this as `expired` / `not rendered since <date>`.

In `serve` mode, awg-forge periodically enforces expired clients and re-renders affected tunnels. Enforcement normally happens within one minute after the actual expiration time.

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
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200 --level error
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config <client-id>
docker exec awg-forge awg-forge client disable <client-id>
docker exec awg-forge awg-forge client enable <client-id>
docker exec awg-forge awg-forge client remove <client-id>
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
```

## Local CLI

```bash
awg-forge init
awg-forge init --server-host vpn.example.com --external-interface eth0 --profile awg_2_0 --tunnel-name awg20 --listen-port 51830 --ipv4-subnet 10.20.0.0/24
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
awg-forge logs
```

## Client Config Import

The supported path is `.conf` file import.

The `Import key` action is experimental. It returns a `vpn://` key that contains the same rendered client config encoded for AmneziaVPN-style text import. It has been checked on iOS, but the format is not iOS-specific. Use it only for compatibility testing with AmneziaVPN or DefaultVPN. Routers, the AmneziaWG native app, and production fallback should continue to use `.conf`.

QR import is not shown in the UI and is not supported as a product path yet. Future QR support should be implemented as explicit, tested compatibility:

- native AmneziaWG app: QR from the full `.conf`, for example `qrencode -t ansiutf8 < tunnel.conf`;
- AmneziaVPN: separate import-format validation, because its behavior may differ from the native AmneziaWG app.
