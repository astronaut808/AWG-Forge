# Snapshot refresh delivery checkpoint

`snapshot.refresh` is the only delivered operation. It reads committed node state
and existing runtime observations; it never calls Init, render/apply, or writes
node history, desired state, generation or client revisions. Remote mutations and
secret export remain future work.

## Contract and storage

The same-origin administrator browser API creates refresh with
`POST /api/controller/nodes/{node_id}/refresh` and reads status with
`GET /api/controller/nodes/{node_id}/operations/{operation_id}`. Creation supplies
canonical UUID operation/idempotency IDs and expected state/binding/generation.
Exact replay returns the same row; either ID reused with different intent conflicts.
A supported live node with an available matching projection is required. Old
nodes without `snapshot.refresh.v1` fail explicitly before queue insertion.

Controller SQLite is durable authority for queue, lease, correlation and terminal
result. Admission fails closed on storage failure. Limits: one pending operation
per node, 64 retained rows per node, 4096 globally, five seconds between new node
requests and at most 64 globally per minute. Expiry is two minutes; terminal replay
is retained until 24 hours after expiry. Pruning runs in admission/poll/status
transactions; quotas reject rather than discard unexpired replay records.
Requests/results are strict allowlisted JSON capped at 4096 bytes.

Delivery is at least once. DB-off nodes remain DB-off. The single outbound worker
owns the per-boot snapshot sequence and keeps at most one pending result in memory.
A crash can repeat the read-only collection/upload under a reclaimed lease; there
is no exactly-once execution claim. Controller permits only one terminal transition
and identical terminal replay under current authority. A restarted or renewed node
must establish its current presence before reclaiming unfinished work.

## Lease, correlation and generation

`POST /control/v1/node/operations/poll` carries current session, boot/sequence,
state/binding and generation. Only the verified registry mTLS identity supplies
node/controller identity. One held poll per node and at most eight globally reserve
transport capacity for presence/renewal. Wait is 0–5 seconds, below the 30-second
server write timeout and 15-second client timeout. Wait never holds an application
mutation lock or SQLite transaction; each bounded dequeue rechecks authority.

A 40-second lease is bound to exact current certificate, session, boot and epochs.
Duplicate polls return the same active delivery; expired or superseded-session
leases get a fresh UUID fence. Expired operations are never redelivered.
Snapshot upload carries optional operation/lease correlation. In the same
transaction as accepting a monotonically newer snapshot, the controller records
its sequence against that exact unexpired lease. Periodic snapshots cannot supply
this evidence. Result upload at the exact UUID-only route
`PUT /control/v1/node/operations/{operation_id}/result` succeeds only when it cites
the stored accepted sequence and matching expected generation/authority. Snapshot
acceptance survives later periodic projection replacement. Identical result replay
is acknowledged after expiry within retention only while its session/certificate
and binding remain current; a conflicting terminal result is rejected.

Local desired changes remain authoritative. A stale queued generation ends in
`conflict / generation_conflict`; the UI must read a new projection before an
explicit new refresh. A snapshot from a different generation cannot prove success.
The controller owns expiry, lease and completion timestamps; node clock cannot
create or prolong authority.

Cold restore removes all operation rows in the existing atomic authority reset,
keeps control disabled and revokes archived credentials. Schema 12 archives verify
strict envelopes/receipts, row consistency, ownership, bounds and certificate
references. Revocation/rebind fences pending work and every late request, including
keep-alive; no operation can reopen authority. Terminal browser status omits session,
certificate and lease fields. Fault tests and local runtime evidence are tracked
outside the repository; this document itself is not acceptance evidence.
