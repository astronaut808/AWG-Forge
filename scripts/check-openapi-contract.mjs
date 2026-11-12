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
const login = ajv.compile(schemas.LoginRequest);
const serverName = ajv.compile(schemas.UpdateServerNameRequest);
const cases = [
  ["server name", serverName, { name: "Moscow02" }, true],
  ["clear server name", serverName, { name: "" }, true],
  ["missing server name", serverName, {}, false],
  ["null server name", serverName, { name: null }, false],
  ["invalid server name", serverName, { name: "moscow-02" }, false],
  ["long server name", serverName, { name: "a".repeat(33) }, false],
  ["legacy login", login, { password: "secret" }, true],
  ["username login", login, { username: "operator", password: "secret" }, true],
  ["login rejects empty username", login, { username: "", password: "secret" }, false],
  ["login rejects missing password", login, { username: "admin" }, false],
  ["create without expiration", createClient, { tunnel_id: "tunnel-1", name: "phone" }, true],
  ["create with empty expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "" }, true],
  ["create with RFC3339 expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "2026-09-05T12:00:00Z" }, true],
  ["create with invalid expiration", createClient, { tunnel_id: "tunnel-1", name: "phone", expires_at: "tomorrow" }, false],
  ["update with empty expiration", updateClient, { name: "phone", notes: "", expires_at: "" }, true],
  ["update with RFC3339 expiration", updateClient, { name: "phone", notes: "", expires_at: "2026-09-05T12:00:00Z" }, true],
  ["update with invalid expiration", updateClient, { name: "phone", notes: "", expires_at: "tomorrow" }, false],
];

for (const [name, validate, value, expected] of cases) {
  const actual = validate(value);
  if (actual !== expected) {
    throw new Error(`${name}: expected valid=${expected}, got valid=${actual}: ${ajv.errorsText(validate.errors)}`);
  }
}

console.log(`OpenAPI request schema checks passed (${cases.length} cases)`);
