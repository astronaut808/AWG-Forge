# Multi-node failure and recovery matrix

Status: proposed. Each row becomes an executable integration or fault-injection
test before the corresponding capability can be released.

| Failure point | Required observable outcome | Persistent authority | Recovery/test assertion |
| --- | --- | --- | --- |
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
| Controller restored with same identity | Nodes reconnect after endpoint recovery | Restored controller ID/CA | No re-enrollment; redelivered operations do not execute twice |
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
