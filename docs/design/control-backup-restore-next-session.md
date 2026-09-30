# Controller backup/restore and node revocation: next checkpoint

Status: internal implementation in `feature/control-backup-restore`, based on
`develop` at `9a213c8` on 2026-09-30; not committed or merged yet. The contract
below is implemented without a migration, archive-format change, control
enablement or new route. Final acceptance evidence is recorded below.

This follows [After this checkpoint](control-server-leaf-rotation-next-session.md#after-this-checkpoint)
and the [registry checkpoint](control-node-certificate-registry.md).
The [parent PKI plan](control-tls-pki-plan.md),
[security model](multi-node-security.md),
[failure matrix](multi-node-failure-matrix.md) and
[multi-node design](multi-node-v1.md#controller-authentication-and-recovery)
supply the surrounding constraints.

## Objective and boundary

Make encrypted controller create/verify/cold restore reconcile the archived
SQLite certificate registry with `controller_id` and the active immutable CA
and server generations. A snapshot taken before certificate or binding
revocation must never silently return that authority to service.

Use the conservative recovery policy already required by the handoff:
**every successful controller restore invalidates every restored node
certificate and current binding**, even if the archive is recent or all its
rows say active. Preserve the public registry history. Nodes require explicit
local recovery and fresh enrollment; no seamless reconnect is promised.
This checkpoint establishes the controller-side denial contract only. It does
not implement the future node-side recovery or enrollment workflow.

Keep control disabled. Do not add enablement, a production runtime owner,
scheduler, enrollment/renewal/rebind routes, CA rotation, endpoint changes,
browser API/UI, installer, AWG or tunnel work. Keep existing archive encryption,
limits and public report/payload shapes. Do not change `ConfigRevision` as a
side effect of security reconciliation. Ordinary restore still restores the
archive's tunnel state according to the existing contract.

## Baseline inspected before this checkpoint

| Baseline source | Baseline behavior | Work implemented in this checkpoint |
| --- | --- | --- |
| `internal/backup/backup.go`: `Create`, `createFromState`, `prepareControllerSnapshot` | Mutation lock, private `VACUUM INTO` snapshot, active CA/server files, encrypted archive verification before publication, 64 MiB bounds | Add semantic registry/PKI checks to the same create and verify path |
| `internal/backup/backup.go`: `validateControllerArchive` | Allowlisted paths, required auth keys/SQLite, PKI generation/pin/SAN/key validation | Tie public registry records to this controller and archived issuer |
| `internal/sqldb/snapshot.go`: `VerifyControllerSnapshot` | Private regular file, SQLite `quick_check`, exactly one administrator, schema version 5 through current | Version-aware schema/provenance and registry integrity validation; the current schema is 8 |
| `internal/backup/controller_restore.go`: `restoreControllerWithFiles`, `verifyRestoredController` | Both offline locks, same existing controller ID, verified encrypted pre-restore backup, durable restore-pending marker, internal/external DB installation, migration and browser-auth reset before marker clear | Atomically invalidate restored node authority together with browser credentials, then verify durable postconditions |
| `internal/sqldb/controller_auth.go`: `DisableControllerAuthAfterRestore` | Disables administrator and deletes sessions, recovery codes and auth attempts | Currently leaves node certificate/binding tables untouched |
| `internal/app/control_node_certificates.go` and `internal/sqldb/control_node_certificates.go` | Per-request issuer/serial/DER/key/binding/revocation checks; private issuance/renewal/rebind reject restore-pending | Prove denial after stale restore through these actual authorization and retry paths |
| `internal/storage/restore_pending.go`, `cmd/awg-forge/main.go`, `internal/app/init.go` | Any existing marker blocks startup, including malformed/symlinked markers; restore preserves it outside its move set | Preserve this crash barrier and extend its verified completion conditions |

Existing prepared/rotated-identity backup and restore interruption tests are
regression foundations. This checkpoint adds stale-authority and atomic-reset
tests plus semantic validation before publication and replacement.
`ValidateControlIdentityState` currently rejects `Enabled=true`; preserve that
boundary rather than introducing enabled-archive compatibility in this slice.

## Fixed recovery decisions

1. **No backup-local replay counter.** A generation, timestamp or epoch stored
   only in the archive or restored database rolls back with it. It cannot
   distinguish a stale snapshot from the latest authority. Do not invent a
   generic `control_epoch`, external service, ledger or new trust generation.
   Blanket invalidation needs no claim of retained post-backup revocation history.
2. **One transactional reset.** Extend the existing restore reset narrowly so
   disabling browser authentication, deleting its replay credentials, revoking
   all restored certificate rows and revoking all current binding rows commit
   in one SQLite transaction. Use a fixed, validated restore time for an attempt;
   set only missing revocation timestamps, preserving prior revocation and
   supersession evidence, DER/CSR/key hashes, predecessor links and epochs.
   An interrupted transaction changes none of those authorities. Repeating
   reconciliation must not clear a revocation or advance a binding epoch.
3. **Keep history and retries fenced.** Do not delete registry rows or recreate
   bindings at epoch 1. After administrator recovery, initial CSR retries and
   renewal must still deny the restored records. Even after an explicitly
   authorized future rebind, every archived serial remains revoked. New
   certificates must have a fresh key and require a separate recovery approval;
   the existing private rebind primitive is not such approval by itself.
4. **Respect unknown history.** A certificate issued after the snapshot is
   absent from the restored registry and must also be denied. Restored
   `binding_epoch` is not a fleet-wide high-water mark: the node may have a
   later epoch than the archive. Do not automatically invoke rebind with the
   archived tuple or promise cross-domain epoch continuity. That reconciliation
   belongs to the later locally authorized node recovery flow.
5. **Preserve trust material.** Restore exactly the archived controller ID,
   CA generation/pin and server generation. Add no CA replacement, signing or
   node private key to restore. Expired but internally consistent archived
   identities and expired public registry history remain recoverable with
   control closed; expiry never grants admission.
6. **Layer ownership.** Security recovery decisions and restored-state policy
   belong in `internal/app`; `internal/sqldb` owns the atomic reset and bounded
   integrity queries. Keep archive parsing/staging and existing cold file
   orchestration in `internal/backup`, durability/path primitives in
   `internal/storage`, and handlers thin. Reuse the already-held offline locks;
   do not call a helper that reacquires the mutation lock or broadly refactor
   restore to introduce an import cycle.

## Archive consistency contract

Validate the actual snapshot, not separately queried live registry rows. Use
bounded version-aware read-only checks before any target replacement. For
schemas 5/6/7, understand their exact available columns and constraints;
never migrate or alter the decrypted archive just to make verification pass.
Migration of the installed database remains under restore-pending.

- Verify the supported migration sequence/checksums against this binary and
  required version-specific tables, columns and constraints. Reject newer,
  malformed or incomplete schemas before target mutation. Preserve legitimate
  auth-only schema-5 archives and empty registries.
- Verify foreign-key relationships, canonical identities, positive epochs,
  certificate uniqueness, renewal predecessor references and version-specific
  uniqueness rules. Historical certificates may refer to an older binding
  epoch; do not confuse that with malformed active authority. Validate linked
  renewal identity/epoch and issuer consistency.
- For each certificate, parse bounded DER; verify its serial, full certificate
  and public-key hashes, stored validity interval and client-only profile
  against the record, plus its signature and lifetime bounds against the
  archived CA. Do not require historical certificates to be valid today.
  The CSR hash is opaque retry evidence: the archive does not contain CSR DER,
  so its original CSR cannot be reconstructed or independently revalidated.
- Binding and certificate controller IDs must agree with archive state.
  With no CA-rotation feature and only the active CA archived, a registry
  issuer generation without matching archived CA material is unsupported
  and rejected, including revoked history. A nonempty registry without
  prepared control identity is rejected. Do not guess or silently discard rows.
- Retain current allowlisted generation files, journal rejection, `0700`/
  `0600` modes, symlink/path checks, encrypted size limits and safe diagnostics.
  Do not expose DER, CSRs, key bytes, passwords or full configurations in
  errors, verify reports, logs, Doctor or support bundles.

Default to the existing state/archive/schema shapes: their revocation fields
can express this policy. Add a migration or format version only if a concrete
validation/recovery invariant cannot be represented; document that evidence
before expanding scope.

## Durable restore sequence

1. Decrypt and validate the combined archive without writing target authority.
   Acquire the existing state-directory and mutation locks; re-read the target,
   check same-controller identity, pending journals, external paths and archive
   location. Validate and save the encrypted pre-restore backup as today.
2. Persist and directory-sync restore-pending before moving any authoritative
   target file. Preserve it and the lock files outside the move set. Stage only
   private files; keep both internal and external SQLite paths supported.
3. Install the archived files/database while the marker keeps browser and
   control admission closed. Keep the existing state-last file move contract;
   do not describe it as a single atomic transaction across all files.
4. Revalidate the installed identity/PKI and supported snapshot, migrate the
   installed DB, then run the combined browser/node reset transaction. Check
   postconditions from a reopened database, including no unrevoked restored
   certificates or bindings, no sessions/codes and a disabled administrator.
5. Verify control is disabled, exact selected generations and expected private
   modes; sync restored files/directories and ensure SQLite/WAL durability with
   the existing full-synchronous configuration. An external DB requires its own
   parent-directory durability checks; syncing only the config directory is
   insufficient. Clear and directory-sync the marker only after all denial
   postconditions are durable. Return success only after this completion point.

Failures after marker creation require offline inspection unless a complete
rollback of original files and both database locations is verified and synced.
No new automatic resume or marker-removal command is part of this checkpoint.
If marker unlink succeeds but directory sync fails, do not assert the file is
still present: report an incomplete restore. Authority must already be durably
invalidated before unlink, so either crash outcome for the marker stays safe.
Test that distinction explicitly; no reset may be deferred until after unlink.

## Execution order and acceptance evidence

| Step | Work | Observable gate |
| --- | --- | --- |
| 1. Preflight | Re-read AGENTS, this plan and current source/tests; preserve user changes; use a feature branch for implementation | Scope remains internal; no production control listener/caller |
| 2. Lock policy in tests | Issue certificates in test fixtures; backup before serial/binding revoke, renewal or rebind; restore the stale encrypted archive | Every archived cert and post-snapshot cert denies; administrator recovery does not undo denial |
| 3. Snapshot validation | Add narrowly reusable version-aware schema and registry/CA validation | Create and verify reject inconsistent artifacts; restore rejects them before target files/marker change |
| 4. Transactional reset | Add browser/node reconciliation with failure injection and idempotent retry | Failure before commit preserves all rows; successful commit fences every restored binding and cert without deleting history |
| 5. Restore integration | Wire app policy into existing cold restore and post-install checks | Exact identity/generations, durable denial, no new revision changes, private files, same target-lock behavior |
| 6. Crash and transport evidence | Test both DB locations and marker boundaries; use isolated real loopback TLS/mTLS runtime | No partial authority, no restored serial admission, per-request denial on keep-alive, no handler invocation through denial |
| 7. Documentation/review/checks | Update design status/failure matrix, English/Russian recovery docs; independent security review and final checks | No unresolved material finding; evidence is measured on the final implementation tree |

Required failure cases:

- Backup before revocation: serial-only and binding-wide revocation; active
  renewal overlap and an already superseded predecessor; backup before rebind;
  unknown post-snapshot successor. Preserve valid backup records without
  granting them post-restore authority.
- Restore the same archive twice, and restore the encrypted pre-restore backup:
  each successful restore applies blanket invalidation again. Exact initial
  issuance/renewal retries cannot recover archived PEM as active credentials.
  Test private rebind only to prove an archived serial remains denied after a
  fresh certificate commits; expose no recovery flow.
- Corrupt or mismatched CA/pin, server pair, issuer generation, controller ID,
  serial/hash/DER/profile/validity, orphan binding/predecessor and bad migration
  provenance; schema-5 auth-only archives and legitimate schemas 6/7/8;
  missing SQLite/keys, rotated generations and expired consistent history.
- Fail before/after marker write and sync, each file/DB switch (including DB
  replaced before state), migration, each reset statement, transaction commit,
  reopened verification, file/parent sync, marker unlink and final directory
  sync. Exercise failed rollback, malformed/symlinked marker, permissions and
  pending preparation/rotation/desired-state/activation journals.
- Backup versus issuance/renewal/rebind and two services/processes versus cold
  restore; assert the existing mutation lock and SQLite snapshot consistency.
  Do not infer a replay fence from concurrency alone. A pending restore fences
  direct private mutations and startup, even without `Init` having run first.
- Standalone/DB-off and managed-node restore regressions, browser-session and
  used-code denial, no CA generation/enablement at startup, and seeded secrets
  absent from non-secret output. Encrypted backups intentionally contain the
  protected controller keys; do not assert their absence from decrypted archives.

Run focused checks during implementation in `internal/backup`, `internal/sqldb`,
`internal/app`, `internal/storage`, `internal/controlserver` and `cmd/awg-forge`.
Final implementation gates: `make ci`, `make quality`, `make security`,
`make test-race` and `git diff --check`. Inspect called scripts before execution;
preserve full large diagnostics in private unique logs. Docker build/host-network
smoke is required only if packaging/runtime binding changes; no such change is
planned. The final security gate requires the full rule set; `security-fast`
does not substitute for it.
An unavailable check is unverified, not passed.

During implementation, update `control-tls-pki-plan.md` to remove stale claims
that PKI files still need to join the archive, and keep the registry, security
model and failure matrix aligned. The multi-node design already conditions
seamless reconnect on a proven replay fence; preserve that qualification.
Update matching `docs/en` and `docs/ru` recovery sections when behavior changes.
No public API contract change is planned.

Completion requires tested archive consistency, durable blanket invalidation,
crash/rollback denial and independent review. It does not prove uniqueness of
cloned controllers, raw-filesystem rollback protection, monotonic node epochs
across stale restore, or a completed node recovery workflow. Only after this
gate may a separate checkpoint plan explicit loopback enablement; non-loopback
exposure still waits for authenticated enrollment and its failure gates.

## Implementation evidence

- `internal/sqldb/control_snapshot.go` verifies supported migration provenance,
  every schema object, references and registry certificates against archived CA.
  Unknown triggers are rejected even if their names begin with `sqlite_`.
- `internal/sqldb/controller_auth.go` resets browser and node authority in one
  transaction; `internal/app/controller_restore.go` owns reconciliation and
  reopened denial checks before the backup layer syncs and clears its marker.
- Tests cover internal/external SQLite, stale serial/binding revocation, renewal
  overlap, supersession and rebind, repeated/pre-restore archives, supported
  historical schemas, reset statement/commit failures, failed rollback and
  marker/file/DB durability boundaries. Isolated real mTLS proves denial on the
  next request over an existing keep-alive connection before handler execution.
- Passed on the implementation tree: focused registry/reset/recovery and all
  affected-package tests, `make ci` (including 20 Playwright cases),
  `make quality`, full `make security`, `make test-race` and `git diff --check`.
  The full Semgrep auto-rule scan ran 597 rules on 256 files: no findings.
- Registry validation and rollback fixtures use complete literal SQL statements,
  preserving all seven rollback scenarios and the distinct schema-6/schema-7+
  queries. Independent followup review confirmed all 23 Scan fields retain
  their order and semantics; no scanner rules were suppressed.
- Independent security review found no unresolved material issue. Its earlier
  reserved-name trigger reproduction now rejects the archive before restore.
  Source/diff and matching English/Russian recovery docs were inspected.
  Private gate evidence and original diagnostics remain in the central Codex
  task-state directory; no logs or tooling state were added to the repository.

## Original implementation instruction

> Continue AWG-Forge from the current checkout. Read AGENTS.md and
> docs/design/control-backup-restore-next-session.md plus its linked contracts,
> then verify Git and source/tests. Implement only the internal controller
> backup/restore reconciliation checkpoint: validate archived SQLite/registry
> against PKI and controller identity, atomically invalidate restored browser
> and node authority, preserve history and durable restore-pending crash gates.
> Keep control disabled. Do not add enrollment/rebind routes, enablement, CA
> rotation, UI/API, installer or tunnel work. Preserve unrelated changes.
> Do not commit, push, create or merge a PR without explicit confirmation.
