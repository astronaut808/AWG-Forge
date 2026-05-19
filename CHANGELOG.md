# Changelog

## v0.3.0 - 2026-05-19

Web UI refresh.

### Added

- New polished Web UI visual system with glass-style topbar, profile tabs, panels, dialogs, and toast surfaces.
- Inline awg-forge shield mark in the login and dashboard headers.
- Topbar theme toggle with sun/moon icons and persisted light/dark theme selection.
- Subtle pointer parallax for background lighting, grid layers, and major UI surfaces on desktop pointer devices.
- Light/dark theme tokens with semantic colors, focus rings, and reduced-transparency fallbacks.
- `prefers-reduced-motion` support that disables decorative motion and interaction scaling.
- Clearer status indicators for tunnel state, client enabled state, stale configs, and health values.

### Changed

- Improved dashboard spacing and layout rhythm so topbar, profile tabs, and content panels no longer overlap.
- Reworked responsive layout for mobile screens, including stacked toolbar actions, forms, tunnel facts, and client rows.
- Limited tunnel cards to two columns on wide screens and one column on narrower screens for better readability.
- Simplified empty tunnel states to avoid looking like drop zones and kept a single `Create tunnel` action per profile.
- Improved modal headers and close controls while preserving existing API behavior and form flows.
- Rendered endpoint, subnet, DNS, MTU, interface, and client address values with monospace styling for easier scanning.
- Improved login layout, tunnel cards, client list headers, QR panels, doctor output, and health panels.

### Notes

- No backend routes, API payloads, storage format, or protocol rendering behavior changed in this release.
- The frontend remains plain embedded HTML/CSS/JavaScript with no Node, npm, React, Vue, Tailwind, or build pipeline.

## v0.2.2 - 2026-05-19

Firewall reconciliation and client health diagnostics.

### Added

- Per-tunnel Web UI Health action that samples runtime client counters and reports handshake, rx/tx totals, rx/tx deltas, and connection status.
- Client health warnings for handshake-only connections and cases where clients send traffic but the server sends no bytes back.
- Idle clients with a fresh handshake are now reported as `idle, handshake ok` instead of a warning.
- Tiny client rx deltas below 1 KiB are treated as idle noise instead of NAT/forwarding failures.
- Doctor checks for per-tunnel `MASQUERADE` and `FORWARD` firewall rules.
- Tests for runtime transfer counter parsing and AWG 1.5 idempotent firewall rendering.

### Fixed

- Tunnel settings changes can no longer leave stale NAT/firewall rules from an older subnet, port, or interface.
- Firewall rules are now reconciled during apply/sync, including `awg syncconf` paths where `PostUp` is not re-run by `awg-quick`.
- `PostUp` rules are idempotent and inserted before UFW/Docker forwarding rejects.
- `PostDown` removes duplicate awg-forge firewall rules left by older versions.
- The firewall fix applies to Legacy / 1.0, 1.5, and 2.0 tunnels.

## v0.2.1 - 2026-05-18

### Added

- Doctor runtime diagnostics for tunnel links, `awg show` port matching, runtime peers, stale client configs, handshakes, and transfer counters.

## v0.2.0 - 2026-05-18

AWG 2.0 release.

### Added

- AWG 2.0 profile support with `S3/S4`, ranged `H1-H4`, `I1-I5`, validation, and golden tests.
- AWG 2.0 tunnel creation through the existing multi-profile UI.
- AWG 2.0 `.conf` import validated on desktop and iOS clients with compatible AmneziaVPN builds.

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
