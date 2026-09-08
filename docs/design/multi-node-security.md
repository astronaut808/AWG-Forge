# Multi-node security model

Status: proposed. This threat model applies to the design in
[`multi-node-v1.md`](multi-node-v1.md).

## Protected assets

- node and controller private identity keys;
- controller CA private key;
- administrator credentials, TOTP secrets, sessions, and recovery codes;
- tunnel and client private keys and preshared keys;
- raw `.conf`, QR, and `vpn://` client artifacts;
- WARP credentials;
- node-local desired state and monotonic revision metadata;
- integrity of remote operations and audit history;
- availability of existing tunnel forwarding.

## Trust boundaries

1. Browser to controller Web UI: hostile Internet input reaches an authenticated
   administrative surface.
2. Node to controller enrollment: no node certificate exists yet; security
   depends on verified server TLS, the invitation secret, CSR possession, and
   explicit human approval.
3. Enrolled node to control listener: mTLS authenticates both endpoints, while
   application checks certificate status, node identity, binding epoch, and
   authorization.
4. Controller to node application services: only typed operations may cross
   from remote input into local state mutation.
5. `state.json` to SQLite: desired state and operational history have different
   authority and cannot be committed atomically by the databases themselves.
6. Controller to browser artifact delivery: a transient secret crosses the
   controller but must not become durable controller data.
7. Host root boundary: a root compromise can access local keys and is outside
   the protection offered by application-level encryption.

## Security invariants

- Control traffic never supports plaintext or disabled TLS verification.
- Enrollment secrets never appear in URLs, command arguments, environment,
  Compose, logs, audit events, support bundles, or database plaintext.
- Node private keys are generated and retained on the node.
- Every non-enrollment control request requires a verified, active node
  certificate and matching controller binding.
- Forwarded certificate headers never establish node identity.
- Remote operations cannot invoke shell commands, Docker, arbitrary paths,
  arbitrary files, or environment changes.
- Controller snapshots are allowlists and contain no secret material.
- A mutating success is reported only after the desired state and its operation
  receipt are durably committed together.
- Existing forwarding never depends on controller availability.
- Standalone authentication and behavior do not change before explicit
  controller activation or node enrollment.

## Threats and mitigations

| Threat | Required mitigation | Residual risk |
| --- | --- | --- |
| Invitation copied from terminal or UI | Separate non-secret ID from hidden high-entropy secret; short TTL; single CSR claim; hash at rest; rate limit; consume atomically | A live secret plus controller pin can be used before the legitimate node claims it |
| Enrollment MITM | Version-pinned command carries CA/SPKI pin; TLS verification has no bypass; compare code binds invitation and CSR | Compromised controller UI/host can issue a malicious command |
| Enrollment replay | Bind invitation to first accepted CSR; explicit approval; repeated claims return `410`; bounded claim token | A controller database rollback requires epoch/replay reconciliation |
| Managed backup restored onto another installation | Compare complete persisted managed identity before writing; explicit local detach removes controller authority and replay metadata before reuse | A full raw filesystem clone duplicates the comparison metadata and cannot be distinguished locally |
| Raw node data directory cloned | Keep the clone offline until explicit local detach; detect duplicate active identity at the controller and revoke/re-enroll one side | A clone has the old private key and can impersonate the node until controller revocation is enforced |
| Stolen node certificate without key | Short lifetime and serial tracking; proof of private-key possession on renewal | Certificate metadata may reveal node identity |
| Stolen node key and certificate | Revoke node and increment binding epoch; audit reconnects; require local re-enrollment | Attacker can act as node until revocation reaches the controller |
| Controller database disclosure | Store only hashes for bearer credentials; encrypt TOTP secrets with a key held outside SQLite; keep the control CA key in a separate root-only `0600` file; keep node/client secrets out of DB | These boundaries do not protect against full host compromise |
| Full controller compromise | Nodes accept only typed operations and generation/epoch checks; local state remains authoritative; retain local detach/rebind | Controller can issue authorized destructive operations while trusted |
| Malicious or compromised node | Per-node identity; strict node-to-resource mapping; bounded schemas; snapshots treated as untrusted observations | Controller UI may display false health information from that node |
| Operation replay | Immutable operation ID, idempotency key, expiry, expected generation, and a success receipt committed with desired state | Database restore may reintroduce old queued work; generation and receipts must reject duplicate execution |
| Out-of-order mutation | One mutation lease per node; expected generation conflict; no blind last-write-wins | Long-running operations may delay later work |
| Crash between runtime apply and state save | Explicit commit protocol and startup reconciliation to persisted desired state | Brief runtime divergence before reconciliation |
| Secret artifact logged or retained | Dedicated in-memory TTL store, no-store response, redaction tests, no durable operation payload | Secret exists in controller memory while being relayed |
| Offline destructive command surprises operator | No implicit offline queue; explicit **Run when online**, visible expiry, cancellation | Operator can still intentionally queue a harmful action |
| Long-poll resource exhaustion | One poll per node, body and concurrency limits, server timeouts, jittered reconnect, global quotas | A large legitimate fleet still requires capacity measurement |
| Reverse proxy impersonates node | Dedicated built-in mTLS listener; ignore forwarded certificate identity | Misconfigured public listener can increase DoS exposure |
| Expired certificate strands node | Renew before final lifetime third; overlap old/new certs; local re-enrollment path | Long controller outage past expiry requires local recovery |
| Controller restored twice | Stable controller ID plus documented single-active restore; detect duplicate session patterns | No distributed lease can prove uniqueness during a partition in v1 |
| TOTP replay or credential stuffing | Mandatory TOTP, last-step replay rejection, Argon2id, account/source/global rate limits, recent-auth checks | Phishing and full browser compromise remain possible |
| Cross-site browser action | Same-origin cookies, CSRF/origin enforcement, no CORS, secure headers, recent-auth for critical actions | Same-origin XSS would retain controller privileges |

## Authorization model

The first controller release has one administrator role. This is deliberate:
adding cosmetic roles before defining resource-level authorization would create
false confidence.

Node mTLS identity authorizes only node protocol actions for that node. It is
never accepted by the browser API or future operator API. Browser sessions are
never accepted on the node control listener.

The future external operator API uses scoped, expiring opaque tokens whose
digests are stored server-side. It must not reuse browser cookies or node
certificates.

## Credential lifecycle

- Node certificates are short-lived and renew before the final third of their
  lifetime.
- Renewal requires the current valid node identity and a new node-generated CSR.
- Certificate rotation permits a bounded overlap and revokes superseded serials.
- Control CA rotation is staged: distribute old plus new trust, issue new node
  certificates, confirm adoption, then retire old trust.
- Recovery codes are generated once, displayed once, hashed at rest, and
  consumed atomically.
- Session rotation invalidates the previous token; logout and administrator
  recovery revoke all selected sessions.

Exact lifetimes and Argon2id parameters are implementation decisions that must
be benchmarked and documented with the code. They are not hard-coded in this
design document.

## Logging and support bundles

Allowed:

- request/operation IDs, node ID, safe operation kind, state transition, status,
  latency, version, capability names, and redacted error code;
- certificate serial/fingerprint metadata when needed for audit;
- redacted node health and tunnel identifiers already safe in the local UI.

Forbidden:

- invitation or claim tokens;
- session, recovery, or operator bearer tokens;
- private keys, PSKs, TOTP secrets, or controller CA private key;
- raw CSR private material;
- full client configs, QR data, `vpn://` links, or WARP credentials;
- arbitrary request bodies or raw command output.

Security tests must scan normal logs, audit records, operation rows, database
backups, and support bundles for seeded canary secrets.

## Recovery authority

Local root access is the final authority for a node. It may detach or explicitly
rebind the node without changing tunnels. A controller cannot remotely transfer
a node to another controller.

Controller backup is encrypted and includes its identity, CA, authentication
database, registry, and operation journal. Restoring without the identity is a
new controller and requires explicit node rebind.

## Required security tests

- real TLS/mTLS handshakes with wrong CA, wrong node, expired, revoked, and
  overlapping renewal certificates;
- enrollment replay, competing CSR, expired invitation, wrong pin, rejection,
  and approval timeout;
- request-size, concurrency, source/account/global rate-limit, and reconnect
  backoff tests;
- operation replay, stale epoch/generation, unsupported type, expiry, and
  cross-node authorization tests;
- canary-secret absence from logs, SQLite, support bundles, and backups;
- browser CSRF/origin, session rotation, TOTP replay, recovery-code reuse, and
  recent-auth tests;
- controller outage and full node restart while existing tunnels continue.
