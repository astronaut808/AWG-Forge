# Control node certificate registry checkpoint

Status: merged into `develop` in PR #110. This document describes internal
prerequisites, not an enabled node-management feature. The control listener
remains closed and no enrollment or renewal route is registered. Internal
renewal is being developed on `feature/control-node-cert-renewal`; the
[handoff plan](control-node-renewal-next-session.md) defines its bounds.

## Scope and sequence

PR #110 implemented the first bounded part of [PKI slice 3](control-tls-pki-plan.md):

1. Verify a bounded, signed, extension-free Ed25519 PKCS#10 CSR. Ignore all
   applicant-provided identity; issue a 30-day client-auth-only certificate
   under the already prepared control CA. The node retains its private key.
2. Commit an approved initial `node_id`/`controller_id`/`binding_epoch = 1`
   binding and the public certificate in one SQLite transaction before
   returning PEM. An exact full-CSR retry returns the original stored DER; a
   different CSR for that node conflicts. There is deliberately no route or
   approval workflow that can call the private application issuance method.
3. Resolve the presented certificate through the active issuer generation,
   serial, full certificate fingerprint, public-key fingerprint, current
   binding and revocation state on every request. The transport passes only
   the resulting typed identity to a handler. No header, URL, body or CSR
   subject can supply it. SQLite errors deny admission.

The schema reserves a supersession cutoff. Certificate and binding revocation
are durable primitives here; explicit rebind and renewal routes are not
exposed. `state.json` still decides whether the
control identity is prepared or enabled, while SQLite stores the certificate
registry. No tunnel revision or AWG state changes.

## Decision and trade-offs

`control_node_bindings` holds the current node binding; issued certificates
refer to it and record their own binding epoch. This extra join lets a later
explicit rebind or binding revocation fence every old serial immediately. A
registry row includes the full public DER and SHA-256 of the full CSR DER.
Keeping the DER costs some database space but makes exact retries return the
same certificate even if a fresh signature was computed before the registry
lookup. The `(issuer generation, serial)` key supports later CA generations.
The certificate fingerprint prevents a serial
lookup from becoming authorization by itself.

Signing happens before the SQLite transaction. A failed commit can leave only
an undisclosed, unusable certificate in memory; a successful commit makes the
record durable before PEM is returned. SQLite is configured with WAL and full
synchronous writes. The controller's CA key remains in the protected
generation files and never enters the database. A missing database, a revoked
record in the current database, corrupt key, or expired CA fails closed. A
restored backup that predates revocation is handled by the later restore gate
before any listener is enabled.

## Remaining checkpoints before exposure

The internal renewal operation verifies the presented predecessor against the
active CA and current SQLite binding before signing. Its transaction repeats
the predecessor and binding check, inserts a linked successor, and sets the
old serial's cutoff. Migration `000007` adds a unique predecessor link, so
one original certificate can have at most one successor even across processes.
The first renewal is eligible at exactly two thirds of the predecessor's
actual certificate validity interval; there is no recovery exception for an
expired certificate. An exact full-CSR DER retry returns the stored public DER
while the predecessor can still authenticate, without extending the cutoff.
A different CSR conflicts, even with the same new key. The cutoff is the earlier
of 24 hours after renewal and the predecessor's expiry. Serial revocation
fences that serial; binding revocation fences both old and new. The operation
does not change `ControlIdentityState.Enabled` or publish a route.

1. Add explicit rebind fencing and server-leaf rotation with immutable
   generations, failure injection, and expiry behavior. CA trust rotation
   remains a later staged operation requiring node acknowledgement.
2. Reconcile controller backup/restore with the new registry. An old backup
   must not silently undo a revocation: until a replay fence exists, restored
   node certificates require fail-closed re-enrollment. Verify the archived
   SQLite schema and PKI files together.
3. Only then wire an explicit loopback enablement transition. Non-loopback
   exposure waits for authenticated enrollment, its backup gate and the
   failure-matrix tests. The existing Web UI, standalone mode and DB-off mode
   remain independent throughout.

The [failure matrix](multi-node-failure-matrix.md) remains the release gate for
each capability when it becomes reachable.
