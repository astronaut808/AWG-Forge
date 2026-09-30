# Control server-leaf rotation: next-session handoff

Status: implemented internal checkpoint on the local feature branch after PR
#113. This remains an internal prerequisite, not an enabled product feature.
The controller has no production control listener, scheduler, rotation owner,
enrollment, renewal or rebind route. Existing CA trust, endpoint, controller
identity, SQLite and tunnel state are unchanged.

Implementation and executable contracts:

- `internal/controlpki/server_rotation.go`: intact expired-leaf recovery with a
  currently valid committed CA; fresh Ed25519 leaf and CA-bounded lifetime.
- `internal/storage/control_server_rotation.go`: versioned strict secret-free
  journal, exclusive generations, durable private files and exact retirement.
- `internal/app/control_server_rotation.go`: private expected-generation
  operation under the cross-process mutation lock, commit/recovery fences and
  test-only joint runtime transition. No production caller exists.
- `internal/controlserver/snapshot.go`: immutable certificate/deadline snapshot,
  SNI-free `GetCertificate`, tracked accepted sockets and committed-failure close.
- Focused `server_rotation`, `control_server_rotation` and `snapshot` tests cover
  crash boundaries, concurrent attempts, real TLS/mTLS, expiry and encrypted
  active-generation backup/restore. Final check results belong in the session
  handoff, not an assumption in this document.

The execution contract below records this checkpoint's acceptance criteria.
Recheck Git and source before any later work.

## Objective and exact boundary

Implement **server-leaf-only** rotation under the existing control CA. Keep the
same CA generation, CA public certificate, CA SPKI pin, controller ID, bind IP,
advertised name/IP and port. Stage a new Ed25519 server key/certificate in an
immutable generation, verify it, durably switch `ServerGeneration` in
`state.json`, and make the isolated TLS runtime capable of serving the new
complete certificate snapshot without rebinding its loopback listener. Prove
crash and expiry behavior. No node certificate, binding, SQLite schema, tunnel
state or `ConfigRevision` changes are part of this operation.

This checkpoint provides a private application operation and testable runtime
reload only. There is no scheduler or production runtime owner yet, because
explicit control-listener enablement is a later checkpoint. The future enabled
controller will call this operation at or before the final third of the current
leaf's actual lifetime. Implement a pure due-time calculation now if useful,
but do not imply that automatic rotation already runs. An internal explicit
rotation may recover a cryptographically valid **expired server leaf** while
the existing CA is still valid; a missing, corrupt or mismatched committed CA,
pin, server leaf, or server key is an offline recovery case, not a reason to
silently generate a replacement.

No production route/listener, browser API, CLI command, enrollment, node-side
installation, public API, endpoint/SAN change, CA trust rotation, Web UI TLS or
ACME change, backup/restore policy expansion, AWG or tunnel change belongs in
this checkpoint. In particular, do not set `ControlIdentityState.Enabled`.

## Current source to inspect before editing

- `internal/controlpki/pki.go`: `Generate` creates CA and server leaf together;
  `Validate` checks both and requires the exact advertised SAN.
- `internal/config/state.go`: `ControlIdentityState` has one active
  `CAGeneration` and `ServerGeneration`.
- `internal/storage/control_identity.go`: protected immutable generation files,
  private-path checks, fsync steps and preparation journal.
- `internal/app/control_identity.go`, `internal/app/init.go`,
  `internal/app/service.go`: application mutation lock, explicit preparation,
  startup recovery and safe warnings.
- `internal/controlserver/runtime.go`: a fixed `tls.Config.Certificates` pair,
  fixed expiry timer and no production caller. `ServeTLS` clones its config.
- `internal/backup/backup.go`, `internal/backup/controller_restore.go`: archives
  contain the four files named by the **current** state generations and reject
  unlisted control paths. Backup uses the application mutation lock.
- Focused tests in `internal/controlpki`, `internal/storage`, `internal/app`,
  `internal/controlserver` and `internal/backup`; the failure matrix in
  `multi-node-failure-matrix.md`.

The [parent PKI plan](control-tls-pki-plan.md),
[registry checkpoint](control-node-certificate-registry.md),
[security model](multi-node-security.md) and
[failure matrix](multi-node-failure-matrix.md) supply the wider gates. Historical
handoffs for identity preparation and node renewal describe completed work;
they must not reopen those slices.

## Fixed design decisions

1. **Trust and certificate profile.** Reuse the committed CA key from its
   protected generation. Verify its key match, self-signature, constraints,
   exact SPKI pin and current validity before signing. Generate a fresh random
   Ed25519 server key and positive random serial; never reuse a key or serial.
   Issue only a non-CA digital-signature, server-auth certificate with the
   existing exact single DNS or IP SAN. Its `NotAfter` is the earlier of 30 days
   from issue time and the CA expiry; reject an already-expired result. Keep
   the present small clock-skew allowance at `NotBefore`. No CA replacement or
   changed pin is allowed. Split CA and leaf validation as needed so an expired
   but otherwise intact old leaf can be rotated without accepting corrupt
   committed material.
2. **Serialized, idempotent intent.** The private app operation accepts the
   expected active server generation and a testable time source. Hold the
   existing cross-process state mutation lock through validation, stage,
   commit and recovery bookkeeping. Reject a stale expected generation rather
   than issuing another leaf on a retry after an uncertain commit. Re-read
   state under the lock and refuse restore-pending, desired-state, preparation
   or rotation journals that cannot be safely reconciled. Do not modify the
   control endpoint or CA fields.
3. **Durable switch.** Create a separate secret-free rotation journal before
   writing anything, recording controller ID, CA generation, old and proposed
   server generations. Use exclusive new generation paths, `0700` directories,
   `0600` files, component/symlink checks, file fsync and parent-directory
   fsync. Validate the staged pair against the committed CA/pin and exact SAN.
   Atomically replace `state.json` with only `ServerGeneration` changed and
   sync its directory. Never publish a new generation before its files are
   durable. Never return success before the state commit is durable.
4. **Recovery and old material.** On pre-commit failure/restart, keep the old
   state and pair; remove only the journal-owned new generation after verifying
   its exact paths. On post-commit failure/restart, validate the committed new
   pair, then finish journal cleanup. Any mismatch or unsafe path preserves the
   journal and fails closed for control work, with a safe diagnostic; browser
   administration and local tunnels remain independent. The previous server
   generation is no longer selected for new handshakes after the switch. Since
   nodes pin the unchanged CA, there is no need to serve old and new leaf
   concurrently. Retire the old generation immediately after the new state
   and pair are durably validated, before deleting the journal. There is no
   old-leaf handshake overlap. Remove only the journal-named key/cert and
   directory after checking for unexpected entries; do not use a broad
   `RemoveAll`. If cleanup fails or the process crashes, retain the journal,
   retry exact cleanup on startup and keep control admission closed until it
   succeeds. Never roll committed state back to the old leaf.
5. **TLS snapshot.** Add a concurrency-safe immutable certificate snapshot
   whose key/cert and expiry move together. Supply it through
   `tls.Config.GetCertificate` with no fallback `Certificates` entry: Go calls
   `GetCertificate` without SNI only when `Certificates` is empty. Keep TLS
   1.3, verified optional client certificates, current client CA, disabled
   session tickets, exact route gate and resource limits. Reject handshakes
   once the active server leaf or CA expires; update the runtime expiry watch
   when a new snapshot is published. An expired active identity closes the
   loopback listener and established connections rather than falling back to
   the predecessor. On rotation, close pre-switch TLS connections before the
   reload reports success; new handshakes use only the new snapshot. Make
   concurrent accept/handshake and close safe, while per-request node
   authorization continues to recheck SQLite. Candidate validation failure
   **before commit** leaves the valid current snapshot untouched. After a
   durable state commit, failed live publication must close the socket and
   old connections until restart loads the committed identity; serving the
   predecessor would contradict `state.json`. Provide this fail-closed runtime
   transition as a testable contract, but no production caller in this slice.
6. **Archive compatibility.** The existing encrypted backup/restore path must
   still verify and round-trip the **active rotated** generation. An archive
   contains only the generation referenced by its state, not a retired key.
   A restored expired leaf remains recoverable with control disabled. Reject
   a rotation journal during backup and cold restore. Do not claim that this
   solves stale-backup node-revocation rollback; registry reconciliation is
   the next separate checkpoint.

The current `state.json` shape can express the active generation, so avoid a
state-schema or SQLite migration unless source-backed evidence proves one is
necessary. A new journal format needs strict parsing and compatibility tests.

## Execution order and acceptance gates

1. **Preflight.** Read repository `AGENTS.md`, this entire handoff and the
   linked parent/security/failure documents. Run `git status --short --branch`
   before touching files. If still on `develop`, start a feature branch (name
   suggestion: `feature/control-server-leaf-rotation`), carrying this uncommitted
   handoff and preserving every other user change. If that name exists, inspect
   it before choosing a new name; never reset or overwrite it. Inspect current
   functions/callers/tests; do not infer behavior from this handoff if the
   code has changed.
2. **Lock the contract with focused tests.** Cover unchanged CA bytes/pin,
   endpoint and controller ID; new key/serial/generation; exact SAN/EKU;
   near-CA-expiry lifetime; stale expected generation and concurrent attempts;
   expired old leaf recovery; invalid/expired CA and corrupt/missing old pair
   rejection; no startup auto-generation and no enablement.
3. **PKI and storage.** Extract narrowly reusable validation/signing helpers,
   then add server-only immutable generation write/load and a rotation journal.
   Fault-inject before/after journal sync, generation directory creation,
   each file write/sync, staged validation, state save/sync, post-commit
   validation, old-generation retirement and journal deletion. Assert exact
   active generation and private-file modes after each restart/retry. Exercise
   symlink, wrong permission, extra file, generation collision and unexpected
   journal/state combinations.
4. **Application operation.** Use the existing mutation lock and recovery
   fences. Confirm a concurrent invocation across two service instances cannot
   create two committed successors, and a retry with an old expected generation
   cannot rotate again. Keep `ConfigRevision`, SQLite and AWG state unchanged.
   Ensure startup, preparation, initial node issuance, renewal, rebind and
   backup detect and safely reconcile or reject a pending or malformed
   rotation journal. Test these direct private calls without relying on `Init`
   having run first; no node certificate may be issued through a crash gate.
5. **Live TLS test.** Run a real loopback TLS/mTLS server using the test-only
   runtime. Verify CA pin/hostname and client authorization still work before
   and after reload; new handshakes observe exactly the new leaf; a pre-commit
   invalid candidate does not replace a valid snapshot; concurrent handshakes
   and swaps are race-free; expiry closes admission and existing connections;
   a successful timely switch prevents the old fixed timer from closing the
   new identity. In a joint app/runtime test, commit a new `ServerGeneration`,
   inject publication failure, assert that socket and old connections close,
   then restart with exactly the committed generation. Test SNI-free IP clients
   because they exercise the
   `GetCertificate` fallback rule. No production route or caller is added.
6. **Backup, docs, and review.** Round-trip a rotated prepared identity through
   encrypted controller backup/verify/cold restore, with no retired key in the
   archive. Check legacy unrotated archives and expired but consistent leaf
   recovery. Update this and the parent checkpoint status, English/Russian
   operator docs only if user-visible behavior changes, and the failure matrix
   if a verified contract changes. Obtain an independent security review of
   key handling, crash recovery, TLS concurrency/expiry and archive behavior;
   fix findings and rerun affected checks.
7. **Final gates on the final tree.** Run focused Go tests during iteration,
   `make ci`, `make quality`, `make security-fast`, `make test-race`, and
   `git diff --check`. Preserve full large diagnostics in private unique logs,
   report exit codes and relevant failures, and inspect the complete diff.
   Run Docker image/host-network smoke only if packaging or runtime binding
   changes; those changes are not expected. `make security` is the release gate
   when scanner data is available. If a gate cannot run, mark it unverified,
   not passed.

Completion means the internal rotation and runtime tests demonstrate each
applicable failure-matrix row, the old CA/pin and product defaults are intact,
the independent review has no unresolved material finding, and the final diff
and check results are shown. Do not commit, push, create or merge a PR without
the user's explicit confirmation. Suggest a `feat(control): ...` commit title
at handoff, without creating the commit.

## After this checkpoint

The [backup/restore checkpoint](control-backup-restore-next-session.md) reconciles
controller backup/restore with the node-certificate registry and its PKI
generations. A stale backup must not
silently undo revocation; use fail-closed re-enrollment until a proven replay
fence exists. Only after that recovery gate may explicit loopback enablement
be wired. Non-loopback exposure waits for authenticated enrollment and its
backup, restore and failure-matrix gates.

## Ready-to-paste instruction for the next checkpoint

> Continue AWG-Forge from the current checkout. Read `AGENTS.md`, this completed
> internal server-leaf rotation contract, the parent PKI plan, registry checkpoint,
> security model and failure matrix, then verify current source/tests and Git.
> Preserve uncommitted work. Plan the separate controller backup/restore and node
> registry reconciliation checkpoint: a stale backup must not reactivate revoked
> node authority. Keep control disabled until an explicit, verified recovery fence
> exists. Do not reopen enrollment, production listener, CA rotation, UI/API,
> installer, AWG or tunnel work. Do not commit, push or create a PR without explicit
> confirmation.

The hot-reload contract uses Go's documented
[`tls.Config.GetCertificate`](https://pkg.go.dev/crypto/tls#Config.GetCertificate)
and [`http.Server.ServeTLS`](https://pkg.go.dev/net/http#Server.ServeTLS)
behavior. Source and tests remain authoritative for this repository.
