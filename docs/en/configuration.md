# Configuration

The main example is [.env.example](../../.env.example).

## Common Variables

- `SERVER_HOST`: public DNS name or IP clients connect to.
- `TUNNEL_NAME`: first tunnel name and interface, for example `awg0`, `awg15`, or `awg20`.
- `LISTEN_PORT`: default port for the first tunnel.
- `WEBUI_HOST`: Web UI bind address. Defaults to `127.0.0.1`.
- `WEBUI_PORT`: Web UI port. Defaults to `51821`.
- `PASSWORD`: Web UI password. Required for public binds and recommended always.
- `SESSION_COOKIE_SECURE`: Secure cookie policy for UI sessions. Values: `auto`, `true`, `false`. Defaults to `auto`.
- `EXTERNAL_INTERFACE`: server egress interface, often `eth0` or `ens3`. In bridge networking this is usually `eth0` inside the container.
- `IPV4_SUBNET`: subnet for the first tunnel, for example `10.8.0.0/24`.
- `DNS`: DNS value rendered into client configs.
- `ALLOWED_IPS`: client-side allowed IPs. Usually `0.0.0.0/0`.
- `PERSISTENT_KEEPALIVE`: `PersistentKeepalive` value in client configs. Defaults to `0`.
- `MTU`: tunnel MTU. `0` means auto/omit, so `MTU = ...` is not rendered. Common explicit values: `1280`, `1380`, `1400`, `1420`.
- `PROTOCOL_PROFILE`: first tunnel profile. Usually `awg_legacy_1_0`.
- `APPLY_CONFIG`: when `true`, awg-forge applies runtime tunnel changes with AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: published Docker UDP ports/ranges, for example `51820-51840,7443`.
- `AUDIT_LOG_ENABLED`: enables the safe audit log. Defaults to `true`.
- `AUDIT_LOG_PATH`: audit log path. Defaults to `/etc/awg-forge/audit.log`.
- `AUDIT_LOG_MAX_SIZE`: file size before rotation. Defaults to `5242880`.
- `AUDIT_LOG_MAX_FILES`: number of rotated files to keep. Defaults to `3`.

The quick installer asks for `PROTOCOL_PROFILE` before tunnel defaults, so profile-specific defaults stay aligned:

| Profile | Tunnel name | Port | Subnet |
| --- | --- | --- | --- |
| `awg_legacy_1_0` | `awg0` | `51820` | `10.8.0.0/24` |
| `awg_1_5` | `awg15` | `51825` | `10.15.0.0/24` |
| `awg_2_0` | `awg20` | `51830` | `10.20.0.0/24` |

When creating more tunnels in the Web UI, awg-forge suggests free names, ports, and subnets across all profiles. Backend validation still rejects manual conflicts.

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

## SERVER_HOST and Tunnel Endpoint

`SERVER_HOST` defines the global host awg-forge uses in `Endpoint = <host>:<port>` for client `.conf` files.

Each tunnel also has a `Server host` field in the Web UI. When it is empty, the tunnel inherits the global `SERVER_HOST`. When it is set, it overrides the endpoint host only for that tunnel.

This is useful when different tunnels are published through different subdomains, for example:

```text
legacy.example.com:44865
awg20.example.com:44867
```

Important:

- `Server host` must not include a scheme, path, or port;
- the port comes from the tunnel settings;
- after changing the host, clients should download fresh `.conf` files;
- already imported clients do not update themselves.

## MTU

`MTU=0` means awg-forge does not add `MTU = ...` to server/client configs.

If you explicitly set tunnel MTU, it is rendered exactly the same into server and client configs. awg-forge does not use hidden MTU decisions.

Practically:

- `Auto` is a good starting point;
- `1280` often helps on problematic networks, mobile networks, routers, and complex routes;
- the Web UI offers `Auto`, common presets, and `Custom` for explicit MTU values;
- after changing MTU, clients should download fresh `.conf` files.

## Tunnel Egress and WARP

Each tunnel can use one of two egress modes:

- `Server WAN`: client traffic leaves through the server external interface from `EXTERNAL_INTERFACE`;
- `Cloudflare WARP`: client traffic leaves through a shared `warp0` outbound interface.

WARP is not an AmneziaWG protocol profile. It is an outbound routing mode for existing tunnels. This means Legacy / 1.0, AWG 1.5, and AWG 2.0 tunnels can independently choose WAN or WARP egress.

Recommended flow:

1. Open `Tunnel settings` for the tunnel that should use WARP.
2. Change `Egress` from `Server WAN` to `Cloudflare WARP`.
3. Click `Save`.

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
