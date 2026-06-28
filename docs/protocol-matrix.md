# Protocol Matrix

awg-forge is a launcher and manager for existing AmneziaWG implementations. It does not implement a VPN protocol itself. The Go code renders config files and asks upstream `awg`, `awg-quick`, and `amneziawg-go`/AmneziaWG tools to run them.

## Implemented

| Profile | Status | Notes |
| --- | --- | --- |
| `awg_legacy_1_0` | Implemented | Renders AmneziaWG Legacy / 1.0 config fields: `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1`, `H2`, `H3`, `H4`. Defaults are generated for obfuscation, not WireGuard fallback. |
| `awg_1_5` | Implemented | Adds `I1-I5` signature/masking packets to client configs. Defaults include the official DNS-like `I1` conversion packet plus small generated runtime-random signature packets for `I2-I5`. |
| `awg_2_0` | Implemented | Uses `I1-I5`, adds `S3` and `S4`, supports `H1-H4` ranges, validates non-overlapping header ranges, and renders fresh tunnel/client configs. Defaults use a generated QUIC Initial-like `I1` CPS signature. `.conf` import has been validated on desktop and iOS clients with compatible AmneziaVPN builds. |

## Planned, Not Implemented

| Profile | Status | Notes |
| --- | --- | --- |
| `custom` | Planned | Reserved for explicit user-provided config parameters after validation rules are clear. |

## Source Findings For AWG 2.0

Official Amnezia docs say AmneziaWG 2.0 is supported by AmneziaVPN app version `4.8.12.9` and later. Existing AmneziaWG 1.0 installations are shown as Legacy, and moving to 2.0 requires new guest configuration files/keys; it is not an in-place upgrade.

Official Amnezia docs describe 2.0 changes versus 1.5:

- adds `S3` and `S4`;
- adds range support for `H1-H4`;
- `H1-H4` ranges must not overlap;
- removes older `j1-j3` and `itime`;
- keeps `I1-I5`, introduced by 1.5.

Official 2.0 parameter ranges:

| Parameter | Range / syntax | Notes |
| --- | --- | --- |
| `I1-I5` | CPS signature strings | Each value is a sequence of tags such as `<b 0x...>`, `<r N>`, `<rd N>`, `<rc N>`, `<t>`. Missing values are skipped. |
| `S1-S3` | `0..64` | Fixed random prefix sizes for init, response, and cookie reply packets. |
| `S4` | `0..32` | Fixed random prefix size for transport data packets. |
| `Jc` | `0..10` in official docs | Number of junk packets after `I1-I5`. `amneziawg-go` README still says recommended `4..12`, so awg-forge should stay inside `0..10` for compatibility with the docs and AmneziaVPN UI. |
| `Jmin`, `Jmax` | `64..1024`, with `Jmin <= Jmax` | Junk packet size range. Keep `Jmax` below the effective system MTU to avoid fragmentation. |
| `H1-H4` | single `uint32` value or range `x-y` | 2.0 should use ranges by default. Ranges must not overlap. |

The `amneziawg-go` README confirms the config syntax for header ranges: either a single value like `1234` or a range like `123-456`. It also says unspecified parameters are treated as zero.

Amnezia client `dev` maps config keys directly to INI names:

- `Jc`, `Jmin`, `Jmax`;
- `S1`, `S2`, `S3`, `S4`;
- `H1`, `H2`, `H3`, `H4`;
- `I1`, `I2`, `I3`, `I4`, `I5`;
- `protocol_version` in imported native JSON, with AWG 2.0 represented as `"2"`.

The current Amnezia client import path detects AWG 2.0 when a WireGuard/AWG config has all required Legacy fields plus both `S3` and `S4`. It detects AWG 1.5 when it has Legacy fields plus at least one `I1-I5`, but no `S3/S4`.

## Rendering Rules By Profile

| Field | Legacy / 1.0 | AWG 1.5 | AWG 2.0 |
| --- | --- | --- | --- |
| `Jc/Jmin/Jmax` | Client and server interface | Client and server interface | Client and server interface |
| `S1/S2` | Client and server interface | Client and server interface | Client and server interface |
| `S3/S4` | Not rendered | Not rendered | Client and server interface |
| `H1-H4` | Single values | Single values | Ranges by default, single values allowed only for explicit custom params |
| `I1-I5` | Not rendered by awg-forge Legacy profile | Client interface only in current 1.5-oriented profile | Client and server interface by default, because 2.0 server settings are inherited by generated configs |
| `protocol_version` | Not an INI field | Not an INI field | Not an INI field; only native Amnezia JSON import metadata should use `"2"` |

## Current Defaults

`awg-forge` generates non-zero obfuscation defaults:

| Parameter | Legacy / 1.0 and 1.5 generated default |
| --- | --- |
| `Jc` | random `4..10` |
| `Jmin` | random `64..256` |
| `Jmax` | random `768..1024`, always greater than `Jmin` |
| `S1` | random `15..64` |
| `S2` | random `15..64`, avoiding `S1 + 56 == S2` |
| `H1-H4` | crypto-random unique non-zero single values, generated without modulo reduction |

For AWG 2.0, defaults are:

- `Jc`: random `4..10`;
- `Jmin`: random `64..256`;
- `Jmax`: random `768..1024`;
- `S1-S3`: random `15..64`;
- `S4`: random `8..32`;
- `H1-H4`: crypto-random non-overlapping ranges with width `30000..65535`, not single values;
- `I1`: generated per tunnel as a `1200..1232` byte QUIC Initial-like CPS packet, with a randomized protected first byte, QUIC v1 marker, one of several destination/source connection ID profiles, a valid QUIC varint length field, and runtime-random protected payload bytes split into parser-safe randomized `<r ...>` chunks of at most `999` bytes;
- `I2-I5`: the same small CPS entropy chain currently used by the 1.5 profile.

Zero-valued obfuscation parameters are treated as weak defaults because Amnezia docs note that all-zero behavior falls back toward standard WireGuard behavior.

AWG 2.0 uses a randomized QUIC Initial-like `I1` signature by default. Only the UDP payload shape is modeled: Ethernet/IP/UDP headers from packet captures are not included. The generated packet is intended for AmneziaWG CPS masking, not for establishing a real QUIC session. Its size is randomized within `1200..1232` bytes, and large random sections are split into randomized CPS `<r ...>` chunks below the parser boundary.

## Validation Status For AWG 2.0

Validated:

- generated `.conf` imported and connected on a desktop client;
- generated `.conf` imported and connected on iOS after updating to a compatible AmneziaVPN build;
- Docker/server-side `awg show` reports 2.0 params, handshake, and traffic for `awg20`.

Not implemented:

- Amnezia native import payloads for 2.0 and iOS;
- exact native import schema across AmneziaVPN platforms.

## Sources

- [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/)
- [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amnezia-vpn/amneziawg-go README](https://github.com/amnezia-vpn/amneziawg-go)
- [amnezia-client `protocols_defs.h`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/protocols/protocols_defs.h)
- [amnezia-client `importController.cpp`](https://raw.githubusercontent.com/amnezia-vpn/amnezia-client/dev/client/ui/controllers/importController.cpp)
- [RFC 9000, QUIC: A UDP-Based Multiplexed and Secure Transport](https://www.rfc-editor.org/rfc/rfc9000)
