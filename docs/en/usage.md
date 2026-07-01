# Web UI and CLI

## Web UI

Main workflow:

1. Open the UI through an SSH tunnel or a protected admin endpoint.
2. Log in.
3. Select profile tab `1.0`, `1.5`, or `2.0`.
4. Create a tunnel if needed.
5. Create a client inside the selected tunnel.
6. Open `Config` for the client.
7. Choose AmneziaVPN QR, AmneziaWG `.conf` QR, `.conf` download, or copy the `vpn://` key.
8. Import the config into a compatible AmneziaWG or AmneziaVPN client.

## UI Actions

Tunnel actions:

- `Create tunnel`: create a new tunnel inside the selected profile.
- `Create client`: create a client inside a specific tunnel.
- `Config`: choose how to import a client config: AmneziaVPN QR, AmneziaWG `.conf` QR, `.conf` download, or `vpn://` key copy.
- `Edit`: rename a client or store admin-only notes without changing VPN config.
- `Settings`: tunnel settings, including optional per-tunnel `Server host` endpoint override.
- `Protocol`: protocol params and regenerate.
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
- `last seen`, `received`, `sent`: latest handshake time and runtime counters from the server side.

AmneziaWG/WireGuard does not keep a permanent TCP-like connection, so `active now` is only an approximate online indicator, not a strict online/offline status. In the dashboard, active means the latest handshake is younger than about 3 minutes. The UI also shows `received` / `sent` counters when runtime exposes them.

When runtime reports a handshake, awg-forge persists that the client has connected before and stores the latest handshake time in `state.json`. After an interface restart, the client may show `last seen` until a fresh runtime handshake appears.

Doctor may warn about clients with no handshake yet. This is useful for spotting unused or wrongly imported configs, but it does not mean the whole tunnel is broken when other clients on the same tunnel work.

For deeper diagnostics, use `Maintenance` -> `Doctor`.

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

The most reliable path is `.conf` file import. The UI also provides separate QR options for different official clients. Every option contains client secrets, so show QR codes only on a trusted screen and never share them publicly.

The `AmneziaVPN` option shows a QR code built for AmneziaVPN import. The payload is a JSON wrapper with `last_config`, compressed with zlib, wrapped in the Qt/qCompress-style binary header used by AmneziaVPN, and encoded as base64url before QR generation. If a specific AmneziaVPN build does not scan it, use the `.conf` file fallback.

The `AmneziaWG` option shows a raw full `.conf` QR. It is intended for AmneziaWG-compatible clients that scan config QR codes. AmneziaVPN may ignore raw `.conf` QR codes on some platforms.

Use the client `Config` action to choose between:

- `AmneziaVPN`: AmneziaVPN-compatible QR import;
- `AmneziaWG`: raw full `.conf` QR for AmneziaWG-compatible import;
- `.conf / vpn://`: the most reliable fallback for AmneziaWG, AmneziaVPN, routers, and manual imports, plus `vpn://` key copy for clients that support text import.

If an official client cannot import the QR on a specific platform or version, download and import the `.conf` file instead.
