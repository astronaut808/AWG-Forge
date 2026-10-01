# Multi-node failure and recovery matrix

Status: proposed. Each row becomes an executable integration or fault-injection
test before the corresponding capability can be released.

| Failure point | Required observable outcome | Persistent authority | Recovery/test assertion |
| --- | --- | --- | --- |
| Control identity is absent after controller-auth activation | No control port is opened and no CA is created at startup | Existing controller `state.json` and auth files | Standalone/DB-off and controller browser behavior are unchanged |
| Control CA, server key, or SQLite is missing after explicit control setup | Control listener stays closed; no replacement identity is generated | Committed controller identity generation | Browser administrator can see a safe failure; node traffic never falls back to HTTP or Web UI TLS |
| Control setup crashes before or after state commit | Pre-commit stage is removed or recoverable; post-commit identity is loaded exactly | `state.json` plus secret-free setup journal | No orphan listener, partial key pair, or silent identity replacement |
| Loopback enable uses a stale backup receipt, replaced/revoked session or changed identity | Reject before serving; every matching attempt consumes its receipt | Process-local receipt, complete identity and recent administrator session | Real encrypted backup adapter; generation/session/process fences and concurrent enable tests |
| Loopback bind, enable commit or post-commit serving fails | Release the reserved socket; never roll back an uncertain commit; explicit retry loads committed state | Application runtime owner and `state.json` | Bind/save/sync/serve fault tests; local Init remains available and never auto-starts control |
| Loopback disable interrupts a held request | Close admission, cancel request context, drain with a deadline, release owned registry, persist disabled state | Application runtime owner | Handler cleanup barrier test; identity, tunnel configuration and revisions are preserved |
| Bootstrap reaches a node-only path without a client certificate | Request is rejected even though the TLS handshake may omit a client certificate | Exact route allowlist and node certificate registry | Browser cookie and forwarded certificate headers cannot authorize a control request |
| Server hostname or CA pin is wrong | Node refuses the TLS connection before sending an invitation secret or node data | Node-pinned CA and advertised endpoint | No insecure verification fallback or secret-bearing retry |
| Node certificate is revoked on an existing keep-alive connection | Next request is denied; an awakened poll is rechecked | SQLite certificate and binding status | Revocation is enforced per request, not only at TLS handshake |
| Server-leaf rotation fails or crashes before state commit | Old identity remains selected; remove only the journal-owned candidate after validating state and paths | Unchanged `state.json`, existing CA and rotation journal | Fault tests at journal/write/sync/staging boundaries; no replacement CA or mixed pair |
| Server-leaf rotation crashes after state commit or during retirement | Keep the committed successor; validate it, retire the journal-named predecessor exactly, then clear the journal | New `ServerGeneration` and rotation journal | No rollback; incomplete or unsafe cleanup fences direct control issuance, renewal, rebind, preparation and backup |
| Two server-leaf rotations or an uncertain-commit retry use the same expected generation | Exactly one successor commits; stale intent conflicts | Cross-process mutation lock and active `ServerGeneration` | Two-service race test; unchanged controller/CA/endpoint, SQLite and tunnel revisions |
| Live server-leaf publication fails after durable state commit | Close the listener and all old sockets until restart loads the committed generation | Committed `state.json` | Joint app/runtime TLS test; no predecessor fallback or SNI-free fixed certificate |
| Server leaf or CA expires | Close admission and accepted sockets, including unfinished handshakes; explicit intact expired-leaf rotation requires a valid CA | Current immutable TLS snapshot and committed identity | Real expiry and timely-reload tests; predecessor timer cannot expire a valid successor |
| Rotated identity is backed up or cold-restored | Archive contains only the active CA/server generation; pending rotation journals are rejected | Archive state and protected generation files | Encrypted create/verify/restore tests; legacy and expired consistent leaves remain recoverable with control disabled |
| Controller archive records enabled loopback control | Verify the original archive; install a disabled restore candidate before reconciliation | Archived identity and durable restore-pending gate | Encrypted enabled archive test; restored listener disabled, all node authority revoked, tunnel revisions preserved |
| Controller backup predates a session/code use, node revocation, renewal or rebind | Restore invalidates all archived browser/node authority, including repeated and pre-restore archives | Preserved registry history plus disabled admin and revoked certificates/bindings | Internal/external DB tests deny archived and unknown post-snapshot certs; admin recovery never un-revokes them |
| Controller archive has inconsistent schema, registry or PKI | Create/verify/restore reject it before target replacement | Embedded migration provenance, controller identity and archived CA | Version-aware schemas 5–8, DER/hash/profile/reference checks and rejection of unknown schema objects, including reserved-name triggers |
| Browser/node reset statement or commit fails | No partial reset commits; pending marker fences admission | One SQLite transaction and durable restore-pending marker | Statement and commit fault tests preserve original authority until the entire reset succeeds |
| Restore fails during final marker unlink/directory sync | Marker may remain or be absent; old authority is already durably invalidated | Reopened reset postconditions and synced files/DB parent | Test both unlink outcomes; any reported failure requires offline inspection |
| Controller restore crashes after replacing SQLite but before `state.json` | Startup remains closed to both browser and node authority | Durable restore-pending marker outside the file move set | Marker survives all pre-completion switches and failed rollback; removal occurs only after reopened denial checks and file/parent sync |
| Controller unavailable at node startup | Node starts local UI and tunnels; control worker backs off | Node `state.json` | Existing handshakes continue; no restart loop or busy poll |
| Controller stops during normal forwarding | Data plane is unaffected | Node runtime and `state.json` | Traffic continues while presence becomes offline |
| Node unavailable | Controller marks presence stale without inventing tunnel failure | Last redacted snapshot | No hidden retry storm; queued work follows explicit expiry policy |
| Network partition during long poll | Request times out and reconnects with jitter | No state change | At most one active poll after reconnect |
| Duplicate active poll | Controller keeps one lease and rejects/replaces the stale poll | Controller node session | No concurrent execution; redelivery remains possible and idempotent |
| Invitation expires before claim | Enrollment fails without identity or binding changes | Existing node mode/binding | Retry requires a new invitation |
| Two CSRs claim one invitation | Only the first bound CSR may proceed | Controller enrollment row | Second claim is rejected and audited |
| Approval is rejected or times out | Pending key/cert material is discarded; installation remains usable | Previous mode/binding | No partial controller binding or startup retry |
| Controller fails after approval before certificate fetch | Node can retrieve the same issued certificate using the bounded claim token | Controller enrollment/issued cert | No second node identity or certificate binding |
| Node fails after certificate fetch before binding save | Previous binding remains active; enrollment restarts with a new invitation if its in-memory claim credential is lost | Atomic binding files | No mixed old/new controller files |
| Certificate expires during controller outage | Tunnels continue; control becomes unavailable; local recovery remains | Node state/runtime | Re-enrollment does not modify tunnels |
| Controller CA rotation interrupted | Old trust remains valid until new trust and certificates are confirmed | Staged trust bundle | No fleet-wide simultaneous lockout |
| Controller restored with same identity | Control remains disabled; every restored certificate and binding is revoked | Exact archived controller ID/CA/server generation and preserved registry history | Explicit local recovery and fresh enrollment required; existing keep-alive next request denies before handler invocation |
| Controller restored twice | Duplicate identity is detected operationally; automatic leader behavior is absent | Operator-controlled restore | Documentation and Doctor warn; no split-brain claim |
| Controller lost without backup | Nodes continue locally and require explicit root-authorized rebind | Node old binding | Old controller cannot remotely transfer nodes |
| Managed backup restored onto a different installation | Restore rejects the identity mismatch; local root may explicitly detach before reuse and later enrollment establishes a new identity | Target node identity plus restored local configuration | Rejected restore writes no state; detached state has no controller authority |
| Node data directory cloned byte for byte | Clone remains offline until local root detaches it; controller later detects duplicate active identity and revokes/re-enrolls one side | Copied node identity until detach | Local code cannot identify a complete clone without controller evidence; never run both copies as managed nodes |
| Operation delivered twice before execution | Second delivery observes accepted/leased operation state | Node SQLite | Application service executes once per active operation |
| Crash before operation acceptance is durable | Redelivery is safe and starts execution once | Controller queued operation | No local mutation occurred |
| Crash after acceptance before candidate build | Resume the same accepted operation | Node SQLite | No mutation and no duplicate resource |
| Validation or capability check fails | Typed terminal failure; no render, save, or runtime apply | Node SQLite failure receipt | Stable problem code returned on replay |
| Candidate render fails | Current state/runtime remain unchanged | Existing node state | Rollback is not needed because apply did not start |
| Runtime apply fails | Existing runtime and state are restored | Existing node state | Doctor reports failure without generation increment |
| Crash after runtime apply before final state save | Startup restores runtime from persisted desired state | Existing node state; secret-free pending journal is recovery evidence only | Operation remains unknown/reconcilable, never successful; journal is removed only after convergence |
| Final state save fails | Runtime rollback restores previous configuration | Existing node state; secret-free pending journal is recovery evidence only | No generation increment or success result; failed rollback leaves the journal for startup recovery |
| Crash after state and receipt commit before result upload | Replay returns durable result without reapplying | New node state plus receipt | Resource ID and generation are unchanged |
| Controller crashes after receiving result before acknowledgement | Node resubmits the same result | `state.json` success receipt or SQLite non-mutating receipt | Controller stores one terminal transition |
| Stale expected generation | Operation fails with conflict and requests fresh snapshot | Node `state.json` | No last-write-wins mutation |
| State epoch or binding epoch mismatch | Operation is rejected as stale authority | Node identity/binding | No mutation even if operation ID is new |
| Local UI or CLI changes desired state while the controller is unavailable | Node commits locally and advances `desired_generation`; the next snapshot refreshes the controller | Node `state.json` at the new generation | Existing tunnels remain manageable without detaching the node |
| Local CLI and controller request overlap | One process holds the node mutation lock through apply and commit; the other reloads state after acquiring it | Node `state.json` plus runtime | Both successful changes are preserved or the stale remote request conflicts explicitly |
| A queued controller operation targets the generation preceding a local change | Node rejects it as stale before its mutation callback or runtime apply | Newer node `state.json` | Controller refreshes its snapshot and requires an explicit retry |
| Managed state requires automatic repair | Node repairs atomically and advances `desired_generation` | Repaired node `state.json` | Controller observes the repair as a newer node commit |
| Runtime health changes without a desired-state change | Same-generation snapshot with a newer per-boot `snapshot_sequence` refreshes observations only | Existing desired-state projection plus new runtime observations | Health, handshakes, and counters do not become stale or overwrite configuration |
| Delayed presence or snapshot arrives after a newer process start | Controller rejects a lower `boot_sequence`, a mismatched `boot_id` at the same sequence, an older snapshot sequence, or an expired/superseded `session_id` | Latest accepted snapshot and active node session | No observational rollback from reordered delivery |
| Operation expires while node is offline | Operation becomes expired and is never delivered | Controller operation row | Reconnect does not execute it |
| Controller SQLite unavailable | Controller auth and mutations fail closed; nodes keep forwarding | Node state/runtime | No fallback auth or in-memory mutation queue |
| Node SQLite unavailable | Remote delivery and acceptance pause; committed success replay remains available from node state | Node `state.json` success receipts | No mutation starts without its durable acceptance path; local standalone operation remains diagnosable |
| Snapshot contains unknown/new fields | Compatible controller ignores allowed additive fields | Node remains authority | Current/previous contract compatibility test passes |
| Node lacks requested capability | Controller disables action and node rejects forged request | Advertised capabilities | No fallback to generic command execution |
| Secret artifact generated, then controller crashes | Artifact is lost and must be regenerated | Node state only | No secret exists in operation DB or logs |
| Artifact expires before browser fetch | Browser receives an expired/not-found problem and may regenerate | No durable artifact | TTL and memory bounds are enforced |
| Browser fetches artifact twice | One-use policy rejects the second fetch | In-memory artifact record | No cacheable secret response |
| Managed restart operation succeeds | Process exits gracefully; Docker restarts it; new `boot_id` confirms | Node desired state unaffected | Timeout becomes unknown, not false success |
| Docker restart policy is absent | Preflight rejects remote restart | Reported capability/readiness | Controller cannot deliberately stop an unrecoverable node |

## Test ordering

1. Unit-test state transition and operation receipt semantics with injected
   failures at every commit boundary.
2. Run race tests for poll sessions, leases, receipt replay, shutdown, and
   session rotation.
3. Run real loopback TLS/mTLS integration tests with an ephemeral CA.
4. Run container tests proving controller outage does not interrupt AWG and
   that restart confirmation uses a new `boot_id`.
5. Run upgrade/restore tests from the latest standalone release with database
   off and with SQLite enabled.
6. Run compatibility tests between current and previous `/control` contracts.

The matrix is release-blocking for implemented rows. Unimplemented rows do not
justify shipping a partial controller as a stable user-facing feature.
