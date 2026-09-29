# Next checkpoint: prepared controller control identity

Status: historical checkpoint, merged into `develop` in PR #106. This is
a bounded follow-up to `control-tls-pki-plan.md`, not an enabled control-plane
feature. The starting point below describes `develop` before this checkpoint
on 2026-09-26.

## Outcome and starting point

The previous controller-auth and backup work is on `develop`: controller state
has `controller_id`, encrypted controller backups contain the authentication
key and a consistent SQLite snapshot, cold restore fences the controller ID,
and a durable marker blocks startup after an incomplete restore. There is no
control CA, node certificate registry, `/control/v1` listener, or enrollment.

Deliver **one reviewable feature branch from current `develop`** that can
prepare and validate a disabled control CA and server identity for an existing
controller, and can back up and restore that exact identity. The preparation
operation belongs to `internal/app`; its public entry point and actual listener
enablement are later checkpoints. No automatic install, upgrade, startup, or
controller-auth activation may call preparation. Keep this branch's control
listener permanently absent; `enabled` must remain false.

The CA and server identity must join encrypted controller backup/restore in
the same PR. Splitting those changes would leave a newly prepared identity
outside the recovery path. Small, separately reviewable commits are fine only
with the user's confirmation; never commit, push, create a PR, or merge on the
user's behalf without explicit authorization.

## Architecture decisions for this checkpoint

| Area | Decision and reason |
| --- | --- |
| Ownership | `internal/controlpki` owns generation, parsing, and validation; `internal/storage` owns private atomic persistence and the recovery journal; `internal/app` owns the mutation and state transition. Do not add another service, DI framework, or generic PKI abstraction. |
| Authority | Add an optional control section under `config.ControllerState`. `nil` means absent; a non-nil section with `enabled: false` means prepared. The secret-free state stores the bind IP, advertised DNS name or IP, port, CA generation, server generation, and public CA SPKI pin. The journal alone represents preparation in progress. Do not increment tunnel/client `ConfigRevision`. |
| Endpoint | The request provides a literal bind IP, one advertised DNS name or IP, and a TCP port. Normalize and validate once before generating keys. Reject URL syntax, wildcard or unspecified advertised names/addresses, ambiguous DNS, zones, empty values, and port conflicts with the existing Web UI and ACME HTTP listener. Reject wildcard bind IPs in this checkpoint; later exposure policy may revisit them. Neither a successful bind nor public reachability is promised during preparation. |
| Crypto | Use Go standard-library Ed25519 and X.509. Generate positive random serials. The CA is a CA with valid basic constraints and `keyCertSign`, no intermediate delegation; the server leaf is not a CA, has digital-signature usage, server-auth EKU, and exactly the configured DNS or IP SAN. Use the existing design lifetimes of five years for the CA and 30 days for the server leaf, with an injected clock for tests. CN is never a hostname authority. |
| Pin | SHA-256 of the CA certificate's DER SubjectPublicKeyInfo, encoded as `sha256:` plus 64 lowercase hexadecimal characters. Store only this public pin in state. Future join and node state must use the same format; do not substitute the certificate's DER fingerprint. |
| Files | Put separate immutable CA and server generations under `CONFIG_DIR/control/`, with random validated generation IDs. Use `0700` directories and `0600` regular key/certificate files; reject symlinks in every component. Use exclusive creation, checked file and directory sync, and atomic publication. Never overwrite the last committed generation or delete a path that was not created by the current preparation. |
| Existing behavior | The Web UI/ACME TLS tree, browser authentication, tunnel state, SQLite auth schema, Docker image, install/upgrade scripts, and current APIs stay as they are. No new route, UI action, environment enable switch, node certificate, or SQLite registry is needed in this PR. |

The optional state field is backward compatible: existing standalone, node,
DB-off, and controller state files without it keep their meaning. Determine
whether the current state-schema convention needs a version bump by inspecting
existing migrations; do not bump solely to advertise an optional field.

## Transaction and failure behavior

Implement a single `PrepareControlIdentity` application operation, with a
request value for the endpoint and a public, secret-free result. It acquires
the existing state mutation lock, loads an active controller state, requires
SQLite and initialized controller authentication, and rejects restore or
desired-state journals before changing anything. An exact retry against an
already prepared identity first validates the committed files and then returns
its existing public summary without regenerating keys; a different endpoint
conflicts until an explicit future change operation exists.

1. Validate the endpoint, current state, paths, and absence of conflicting
   journals. Generate random CA/server generation IDs.
2. **Durably write a secret-free preparation journal before creating the first
   identity file.** It contains only controller ID, generation IDs, and phase
   metadata needed for cleanup. This corrects the ambiguous ordering in the
   parent plan: staging before a durable journal could orphan a private key.
3. Generate the CA and server key/certificate pairs. Write and sync only their
   newly owned generation directories. Parse them back from disk and verify
   CA key match and constraints, server key match, chain, validity, exact SAN,
   EKU, and SPKI pin with a private root pool and an injected time.
4. Atomically save the optional prepared control section in `state.json` with
   `enabled: false`. No socket is opened. Sync the state directory and then
   delete/sync the journal. A journal-cleanup failure is visible but must not
   cause a second CA to be generated on retry.
5. On restart, compare any journal to state under the mutation lock. If state
   does not reference its generations, clean up **only** those journal-owned
   staged files; if state references them, verify them and clear the journal.
   If inspection or cleanup is uncertain, retain the journal and deny further
   preparation/backup of the incomplete identity. Do not regenerate silently.

Loading a committed prepared identity must validate it without modifying it.
Missing, corrupt, expired, mismatched, overly permissive, or symlinked control
files make control identity unusable and produce a redacted diagnostic. They
must never create replacement keys. Because no control listener exists in this
checkpoint, the browser and local tunnel runtime must remain available for
operator repair when only control identity is broken. Keep the control failure
separate from the existing controller-auth and restore-pending startup gates.

## Backup and restore integration in the same PR

- While holding the existing mutation lock, include only the CA and server
  generation files referenced by committed state. Reject an in-progress
  preparation journal. Do not archive orphan generations, journal contents,
  or private files through a broad `control/` walk.
- Extend the controller archive allowlist and validation conditionally: a
  legacy controller state without a control section remains valid; a prepared
  state requires the exact referenced CA/server files, private modes, key
  matches, chain constraints, SAN, and pin. Reject extra or mismatched control
  entries. An expired but internally consistent identity remains recoverable:
  restore it with control disabled and report that it cannot be used until an
  explicit future renewal/rotation. Do not make certificate expiry a reason to
  lose the rest of a controller backup. Retain the existing 64 MiB size bounds
  and encrypt-then-verify publication contract.
- Cold same-ID restore must stage those files, validate the committed identity
  and SQLite/auth state, reset browser credentials, sync all restored paths,
  and only then clear `restore-pending`. Restore never enables a control
  listener. Preserve backup history and audit logs as the current mover does.
- A backup made before preparation may restore the controller to the absent
  control state in this checkpoint because no node can enroll yet. Revisit
  certificate rollback and re-enrollment **before** issuing node certificates;
  do not infer a safe future fleet policy from this temporary condition.
- Keep the current fail-closed marker on interrupted restore. A supported
  operator recovery procedure remains a separate operational readiness task;
  do not add an unverified automatic `resume` in this PR.

## Execution order and observable gates

Use the project `awg-forge-pro` skill and route Go work through
`golang-skill-router` before implementation. Apply the selected Go design,
security, and testing guidance to the existing code, and use
`api-security-hardening` only for trust-boundary checks relevant to this
checkpoint; it does not justify adding an API here. Inspect current symbols,
callers, and focused tests before each edit. Prefer targeted `rg`/source reads,
Go package tests, and the repository `make` gates; record exact exit status and
relevant diagnostics. An independent reviewer should examine the final
security-sensitive diff before handoff. Do not use upstream news as a reason to
add AWG protocol or client configuration changes to this branch.

1. **Preflight.** Confirm `develop` is at the intended base and the backup
   foundation is merged; inspect status before touching files. Read
   `AGENTS.md`, this document, the parent plan, current `ControllerState`,
   activation journal, storage atomic writes, and backup/restore tests. If
   these two design documents are still uncommitted in the same checkout,
   carry them intact onto the feature branch. Preserve any other user changes
   and resolve the base before editing. Never discard local plan edits to get
   a clean checkout.
2. **Contract tests first.** Test endpoint parsing, legacy state compatibility,
   repeat preparation, conflict behavior, and that ordinary `Init`/`serve`
   never generate a CA. Use focused tests rather than broad refactoring.
3. **PKI core.** Implement generation and load/verify with no filesystem side
   effects during load. Test wrong SAN, pin, chain, key, CA/leaf usage, expiry,
   serial uniqueness, and `0600`/`0700` requirements. Use the standard
   library; add a dependency only for a concrete gap.
4. **Durable preparation.** Add journal, app transition, and restart handling.
   Inject failure before file creation, after each critical file/directory
   sync, before state commit, after state commit, and during journal cleanup.
   Assert no unreferenced identity becomes active and browser/tunnels remain
   available when only optional control identity fails.
5. **Recovery coverage.** Extend `Create`, `Verify`, and cold `Restore` for the
   referenced generations. Test complete round trip, missing/corrupt/symlinked
   and extra archive paths, expired but consistent identity, legacy controller
   archive, size bounds, same-ID
   fencing, stopped-server requirement, interrupted restore marker, auth reset,
   and absence of secrets in API/audit/Doctor/support-bundle outputs.
6. **Documentation and review.** Update this design if an evidence-backed
   choice changes. Update English and Russian operator docs together only
   where behavior becomes user-visible. Inspect the complete diff and obtain
   an independent security review of persistence, trust, backup, and crash
   boundaries before declaring the checkpoint ready.

Required checks for the exact final tree: focused package tests while editing;
`make ci`, `make quality`, `make security-fast`, and `go test -race ./...` before
handoff. Use `make security` before a release if scanner data is available.
Build the Docker image and run host-network smoke tests only if packaging or
runtime binding changes; neither is expected here. Report unavailable gates
as unverified, with saved diagnostics. Do not claim a pass from truncated
output. Do not commit without the user's confirmation.

**Stop and re-evaluate** if implementation requires a public route, live
listener, node registry, new storage system, automatic CA rotation, cross-host
restore, or a broad rewrite of `internal/app`/`internal/storage`. Record the
specific requirement and evidence before expanding scope. No master branch,
floating dependency, protocol syntax, IPv6 egress, or unrelated AWG upstream
compatibility change belongs in this checkpoint.

## After this checkpoint

The loopback-only dedicated TLS runtime was merged in PR #107, and the
[node certificate registry](control-node-certificate-registry.md) in PR #110.
The next checkpoint is [internal certificate renewal](control-node-renewal-next-session.md).
Rebind, enrollment, and non-loopback enablement need their own reviewed
steps. No non-loopback control listener is enabled before an enrollment route,
per-request registry authorization, recoverable PKI backup, and the
restore/revocation decision are implemented and tested.

Go's [X.509 documentation](https://pkg.go.dev/crypto/x509) specifies SAN,
verification options, CA constraints, and key usages. The [TLS documentation](https://pkg.go.dev/crypto/tls)
defines client-certificate verification modes for the later listener work.
