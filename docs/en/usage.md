# Web UI and CLI

## Web UI

Sidebar navigation switches sections without reloading the document. Opened sections retain their search, selected view and loaded data until logout or a page reload; returning to traffic refreshes its data in the background. Hidden sections pause their subscriptions and polling. Browser Back and Forward remain available.

Main workflow:

1. Open the UI through an SSH tunnel or a protected admin endpoint.
2. Log in with username `admin` and your existing password. Set `WEBUI_USERNAME` in `.env` to change the username.
3. Open `Appearance` in the sidebar (the sun/moon button on narrow screens) to choose English/Russian and a light/dark theme. Use the visible theme and language options; changes apply immediately and are saved in the browser. The same dialog provides documentation and project links.
4. Open `Tunnels`, select a tunnel from the local list, or click `Create tunnel`.
5. When creating a tunnel, select its protocol and settings in the dialog.
6. Create a client inside the selected tunnel.
7. Open `Actions` for the client and click `Connect`.
8. Choose one of the import methods offered for that profile.
9. Import the config into a compatible AmneziaWG or AmneziaVPN client.

The tunnel list supports search and filters for profiles with existing tunnels; the creation dialog offers all available profiles. `Clients` searches across tunnels, `Traffic` opens recorded traffic, and `Overview` provides a real server summary. On narrow screens, the same navigation scrolls horizontally above the content.

The opening `Overview` shows tunnel count, enabled/runtime client status, recorded 30-day traffic and a client preview. Traffic is shown as unavailable or not yet recorded until SQLite has samples; missing runtime counters are displayed as a dash. When creating from `Clients` with all tunnels shown, choose the tunnel explicitly in the form. An active tunnel filter or tunnel page preselects that tunnel, and the selection remains editable. Protocol fields are grouped by purpose. Regeneration preserves port, subnet, DNS and MTU; for AWG 3.x it also rotates the header protection key. Import updated configs after confirming regeneration.

AWG 3.x offers the same four export choices: `.conf`, raw-config AmneziaWG QR, structured AmneziaVPN QR, and `vpn://`. Use AmneziaVPN 5.0.1.5 or newer for AWG 3.1, and keep `.conf` as the fallback when a client-specific import path fails.

On phones, use the compact selector to switch tunnels. A separate control opens search and filters. Connection facts are optional disclosures while warnings stay visible. Connection settings are grouped without hiding expert fields. “Not running” describes runtime, while “Enabled” describes configuration.

Use the `Connect` dialog to choose a QR mode, download a `.conf` file, or copy a `vpn://` link. The import key is fetched only after an explicit copy action. A readable field remains available when clipboard access fails. Failed multipart QR parts can be retried inside the enlarged view. The deletion confirmation shows the client name, tunnel, and IP address. Traffic also distinguishes loading, absent history and failure while preserving the last successful data.

## UI Actions

Tunnel actions:

- `Create tunnel`: choose a protocol in the dialog and create a new tunnel.
- `Create client`: create a client inside a specific tunnel.
- Expand `Actions` to access `Connect`, client `Edit`, enable/disable and deletion. `Connect` opens one dialog for all four import methods. `Edit` manages name, admin-only notes, expiration and optional traffic limits.
- `Connection`: tunnel settings, including optional per-tunnel `Server host`, DNS, MTU, port, subnet, keepalive, allowed IPs and egress. Each tunnel keeps its own MTU. Tunnel deletion is at the bottom of this pane.
- `Protocol parameters`: all parameters supplied by the selected profile. Save edits here or use the form’s `Regenerate`. The header shortcut is shown on the other panes, so only one regeneration entry is visible at a time. Regeneration asks for confirmation and makes affected client configurations stale.
- `Restart`: restart a tunnel.
- `Delete`: delete a tunnel or client. Deleting a tunnel with a client that connected before requires typing the tunnel name.

Maintenance actions are available on the `Maintenance` page in the sidebar:

- `Overview`: active tunnels and enabled clients. It also shows manual mode when runtime configuration apply is off.
- `Doctor`: system and runtime diagnostics grouped by category, with OK/WARN/FAIL status per check. Firewall repair appears here only when Doctor finds a repairable managed-rule issue.
- `WARP`: register, import, restart, or remove the optional WARP egress configuration.
- `Backup and restore`: download an encrypted backup and verify an `.afbackup` in dry-run mode. Actual restore remains CLI-only.
- `Traffic`: aggregate traffic history when SQLite is enabled.
- `Audit log`: inspect recent safe audit events. The panel auto-refreshes while the Audit log tab is open and shows newest events first.
- `Support`: download a support bundle without secrets and view the safe runtime, database, TLS, and version summary.

## Stale Configs

Changing tunnel settings or protocol params can make old client configs stale.

After such changes, affected clients show a `New config needed` badge until a fresh config is exported from `Actions`.

Client rename and notes are metadata-only changes and do not make configs stale.

## Client Runtime Status

A client row shows `Enabled`, `Disabled`, `Expired`, or `Active now`. An enabled client is shown as active when its latest handshake is no more than two minutes old. The last activity and traffic columns show the latest handshake time and runtime counters when available.

If SQLite is enabled, client rows show total recorded traffic. `Maintenance` -> `Traffic` shows aggregate traffic history for today, 7 days, and 30 days across all clients and tunnels. Values are sampled from runtime counters once per minute; the first sample is treated as a baseline.

When SQLite is enabled, client creation and client settings include an optional traffic limit. `Unlimited` means no limit; `Limit` accepts MiB, GiB, or TiB. Choose `Lifetime` for all recorded traffic or `Rolling 30 days (UTC)` for a sliding quota. Existing limits are lifetime limits. When recorded traffic reaches the active limit, awg-forge disables the client and the row shows `limit exceeded`. Attempts to enable the client are rejected while the active period is over its limit. The 30-day window advances as UTC-day aggregates age out. A client disabled by that quota is re-enabled automatically when its rolling usage drops below the limit; a manually disabled client is never auto-enabled.

Doctor also warns about clients whose recorded traffic already exceeds their configured limit.

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

When the expiration passes, the client remains visible in the UI and `state.json`, but becomes `expired` and is no longer rendered into the server config as a peer. This is safer than deletion because name, notes, last seen, and support bundle history are preserved. The client row shows `Expired`.

In `serve` mode, awg-forge periodically enforces expired clients and re-renders affected tunnels. Enforcement normally happens within one minute after the actual expiration time.

## CLI In Docker

For backup, verification, and restore, see [Diagnostics](diagnostics.md#encrypted-backup--restore).

```bash
docker exec awg-forge awg-forge doctor
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge firewall check
docker exec awg-forge awg-forge support-bundle
docker exec awg-forge awg-forge updates
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200 --level error
docker exec awg-forge awg-forge client add phone
docker exec awg-forge awg-forge client add laptop awg15
docker exec awg-forge awg-forge client config "${CLIENT_ID:?Set CLIENT_ID first}"
docker exec awg-forge awg-forge client disable "${CLIENT_ID:?Set CLIENT_ID first}"
docker exec awg-forge awg-forge client enable "${CLIENT_ID:?Set CLIENT_ID first}"
docker exec awg-forge awg-forge client remove "${CLIENT_ID:?Set CLIENT_ID first}"
docker exec awg-forge awg-forge tunnel create awg_1_5 awg15 51825 10.15.0.0/24
```

## Local CLI

```bash
awg-forge init --server-host vpn.example.com --external-interface eth0 --profile awg_2_0 --tunnel-name awg20 --listen-port 51830 --ipv4-subnet 10.20.0.0/24
awg-forge serve
awg-forge render
awg-forge doctor
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

Open client `Actions` → `Connect` to choose between:

- `AmneziaVPN`: AmneziaVPN-compatible QR import;
- `AmneziaWG`: raw full `.conf` QR for AmneziaWG-compatible import;
- `.conf / vpn://`: the most reliable fallback for AmneziaWG, AmneziaVPN, routers, and manual imports, plus `vpn://` key copy for clients that support text import.

For AWG 3.x, the structured AmneziaVPN QR uses protocol version `3.1` and includes the same Header Protection and timing fields as the rendered `.conf`. The `vpn://` value is base64url-encoded raw `.conf`, which AmneziaVPN 5.0.1.5+ passes through its normal config importer. Disabled `RandomTrailers` and `DisableCookies` options are omitted from client exports. These links and QR payloads contain the client private key, PSK, and AWG 3.x Header Protection key.

If an official client cannot import the QR on a specific platform or version, download and import the `.conf` file instead.
