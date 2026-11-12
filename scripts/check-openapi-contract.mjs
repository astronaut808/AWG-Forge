import fs from "node:fs";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const document = readDocument("api/openapi.json");
const controlDocument = readDocument("api/control-v1.openapi.json");
const schemas = document.components?.schemas;
if (!schemas) {
  throw new Error("OpenAPI components.schemas is missing");
}

const ajv = new Ajv2020({ allErrors: true, strict: true });
addFormats(ajv);

const serverName = ajv.compile(schemas.UpdateServerNameRequest);
const createClient = ajv.compile(schemas.CreateClientRequest);
const updateClient = ajv.compile(schemas.UpdateClientSettingsRequest);
const profile = ajv.compile(schemas.Profile);
const controllerActivation = ajv.compile(schemas.ControllerActivationRequest);
const controllerLogin = ajv.compile(schemas.ControllerLoginRequest);
const controlEnable = ajv.compile(schemas.ControlEnableRequest);
const enrollmentDecision = ajv.compile(schemas.EnrollmentDecisionRequest);
const enrollmentOnboardingStatus = ajv.compile(schemas.EnrollmentOnboardingStatus);
const schemaUUID = "103354a0-e154-4d4c-bfde-f71dbbc7394f";
const schemaPassword = "p".repeat(16);
const schemaTOTPSecret = "A".repeat(32);
const schemaCode = "1".repeat(6);
const cases = [
  ["server name set", serverName, { name: "Office02" }, true],
  ["server name clear", serverName, { name: "" }, true],
  ["server name missing", serverName, {}, false],
  ["server name null", serverName, { name: null }, false],
  ["server name invalid", serverName, { name: "invalid-name" }, false],
  ["server name too long", serverName, { name: "a".repeat(33) }, false],
  ["control enable requires saved backup", controlEnable, { receipt: schemaUUID, backup_saved: true }, true],
  ["control enable permits explicit external consent", controlEnable, { receipt: schemaUUID, backup_saved: true, allow_non_loopback: true }, true],
  ["control enable permits default loopback restriction", controlEnable, { receipt: schemaUUID, backup_saved: true, allow_non_loopback: false }, true],
  ["control enable rejects string consent", controlEnable, { receipt: schemaUUID, backup_saved: true, allow_non_loopback: "true" }, false],
  ["control enable rejects null consent", controlEnable, { receipt: schemaUUID, backup_saved: true, allow_non_loopback: null }, false],
  ["control enable rejects unsaved backup", controlEnable, { receipt: schemaUUID, backup_saved: false }, false],
  ["control enable rejects absent confirmation", controlEnable, { receipt: schemaUUID }, false],
  ["enrollment decision requires explicit approval", enrollmentDecision, { enrollment_id: schemaUUID, verification_code: "ABCD-EFGH", approve: true }, true],
  ["enrollment decision supports explicit rejection", enrollmentDecision, { enrollment_id: schemaUUID, verification_code: "ABCD-EFGH", approve: false }, true],
  ["enrollment decision rejects implicit rejection", enrollmentDecision, { enrollment_id: schemaUUID, verification_code: "ABCD-EFGH" }, false],
  ["enrollment decision rejects null", enrollmentDecision, { enrollment_id: schemaUUID, verification_code: "ABCD-EFGH", approve: null }, false],
  ["onboarding status exposes only its public fields", enrollmentOnboardingStatus, { invitation_id: schemaUUID, controller_id: schemaUUID, status: "approved", connected: true, expires_at: "2026-09-05T12:00:00Z", binding_epoch: 1 }, true],
  ["onboarding status rejects secrets", enrollmentOnboardingStatus, { invitation_id: schemaUUID, controller_id: schemaUUID, status: "approved", connected: true, expires_at: "2026-09-05T12:00:00Z", certificate: "secret" }, false],
  ["controller activation requires MFA confirmation", controllerActivation, { username: "admin", password: schemaPassword, totp_secret: schemaTOTPSecret, confirmation_code: schemaCode }, true],
  ["controller activation rejects missing MFA confirmation", controllerActivation, { username: "admin", password: schemaPassword, totp_secret: schemaTOTPSecret }, false],
  ["controller login requires the second factor", controllerLogin, { username: "admin", password: schemaPassword, code: schemaCode }, true],
  ["controller login rejects a token in the body", controllerLogin, { username: "admin", password: schemaPassword, code: schemaCode, token: "x".repeat(8) }, false],
  ["create without expiration", createClient, { tunnel_id: "tunnel-1", name: "phone" }, true],
  ["create with empty expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "" }, true],
  ["create with RFC3339 expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "2026-09-05T12:00:00Z" }, true],
  ["create with invalid expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "tomorrow" }, false],
  ["update with empty expiration", updateClient, { name: "phone", notes: "", expires_at: "" }, true],
  ["update with RFC3339 expiration", updateClient, { name: "phone", notes: "", expires_at: "2026-09-05T12:00:00Z" }, true],
  ["update with invalid expiration", updateClient, { name: "phone", notes: "", expires_at: "tomorrow" }, false],
  ["complete profile catalog entry", profile, { id: "awg_3", name: "AmneziaWG 3.x", tab: "3.x", label: "Experimental", experimental: true, available: true, suggested_name: "awg3", suggested_port: 51840, suggested_subnet: "10.30.0.0/24" }, true],
  ["profile without display name", profile, { id: "awg_3", tab: "3.x", label: "Experimental", experimental: true, available: true, suggested_name: "awg3", suggested_port: 51840, suggested_subnet: "10.30.0.0/24" }, false],
];

for (const [name, validate, value, expected] of cases) {
  assertValidation(name, validate, value, expected, ajv);
}

const controlSchemas = controlDocument.components?.schemas;
if (!controlSchemas) {
  throw new Error("Control OpenAPI components.schemas is missing");
}
if (controlDocument.openapi !== "3.1.1") {
  throw new Error(`Control OpenAPI version must be 3.1.1, got ${controlDocument.openapi}`);
}
if (controlDocument.info?.version !== "0.1.0-draft") {
  throw new Error("The unimplemented control contract must retain an explicit draft version");
}

const controlAjv = new Ajv2020({ allErrors: true, discriminator: true, strict: true });
addFormats(controlAjv);
controlAjv.addSchema({
  $id: "control",
  $schema: "https://json-schema.org/draft/2020-12/schema",
  $defs: normalizeControlSchemas(controlSchemas),
});

const exportRequestSchema = ajv.compile(schemas.ClientConfigExportRequest);
const exportLocatorSchema = ajv.compile(schemas.ClientConfigExportLocator);
const exportMetadataSchema = ajv.compile(schemas.ClientConfigExport);
const exportDeliverySchema = requireSchema(controlAjv, "ClientConfigExportDelivery");
const exportResultSchema = requireSchema(controlAjv, "ClientConfigExportResult");
const exportRequest = { expected_state_epoch: schemaUUID, expected_binding_epoch: 1, expected_desired_generation: 0, tunnel_id: "aaaaaaaaaaaaaaaa", client_id: "bbbbbbbbbbbbbbbb", expected_tunnel_revision: 1 };
const exportAuthority = { session_id: schemaUUID, boot_id: schemaUUID, boot_sequence: 1, state_epoch: schemaUUID, binding_epoch: 1, desired_generation: 0, wait_seconds: 0 };
const exportCases = [
  ["export exact prepare", exportRequestSchema, exportRequest, true, ajv],
  ["export rejects null generation", exportRequestSchema, { ...exportRequest, expected_desired_generation: null }, false, ajv],
  ["export rejects paths", exportRequestSchema, { ...exportRequest, client_id: "../escape" }, false, ajv],
  ["export rejects secret prepare", exportRequestSchema, { ...exportRequest, config: "secret" }, false, ajv],
  ["export exact locator", exportLocatorSchema, { export_id: schemaUUID }, true, ajv],
  ["export rejects secret metadata", exportMetadataSchema, { export_id: schemaUUID, node_id: schemaUUID, tunnel_id: "aaaaaaaaaaaaaaaa", client_id: "bbbbbbbbbbbbbbbb", expires_at: "2026-10-13T00:00:00Z", config: "secret" }, false, ajv],
  ["export delivery", exportDeliverySchema, { export_id: schemaUUID, request: exportRequest, authority: exportAuthority, expires_at: "2026-10-13T00:00:00Z" }, true, controlAjv],
  ["export rejects held poll", exportDeliverySchema, { export_id: schemaUUID, request: exportRequest, authority: { ...exportAuthority, wait_seconds: 5 }, expires_at: "2026-10-13T00:00:00Z" }, false, controlAjv],
  ["export ready result", exportResultSchema, { export_id: schemaUUID, authority: exportAuthority, status: "ready", config: "disposable" }, true, controlAjv],
  ["export fixed failure", exportResultSchema, { export_id: schemaUUID, authority: exportAuthority, status: "failed", config: "" }, true, controlAjv],
  ["export failure forbids secret", exportResultSchema, { export_id: schemaUUID, authority: exportAuthority, status: "failed", config: "secret" }, false, controlAjv],
  ["export rejects oversized config", exportResultSchema, { export_id: schemaUUID, authority: exportAuthority, status: "ready", config: "x".repeat(16385) }, false, controlAjv],
];
for (const [name, validate, value, expected, validator] of exportCases) {
  assertValidation(name, validate, value, expected, validator);
}

const enrollmentClaim = requireSchema(controlAjv, "EnrollmentClaimRequest");
const enrollmentBootstrap = requireSchema(controlAjv, "EnrollmentBootstrap");
if (!enrollmentBootstrap({ ca_cert_pem: "public CA" }) || enrollmentBootstrap({ ca_cert_pem: "public CA", secret: "test canary" }) || enrollmentBootstrap({ ca_cert_pem: "x".repeat(8193) })) {
  throw new Error("Bootstrap must contain bounded public CA material only");
}
const certificateRenewal = requireSchema(controlAjv, "CertificateRenewalRequest");
const nodePresence = requireSchema(controlAjv, "NodePresence");
const presenceAccepted = requireSchema(controlAjv, "PresenceAccepted");
const nodeSnapshot = requireSchema(controlAjv, "NodeSnapshot");
const nodeInventory = ajv.compile(normalizeControlSchemas({ ...schemas.NodeInventory, $defs: { NodeView: schemas.NodeView } }));
const operationEnvelope = requireSchema(controlAjv, "OperationEnvelope");
const problem = requireSchema(controlAjv, "Problem");

const ids = {
  boot: "103354a0-e154-4d4c-bfde-f71dbbc7394f",
  epoch: "7238def3-36b3-4cdd-896a-7dd05571bd26",
  operation: "4c0fb636-e234-4726-85d5-38d95de9029f",
  session: "7e11fcbf-21fa-4b74-aa41-b0f62aee4e39",
};
const redactedSnapshot = {
  contract_version: 1, session_id: ids.session, boot_id: ids.boot, boot_sequence: 7,
  snapshot_sequence: 1, state_epoch: ids.epoch, binding_epoch: 1, desired_generation: 3,
  observed_at: "2026-09-08T00:00:00Z",
  desired: { tunnels: [{ id: "7d774c16b2c92872", name: "AWG 2", interface: "awg20", profile: "awg_2_0", enabled: true, listen_port: 49411, revision: 1,
    clients: [{ id: "12a6acf567ffb5ae", name: "phone", enabled: true, address: "10.28.0.2" }] }] },
  observations: { apply_enabled: true, history_available: false,
    tunnels: [{ id: "7d774c16b2c92872", known: true, up: true, apply_failed: false,
      clients: [{ id: "12a6acf567ffb5ae", present: true, last_handshake: "2026-09-08T00:00:00Z", rx_bytes: 1024, tx_bytes: 2048 }] }],
    doctor: { scope: "runtime", tun_available: true, forwarding: true, runtime_unknown: 0, tunnels_down: 0, apply_failures: 0 } },
};
const snapshotWithoutSequence = structuredClone(redactedSnapshot);
delete snapshotWithoutSequence.snapshot_sequence;
const snapshotWithoutSession = structuredClone(redactedSnapshot);
delete snapshotWithoutSession.session_id;

const controlCases = [
  ["inventory final page", nodeInventory, { controller_id: schemaUUID, nodes: [] }, true],
  ["inventory next page cursor", nodeInventory, { controller_id: schemaUUID, nodes: [], next_cursor: schemaUUID }, true],
  ["inventory rejects malformed cursor", nodeInventory, { controller_id: schemaUUID, nodes: [], next_cursor: "invalid" }, false],
  ["inventory rejects session material", nodeInventory, { controller_id: schemaUUID, nodes: [], session_id: schemaUUID }, false],
  [
    "valid enrollment claim",
    enrollmentClaim,
    {
      requested_name: "edge-1",
      csr_pem: `-----BEGIN CERTIFICATE REQUEST-----\n${"A".repeat(128)}\n-----END CERTIFICATE REQUEST-----`,
      boot_id: ids.boot,
      application_version: "v0.20.0",
      contract_versions: [1],
      capabilities: ["snapshot.read"],
    },
    true,
  ],
  [
    "certificate renewal request",
    certificateRenewal,
    {
      boot_id: ids.boot,
      current_serial: "123456789",
      csr_pem: `-----BEGIN CERTIFICATE REQUEST-----\n${"A".repeat(128)}\n-----END CERTIFICATE REQUEST-----`,
    },
    true,
  ],
  [
    "certificate renewal rejects an unknown field",
    certificateRenewal,
    {
      boot_id: ids.boot,
      current_serial: "123456789",
      csr_pem: `-----BEGIN CERTIFICATE REQUEST-----\n${"A".repeat(128)}\n-----END CERTIFICATE REQUEST-----`,
      node_id: ids.epoch,
    },
    false,
  ],
  [
    "enrollment rejects a private key",
    enrollmentClaim,
    {
      requested_name: "edge-1",
      csr_pem: `-----BEGIN CERTIFICATE REQUEST-----\n${"A".repeat(128)}\n-----END CERTIFICATE REQUEST-----`,
      private_key: "must-not-cross-the-boundary",
      boot_id: ids.boot,
      application_version: "v0.20.0",
      contract_versions: [1],
      capabilities: ["snapshot.read"],
    },
    false,
  ],
  [
    "presence carries a monotonic boot sequence",
    nodePresence,
    {
      boot_id: ids.boot,
      boot_sequence: 7,
      application_version: "0.20.0-unreleased",
      contract_version: 1,
      state_epoch: ids.epoch,
      binding_epoch: 1,
      desired_generation: 3,
      capabilities: ["snapshot.read"],
      observed_at: "2026-09-08T00:00:00Z",
    },
    true,
  ],
  [
    "presence rejects a missing boot sequence",
    nodePresence,
    {
      boot_id: ids.boot,
      application_version: "0.20.0-unreleased",
      contract_version: 1,
      state_epoch: ids.epoch,
      binding_epoch: 1,
      desired_generation: 3,
      capabilities: ["snapshot.read"],
      observed_at: "2026-09-08T00:00:00Z",
    },
    false,
  ],
  [
    "presence acceptance establishes a node session",
    presenceAccepted,
    {
      controller_id: "0e23d194-5036-4b4f-af38-e0a4fbeb6198",
      session_id: ids.session,
      session_expires_at: "2026-09-08T00:05:00Z",
      server_time: "2026-09-08T00:00:00Z",
      next_poll_seconds: 10,
      renew_certificate_after: "2026-10-08T00:00:00Z",
    },
    true,
  ],
  [
    "redacted snapshot",
    nodeSnapshot,
    redactedSnapshot,
    true,
  ],
  [
    "snapshot requires per-boot ordering",
    nodeSnapshot,
    snapshotWithoutSequence,
    false,
  ],
  [
    "snapshot requires the active controller session",
    nodeSnapshot,
    snapshotWithoutSession,
    false,
  ],
  [
    "snapshot rejects secret-shaped additions",
    nodeSnapshot,
    {
      ...redactedSnapshot,
      private_key: "must-not-cross-the-boundary",
    },
    false,
  ],
  [
    "typed snapshot refresh operation",
    operationEnvelope,
    {
      operation_id: ids.operation,
      idempotency_key: ids.session,
      issued_at: "2026-09-08T00:00:00Z",
      expires_at: "2026-09-08T00:05:00Z",
      expected_state_epoch: ids.epoch,
      expected_binding_epoch: 1,
      expected_desired_generation: 3,
      operation: { kind: "snapshot.refresh", schema_version: 1, payload: {} },
    },
    true,
  ],
  [
    "arbitrary shell operation is rejected",
    operationEnvelope,
    {
      operation_id: ids.operation,
      idempotency_key: ids.session,
      issued_at: "2026-09-08T00:00:00Z",
      expires_at: "2026-09-08T00:05:00Z",
      expected_state_epoch: ids.epoch,
      expected_binding_epoch: 1,
      expected_desired_generation: 3,
      operation: { kind: "shell.exec", schema_version: 1, payload: { command: "id" } },
    },
    false,
  ],
  [
    "RFC 9457 problem",
    problem,
    {
      type: "https://awg-forge.example/problems/stale-generation",
      title: "Stale desired generation",
      status: 409,
      code: "stale_desired_generation",
    },
    true,
  ],
];

for (const [name, validate, value, expected] of controlCases) {
  assertValidation(name, validate, value, expected, controlAjv);
}

assertControlSecurity(controlDocument);
assertNoForbiddenSnapshotFields(controlSchemas.NodeSnapshot);

const pollSchema = requireSchema(controlAjv, "OperationPollRequest");
const deliverySchema = requireSchema(controlAjv, "OperationDelivery");
const receiptSchema = requireSchema(controlAjv, "OperationReceipt");
const refreshSchema = ajv.compile(schemas.RefreshRequest);
const poll = { session_id: ids.session, boot_id: ids.boot, boot_sequence: 1, state_epoch: ids.epoch, binding_epoch: 1, desired_generation: 0, wait_seconds: 5 };
const refresh = { operation_id: ids.operation, idempotency_key: ids.session, expected_state_epoch: ids.epoch, expected_binding_epoch: 1, expected_desired_generation: 0 };
const delivery = { lease_token: ids.operation, lease_expires_at: "2026-09-08T00:00:40Z", accepted_snapshot_sequence: 0, operation: { ...refresh, issued_at: "2026-09-08T00:00:00Z", expires_at: "2026-09-08T00:02:00Z", operation: { kind: "snapshot.refresh", schema_version: 1, payload: {} } } };
const receipt = { ...poll, wait_seconds: 0, lease_token: ids.operation, status: "succeeded", code: "snapshot_accepted", result: { snapshot_accepted: true, snapshot_sequence: 2 } };
const refreshCases = [
  ["bounded refresh create", refreshSchema, refresh, true],
  ["nil operation ID", refreshSchema, { ...refresh, operation_id: "00000000-0000-0000-0000-000000000000" }, false],
  ["nil idempotency ID", refreshSchema, { ...refresh, idempotency_key: "00000000-0000-0000-0000-000000000000" }, false],
  ["refresh generation null", refreshSchema, { ...refresh, expected_desired_generation: null }, false],
  ["refresh arbitrary intent", refreshSchema, { ...refresh, command: "id" }, false],
  ["bounded poll", pollSchema, poll, true],
  ["poll exceeds write deadline", pollSchema, { ...poll, wait_seconds: 60 }, false],
  ["leased delivery", deliverySchema, delivery, true],
  ["correlated receipt", receiptSchema, receipt, true],
  ["receipt cannot just assert success", receiptSchema, { ...receipt, result: { snapshot_accepted: true } }, false],
  ["receipt rejects diagnostics", receiptSchema, { ...receipt, raw_config: "canary" }, false],
  ["receipt rejects poll wait", receiptSchema, { ...receipt, wait_seconds: 1 }, false],
  ["conflict receipt", receiptSchema, { ...poll, wait_seconds: 0, lease_token: ids.operation, status: "conflict", code: "generation_conflict" }, true],
  ["failed receipt", receiptSchema, { ...poll, wait_seconds: 0, lease_token: ids.operation, status: "failed", code: "collection_failed" }, true],
  ["conflict rejects failed code", receiptSchema, { ...poll, wait_seconds: 0, lease_token: ids.operation, status: "conflict", code: "collection_failed" }, false],
  ["failed rejects conflict code", receiptSchema, { ...poll, wait_seconds: 0, lease_token: ids.operation, status: "failed", code: "generation_conflict" }, false],
];
for (const [name, validate, value, expected] of refreshCases) assertValidation(name, validate, value, expected, controlAjv);
const clientRequestSchema = ajv.compile(schemas.ClientEnabledRequest);
const clientStatusSchema = ajv.compile(normalizeControlSchemas({ ...schemas.ClientOperationStatus, $defs: { ClientEnabledResult: schemas.ClientEnabledResult } }));
const clientRequest = { ...refresh, tunnel_id: "awg20", client_id: "phone", enabled: false };
const clientDelivery = { ...delivery, operation: { ...delivery.operation, operation: { kind: "client.enabled.set", schema_version: 1, payload: { tunnel_id: "awg20", client_id: "phone", enabled: false } } } };
const clientResult = { committed_desired_generation: 1, tunnel_id: "awg20", client_id: "phone", enabled: false };
const clientReceipt = { ...poll, wait_seconds: 0, desired_generation: 1, lease_token: ids.operation, status: "succeeded", code: "client_enabled_committed", intent_hash: "a".repeat(64), client_result: clientResult };
const clientStatus = { controller_id: ids.boot, node_id: ids.session, operation_id: ids.operation, state_epoch: ids.epoch, binding_epoch: 1, expected_desired_generation: 0, status: "succeeded", code: "client_enabled_committed", issued_at: delivery.operation.issued_at, expires_at: delivery.operation.expires_at, completed_at: delivery.operation.expires_at, snapshot_sequence: 0, kind: "client.enabled.set", client_result: clientResult };
const clientCases = [
  ["client explicit false", clientRequestSchema, clientRequest, true],
  ["client explicit true", clientRequestSchema, { ...clientRequest, enabled: true }, true],
  ["client missing enabled", clientRequestSchema, Object.fromEntries(Object.entries(clientRequest).filter(([key]) => key !== "enabled")), false],
  ["client null enabled", clientRequestSchema, { ...clientRequest, enabled: null }, false],
  ["client string enabled", clientRequestSchema, { ...clientRequest, enabled: "false" }, false],
  ["client path injection", clientRequestSchema, { ...clientRequest, tunnel_id: "../state" }, false],
  ["client command field", clientRequestSchema, { ...clientRequest, command: "id" }, false],
  ["typed client delivery", deliverySchema, clientDelivery, true],
  ["reconciliation client delivery", deliverySchema, { ...clientDelivery, reconciliation_only: true }, true],
  ["refresh cannot reconcile a mutation", deliverySchema, { ...delivery, reconciliation_only: true }, false],
  ["typed client receipt", receiptSchema, clientReceipt, true],
  ["client missing receipt enabled", receiptSchema, { ...clientReceipt, client_result: { committed_desired_generation: 1, tunnel_id: "awg20", client_id: "phone" } }, false],
  ["client no full intent", receiptSchema, Object.fromEntries(Object.entries(clientReceipt).filter(([key]) => key !== "intent_hash")), false],
  ["client no diagnostic leak", receiptSchema, { ...clientReceipt, message: "canary" }, false],
  ["client result ack", requireSchema(controlAjv, "ClientResultAck"), { ...poll, wait_seconds: 0, intent_hash: "a".repeat(64) }, true],
  ["client browser committed result", clientStatusSchema, clientStatus, true],
  ["client browser succeeded requires result", clientStatusSchema, Object.fromEntries(Object.entries(clientStatus).filter(([key]) => key !== "client_result")), false],
  ["client browser honest reconciliation", clientStatusSchema, Object.fromEntries(Object.entries({ ...clientStatus, status: "reconciling", code: "", completed_at: null }).filter(([key]) => key !== "client_result")), true],
];
for (const [name, validate, value, expected] of clientCases) assertValidation(name, validate, value, expected, controlAjv);
const crudCases = [];
for (const action of ["Create", "Delete"]) {
 const last = action === "Create" ? "name" : "confirmed";
 const field = action.toLowerCase() + "_result";
 const kind = "client." + action.toLowerCase();
 const code = action === "Create" ? "client_created_committed" : "client_deleted_committed";
 const requestSchema = ajv.compile(schemas["Client" + action + "Request"]);
 const payload = { tunnel_id: "awg20", client_id: "0123456789abcdef", [last]: action === "Create" ? "phone" : true };
 const request = { ...refresh, ...payload };
 const result = { committed_desired_generation: 1, tunnel_id: payload.tunnel_id, client_id: payload.client_id, [action === "Create" ? "name" : "deleted"]: action === "Create" ? "phone" : true };
 const receipt = { ...poll, desired_generation: 1, wait_seconds: 0, lease_token: ids.operation, status: "succeeded", code, intent_hash: "a".repeat(64), [field]: result };
 const status = { ...clientStatus, kind, code, [field]: result }; delete status.client_result;
 const statusSchema = ajv.compile(normalizeControlSchemas({ ...schemas["Client" + action + "OperationStatus"], $defs: { ["Client" + action + "Result"]: schemas["Client" + action + "Result"] } }));
 crudCases.push(
  [kind + " valid", requestSchema, request, true],
  [kind + " missing field", requestSchema, Object.fromEntries(Object.entries(request).filter(([key]) => key !== last)), false],
  [kind + " null", requestSchema, { ...request, [last]: null }, false],
  [kind + " mismatched enabled", requestSchema, { ...request, enabled: false }, false],
  [kind + " secret", requestSchema, { ...request, private_key: "canary" }, false],
  [kind + " delivery", deliverySchema, { ...delivery, operation: { ...delivery.operation, operation: { kind, schema_version: 1, payload } } }, true],
  [kind + " reconciliation", deliverySchema, { ...delivery, reconciliation_only: true, operation: { ...delivery.operation, operation: { kind, schema_version: 1, payload } } }, true],
  [kind + " receipt", receiptSchema, receipt, true],
  [kind + " mixed receipt", receiptSchema, { ...receipt, client_result: clientResult }, false],
  [kind + " browser", statusSchema, status, true],
  [kind + " mixed browser", statusSchema, { ...status, client_result: clientResult }, false],
  [kind + " no result", statusSchema, Object.fromEntries(Object.entries(status).filter(([key]) => key !== field)), false],
 );
 if (action === "Create") {
  crudCases.push(["create invalid stable ID", requestSchema, { ...request, client_id: "UUID" }, false], ["create invalid name", requestSchema, { ...request, name: "../" }, false], ["create max name", requestSchema, { ...request, name: "x".repeat(64) }, true]);
 } else crudCases.push(["delete explicit false denied", requestSchema, { ...request, confirmed: false }, false]);
}
for (const [name, validate, value, expected] of crudCases) assertValidation(name, validate, value, expected, controlAjv);
console.log(`OpenAPI schema checks passed (${cases.length + controlCases.length + refreshCases.length + clientCases.length + crudCases.length + exportCases.length} cases)`);

function readDocument(path) {
  return JSON.parse(fs.readFileSync(path, "utf8"));
}

function requireSchema(validator, name) {
  const schema = validator.getSchema(`control#/$defs/${name}`);
  if (!schema) {
    throw new Error(`Control OpenAPI schema ${name} is missing or could not be compiled`);
  }
  return schema;
}

function assertValidation(name, validate, value, expected, validator) {
  const actual = validate(value);
  if (actual !== expected) {
    throw new Error(`${name}: expected valid=${expected}, got valid=${actual}: ${validator.errorsText(validate.errors)}`);
  }
}

function assertControlSecurity(control) {
  const globalSecurity = JSON.stringify(control.security);
  if (globalSecurity !== JSON.stringify([{ nodeMTLS: [] }])) {
    throw new Error("Control OpenAPI must require nodeMTLS by default");
  }

  const enrollmentSchemes = new Map([
    ["/control/v1/enrollments/{invitation_id}/claim", "enrollmentSecret"],
    ["/control/v1/enrollments/{enrollment_id}", "claimToken"],
  ]);
  for (const [path, item] of Object.entries(control.paths ?? {})) {
    if (!path.startsWith("/control/v1/")) {
      throw new Error(`Control path is outside /control/v1: ${path}`);
    }
    for (const [method, operation] of Object.entries(item)) {
      if (!new Set(["get", "post", "put", "patch", "delete"]).has(method)) {
        continue;
      }
      if (typeof operation.summary !== "string" || operation.summary.trim() === "") {
        throw new Error(`${method.toUpperCase()} ${path} must have a summary`);
      }
      const enrollmentScheme = enrollmentSchemes.get(path);
      if (path === "/control/v1/bootstrap") {
        if (method !== "get" || JSON.stringify(operation.security) !== "[]" || operation.requestBody !== undefined || operation.parameters !== undefined) {
          throw new Error("Only exact secret-free public CA bootstrap may omit nodeMTLS");
        }
      } else if (enrollmentScheme) {
        if (JSON.stringify(operation.security) !== JSON.stringify([{ [enrollmentScheme]: [] }])) {
          throw new Error(`${method.toUpperCase()} ${path} must use only ${enrollmentScheme}`);
        }
      } else if (operation.security !== undefined) {
        if (JSON.stringify(operation.security) !== globalSecurity) {
          throw new Error(`${method.toUpperCase()} ${path} may not weaken nodeMTLS`);
        }
      }
    }
  }

  const problemContent = control.components?.responses?.Problem?.content;
  if (!problemContent?.["application/problem+json"]) {
    throw new Error("Control errors must use application/problem+json");
  }
  if (JSON.stringify(control.components).includes('"additionalProperties":true')) {
    throw new Error("Control schemas may not accept unbounded additional properties");
  }

  assertNoStoreResponses(control);
}

function normalizeControlSchemas(schemasToNormalize) {
  const serialized = JSON.stringify(schemasToNormalize).replaceAll(
    "#/components/schemas/",
    "#/$defs/",
  );
  return JSON.parse(serialized);
}

function assertNoStoreResponses(control) {
  for (const [path, item] of Object.entries(control.paths ?? {})) {
    for (const [method, operation] of Object.entries(item)) {
      if (!new Set(["get", "post", "put", "patch", "delete"]).has(method)) {
        continue;
      }
      for (const [status, responseOrReference] of Object.entries(operation.responses ?? {})) {
        const response = responseOrReference.$ref
          ? resolveLocalReference(control, responseOrReference.$ref)
          : responseOrReference;
        const cacheControl = response?.headers?.["Cache-Control"];
        if (cacheControl?.$ref !== "#/components/headers/NoStore") {
          throw new Error(`${method.toUpperCase()} ${path} response ${status} must set Cache-Control: no-store`);
        }
      }
    }
  }
}

function resolveLocalReference(documentToResolve, reference) {
  if (!reference.startsWith("#/")) {
    throw new Error(`Only local OpenAPI references are allowed here: ${reference}`);
  }
  return reference
    .slice(2)
    .split("/")
    .reduce((value, segment) => value?.[segment.replaceAll("~1", "/").replaceAll("~0", "~")], documentToResolve);
}

function assertNoForbiddenSnapshotFields(snapshotSchema) {
  const serialized = JSON.stringify(snapshotSchema).toLowerCase();
  const forbidden = [
    "private_key",
    "preshared_key",
    "raw_config",
    "qr_payload",
    "vpn_url",
    "warp_credentials",
    "command_output",
  ];
  for (const field of forbidden) {
    if (serialized.includes(`\"${field}\"`)) {
      throw new Error(`NodeSnapshot must not expose ${field}`);
    }
  }
}
