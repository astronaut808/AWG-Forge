# Changelog

## v0.8.0 - 2026-05-24

### Added

- Added Maintenance Center 2.0 with tabbed Overview, Doctor, Firewall, Backup, Restore, Updates, Support, and System sections.
- Added Web UI restore verification for encrypted `.afbackup` files as a dry-run that validates backups without writing to `CONFIG_DIR`.
- Added `/api/restore/verify` for authenticated restore dry-runs from the Web UI.
- Added tests for restore verify API success and wrong backup password handling.

### Changed

- Reworked Maintenance UI from separate cards into a single operations center.
- Firewall repair, backup download, support bundle download, update checks, and restore guidance are now grouped under Maintenance.

### Security

- Added `Cache-Control: no-store` hardening for backup, support bundle, and restore verify responses.
- Restore verification upload is size-limited and uses a temporary file that is removed after validation.
- Limited JSON API request bodies to reduce accidental or malicious memory pressure.
- Backup restore/verify now rejects oversized encrypted backup files and unsupported KDF parameters before decryption work.
- Updated `golang.org/x/crypto` to remove known vulnerable module findings from dependency scanning.

## v0.7.0 - 2026-05-24

### Added

- Added an authenticated experimental `Import key` action for clients.
- Added `/api/clients/<id>/import-key`, returning a `vpn://` text key generated from the rendered client `.conf` for AmneziaVPN / DefaultVPN compatibility testing.
- Added tests for import key generation and AWG 2.0 import key API output.
- Added English and Russian AmneziaVPN import/subscription research notes.

### Changed

- Documented `.conf` download as the stable production import path and `Import key` as an experimental compatibility path.
- Updated quick install documentation to recommend downloading `install.sh` before running it with sudo for more reliable interactive prompts.

### Security

- Added `Cache-Control: no-store` hardening for rendered client `.conf` downloads and import key JSON responses.
- Import keys remain authenticated-only and contain the same client secrets as `.conf` files; they are not public subscription links.

## v0.6.0 - 2026-05-22

### Added

- Added `awg-forge restore verify <backup.afbackup>` as a safe restore dry-run command.
- Restore verification decrypts encrypted backups, validates metadata, schema versions, file checksums, archive paths, state sanity, and server/client config rendering without writing to `CONFIG_DIR`.
- Restore verification prints a redacted backup summary with format, schema, file count, tunnel count, client count, server host, and per-tunnel profile/port/subnet information.
- Added tests for successful restore verification, wrong backup passwords, no-write dry-run behavior, and duplicated tunnel listen port detection.

### Changed

- Restore now shares the same decrypt/read/validate path as restore verification before replacing the config directory.
- Documentation now recommends running `restore verify` before `restore` in both Docker and local CLI workflows.

### Security

- Restore verification never prints private keys, preshared keys, session secrets, or rendered client configs.
- Backup validation now rejects duplicate tunnel IDs, names, interfaces, listen ports, subnets, client IDs, invalid tunnel subnets, and invalid client IP addresses before restore.

## v0.5.0 - 2026-05-21

### Added

- Added Support bundle generation from CLI with `awg-forge support-bundle [output.zip]`.
- Added authenticated Web UI support bundle download via `/api/support-bundle`.
- Support bundles include redacted config/state summaries, Doctor results, runtime command output, and config directory file inventory without rendered config contents.
- Added tests to ensure support bundles do not leak private keys, preshared keys, passwords, or session secrets.
- Added encrypted backups with `awg-forge backup [output.afbackup]` using a separate `BACKUP_PASSWORD`.
- Added safe CLI restore with `awg-forge restore <backup.afbackup>`, password verification, schema checks, checksum checks, and a pre-restore encrypted backup.
- Added Web UI encrypted backup download with a dedicated backup password prompt.
- Added `awg-forge firewall check` and `awg-forge firewall repair` for manual runtime firewall reconciliation.
- Added Web UI firewall repair action inside the Doctor modal.
- Added shared firewall rule modeling and tests for expected rules, missing rules, duplicates, and disabled tunnel handling.
- Added Web UI Maintenance hub for Doctor, firewall repair, encrypted backups, support bundles, update checks, and CLI-only restore guidance.
- Added self-contained interactive `install.sh` quick installer for Linux/VPS Docker host-network setup.
- Added MIT license, contributing guide, security policy, and Dependabot config for public repository readiness.
- Added `uninstall.sh` to remove runtime interfaces, managed firewall rules, containers, and optionally local install files.
- Added per-tunnel `Server host` override in Web UI settings for client config endpoints.

### Changed

- Runtime tunnel apply now uses the same firewall repair logic as manual maintenance.
- Doctor firewall diagnostics now point missing and duplicate managed rules to `awg-forge firewall repair`.
- Topbar maintenance actions are grouped under `Maintenance`, and primary buttons use a calmer hover/border treatment.
- Documentation now describes Maintenance hub actions instead of the old separate topbar maintenance buttons.
- Tunnel endpoint cards now show whether the host is inherited from global `SERVER_HOST` or customized per tunnel.
- `.env.example` now includes `SESSION_SECRET` so generated installs can keep stable UI sessions explicitly.
- `.gitignore` and `.dockerignore` now exclude local env files, backups, configs, and support archives.
- The quick installer now detects stale AWG-like interfaces before startup and recreates the container when applying a new `.env`.
- Changing global `SERVER_HOST` now refreshes inherited tunnel endpoints while preserving tunnels with explicit host overrides.
- Backup restore path validation is hardened against archive traversal paths.

## v0.4.0 - 2026-05-20

Runtime safety, subnet correctness, and manual AmneziaWG update checks.

### Added

- Added reproducible AmneziaWG tool pinning through `build/amneziawg.refs`.
- Added `awg-forge updates` to compare bundled AmneziaWG refs with upstream GitHub commits.
- Added Web UI `Updates` modal for checking bundled AmneziaWG component status.
- Added `/api/updates` for authenticated UI update checks.
- Added `Makefile` helpers for local and Docker workflows:
  - `make updates-local`;
  - `make updates-docker`;
  - `make update-amneziawg-refs`;
  - `make docker-build`;
  - `make ci`.
- Added `scripts/update-amneziawg-refs.sh` to update pinned AmneziaWG refs for manual PR-based upgrades.
- Added build metadata for awg-forge version, awg-forge commit, pinned `amneziawg-go`, and pinned `amneziawg-tools`.
- Added Russian and English README entrypoints, plus split Russian and English docs for setup, configuration, usage, diagnostics, updates, development, and security.
- Added tests for non-`/24` subnet allocation and rendering across Legacy / 1.0, 1.5, and 2.0.
- Added tests for apply failure rollback and stricter Origin/Host validation.

### Changed

- Docker builds now fetch pinned AmneziaWG commits instead of cloning floating upstream `HEAD`.
- Docker image labels and environment metadata now expose awg-forge and pinned AmneziaWG build refs.
- GitHub Docker workflow passes awg-forge version and commit metadata into image builds.
- Server configs now render the actual tunnel subnet prefix instead of hardcoding `/24`.
- Tunnel subnet input is normalized to canonical IPv4 CIDR form.
- Client IP allocation now works across supported IPv4 CIDR sizes, not only the last octet of `/24`.
- Supported tunnel subnet sizes are constrained to `/16` through `/30` to avoid accidental huge allocations or unusable networks.
- Apply failures for mutating operations now roll back state and rendered configs so the UI does not show changes that failed to apply.
- Runtime apply errors now return server errors for mutating API calls instead of looking like validation failures.
- Web UI closes the active dialog and refreshes state after apply failures so runtime errors are visible.
- Web UI now automatically starts the `.conf` download after a client is created successfully.
- Removed experimental QR generation, QR API routes, and QR actions from the Web UI. `.conf` download is now the only supported client import path.
- Origin/Host validation is stricter:
  - same-origin public requests are allowed;
  - localhost and SSH tunnel usage remain supported;
  - opaque origins such as `null` and browser-extension origins are rejected for mutating requests;
  - POST requests without Origin/Referer are only allowed for loopback hosts.

### Notes

- awg-forge only detects AmneziaWG upstream updates. It never updates tools inside a running container.
- AmneziaWG upgrades remain manual: update pinned refs, rebuild the Docker image, test real tunnels/clients, then release a new awg-forge image.

## v0.3.1 - 2026-05-19

Web UI typography and diagnostics polish.

### Added

- Bundled local JetBrains Mono webfont assets for offline-safe Web UI rendering in Docker and embedded deployments.
- Added `@font-face` definitions for JetBrains Mono Regular, Medium, SemiBold, and Bold.
- Added monospace diagnostic message styling for Doctor output.
- Added reusable `code`, `pre`, `kbd`, `samp`, and `.mono` typography rules for config-like values and diagnostic text.

### Changed

- Updated the UI monospace stack to prefer local JetBrains Mono before system monospace fallbacks.
- Improved readability of endpoints, subnets, DNS values, MTU values, interfaces, client addresses, and Doctor messages.
- Rendered Doctor result messages as compact diagnostic blocks with preserved line breaks and better wrapping.
- Improved modal scrolling behavior on smaller screens.
- Improved toast animation by replacing `display` toggling with opacity and transform transitions.
- Disabled monospace ligatures for `.mono` values so IPs, ports, subnets, and config fragments remain visually exact.

### Notes

- No backend routes, API payloads, storage format, tunnel rendering, protocol generation, or firewall behavior changed in this release.
- JetBrains Mono is served locally from `/static/fonts/` and does not require CDN access.

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
- Limited tunnel cards to two columns on wide screens, expanded single-tunnel views to full width, and used one column on narrower screens for better readability.
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
