# Browser Control API

[`api/openapi.json`](../../api/openapi.json) is the OpenAPI 3.1 contract for the stable control-plane
requests used by the bundled Web UI. It covers authentication, controller activation,
control-listener lifecycle and enrollment approval, state, tunnel and client
operations, traffic limits, and WARP. Ordinary config/QR downloads, backup/restore,
diagnostics and support bundles remain private Web UI details outside the contract;
the dedicated controller enablement-backup route is documented.

This is **not** a public remote-management API. It has no CORS support and uses
an HttpOnly browser session cookie: signed in standalone mode, opaque and
SQLite-backed in controller mode. Keep it behind the normal AWG-Forge access
controls. Do not expose it to untrusted
origins or attempt to automate it with copied browser cookies.

## Mutation safety

The bundled UI sends an `Idempotency-Key` header for tunnel, client, traffic-limit
and WARP mutations. Authentication, controller lifecycle and enrollment approval
use their own transaction and replay rules, not this response cache. The key is optional for
backward compatibility, but callers without one do not receive replay protection.
It is limited to 128 bytes and scoped to the operation. Repeating the same
request body replays its original JSON result for ten minutes. Reusing a key
with a different request body returns `409` with the code
`idempotency_key_reused` rather than applying a second mutation.

Every response includes `X-Request-ID`. Use it to correlate a failed UI action
with runtime logs or a support bundle. Error responses retain the existing
human-readable `error` field and add a stable machine-readable `code` field.
They do not include internal command output, paths, or secrets.

## Contract evolution

The initial contract is a compatibility boundary for the bundled UI, not a
promise of third-party API stability. Future external multi-node automation
will introduce a separately versioned `/api/v1` surface with scoped API tokens,
TLS-only access, pagination, and an intentionally designed authentication model.
It will not reuse browser session cookies as an external credential.

## Snapshot refresh delivery

`POST /api/controller/nodes/{node_id}/refresh` requires the existing administrator
session and same-origin policy. Its strict JSON body includes UUID `operation_id`,
UUID `idempotency_key`, `expected_state_epoch`, `expected_binding_epoch` and
`expected_desired_generation`. Both IDs belong in the body; this route uses durable
SQLite replay semantics rather than the local mutation response cache. Identical
intent returns the same operation; changed intent with either reused ID returns
`409 operation_fenced`. The UI reads safe status with
`GET /api/controller/nodes/{node_id}/operations/{operation_id}`. Responses are
`no-store` and disclose no lease, session or certificate credentials.

Limits are 4096-byte strict requests/results, one pending request per node, 64
retained rows per node, 4096 globally, at least five seconds between new requests
per node and 64 admissions per minute globally. Capacity returns `429
operation_limit`; unsupported capability returns `409 refresh_unsupported`, absent
current presence/projection `409 node_unavailable`, and storage failure `503
control_unavailable`. TTL is two minutes and retention ends 24 hours after expiry.
Terminal statuses include succeeded, conflict, failed, expired and rejected.

The implemented mTLS routes are `POST /control/v1/node/operations/poll` and
`PUT /control/v1/node/operations/{operation_id}/result`; see the
[internal control contract](../../api/control-v1.openapi.json) and
[delivery design](../design/snapshot-refresh-delivery.md). The controller checks
current identity, lease and accepted snapshot correlation before recording success.
This internal protocol does not introduce a public operator API.
