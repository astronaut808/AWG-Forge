# Client create and delete delivery checkpoint

The `client.create` and `client.delete` variants extend the existing bounded
client operation queue and node transaction. They require `client.create.v1` or
`client.delete.v1` respectively. Tunnel mutations and remote secret export remain
outside this checkpoint.

## Immutable contract and admission

Same-origin administrator requests use
`POST /api/controller/nodes/{node_id}/client-create` or `client-delete`.
Both carry the existing operation/idempotency UUIDs, expected state/binding epochs
and desired generation, and exact tunnel/client IDs. Create adds only `name`,
validated by the existing local ASCII name rule (1–64 characters). Its stable
resource ID is eight random bytes rendered as 16 lowercase hex characters, chosen
once while preparing the controller browser intent. Delete requires
`confirmed: true`. Null, absent, unknown and mismatched fields are rejected.
The controller checks the selected node capability, live authority and current
projection before inserting. Missing tunnel/client returns `tunnel_unavailable`
or `client_unavailable`; an existing create ID returns `resource_conflict`.
No client is adopted or overwritten. Exact retained replay bypasses target
existence checks and returns the original operation.

## Node transaction and recovery

The existing cross-process mutation lock covers boot/epoch/generation preflight,
durable accepted intent, candidate construction, render/apply, rollback and atomic
state/receipt commit. Keys, PSK and IPv4 allocation are performed only on the node,
using the local candidate builder and defaults. Other clients' keys and
`ConfigRevision` do not change. Deletion checks the exact tunnel association and
applies the resulting peer set to that tunnel. Receipt commit advances desired
generation exactly once. Resource-ID collision, missing target and unavailable
address are fixed failures without state commit.

Successful create returns only `create_result`: committed generation, tunnel ID,
client ID and the intent's name. Delete returns only `delete_result`: committed
generation, tunnel ID, client ID and `deleted: true`. Codes are
`client_created_committed` and `client_deleted_committed`. No result contains
credentials, addresses, configuration or QR/export data. Replay reconstructs the
original safe result from the atomic receipt and full intent, regardless of a
later local deletion or other local change. It never regenerates keys or resources.

After durable deletion, the application attempts idempotent SQLite traffic-policy
cleanup under the lock; operational history remains. Cleanup failure cannot turn
committed deletion into failure or resurrect a client. Replay retries cleanup
unless a later local client has reused that resource ID. Node DB-off stays off.
Accepted intent without a receipt first recovers runtime from committed state and
then fails as `interrupted_rolled_back`; it is never blindly executed again.
Uncertain directory sync retains the runtime journal for receipt reconciliation.

The same 256-entry/2 MiB protected journal, 256 successful receipts, controller
queue/TTL/lease/retention bounds and result/ACK handshake apply to every client
variant. Delivered intents past deadline reconcile only. Current certificate,
session, boot and epochs fence results and ACKs. Strict saved validators retain
old enabled intents/results and schema13 databases; no migration is needed.
Cold controller restore removes all operation authority and keeps control disabled.

## Browser meaning

Confirmation freezes the full intent and displayed node/tunnel/client/name/action.
A changed projection requires fresh confirmation before first submission. Uncertain
POST retries preserve operation, idempotency and resource IDs and exact payload.
Only the exact safe result proves commit. A fresh projection at the committed
generation must show that ID present for create or absent for delete; later
generations may reflect legitimate subsequent local changes. Unsupported, offline
and stale contexts disable actions. Switch, logout and unmount cancel requests.
No intent or secret is persisted in browser storage. EN/RU desktop/mobile flows
use the existing product UI. Remote configuration/QR export is unavailable.
