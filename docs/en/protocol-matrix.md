# Protocol Matrix

awg-forge is a launcher and manager for existing AmneziaWG implementations. It does not implement a VPN protocol itself. The Go code renders config files and asks upstream `awg`, `awg-quick`, and `amneziawg-go`/AmneziaWG tools to run them.

## Implemented

| Profile | Status | Notes |
| --- | --- | --- |
| `awg_legacy_1_0` | Implemented | Renders AmneziaWG Legacy / 1.0 config fields: `Jc`, `Jmin`, `Jmax`, `S1`, `S2`, `H1`, `H2`, `H3`, `H4`. Defaults are generated for obfuscation, not WireGuard fallback. |
| `awg_1_5` | Implemented | Adds `I1-I5` signature/masking packets to client configs. Defaults include the official DNS-like `I1` conversion packet plus small generated runtime-random signature packets for `I2-I5`. |
| `awg_2_0` | Implemented | Uses `I1-I5`, adds `S3` and `S4`, supports `H1-H4` ranges, validates non-overlapping header ranges, and renders fresh tunnel/client configs. Defaults use a generated QUIC Initial-like `I1` CPS signature. `.conf` import has been validated on desktop and iOS clients with compatible AmneziaVPN builds. |
| `awg_3` | Experimental | One AWG 3.x family profile included in the standard image and marked experimental in the Web UI. It uses pinned `amneziawg-go` 3.1.20260828 and `amneziawg-tools` 3.1.20260812 userspace sources and renders `HeaderProtectionKey`, AWG3 ranges, `RandomTrailers`, and `DisableCookies`. Client export supports `.conf`, raw AmneziaWG QR, structured AmneziaVPN QR, and `vpn://`; kernel runtime and production compatibility are intentionally not supported. |

## AWG 3.x Experimental Boundaries

The single AWG 3.x experimental profile is derived from the current AmneziaVPN self-hosted generator and the pinned `amneziawg-go` 3.1.20260828 / `amneziawg-tools` 3.1.20260812 source revisions, fixed by commit SHA in `build/amneziawg.refs`. It uses:

- `Jc`, `Jmin`, `Jmax`, `S1-S4`, `H1-H4`, `I1-I5`;
- `HeaderProtectionKey` generated once per tunnel and kept out of public state and diagnostics; authenticated client exports include it because the client needs it;
- `ContentPaddingAddition`, `RekeyAfterTime`, `RekeyTimeout`, `RejectAfterTime`, `KeepaliveTimeout`, `MaxHandshakeAttempts`, and `PersistentKeepalive` as scalar or ascending `uint16` ranges.
- `RandomTrailers` and `DisableCookies` as upstream `on`/`off` state values, both defaulting to `off`; server runtime configs render the explicit state, while client exports omit disabled options.

The numeric and range defaults mirror the upstream client generator. `I1` reuses AWG-Forge's generated QUIC Initial-like CPS mechanism from the 2.0 profile. Header Protection defaults to compatibility headers `H1-H4=1/2/3/4`, which disable custom message headers, and requires every `S1-S4` value to be at least `12`. The security-sensitive `RandomTrailers` and `DisableCookies` toggles default to `off`. If `RandomTrailers` is enabled, upstream recommends equal `S1-S4` values to reduce packet misclassification risk. AWG3 config parsing in the pinned tools accepts the extended ranges as `uint16`; awg-forge rejects values above `65535` rather than allowing upstream truncation.

AWG3 is forced through the pinned userspace runtime. The complete profile is experimental and must be enabled and used at the operator's own risk. Keep `RandomTrailers` off while the upstream transport-classification and first-packet issues remain unresolved. `DisableCookies=on` suppresses outgoing Cookie Reply messages; in the pinned runtime it bypasses the receiving-side under-load cookie challenge, MAC2 check, and rate-limit branch while incoming Cookie Reply handling remains available. This materially weakens handshake-flood protection and must remain off outside controlled testing. Server configs retain explicit `off` values because runtime updates use `syncconf`; all client exports omit disabled options, matching AmneziaVPN 5.0.1.5 canonical export. Use AmneziaVPN 5.0.1.5+ for interoperability tests and do not use 5.0.0.5 for AWG 3.1. The AmneziaWG QR and `vpn://` contain the same rendered raw `.conf` as the download. The AmneziaVPN QR uses a typed `last_config` with protocol version `3.1`, Header Protection, timing fields, MTU, and the AWG 3.x keepalive range. `.conf` remains the fallback if a platform-specific QR or deep-link import fails.

An explicit tunnel MTU is preserved in all exports. New AWG 3.x tunnels default to `1280` when no MTU is configured; explicit values and existing tunnel state are preserved. See [MTU defaults and client import limitations](configuration.md#mtu) before diagnosing a connection that handshakes but stalls on large packets.

Manual end-to-end validation of the standard image has confirmed `.conf` import, handshake, traffic, restart recovery, WAN egress, and WARP egress with compatible current AmneziaVPN, AmneziaWG, and DefaultVPN clients. This is a bounded acceptance result, not a guarantee for every platform, client build, network, or future 3.x runtime.

The AWG 3.x AmneziaVPN QR and `vpn://` serializers have automated schema and round-trip coverage against the field names and import behavior in AmneziaVPN 5.0.1.5. Real-device scanning and deep-link handling still require platform-specific interoperability testing before they can be treated as equivalent to the validated `.conf` path.

## Source Findings For AWG 2.0

Use an AmneziaVPN build that supports AmneziaWG 2.0. Existing AmneziaWG 1.0 installations are shown as Legacy, and moving to 2.0 requires new guest configuration files/keys; it is not an in-place upgrade.

Official Amnezia docs describe 2.0 changes versus 1.5:

- adds `S3` and `S4`;
- adds range support for `H1-H4`;
- `H1-H4` ranges must not overlap;
- removes older `j1-j3` and `itime`;
- keeps `I1-I5`, introduced by 1.5.

## Parameter Ranges

Official 2.0 parameter ranges:

| Parameter | Range / syntax | Notes |
| --- | --- | --- |
| `I1-I5` | CPS signature strings | Each value is a sequence of tags such as `<b 0x...>`, `<r N>`, `<rd N>`, `<rc N>`, `<t>`. Missing values are skipped. |
| `S1-S3` | `0..64` | Fixed random prefix sizes for init, response, and cookie reply packets. |
| `S4` | `0..32` | Fixed random prefix size for transport data packets. |
| `Jc` | `0..10` in official docs | Number of junk packets after `I1-I5`. `amneziawg-go` README still says recommended `4..12`, so awg-forge should stay inside `0..10` for compatibility with the docs and AmneziaVPN UI. |
| `Jmin`, `Jmax` | `64..1024`, with `Jmin <= Jmax` | Junk packet size range. Keep `Jmax` below the effective system MTU to avoid fragmentation. |
| `H1-H4` | single `uint32` value or range `x-y` | 2.0 should use ranges by default. Ranges must not overlap. |

## Rendering Rules By Profile

| Field | Legacy / 1.0 | AWG 1.5 | AWG 2.0 | AWG 3.x |
| --- | --- | --- | --- | --- |
| `Jc/Jmin/Jmax` | Client and server interface | Client and server interface | Client and server interface | Client and server interface |
| `S1/S2` | Client and server interface | Client and server interface | Client and server interface | Client and server interface |
| `S3/S4` | Not rendered | Not rendered | Client and server interface | Client and server interface |
| `H1-H4` | Single values | Single values | Ranges by default, single values allowed only for explicit custom params | Client and server interface |
| `I1-I5` | Not rendered by awg-forge Legacy profile | Client interface only in current 1.5-oriented profile | Client and server interface | Client interface; retained only as comments in the server config, and generated `I1` uses the shared QUIC Initial-like mechanism |
| `HeaderProtectionKey` | Not rendered | Not rendered | Not rendered | Client and server interface; generated once per tunnel |
| AWG 3.x timing ranges | Not rendered | Not rendered | Not rendered | Client and server interface |
| `RandomTrailers/DisableCookies` | Not rendered | Not rendered | Not rendered | Explicit state in server runtime config; enabled values only in client exports |
| MTU when none is configured | Omitted (`Auto`) | Omitted (`Auto`) | Omitted (`Auto`) | New tunnels render `1280`; existing `0` state is preserved |
| `protocol_version` | Not an INI field | Not an INI field | Not an INI field; only AmneziaVPN JSON import metadata should use `"2"` | Not an INI field |

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

For the experimental AWG 3.x profile, defaults are:

- `Jc`: random `4..6`; `Jmin`: `10`; `Jmax`: `50`;
- `S1/S2`: distinct random values in `12..149`; `S3`: random `12..63`; `S4`: `12`;
- `H1-H4`: distinct values `1`, `2`, `3`, and `4`, protected by a per-tunnel `HeaderProtectionKey`;
- `I1`: the same generated `1200..1232` byte QUIC Initial-like CPS packet as AWG 2.0; `I2-I5` are empty;
- timing and padding ranges: `10-100`, `100-120`, `3-7`, `150-180`, `5-15`, and `15-20`; `PersistentKeepalive`: `25-35`;
- `RandomTrailers` and `DisableCookies`: `off`.

Zero-valued obfuscation parameters are treated as weak defaults because Amnezia docs note that all-zero behavior falls back toward standard WireGuard behavior.

AWG 2.0 uses a randomized QUIC Initial-like `I1` signature by default. Only the UDP payload shape is modeled: Ethernet/IP/UDP headers from packet captures are not included. The generated packet is intended for AmneziaWG CPS masking, not for establishing a real QUIC session. Its size is randomized within `1200..1232` bytes, and large random sections are split into randomized CPS `<r ...>` chunks below the parser boundary.

## Validation Status For AWG 2.0

Validated:

- generated `.conf` imported and connected on a desktop client;
- generated `.conf` imported and connected on iOS after updating to a compatible AmneziaVPN build;
- AmneziaVPN-compatible QR export is implemented as structured JSON with `last_config`, zlib/qCompress-style wrapping, base64url payload, and compatibility-critical JSON field types;
- Docker/server-side `awg show` reports 2.0 params, handshake, and traffic for `awg20`.

Still requires broader validation:

- QR import behavior across AmneziaVPN iOS, Android, and Desktop builds;
- exact native import schema differences across AmneziaVPN platforms.

## Sources

- [AmneziaWG docs](https://docs.amnezia.org/documentation/amnezia-wg/)
- [Using AmneziaWG 2.0 on self-hosted servers](https://docs.amnezia.org/documentation/instructions/new-amneziawg-selfhosted/)
- [amnezia-vpn/amneziawg-go README](https://github.com/amnezia-vpn/amneziawg-go)
- [amneziawg-go v3.1.20260828](https://github.com/amnezia-vpn/amneziawg-go/releases/tag/v3.1.20260828)
- [amneziawg-apple v3.1.3 parser fix](https://github.com/amnezia-vpn/amneziawg-apple/pull/43)
- [AmneziaVPN 5.0.1.5 stable release](https://github.com/amnezia-vpn/amnezia-client/releases/tag/5.0.1.5)
- [AmneziaVPN AWG 3.1 cross-version compatibility issue](https://github.com/amnezia-vpn/amnezia-client/issues/3016)
- [RandomTrailers transport-classification fix](https://github.com/amnezia-vpn/amneziawg-go/pull/183)
- [S4 first-packet race fix](https://github.com/amnezia-vpn/amneziawg-go/pull/184)
- [RandomTrailers and ContentPaddingAddition MTU issue](https://github.com/amnezia-vpn/amneziawg-go/issues/185)
- [AmneziaVPN MTU import fix](https://github.com/amnezia-vpn/amnezia-client/pull/3065)
- [AmneziaVPN explicit imported MTU preservation](https://github.com/amnezia-vpn/amnezia-client/pull/3113)
- [AmneziaVPN AWG 3.1 server/client MTU mismatch report](https://github.com/amnezia-vpn/amnezia-client/issues/3089)
- [Pinned `awg-quick` MTU selection logic](https://github.com/amnezia-vpn/amneziawg-tools/blob/ee0f0a9aa34ff0a0da4b3433b9512781cfe02843/src/wg-quick/linux.bash)
- [Amnezia client MTU defaults on the development branch](https://github.com/amnezia-vpn/amnezia-client/blob/dev/client/core/utils/constants/protocolConstants.h)
- [RFC 8200, IPv6 minimum link MTU](https://www.rfc-editor.org/rfc/rfc8200.html#section-5)
- [RFC 9000, QUIC: A UDP-Based Multiplexed and Secure Transport](https://www.rfc-editor.org/rfc/rfc9000)
