# AWG 2.0 Design

This document records the implementation design for `awg_2_0`. It is intentionally separate from the existing Legacy / 1.0 and 1.5 implementation so 2.0 lives as a new profile without changing working tunnels.

## Product Decision

`awg_2_0` is a new tunnel/server profile.

Do not convert existing Legacy / 1.0 or 1.5 tunnels in place. Official Amnezia docs say 2.0 requires new configuration files/keys, and older app versions cannot use 2.0 profiles.

## Compatibility

Minimum client:

- AmneziaVPN `4.8.12.9` or later for official 2.0 support.

Current product behavior:

- `1.0` tab remains for Legacy tunnels.
- `1.5` tab remains for current 1.5-oriented tunnels.
- `2.0` tab is enabled after profile, rendering, validation, and golden tests.
- `.conf` import has been validated on desktop and iOS clients with compatible AmneziaVPN builds.
- Native Amnezia import payloads are not exposed in the product.

## Parameter Model

Use the existing `config.ProtocolParams map[string]string` storage for v0.1.x. Add helper types in `internal/protocol` for validation and rendering instead of changing persisted state.

Required keys:

```text
Jc
Jmin
Jmax
S1
S2
S3
S4
H1
H2
H3
H4
I1
I2
I3
I4
I5
```

`I1-I5` may be empty strings, but defaults should populate them if we have verified safe CPS values.

## Validation

Numeric fields:

- `Jc`: `0..10`
- `Jmin`: `64..1024`
- `Jmax`: `64..1024`
- `Jmin <= Jmax`
- `S1`: `0..64`
- `S2`: `0..64`
- `S3`: `0..64`
- `S4`: `0..32`

Header fields:

- Accept either a single unsigned 32-bit integer: `1234`
- Or a range: `1234-5678`
- Range start must be `<=` range end.
- Values must fit `0..4294967295`.
- `H1-H4` ranges must not overlap.

CPS fields:

- Accept sequence of tags:
  - `<b 0xHEX>` where `HEX` length is even;
  - `<r N>`;
  - `<rd N>`;
  - `<rc N>`;
  - `<t>`.
- `N` should be `0..1000` based on `amneziawg-go`.
- Reject malformed tags.
- Reject any generated single `I` packet whose estimated size exceeds a conservative bound. Start with `<= 1024`; tighten after real-client verification.

MTU safety:

- Do not invent MTU in protocol code. Use the tunnel MTU exactly as configured.
- Warn if `Jmax` is greater than or equal to the effective system MTU when we can detect the MTU.
- In UI, show a warning if `Jmax >= tunnel MTU` when tunnel MTU is explicit.

## Default Generation

Current default strategy:

- `Jc`: random `4..10`
- `Jmin`: random `64..256`
- `Jmax`: random `768..1024`
- `S1`: random `15..64`
- `S2`: random `15..64`
- `S3`: random `15..64`
- `S4`: random `8..32`
- `H1-H4`: four non-overlapping ranges
- `I1-I5`: reuse the current 1.5 CPS chain.

Open question: exact best default width and position of `H1-H4` ranges. The current implementation uses small, non-overlapping ranges in the lower signed-32-bit space.

## Rendering

Server interface should include:

```ini
[Interface]
PrivateKey = <server-private-key>
Address = <server-address>/24
ListenPort = <port>
MTU = <optional tunnel MTU>
PreUp =
PostUp = <IPv4 NAT/firewall rules>
PreDown =
PostDown = <IPv4 NAT/firewall cleanup rules>
Jc = <value>
Jmin = <value>
Jmax = <value>
S1 = <value>
S2 = <value>
S3 = <value>
S4 = <value>
H1 = <range-or-value>
H2 = <range-or-value>
H3 = <range-or-value>
H4 = <range-or-value>
I1 = <optional CPS>
I2 = <optional CPS>
I3 = <optional CPS>
I4 = <optional CPS>
I5 = <optional CPS>
```

Server peers remain normal WireGuard/AWG peers:

```ini
[Peer]
PublicKey = <client-public-key>
PresharedKey = <client-preshared-key>
AllowedIPs = <client-ip>/32
```

Client interface should include the same protocol params and tunnel MTU only if explicitly configured:

```ini
[Interface]
PrivateKey = <client-private-key>
Address = <client-ip>/32
DNS = <dns>
MTU = <optional tunnel MTU>
Jc = <value>
Jmin = <value>
Jmax = <value>
S1 = <value>
S2 = <value>
S3 = <value>
S4 = <value>
H1 = <range-or-value>
H2 = <range-or-value>
H3 = <range-or-value>
H4 = <range-or-value>
I1 = <optional CPS>
I2 = <optional CPS>
I3 = <optional CPS>
I4 = <optional CPS>
I5 = <optional CPS>
```

Client peer remains:

```ini
[Peer]
PublicKey = <server-public-key>
PresharedKey = <client-preshared-key>
AllowedIPs = <allowed-ips>
PersistentKeepalive = <keepalive>
Endpoint = <server-host>:<port>
```

## Amnezia Native Import

Keep `.conf` download as the supported path.

For a future native JSON import path:

- include all params in `last_config`;
- include raw INI config in `last_config.config`;
- set container to `amnezia-awg`;
- set protocol key to `awg`;
- set `protocol_version` to `"2"`;
- keep it hidden until tested on real iOS and Android clients.

## UI

For the 2.0 tab:

- allow creating `awg20` tunnels;
- show `S3/S4` fields only for 2.0;
- show `H1-H4` as ranges for 2.0;
- show `I1-I5` as advanced fields for 2.0;
- validate overlap client-side for fast feedback, but backend validation is authoritative;
- warn that changing protocol params requires fresh client configs.

## Tests

Implemented tests:

- golden server config for 2.0;
- golden client config for 2.0;
- `H1-H4` overlap rejection;
- single-value H parsing;
- range H parsing;
- invalid range rejection;
- `S4 > 32` rejection;
- missing `S3/S4` rejection;
- CPS syntax tests shared with 1.5;
- native import payload has `protocol_version = "2"` if native import is offered in the future.

## Validation

Validated:

1. `AWG20` profile renders server and client configs.
2. Docker/server-side `awg show` reports `s3/s4`, ranged `h1-h4`, `i1-i5`, handshake, and traffic.
3. `.conf` import works on desktop.
4. `.conf` import works on iOS after updating to a compatible AmneziaVPN build.

Still pending:

1. Native import verification.
2. Cross-platform native import schema validation.
3. Wider compatibility matrix for iOS, Android, desktop, and client versions.
