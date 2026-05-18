# Protocol Matrix

awg-forge is a launcher and manager for existing AmneziaWG implementations. It does not implement a VPN protocol itself. The Go code renders config files and asks upstream `awg`, `awg-quick`, and `amneziawg-go` to run them.

## Implemented

| Profile | Status | Notes |
| --- | --- | --- |
| `awg_legacy_1_0` | Implemented | Renders AmneziaWG Legacy / 1.0 config fields: `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1`, `H2`, `H3`, `H4`. Defaults are generated for strong obfuscation, not WireGuard fallback. |
| `awg_1_5` | Implemented | Adds `I1-I5` signature/masking packets to client configs. Defaults include the official DNS-like `I1` conversion packet plus small generated runtime-random signature packets for `I2-I5`. |

## Planned, Not Implemented

| Profile | Status | Notes |
| --- | --- | --- |
| `awg_2_0` | Planned | Uses `I1-I5`, adds `S3` and `S4`, supports `H1-H4` ranges, requires non-overlapping ranges, and requires new configs/keys when moving from Legacy / 1.0. |
| `custom` | Planned | Reserved for explicit user-provided config parameters after validation rules are clear. |

## Parameter Notes

Official Amnezia docs list these AmneziaWG 2.0 ranges: `I1-I5` arbitrary hex-blob signature packets; `S1-S3` from 0 to 64 bytes; `S4` from 0 to 32 bytes; `Jc` from 0 to 10; `Jmin/Jmax` from 64 to 1024 bytes; and `H1-H4` from 0 to 4,294,967,295 with range support in 2.0.

The `amneziawg-go` README documents that `Jc` controls junk packets before handshakes, `S1-S4` are message paddings, `H1-H4` can be single values or ranges, and `I1-I5` are sent before every handshake in order.

The `amneziawg-linux-kernel-module` README documents Legacy-compatible constraints and recommendations: all parameters except `Jc/Jmin/Jmax` must match between client and server; `Jc` recommended range is 4-12; `S1/S2` recommended range is 15-150; `S1 + 56` must not equal `S2`; `H1-H4` must be unique; and generated packet sizes should avoid MTU fragmentation.

## Legacy / 1.0 Defaults

`awg-forge` generates non-zero obfuscation defaults for Legacy / 1.0:

| Parameter | Generated default |
| --- | --- |
| `Jc` | random 4-12 |
| `Jmin` | random 64-256 |
| `Jmax` | random 768-1024, always greater than `Jmin` |
| `S1` | random 15-150 |
| `S2` | random 15-150, avoiding `S1 + 56 == S2` |
| `H1-H4` | random unique non-zero values in the upstream recommended range |

Zero-valued obfuscation parameters are treated as weak defaults because Amnezia docs note that all-zero behavior falls back toward standard WireGuard behavior.

Before implementing 2.0 or changing 1.5 behavior, verify exact syntax against:

- [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/)
- [Converting AmneziaWG 1.0 to 1.5](https://docs.amnezia.org/documentation/instructions/upgrade-awg-config/)
- [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go)
- [amnezia-vpn/amneziawg-tools](https://github.com/amnezia-vpn/amneziawg-tools)
- [amnezia-vpn/amnezia-client](https://github.com/amnezia-vpn/amnezia-client)
