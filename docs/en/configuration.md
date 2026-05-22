# Configuration

The main example is [.env.example](../../.env.example).

## Common Variables

- `SERVER_HOST`: public DNS name or IP clients connect to.
- `LISTEN_PORT`: default port for the first tunnel.
- `WEBUI_HOST`: Web UI bind address. Defaults to `127.0.0.1`.
- `WEBUI_PORT`: Web UI port. Defaults to `51821`.
- `PASSWORD`: Web UI password. Required for public binds and recommended always.
- `EXTERNAL_INTERFACE`: server egress interface, often `eth0` or `ens3`. In bridge networking this is usually `eth0` inside the container.
- `IPV4_SUBNET`: subnet for the first tunnel, for example `10.8.0.0/24`.
- `DNS`: DNS value rendered into client configs.
- `ALLOWED_IPS`: client-side allowed IPs. Usually `0.0.0.0/0`.
- `PERSISTENT_KEEPALIVE`: `PersistentKeepalive` value in client configs. Defaults to `0`.
- `MTU`: tunnel MTU. `0` means auto/omit, so `MTU = ...` is not rendered. Common explicit values: `1280`, `1380`, `1400`, `1420`.
- `PROTOCOL_PROFILE`: first tunnel profile. Usually `awg_legacy_1_0`.
- `APPLY_CONFIG`: when `true`, awg-forge applies runtime tunnel changes with AmneziaWG tools.
- `PUBLISHED_UDP_PORTS`: published Docker UDP ports/ranges, for example `51820-51840,7443`.

## SESSION_SECRET

`SESSION_SECRET` is optional. If omitted, awg-forge creates and persists one in `state.json`.

It is used to sign UI session cookies. In the normal setup, users do not need to manage it manually.

## EXTERNAL_INTERFACE

To find the server egress interface:

```bash
ip route get 1.1.1.1
```

Example:

```text
1.1.1.1 via 77.110.113.1 dev ens3 src 77.110.113.185
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
- after changing MTU, clients should download fresh `.conf` files.

## APPLY_CONFIG

When `APPLY_CONFIG=true`, mutating operations update state/config files and apply changes to runtime.

If runtime apply fails, awg-forge rolls back state and rendered configs. The UI shows the apply error and should not keep a client or tunnel that failed to apply.

For local development:

```env
APPLY_CONFIG=false
```
