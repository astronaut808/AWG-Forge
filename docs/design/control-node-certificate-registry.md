# Control node certificate registry checkpoint

Status: the registry authorizes explicitly enabled loopback enrollment, presence
and certificate renewal. External listeners and remote management operations
remain unimplemented. The same-node revoked-binding rebind primitive below remains
internal; Linux-root `node rebind` instead performs fresh enrollment with a new
node ID and does not call that primitive.

## Scope and sequence

PR #110 implemented the first bounded part of [PKI slice 3](control-tls-pki-plan.md):

1. Verify a bounded, signed, extension-free Ed25519 PKCS#10 CSR. Ignore all
   applicant-provided identity; issue a 30-day client-auth-only certificate
   under the already prepared control CA. The node retains its private key.
2. Commit an approved initial `node_id`/`controller_id`/`binding_epoch = 1`
   binding and the public certificate in one SQLite transaction before
   returning PEM. An exact full-CSR retry returns the original stored DER; a
   different CSR for that node conflicts. The loopback enrollment workflow
   requires explicit recent-auth administrator approval before issuance.
3. Resolve the presented certificate through the active issuer generation,
   serial, full certificate fingerprint, public-key fingerprint, current
   binding and revocation state on every request. The transport passes only
   the resulting typed identity to a handler. No header, URL, body or CSR
   subject can supply it. SQLite errors deny admission.

The schema stores a supersession cutoff. Certificate and binding revocation
are durable primitives; renewal is exposed only through the loopback node protocol.
`state.json` still decides whether the
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
restored backup that predates revocation is handled by controller restore:
every archived certificate and binding is revoked before completion, preserving
history. Administrator recovery never restores that authority.

## Internal renewal and rebind fencing

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
does not change `ControlIdentityState.Enabled`. The authenticated loopback renewal
route calls this operation; it cannot recover an expired predecessor.

The internal rebind recovery primitive only advances a **revoked binding on
the same controller**. A caller supplies the exact old node/controller/epoch
tuple after separate local recovery authorization; no production caller exists
yet. The transaction checks that tuple is still the revoked current binding,
increments the epoch once, clears revocation for the new epoch, and inserts a
fresh initial certificate. The new public certificate is returned only after
commit. Old serials retain their recorded epoch and immediately fail the
per-request binding join, including on keep-alive connections. Migration
`000008` enforces one initial certificate per node and epoch. An exact full-CSR
retry against the immediately previous epoch recovers the committed DER while
the new binding and certificate remain active; a competing CSR or stale epoch
conflicts. The new key cannot reuse a public key previously certified for that
node. Failure before commit leaves the previous binding revoked.

This primitive does not transfer a node to another controller, modify node
`state.json`, install a key or certificate on a node, or authorize a remote
caller to rebind. The implemented [offline recovery](../en/security.md#offline-node-recovery)
holds the server's exclusive state lease, confirms old node/controller UUIDs and
uses fresh pinned enrollment and administrator approval. It installs a new node
ID/state epoch and replay namespace, preserving the old registry record and
local configuration. A controller cannot transfer a node remotely. Copied old
credentials require separate revocation at the former controller when applicable.

## Integrated lifecycle and remaining exposure gates

1. Automatic [server-leaf rotation](control-tls-pki-plan.md)
   stages immutable generations under the existing CA, commits only the
   active server generation, closes pre-switch connections, and retires the
   predecessor through durable journal recovery. The worker belongs to the
   control runtime owner and stops on cancellation, expiry or uncertain commit.
   CA trust rotation remains a later staged operation requiring
   node acknowledgement.
2. Controller backup/restore validates SQLite schema, registry and PKI together
   and atomically invalidates all restored browser/node authority. Nodes need
   explicit local recovery and fresh enrollment through the offline CLI.
3. The application owner implements explicit loopback enablement with a
   fresh verified backup, recent-auth session receipt, socket reservation before
   commit, disable/restart and live leaf rotation. Browser lifecycle handlers
   require recent authentication and confirmation that the verified backup was
   retained. Non-loopback exposure, installer integration, fleet UI and operation
   delivery require separate implementation and failure-matrix tests. The existing
   standalone Web UI and DB-off node behavior remain independent throughout.

The [failure matrix](multi-node-failure-matrix.md) remains the release gate for
each capability when it becomes reachable.
