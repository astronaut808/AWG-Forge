# AmneziaVPN-compatible QR export plan

Status: implemented in backend and UI; manual import validation against real AmneziaVPN clients is still required before treating QR compatibility as fully verified.

Goal: add a separate QR export path that can be imported by the AmneziaVPN app, while keeping the existing AmneziaWG/raw `.conf` QR and `.conf` download behavior unchanged.

References:

- Amnezia client issue: https://github.com/amnezia-vpn/amnezia-client/issues/2119#issuecomment-3814106499
- Reference implementation: https://github.com/ne0x/wg-easy/blob/feat/amneziavpn-qr/src/server/utils/wgHelper.ts
- Relevant reference methods: `generateAmneziaVPNClientConfig`, `buildAmneziaQrPack`

## Scope

Implement only AmneziaVPN-compatible QR export.

In scope:

- Add a new AmneziaVPN QR payload builder.
- Keep the existing AmneziaVPN QR HTTP/UI entry points if possible, but replace their payload format.
- Generate QR image(s) from the new AmneziaVPN payload.
- Keep UI import options clear:
  - AmneziaVPN QR;
  - AmneziaWG QR from raw full `.conf`;
  - `.conf` download;
  - `vpn://` copy.
- Add tests for the AmneziaVPN QR payload format and wrapper.

Out of scope:

- Do not change existing AmneziaWG QR generation.
- Do not change native/raw `.conf` QR behavior.
- Do not change `.conf` rendering or download behavior.
- Do not change protocol generation.
- Do not change `I1-I5`, `S*`, `H*`, `J*` generation.
- Do not change installer, Docker behavior, or unrelated docs.
- Do not add QR subscription/multi-node behavior in this task.

## Current project behavior to preserve

Existing AmneziaWG QR is already working and should remain untouched.

Current raw QR path:

- Route: `/api/clients/{id}/qr`
- Handler: `clientQRAPI`
- Payload: rendered full client `.conf`
- QR encoding: existing server QR renderer
- Target client: AmneziaWG-compatible clients that accept raw `.conf` in QR

This route must stay exactly semantically equivalent: it should still encode the rendered full `.conf` and should not be replaced with the AmneziaVPN JSON wrapper.

Existing config download must also remain untouched:

- It must still return the rendered full client `.conf`.
- It must still use the current safe filename behavior.
- It must still use `no-store`.

## Problem

AmneziaVPN does not reliably import a raw `.conf` QR and does not expect a plain zlib/base64 payload.

The tested broken path was based on chunking raw `.conf` text into custom packets. It was partially scannable but AmneziaVPN treated the sequence incorrectly. Example observed behavior: QR 1 of 3 and QR 2 of 3 were accepted, but the final QR was interpreted as a new QR 1 of 3 instead of completing the sequence.

The useful reference indicates that AmneziaVPN expects a specific JSON structure wrapped in a Qt/qCompress-like binary envelope, then base64url encoded.

## Confirmed AmneziaVPN QR format

The AmneziaVPN QR payload is:

1. An outer JSON document.
2. Compressed with zlib.
3. Wrapped with a 12-byte binary header.
4. Encoded using base64url.
5. Encoded into a normal QR image.

It is not:

- raw `.conf`;
- plain zlib + base64;
- WireGuard URI;
- `vpn://` text;
- the same format as AmneziaWG raw `.conf` QR.

## Outer JSON structure

Target structure:

```json
{
  "containers": [
    {
      "awg": {
        "isThirdPartyConfig": true,
        "last_config": "{...JSON string...}",
        "port": "51820",
        "protocol_version": "2",
        "transport_proto": "udp"
      },
      "container": "amnezia-awg"
    }
  ],
  "defaultContainer": "amnezia-awg",
  "description": "client-name",
  "dns1": "1.1.1.1",
  "dns2": "1.0.0.1",
  "hostName": "example.com"
}
```

Important: `last_config` is a JSON string, not a nested JSON object.

In Go this should be represented with typed structs, not `map[string]any`, so tests are stable and field names are explicit.

Suggested structs:

```go
type amneziaVPNConfig struct {
	Containers       []amneziaVPNContainer `json:"containers"`
	DefaultContainer string                 `json:"defaultContainer"`
	Description      string                 `json:"description"`
	DNS1             string                 `json:"dns1,omitempty"`
	DNS2             string                 `json:"dns2,omitempty"`
	HostName         string                 `json:"hostName"`
}

type amneziaVPNContainer struct {
	AWG       amneziaVPNAWG `json:"awg"`
	Container string        `json:"container"`
}

type amneziaVPNAWG struct {
	IsThirdPartyConfig bool   `json:"isThirdPartyConfig"`
	LastConfig         string `json:"last_config"`
	Port               string `json:"port"`
	ProtocolVersion    string `json:"protocol_version"`
	TransportProto     string `json:"transport_proto"`
}
```

## `last_config` JSON structure

`last_config` must be marshaled separately, then assigned as a string to outer `awg.last_config`.

Expected fields:

```json
{
  "allowed_ips": "0.0.0.0/0",
  "client_ip": "10.20.0.2/32",
  "client_priv_key": "...",
  "config": "[Interface]\n...",
  "hostName": "example.com",
  "mtu": "1280",
  "persistent_keep_alive": "0",
  "port": "51820",
  "psk_key": "...",
  "server_pub_key": "...",
  "Jc": "8",
  "Jmin": "170",
  "Jmax": "899",
  "S1": "41",
  "S2": "56",
  "S3": "62",
  "S4": "24",
  "H1": "1882317089-1882317120",
  "H2": "1168906724-1168906755",
  "H3": "834492979-834493010",
  "H4": "2054578012-2054578043",
  "I1": "<b 0x...><r 851><r 344>",
  "I2": "<r 8><t><r 16>",
  "I3": "<rd 12><r 12>",
  "I4": "<rc 16><r 10>",
  "I5": "<r 32>"
}
```

Use `omitempty` for optional AWG fields. Do not emit empty `I2`, `I3`, `I4`, `I5` values. This is a safe serialization rule for this AmneziaVPN payload and does not change protocol generation or raw `.conf` rendering.

Suggested Go struct:

```go
type amneziaVPNLastConfig struct {
	AllowedIPs          string `json:"allowed_ips"`
	ClientIP            string `json:"client_ip"`
	ClientPrivateKey    string `json:"client_priv_key"`
	Config              string `json:"config"`
	HostName            string `json:"hostName"`
	MTU                 string `json:"mtu,omitempty"`
	PersistentKeepalive string `json:"persistent_keep_alive"`
	Port                string `json:"port"`
	PresharedKey        string `json:"psk_key"`
	ServerPublicKey     string `json:"server_pub_key"`

	Jc   string `json:"Jc,omitempty"`
	Jmin string `json:"Jmin,omitempty"`
	Jmax string `json:"Jmax,omitempty"`
	S1   string `json:"S1,omitempty"`
	S2   string `json:"S2,omitempty"`
	S3   string `json:"S3,omitempty"`
	S4   string `json:"S4,omitempty"`
	H1   string `json:"H1,omitempty"`
	H2   string `json:"H2,omitempty"`
	H3   string `json:"H3,omitempty"`
	H4   string `json:"H4,omitempty"`
	I1   string `json:"I1,omitempty"`
	I2   string `json:"I2,omitempty"`
	I3   string `json:"I3,omitempty"`
	I4   string `json:"I4,omitempty"`
	I5   string `json:"I5,omitempty"`
}
```

## Field mapping

Use state and rendered config as source of truth, not `.env`.

| AmneziaVPN field | awg-forge source |
|---|---|
| `description` | client name |
| `hostName` | tunnel endpoint host: tunnel `ServerHost` override, otherwise state `ServerHost` |
| `port` | tunnel listen port as string |
| `transport_proto` | constant `udp` |
| `container` | constant `amnezia-awg` |
| `defaultContainer` | constant `amnezia-awg` |
| `isThirdPartyConfig` | constant `true` |
| `config` | rendered full client `.conf` |
| `client_priv_key` | client private key |
| `client_ip` | client address as rendered/allocated; prefer CIDR form if the rendered config uses CIDR |
| `psk_key` | client preshared key |
| `server_pub_key` | server public key |
| `allowed_ips` | tunnel/client allowed IPs used in rendered config |
| `persistent_keep_alive` | tunnel persistent keepalive as string |
| `mtu` | tunnel MTU as string when configured; omit or `"0"` only if reference/manual testing confirms |
| `dns1`, `dns2` | first two IPv4 DNS values from tunnel DNS |
| `Jc`, `Jmin`, `Jmax`, `S*`, `H*`, `I*` | tunnel protocol params |

DNS handling:

- Match the reference behavior: export IPv4 DNS values only.
- Use first IPv4 as `dns1`.
- Use second IPv4 as `dns2`.
- If only one DNS value exists, omit `dns2`.
- Do not add IPv6 DNS until full IPv6 egress support exists.

Protocol version:

- For AWG 2.0, use `"2"`.
- The reference uses presence of `S3`/`S4` as the signal for version 2.
- For Legacy/1.0 and 1.5, preserve an explicit implementation decision in tests. Preferred first implementation: set `"1"` for non-2.0 profiles unless manual AmneziaVPN testing proves the field must be omitted or set differently.
- Do not infer protocol version from UI labels. Use protocol profile ID or validated protocol params.

## Binary wrapper format

After marshaling the outer JSON:

1. Convert JSON string to UTF-8 bytes.
2. Compress bytes with zlib.
3. Allocate final byte slice with `12 + len(compressed)` bytes.
4. Write 12-byte big-endian header:

```text
[0..3]   uint32 magic/version       0x07C00100
[4..7]   uint32 compressed_len + 4
[8..11]  uint32 uncompressed_len
[12..]   zlib compressed bytes
```

5. Base64url encode the final bytes.

Use Go standard library:

- `compress/zlib`
- `encoding/base64`
- `encoding/binary`

Use `base64.RawURLEncoding` unless testing against AmneziaVPN proves padded URL encoding is required. The reference calls this base64url; URL-safe no-padding is the safest default for QR/import strings.

Pseudo-code:

```go
func buildAmneziaVPNQRPack(jsonBytes []byte) (string, error) {
	compressed := zlibCompress(jsonBytes)

	buf := make([]byte, 12+len(compressed))
	binary.BigEndian.PutUint32(buf[0:4], 0x07C00100)
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(compressed)+4))
	binary.BigEndian.PutUint32(buf[8:12], uint32(len(jsonBytes)))
	copy(buf[12:], compressed)

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
```

## QR generation strategy

Use the existing QR image renderer for the final base64url payload.

Do not introduce a new QR rendering dependency for this task unless the current renderer cannot encode the payload. The problem is the payload format, not PNG rendering.

Expected first version:

- One AmneziaVPN QR image per client.
- Existing `/api/clients/{id}/amnezia-vpn-qr-series` may return `{ "chunks": 1 }` to preserve UI contract.
- Remove or disable old custom chunking for AmneziaVPN QR unless a verified AmneziaVPN multi-QR protocol is found.

If the payload is too large for one QR:

- Return a clear error from the AmneziaVPN QR endpoint.
- Do not invent chunking.
- Keep `.conf` download and `vpn://` copy available.
- Later research AmneziaVPN's real multi-QR sequence format separately.

## Proposed backend changes

Preferred files:

- Add `internal/server/amneziavpn_qr.go`
- Add `internal/server/amneziavpn_qr_test.go`
- Update `internal/server/server.go` only where handlers need to call the new builder.
- Add a service method in `internal/app/clients.go` only if existing methods cannot return enough tunnel/client context.

Avoid putting all logic directly into HTTP handlers.

Suggested internal flow:

```text
HTTP handler
  -> service returns client export context:
       state/tunnel/client/rendered conf/server public key/endpoint fields
  -> buildAmneziaVPNClientConfig(context)
  -> buildAmneziaVPNQRPack(json)
  -> writeQRCodePNG(payload)
```

Suggested service DTO:

```go
type ClientExportContext struct {
	State        config.State
	Tunnel       config.Tunnel
	Client       config.Client
	RenderedConf string
}
```

If returning full `State` is too broad, use a narrower DTO with only required fields. The important rule is that HTTP handlers must not reconstruct business logic by reading storage directly.

## Proposed UI changes

Current UI already has the right conceptual layout:

- AmneziaVPN QR block;
- AmneziaWG QR block;
- import options block with `.conf` and `vpn://`.

Keep that split.

Required behavior:

- AmneziaVPN QR block uses `/api/clients/{id}/amnezia-vpn-qr`.
- AmneziaWG QR block uses `/api/clients/{id}/qr`.
- `.conf` button uses existing config download URL.
- `vpn://` copy stays in the import options block, not below QR blocks.
- If series endpoint returns `1`, hide or disable previous/next controls.
- If a future verified series format returns `>1`, allow:
  - previous/next navigation;
  - downloading the active QR;
  - opening the active QR in a larger lightbox;
  - navigating inside the lightbox.

Known UI bug to avoid:

- Do not reset `vpnQRChunk` to `0` on every render or image load.
- Reset QR index only when `client.id` changes or when the number of chunks becomes smaller than the current index.

## Security requirements

AmneziaVPN QR payload contains client secrets:

- client private key;
- preshared key;
- full rendered client config;
- server public endpoint metadata.

Rules:

- Do not log QR payloads.
- Do not log packed base64url strings.
- Do not include QR payloads in audit logs.
- Do not include QR payloads in support bundles.
- Use `Cache-Control: no-store`.
- Keep route authenticated.
- Keep origin/session protections unchanged.
- Do not add debug endpoints returning raw payload unless explicitly gated for local tests and never enabled in production.

Audit logging may record only non-secret metadata:

- event name, e.g. `client.amneziavpn_qr.downloaded`;
- tunnel/interface;
- client name or id;
- chunk number only if a verified multi-QR mode exists.

## Tests

Add unit tests for QR payload builder.

Required tests:

1. `buildAmneziaVPNQRPack` writes correct magic:
   - base64url decode;
   - assert first uint32 is `0x07C00100`.

2. Header lengths are correct:
   - second uint32 is `len(zlibBytes)+4`;
   - third uint32 is `len(originalJSONBytes)`.

3. Wrapped payload decompresses:
   - strip first 12 bytes;
   - zlib decompress;
   - JSON equals original bytes or parses into expected struct.

4. `last_config` is a string:
   - parse outer JSON;
   - assert `containers[0].awg.last_config` JSON type is string;
   - parse that string separately.

5. Required Amnezia fields exist:
   - `containers`;
   - `defaultContainer`;
   - `description`;
   - `hostName`;
   - `awg.isThirdPartyConfig`;
   - `awg.port`;
   - `awg.transport_proto`;
   - `awg.protocol_version`.

6. AWG 2.0 params are included:
   - `S3`, `S4`;
   - H ranges;
   - `I1-I5` when non-empty.

7. Empty optional params are omitted:
   - no `I2: ""`;
   - no `I3: ""`;
   - no `I4: ""`;
   - no `I5: ""`.

8. Existing AmneziaWG QR handler remains unchanged at routing level:
   - `/api/clients/{id}/qr` still renders QR from raw config.
   - This can be a smoke test or explicit route test if current server tests support it.

Manual tests:

1. Create AWG 2.0 client.
2. Open client config modal.
3. Scan AmneziaVPN QR with current AmneziaVPN iOS app.
4. Confirm AmneziaVPN imports client.
5. Confirm imported values:
   - endpoint host;
   - port;
   - DNS;
   - allowed IPs;
   - client address;
   - protocol params.
6. Scan AmneziaWG QR with AmneziaWG-compatible app.
7. Download `.conf` and import manually.

Regression checks:

```bash
go test ./...
npm run ui:lint
npm run ui:build
make ci
```

Use the checks that exist in the current branch. Do not add new toolchains just for this QR task.

## Implementation sequence

1. Inspect current backend QR handlers and client config export service.
2. Add typed AmneziaVPN QR structs and packer in a separate backend file.
3. Add unit tests for packer and JSON shape.
4. Add or extend service method to return the exact tunnel/client/rendered config context.
5. Change only `/api/clients/{id}/amnezia-vpn-qr` to use the new AmneziaVPN payload.
6. Change `/api/clients/{id}/amnezia-vpn-qr-series` to return one chunk unless a verified multi-QR protocol is implemented.
7. Keep `/api/clients/{id}/qr` untouched.
8. Run Go tests.
9. If UI needs adjustment, edit `web/src/main.tsx` and `web/src/api.ts` minimally.
10. Run UI checks/build.
11. Verify with AmneziaVPN app.
12. Update `CHANGELOG.md` only after manual import works.

## Definition of Done

The task is done when:

- AmneziaVPN QR is generated from the JSON + qCompress-like wrapper format.
- AmneziaVPN app imports a generated client using that QR.
- Existing AmneziaWG raw `.conf` QR still works.
- Existing `.conf` download still works.
- Existing `vpn://` copy still works.
- No secrets are logged.
- Tests cover packer/header/decompression/JSON shape.
- UI clearly separates AmneziaVPN QR, AmneziaWG QR, and import options.
- `make ci` or equivalent current project checks pass.

## Open questions for implementation

These should be resolved by tests against real AmneziaVPN clients:

1. Non-2.0 `protocol_version` value:
   - reference strongly indicates `"2"` for S3/S4;
   - first implementation should use `"2"` for AWG 2.0 and `"1"` for non-2.0;
   - adjust only if real AmneziaVPN import requires different behavior.

2. `mtu` representation:
   - if tunnel MTU is configured, use that value;
   - if MTU is auto/zero, either omit `mtu` or set `"0"`;
   - choose based on reference behavior and manual import results.

3. Multi-QR support:
   - do not keep the old custom chunking as if it were proven;
   - implement one QR first;
   - if one QR exceeds capacity, research AmneziaVPN's actual multi-QR format separately.

## New-chat resume prompt

Use this prompt in a new session:

```text
Open /Users/astronaut/Develop/awg-forge.
Read docs/plans/amneziavpn-qr-plan.md.
Implement ONLY AmneziaVPN-compatible QR export according to that plan.
Do not change existing AmneziaWG/raw .conf QR, .conf export, vpn:// export, protocol generation, install.sh, or unrelated docs.
After implementation, run relevant tests and give changed files + commit message.
```
