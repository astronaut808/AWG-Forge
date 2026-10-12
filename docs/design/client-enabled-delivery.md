# Client enabled delivery checkpoint

`client.enabled.set` is the only delivered mutation. It changes one existing
client's `enabled` flag on an enrolled node; it neither creates nor deletes a
client and never exports a client secret, configuration, QR payload, or import
link. This is an implemented checkpoint, not evidence of a released multi-host
control-plane feature.

## Intent, commit, and delivery

The same-origin administrator API creates an operation at
`POST /api/controller/nodes/{node_id}/client-enabled`; the existing operation
status route reads it. The strict JSON request contains canonical operation and
idempotency UUIDs, state/binding epochs, expected desired generation, tunnel ID,
client ID, and the explicit boolean `enabled` value. The controller hashes the
full intent. Exact replay returns its retained operation; reusing either ID for a
different full intent conflicts. A node must advertise `client.enabled.set.v1`,
be online, and have a current matching projection before admission.

Node execution validates the exact typed intent, then checks the current owned
worker boot sequence, epochs and binding and durably accepts it under the existing
mutation lock. It uses the established local render/apply/rollback transaction. A successful no-op still advances
`desired_generation` by one. Success is durable only when the new `state.json`,
successful bounded receipt, and generation are atomically committed together; a
SQLite mirror is operational history, never commit authority. The receipt names
the hash binding the full intent to the returned safe result and committed
generation. Local changes advance generation and make an older queued operation
conflict.

Remote manual disable follows the same local mutation path: before commit it
clears an automatic quota-release marker under that lock and fails closed if its
operational storage cannot be updated. Enable retains the existing quota checks
and post-commit marker cleanup. Detach/rebind retire the protected outcome journal
only after the replacement state commit is proven; every backup excludes it.

The controller queue retains one active operation per node, at most 64 retained
rows per node and 4096 globally. Operations expire after two minutes. Before
expiry, delivery may repeat under a current lease. After expiry a saved node
receipt may be requested only for reconciliation; it cannot begin a new
execution. Node journal `.client-operations.json` is root-private mode `0600`,
bounded to 256 entries and 2 MiB, and preserves only safe typed outcomes. DB-off
nodes remain supported: the journal, rather than SQLite, supplies durable outcomes.

The controller does not discard a successful node receipt merely because the
normal 24-hour retention period passed. The node first acknowledges controller
receipt, and controller confirmation is then required before pruning;
unacknowledged receipts stay safely bounded and fail closed. Restore deletes
controller queue authority while control remains disabled. Rebind, revocation,
changed epochs, expired sessions, old certificates, and old boot authority fence
execution and late acknowledgement. A current mTLS session can carry a durable
outcome from an earlier session only after all current authority and exact-intent
checks; it cannot revive the earlier authority.

The node sends its acknowledgement only after it durably records the successful
`PUT /result` acknowledgement. If a current, authenticated acknowledgement reaches
the controller after a lost response and the retained row has already been pruned,
the controller returns `204` and creates no row. That response is not evidence of
a new state commit and no missing retained row requires acknowledgement.

## Browser meaning

The UI asks for confirmation and displays the selected node/client and exact
target state. `queued` and `leased` report delivery progress. `reconciling`
means a node-side saved outcome is still being confirmed, not that the client
changed. `succeeded` reports the durable node commit but remains pending until a
new controller projection reaches the returned committed generation. Missing or
mismatched result fields never show success. On uncertain create/result, retry
uses the same full intent. Switching node/account/epoch or logging out aborts and
discards the browser context.

The change preserves each client's existing `ConfigRevision` semantics. Enabling
continues to use the node's existing quota/traffic-limit checks; the controller
does not bypass local policy.
