# Multi-Profile And Multi-Server Architecture

awg-forge treats every AmneziaWG protocol generation as a separate tunnel/server instance. A Legacy / 1.0 client connects to a Legacy tunnel, a 1.5 client connects to a 1.5 tunnel, and a future 2.0 client will connect to a 2.0 tunnel with its own keys and config.

This is safer than mutating one global profile because incompatible clients never share one server config.

## Current Product Model

The state model is v2-only:

```go
type State struct {
    SchemaVersion int
    SessionSecret string
    ServerHost string
    ExternalInterface string
    Tunnels []Tunnel
}

type Tunnel struct {
    ID string
    Name string
    InterfaceName string
    Enabled bool
    ListenPort int
    ServerAddress string
    IPv4Subnet string
    DNS string
    AllowedIPs string
    Keepalive int
    ServerPrivateKey string
    ServerPublicKey string
    ProtocolProfileID string
    ProtocolParams ProtocolParams
    Clients []Client
}

type Client struct {
    ID string
    TunnelID string
    Name string
    Enabled bool
    IPv4Address string
    PrivateKey string
    PublicKey string
    PresharedKey string
}
```

Each tunnel has:

- unique interface name, e.g. `awg0`, `awg15`, `awg20`
- unique UDP listen port
- non-overlapping IPv4 subnet
- independent server keypair
- independent protocol params
- independent rendered server config
- clients scoped to exactly one tunnel

## Storage Layout

```text
/etc/awg-forge/
  state.json
  tunnels/
    awg0/
      server.conf
      clients/
        <client-id>.conf
    awg15/
      server.conf
      clients/
        <client-id>.conf
    awg20/
      server.conf
      clients/
        <client-id>.conf
```

Runtime config for `awg-quick` is copied to:

```text
/etc/amnezia/amneziawg/
  awg0.conf
  awg15.conf
  awg20.conf
```

## Protocol Profiles

`ProtocolProfile` is a renderer and validator. It does not implement a VPN protocol itself; the actual tunnel is handled by existing AmneziaWG userspace/kernel tools included in Docker.

Implemented profiles:

- `awg_legacy_1_0`: Legacy / 1.0 fields `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1-H4`
- `awg_1_5`: 1.5-style profile with `I1-I5` support. Defaults include the DNS-like `I1` conversion packet plus small generated runtime-random packets for `I2-I5`.

Validation-stage profile:

- `awg_2_0`: new tunnel profile only, never an in-place conversion of Legacy/1.5 tunnels

Default tunnel suggestions:

| Profile | Interface | Port | Subnet |
| --- | --- | --- | --- |
| Legacy / 1.0 | `awg0` | `51820` | `10.8.0.0/24` |
| AWG 1.5 | `awg15` | `51825` | `10.15.0.0/24` |
| AWG 2.0 | `awg20` | `51830` | `10.20.0.0/24` |

## Runtime Operations

Service operations are tunnel-scoped:

```go
RenderTunnel(tunnelID string) error
RestartTunnelByID(tunnelID string) error
UpdateTunnelProtocol(tunnelID, profileID string, params ProtocolParams) error
RegenerateTunnelProtocol(tunnelID, profileID string) error
AddClientToTunnel(tunnelID, name string) (Client, error)
```

For each tunnel:

- render managed config under `/etc/awg-forge/tunnels/<interface>/server.conf`
- render client configs under `/etc/awg-forge/tunnels/<interface>/clients/`
- copy runtime config to `/etc/amnezia/amneziawg/<interface>.conf`
- if interface is down, run `awg-quick up <interface>`
- if interface is up, run `awg syncconf <interface> <stripped config>`

Each tunnel stores its own last render/apply timestamps and last apply error.

## NAT And Firewall Rules

Per-tunnel `PostUp`/`PostDown` rules use that tunnel's subnet, interface, and listen port:

```ini
iptables -t nat -A POSTROUTING -s <tunnel-subnet> -o <external-interface> -j MASQUERADE
iptables -A INPUT -p udp -m udp --dport <listen-port> -j ACCEPT
iptables -A FORWARD -i <interface> -j ACCEPT
iptables -A FORWARD -o <interface> -j ACCEPT
```

For multiple tunnels these rules are duplicated with different values. Future hardening should generate an idempotent nftables ruleset instead of relying only on `PostUp`/`PostDown`.

## Client Lifecycle

Client creation always targets one tunnel:

1. Select tunnel/profile.
2. Enter client name.
3. Allocate the next free IP from that tunnel subnet.
4. Generate client private/public/preshared keys.
5. Render only that tunnel.
6. Apply only that tunnel when `APPLY_CONFIG=true`.
7. Refresh the dashboard and offer the protected `.conf` download.

Deleting a client frees that IP inside the same tunnel. Disabled clients stay in state but are not rendered into server peers.

## Doctor

Doctor checks are global plus tunnel-aware:

Global:

- root/capabilities
- `/dev/net/tun`
- AmneziaWG/WireGuard tools
- `iptables` backend
- IPv4 forwarding
- external interface
- default route

Per tunnel:

- interface name is valid
- listen port is available or already belongs to that tunnel
- subnet is valid and non-overlapping
- rendered config succeeds
- runtime path can be managed
- last apply error is visible
- runtime `awg show <interface>` listen port matches state
- enabled clients exist as runtime peers
- stale client configs are visible
- latest handshake and transfer counters are visible when clients have connected

## AWG 2.0 Status

2.0 is implemented as a new `ProtocolProfile` plus a new tunnel. It should not modify existing Legacy/1.5 tunnels in place.

The concrete implementation plan lives in [AWG 2.0 Design](awg-2.0-design.md).

Validated:

- `.conf` import with compatible desktop and iOS AmneziaVPN builds
- real tunnel startup with Docker image tools
- handshake and traffic on `awg20`

Still pending:

- native Amnezia import validation
- broader client-version compatibility matrix

The architecture already reserves the multi-server shape for this: adding 2.0 should be additive, not a state rewrite.
