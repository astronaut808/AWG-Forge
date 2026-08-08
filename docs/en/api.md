# Browser Control API

[`api/openapi.json`](../../api/openapi.json) is the OpenAPI 3.1 contract for the stable control-plane
requests used by the bundled Web UI. It currently covers authentication, state,
tunnel and client lifecycle operations, traffic limits, and WARP management.
Download, QR, backup, restore, diagnostics, and support-bundle endpoints remain
private Web UI details and are intentionally outside this initial contract.

This is **not** a public remote-management API. It has no CORS support and uses
the signed, HttpOnly browser session cookie issued by `POST /api/login`. Keep it
behind the normal AWG-Forge access controls. Do not expose it to untrusted
origins or attempt to automate it with copied browser cookies.

## Mutation safety

The bundled UI sends an `Idempotency-Key` header for each documented
state-changing endpoint except login and logout. The key is optional for
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
promise of third-party API stability. Future multi-node or subscription work
will introduce a separately versioned `/api/v1` surface with scoped API tokens,
TLS-only access, pagination, and an intentionally designed authentication model.
It will not reuse browser session cookies as an external credential.
