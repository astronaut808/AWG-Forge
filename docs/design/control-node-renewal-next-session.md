# Next checkpoint: internal node certificate renewal

Status: handoff prepared on `feature/control-node-cert-renewal`, branched from
`develop` at `807b7fa`, which contains PR #110 (`58da046`). The initial node
certificate registry is merged. This checkpoint adds an internal renewal
primitive; it does not make multi-node management available to users.

## Starting point

1. Read `AGENTS.md`, this document, [control-tls-pki-plan.md](control-tls-pki-plan.md),
   [control-node-certificate-registry.md](control-node-certificate-registry.md),
   [multi-node-security.md](multi-node-security.md), and the certificate rows of
   [multi-node-failure-matrix.md](multi-node-failure-matrix.md). Inspect the
   current source and tests before editing. Do not assume this handoff is newer
   than the checkout.
2. Run `git status --short --branch` and inspect `git log -5 --oneline --decorate`.
   The intended checkout is `feature/control-node-cert-renewal`, with these
   handoff documentation edits still uncommitted. If the session opens on
   `develop`, switch to the existing feature branch. Preserve all local edits;
   never reset, stash away, or overwrite someone else's work. Verify the
   branch point before editing.
3. Use `awg-forge-pro`, then `golang-skill-router` with `golang-security` as the
   primary Go guidance for certificate and authorization code. Read those skill
   files in the new session. Add `golang-database` only if migration/transaction
   design needs its separate guidance. Use `secure-code-guardian`, `qa`, and
   `security-review` for the final focused review if useful; do not expand it
   into an unrelated repository audit. Follow `AGENTS.md` and the actual source
   over this historical plan when they disagree.

Current code: `internal/controlpki/node.go` validates a signed, extension-free
Ed25519 CSR and issues a 30-day client-auth certificate. The private initial
issuance method is in `internal/app/control_node_certificates.go`.
`internal/sqldb/control_node_certificates.go` stores binding, public DER, CSR
hash, certificate fingerprint, revocation and a reserved supersession cutoff.
`internal/controlserver` has a loopback-only testable TLS runtime and rechecks
registry authorization per request. `runServe` has no production control
listener; there is no enrollment, renewal handler, node worker, or installer
join flow. `api/control-v1.openapi.json` is a draft internal contract, not an
enabled endpoint.

## Tools and evidence handling

Use Serena for exact symbols/callers when available, and Codebase Memory for a
broad structural map only after checking its index freshness and coverage.
Direct file reads and narrow `rg` searches are sufficient for this bounded
change. Graphify is for a rare mixed code/docs/config question, not a required
index. Context7 belongs to a read-only documentation researcher if current
external library facts are needed; AgentMemory is not a normal root tool. Do
not install an MCP server, hook, scanner or dependency to follow this handoff.

Use normal patch/file tools for edits and direct narrow commands for checks.
Keep large test/scanner output in a private uniquely named temporary log; report
exit code, duration, failure summary and log path, then inspect the original
log before rerunning. For substantial work, follow the centrally stored Unlazy
gate discipline in `/Users/astronaut/.codex/WORKFLOW.md`; keep its ledger out of
the repository. No extra agent is necessary for this bounded checkpoint; use
an independent focused review only when the current routing rules permit it.

## PR boundary

Implement the smallest complete internal renewal operation in
`internal/app`, `internal/sqldb`, and focused tests. Change `internal/controlpki`
only where the existing issuer/CSR checks are insufficient. Add an additive
SQLite migration only if needed to identify the predecessor certificate and
prevent competing successors; preserve existing rows and migration checksums.
Reconcile the draft renewal contract and relevant design docs with the actual
idempotency/conflict policy. This is a distinct PR from rebind, server-leaf or
CA rotation, controller backup/restore expansion, enablement, and enrollment.

No production `/control/v1` route, public/browser API, listener wiring, CLI,
installer option, Web UI, node registration, node-side key storage, AWG/tunnel
change, or non-loopback bind belongs here. Never change `ControlIdentityState.Enabled`
to true as a side effect. No ordinary install, upgrade or startup may generate a
CA, issue a node certificate, or open a control port. Do not change the existing
browser `/api` contract or `api/openapi.json` for this internal primitive.

## Required renewal contract

- Caller authority is the **presented current client certificate**, verified
  against the active controller CA and current SQLite binding. A body-provided
  `node_id`, `controller_id`, `current_serial`, CSR subject, or forwarded header
  never establishes identity. The service must perform its own authorization
  check before signing, and the database transaction must recheck the old
  certificate and binding before publishing the replacement. Use the actual
  certificate fingerprint, issuer generation, serial, node/controller IDs,
  binding epoch, validity, revocation, and supersession state. Database errors
  deny renewal. No authorization cache may cross requests.
- Accept only a bounded, signed Ed25519 CSR that passes `ParseNodeCSR`, with a
  public key different from the current certificate's key. The node retains
  its private key. No caller-provided node identity is copied into the leaf.
  Preserve the existing 30-day TTL and CA-expiry limit. Decide and test the
  renewal window from the existing two-thirds-of-lifetime policy; record the
  exact boundary and any necessary recovery exception before implementation.
- One active predecessor can create **at most one successor**. Persist the
  predecessor issuer generation and serial (or an equivalent durable link) and
  enforce this with a database uniqueness constraint, not only a Go check.
  The existing global full-CSR digest still prevents CSR reuse for another
  node. Do not rewrite historical migration `000006`.
- In one SQLite transaction, recheck predecessor authority, binding and
  renewal eligibility; insert the successor's public DER/metadata; and set the
  predecessor's `superseded_at_unix_ms`. The cutoff is no later than 24 hours
  after commit and no later than the predecessor's own expiry. The new
  certificate is not returned before commit. Use the existing WAL/full-sync,
  immediate-transaction and busy-timeout configuration; do not introduce an
  in-memory source of truth. At the cutoff, the old certificate is denied even
  on an existing keep-alive connection; the successor remains authorized.
- A retry with the **same full CSR DER and same predecessor** returns the
  originally committed certificate DER while that predecessor can still
  authenticate; it does not move the cutoff. A different CSR, including one
  signed with the same new key, conflicts. A retry from a different predecessor
  conflicts even if the CSR digest matches. Concurrent same-CSR attempts must
  converge on one row; concurrent different-CSR attempts must yield one winner
  and one conflict, with no extra usable certificate. Keep error types distinct
  enough to test conflict versus denied authority; externally visible error
  mapping belongs to a later route PR.
- Explicit binding revocation fences old and new certificates immediately.
  Per-serial revocation fences the named serial; a compromised node identity
  requires binding revocation. An expired, revoked, past-cutoff, wrong-issuer,
  wrong-controller, wrong-binding, or forged old certificate cannot renew.
  If commit fails, no returned certificate is usable; if response construction
  fails after commit, an exact authorized retry recovers the stored DER.
- Check cancellation before and during persistence. Preserve the existing
  application mutation lock and restore-pending/identity-journal gates. Do not
  silently create missing key files or a missing controller database. State
  remains the authority for prepared control identity; SQLite remains the
  certificate authorization registry.

The draft `CertificateRenewalRequest` already contains `boot_id`,
`current_serial`, and `csr_pem`. Since this checkpoint has no handler or node
session, do not claim those fields are enforced at the wire boundary. Update
only the draft description/schema needed to make eventual verified-peer serial
matching, exact retry and conflict behavior unambiguous. Do not expose a route
merely to make the draft executable.

## Implementation order

1. Write focused failing tests for the happy path, exact retry, competing CSR,
   boundary times, revocation/rebind and failure paths. Keep clocks injected;
   avoid short real-time deadlines that become flaky under `-race`.
2. Add the additive migration and database transaction, including predecessor
   uniqueness and migration-from-version-six coverage. Prefer explicit rows
   and constraints over a broad repository abstraction. Confirm old rows and
   unrelated auth/audit data survive migration.
3. Add the application entry point after reviewing the existing private initial
   issuance path. Verify prepared controller state, usable CA, SQLite/auth,
   restored-state gate and current certificate before signing. Keep the
   operation internal with no production route or startup caller.
4. Add a test-only exact route on the loopback TLS runtime to exercise a real
   mTLS handshake through the registry authorizer into renewal, then verify old
   and new certificates on the same and new connections. Test wrong chain,
   forged certificate with matching serial, revoked binding, and SQLite outage.
   Keep the route absent from production wiring.
5. Reconcile the design contract/docs; inspect the entire diff and test only
   against the changed behavior. Fix findings before handoff.

## Acceptance evidence

- Database tests: success; one successor; same/different CSR and predecessor;
  concurrent attempts from separate database handles; restart persistence;
  cutoff just before/at/after; expired and revoked source; binding revoke;
  wrong issuer/controller/epoch/fingerprint; lost DB, cancelled context and
  transaction failure; migration from version six without data loss.
- Application/TLS tests: fail closed on missing/corrupt CA, restore marker,
  pending identity journal, uninitialized auth, DB-off/closed DB; real TLS
  wrong CA, old/new overlap, same-connection cutoff and revocation. Assert no
  certificate or secret leaks to logs, audit, Doctor or support output if any
  of those surfaces are touched. Do not claim installer-to-installer E2E:
  enrollment and a production control route do not exist yet.
- During iteration run targeted Go tests. Final gates are `make ci`,
  `make quality`, `make security-fast`, and `go test -race ./...` because this
  change touches certificate authority, SQLite transactions and concurrency.
  If a scanner or Docker/loopback environment is unavailable, report that
  exact gate as unverified with the reason. Docker image and host-network smoke
  are required only if runtime or packaging actually changes.
- Review `git diff --check`, diff stat/files and relevant hunks; confirm no
  generated UI assets, browser API, production listener, installer or AWG
  changes. Include untracked files in the review because ordinary `git diff`
  omits them. Show the final diff summary, meaningful hunks, commands and results,
  remaining limitations and the proposed commit title. Do **not** commit,
  push, create or merge a PR without the user's confirmation.

## Next checkpoints after this PR

Rebind fencing and server-leaf rotation remain separate reviewed work.
Controller backup/restore must then include the registry and PKI generations
and prevent stale-backup revocation rollback. Explicit loopback enablement
follows only after its recovery gate; non-loopback exposure and installer-level
two-node tests wait for authenticated enrollment and its security tests. No
phase enables itself during an ordinary upgrade.

## Ready-to-paste prompt for the next session

```text
Продолжи AWG-Forge на существующей ветке feature/control-node-cert-renewal.
Сначала прочитай AGENTS.md, docs/design/control-node-renewal-next-session.md,
docs/design/control-tls-pki-plan.md и указанные в них документы. Проверь Git,
текущий код и тесты; сохрани незакоммиченные правки handoff-документации.

Используй awg-forge-pro и golang-skill-router; для PKI и авторизации выбери
golang-security, а дополнительные навыки подключай только по необходимости.
Реализуй внутреннее продление сертификата узла по границам, порядку и критериям
готовности из handoff-документа: одна замена на исходный сертификат, точный
повтор того же CSR, конфликт конкурирующих CSR, атомарная запись и ограниченный
период действия старого сертификата. Добавь необходимые миграцию и тесты,
включая реальный loopback mTLS, конкурентные запросы и отказные сценарии.
Согласуй проектный /control/v1 контракт и документацию с реализованной логикой.

Не добавляй production route/listener, enrollment, installer join, Web UI,
публичный API, rebind, ротацию CA/server leaf, изменения AWG или туннелей.
Проведи целевые проверки и финальные gates из документа. Покажи итоговый diff,
результаты проверок и оставшиеся ограничения. Не коммить, не пушь, не создавай
и не вливай PR без моего подтверждения. В конце предложи название коммита.
```
