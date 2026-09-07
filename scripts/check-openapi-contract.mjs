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

const createClient = ajv.compile(schemas.CreateClientRequest);
const updateClient = ajv.compile(schemas.UpdateClientSettingsRequest);
const profile = ajv.compile(schemas.Profile);
const cases = [
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

const enrollmentClaim = requireSchema(controlAjv, "EnrollmentClaimRequest");
const nodeSnapshot = requireSchema(controlAjv, "NodeSnapshot");
const operationEnvelope = requireSchema(controlAjv, "OperationEnvelope");
const problem = requireSchema(controlAjv, "Problem");

const ids = {
  boot: "103354a0-e154-4d4c-bfde-f71dbbc7394f",
  epoch: "7238def3-36b3-4cdd-896a-7dd05571bd26",
  operation: "4c0fb636-e234-4726-85d5-38d95de9029f",
};
const controlCases = [
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
    "redacted snapshot",
    nodeSnapshot,
    {
      boot_id: ids.boot,
      state_epoch: ids.epoch,
      binding_epoch: 1,
      desired_generation: 3,
      observed_at: "2026-09-08T00:00:00Z",
      service_status: "ready",
      network: { external_interface_confirmed: false },
      tunnels: [
        {
          id: "7d774c16b2c92872",
          name: "AWG 2",
          interface: "awg20",
          profile: "awg_2_0",
          enabled: true,
          runtime_status: "up",
          listen_port: 49411,
          subnet: "10.28.0.0/24",
          egress: "wan",
          clients_total: 1,
          clients_online: 1,
          clients: [
            {
              id: "12a6acf567ffb5ae",
              name: "phone",
              enabled: true,
              runtime_status: "online",
              last_seen_at: "2026-09-08T00:00:00Z",
              expires_at: null,
              rx_bytes: 1024,
              tx_bytes: 2048,
            },
          ],
        },
      ],
      findings: [],
    },
    true,
  ],
  [
    "snapshot rejects secret-shaped additions",
    nodeSnapshot,
    {
      boot_id: ids.boot,
      state_epoch: ids.epoch,
      binding_epoch: 1,
      desired_generation: 3,
      observed_at: "2026-09-08T00:00:00Z",
      service_status: "ready",
      network: { external_interface_confirmed: false },
      tunnels: [],
      private_key: "must-not-cross-the-boundary",
    },
    false,
  ],
  [
    "typed snapshot refresh operation",
    operationEnvelope,
    {
      operation_id: ids.operation,
      idempotency_key: "snapshot-refresh-1",
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
      idempotency_key: "shell-1",
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

console.log(`OpenAPI schema checks passed (${cases.length + controlCases.length} cases)`);

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
      if (enrollmentScheme) {
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
