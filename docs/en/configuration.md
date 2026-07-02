# Configuration

The main example is [.env.example](../../.env.example).

## Common Variables

- `WEBUI_HOST`: Web UI bind address. Defaults to `127.0.0.1`.
- `WEBUI_PORT`: Web UI port. Defaults to `51821`.
- `PASSWORD`: Web UI password. Required for public binds and recommended always.
- `SESSION_COOKIE_SECURE`: Secure cookie policy for UI sessions. Values: `auto`, `true`, `false`. Defaults to `auto`.
- `EXTERNAL_INTERFACE`: server egress interface, often `eth0` or `ens3`. In bridge networking this is usually `eth0` inside the container.
- `APPLY_CONFIG`: when `true`, awg-forge applies runtime tunnel changes with AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: published Docker UDP ports/ranges, for example `51820-51840,7443`.
- `AUDIT_LOG_ENABLED`: enables the safe audit log. Defaults to `true`.
- `AUDIT_LOG_PATH`: audit log path. Defaults to `/etc/awg-forge/audit.log`.
- `AUDIT_LOG_MAX_SIZE`: file size before rotation. Defaults to `5242880`.
- `AUDIT_LOG_MAX_FILES`: number of rotated files to keep. Defaults to `3`.

## First Tunnel Initialization

New installs keep runtime settings in `.env` and tunnel settings in `state.json`.

During a fresh install, `install.sh` runs a one-shot `awg-forge init` container before starting the service. That command creates `data/state.json` with the selected first tunnel. After that, `docker compose up -d` starts from ready state, and tunnel settings are managed from the Web UI/API and persisted in `state.json`.

The installer asks for the protocol profile before tunnel defaults, so profile-specific defaults stay aligned. Pressing Enter on the profile question selects AWG 2.0:

| Profile | Tunnel name | Port | Subnet |
| --- | --- | --- | --- |
| `awg_legacy_1_0` | `awg0` | `51820` | `10.8.0.0/24` |
| `awg_1_5` | `awg15` | `51825` | `10.15.0.0/24` |
| `awg_2_0` | `awg20` | `51830` | `10.20.0.0/24` |

When creating more tunnels in the Web UI, awg-forge suggests free names, ports, and subnets across all profiles. Backend validation still rejects manual conflicts.

If you upgrade from an older awg-forge version and `.env` still contains `SERVER_HOST`, `LISTEN_PORT`, `IPV4_SUBNET`, `DNS`, `ALLOWED_IPS`, `PERSISTENT_KEEPALIVE`, `MTU`, or `PROTOCOL_PROFILE`, those values are ignored after `state.json` exists. Verify tunnel settings in the UI, then remove old tunnel variables from `.env` to avoid confusion.

## SESSION_SECRET

`SESSION_SECRET` is optional. If omitted, awg-forge creates and persists one in `state.json`.

It is used to sign UI session cookies. In the normal setup, users do not need to manage it manually.

## SESSION_COOKIE_SECURE

`SESSION_COOKIE_SECURE` controls the `Secure` flag on UI session cookies:

- `auto`: default. For `127.0.0.1`, `localhost`, and `::1`, cookies work over HTTP without `Secure`; for external hosts, cookies use `Secure`.
- `true`: always set `Secure`. Use with HTTPS/reverse proxies.
- `false`: never set `Secure`. This allows login through `http://domain:port`, but is unsafe on the public internet.

If you need plain HTTP Web UI access, use it only on a trusted network or behind separate protection. For production, prefer `WEBUI_HOST=127.0.0.1` with SSH tunneling or HTTPS.

## EXTERNAL_INTERFACE

To find the server egress interface:

```bash
ip route get 1.1.1.1
```

Example:

```text
1.1.1.1 via 203.0.113.1 dev ens3 src 203.0.113.10
```

Then use:

```env
EXTERNAL_INTERFACE=ens3
```

If the interface is wrong, handshakes may work while internet through the VPN does not.

## Tunnel Endpoint

Each tunnel has a `Server host` field in the Web UI. It defines the host awg-forge uses in `Endpoint = <host>:<port>` for client `.conf` files.

On new installs this value is written to `state.json` during the first `awg-forge init`. Changing `SERVER_HOST` in `.env` after state exists does not rewrite existing tunnels.

This is useful when different tunnels are published through different subdomains, for example:

```text
legacy.example.com:44865
awg20.example.com:44867
```

Important:

- `Server host` must not include a scheme, path, or port;
- the port comes from the tunnel settings;
- after changing the host, clients should re-import a fresh config from `Config`;
- already imported clients do not update themselves.

## MTU

`MTU=0` in a tunnel means awg-forge does not add `MTU = ...` to server/client configs.

If you explicitly set tunnel MTU, it is rendered exactly the same into server and client configs. awg-forge does not use hidden MTU decisions.

Practically:

- `Auto` is a good starting point;
- `1280` often helps on problematic networks, mobile networks, routers, and complex routes;
- the Web UI offers `Auto`, common presets, and `Custom` for explicit MTU values;
- after changing MTU, clients should re-import a fresh config from `Config`.

## IPv6 and AllowedIPs

The current awg-forge release manages IPv4 egress. Generated client configs intentionally use:

```ini
AllowedIPs = 0.0.0.0/0
```

`::/0` is not added automatically because the server side does not yet create IPv6 subnets, client IPv6 addresses, IPv6 forwarding, or NAT66/ip6tables/nftables rules. Adding `::/0` without full IPv6 egress could send client IPv6 traffic into the tunnel and blackhole it.

If you need IPv6 leak protection before full IPv6 support lands, disable IPv6 on the client/router or configure IPv4-only behavior on the client side.

## Tunnel Egress and WARP

Each tunnel can use one of two egress modes:

- `Server WAN`: client traffic leaves through the server external interface from `EXTERNAL_INTERFACE`;
- `Cloudflare WARP`: client traffic leaves through a shared `warp0` outbound interface.

WARP is not an AmneziaWG protocol profile. It is an outbound routing mode for existing tunnels. This means Legacy / 1.0, AWG 1.5, and AWG 2.0 tunnels can independently choose WAN or WARP egress.

Recommended flow:

1. Select `Cloudflare WARP` in the `Egress` field while creating a tunnel, or open `Tunnel settings` for an existing tunnel.
2. Change `Egress` from `Server WAN` to `Cloudflare WARP`.
3. Click `Create tunnel` or `Save`.

If WARP is not configured yet, awg-forge automatically registers Cloudflare WARP, creates the shared outbound `warp0` interface, applies runtime routing/NAT, and then switches the tunnel to WARP egress.

`Maintenance` -> `WARP` is for operations: checking status, manually registering or re-registering WARP, restarting `warp0`, deleting WARP config, or importing a config manually.

Manual import is only a fallback when you already have a Cloudflare WARP WireGuard/AmneziaWG config from an external generator or WARP client tool. In that case, open `Manual WARP config import`, paste the full config, and click `Import WARP config`.

Existing client configs do not need to change when only egress mode changes, because the client still connects to the same AmneziaWG tunnel endpoint. Runtime routing/NAT changes on the server side.

Doctor checks WARP runtime, policy rules, and WARP-aware firewall expectations for WARP-enabled tunnels.

## APPLY_CONFIG

When `APPLY_CONFIG=true`, mutating operations update state/config files and apply changes to runtime.

If runtime apply fails, awg-forge rolls back state and rendered configs. The UI shows the apply error and should not keep a client or tunnel that failed to apply.

For local development:

```env
APPLY_CONFIG=false
```

## Audit Log

The audit log stores safe operational events: login success/failure, client create/update/delete, tunnel create/update/delete/restart, firewall repair, backup/support/restore verify, and update checks.

It is meant for cases like “it worked yesterday, then settings changed, now handshakes exist but internet does not work”.

In the Web UI, `Maintenance` -> `Logs` auto-refreshes while the Maintenance window is open and displays newest events first.

The audit log must not contain:

- private keys;
- preshared keys;
- passwords;
- session secrets;
- full client configs;
- import keys or `vpn://`;
- raw protocol parameter values.

Read recent events:

```bash
docker exec awg-forge awg-forge logs
docker exec awg-forge awg-forge logs --tail 200
docker exec awg-forge awg-forge logs --level error
docker exec awg-forge awg-forge logs --event tunnel.apply.failed
```
