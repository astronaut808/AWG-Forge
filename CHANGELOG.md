# Changelog

## v0.2.0 - 2026-05-18

AWG 2.0 release.

### Added

- AWG 2.0 profile support with `S3/S4`, ranged `H1-H4`, `I1-I5`, validation, and golden tests.
- AWG 2.0 tunnel creation through the existing multi-profile UI.
- AWG 2.0 `.conf` import validated on desktop and iOS clients with compatible AmneziaVPN builds.
- Doctor runtime diagnostics for tunnel links, `awg show` port matching, runtime peers, stale client configs, handshakes, and transfer counters.

### Notes

- `.conf` remains the recommended import path.
- QR import remains experimental until native Amnezia QR schema is verified across platforms.

## v0.1.0 - 2026-05-18

Initial public release of awg-forge.

### Added

- Go backend and CLI under module `github.com/astronaut808/awg-forge`.
- Static HTML/CSS/JavaScript Web UI for managing tunnels and clients.
- Multi-profile tunnel model with parallel tunnels for:
  - AmneziaWG Legacy / 1.0;
  - AmneziaWG 1.5-oriented profile;
  - AmneziaWG 2.0 placeholder for future work.
- Client lifecycle:
  - create clients;
  - delete clients;
  - enable and disable clients;
  - download `.conf` files;
  - show AmneziaVPN QR import codes.
- Protocol parameter generation and validation with non-zero obfuscation defaults.
- AWG 1.5 client-side `I1-I5` signature packet defaults.
- Tunnel settings for port, subnet, DNS, allowed IPs, keepalive, MTU, and enabled state.
- Config rendering for server and client files with private output permissions.
- JSON state storage with rendered tunnel/client config files.
- Docker image workflow for `linux/amd64`.
- Host-network and bridge-network Docker Compose examples.
- Doctor command and UI view for runtime checks.
- Login, session cookies, CSRF-style Origin/Host checks, security headers, and rate limiting.
- Idempotency keys for mutating UI requests to prevent duplicate creates on double-click.
- Stale-client indicators when tunnel settings or protocol params require fresh client configs.
- State backups before tunnel deletion and protocol auto-repair.

### Changed

- UI sessions expire after 30 minutes.
- Client configs render interface addresses with `/32`.
- Protocol validation now rejects out-of-range values such as `Jc > 10` and `S1/S2 > 64`.
- Existing states with older out-of-range protocol params are repaired on startup and backed up.

### Notes

- `.conf` file import is the recommended client import path.
- QR import is experimental on AmneziaVPN for iOS. If a QR-imported profile appears connected but does not start the iOS system VPN tunnel, delete it and import the `.conf` file instead.
- AmneziaWG 2.0 is not implemented in this release.
