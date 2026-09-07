import fs from "node:fs";

import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const document = JSON.parse(fs.readFileSync("api/openapi.json", "utf8"));
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
  const actual = validate(value);
  if (actual !== expected) {
    throw new Error(`${name}: expected valid=${expected}, got valid=${actual}: ${ajv.errorsText(validate.errors)}`);
  }
}

console.log(`OpenAPI schema checks passed (${cases.length} cases)`);
