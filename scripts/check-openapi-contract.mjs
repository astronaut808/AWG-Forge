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
const relay = ajv.compile(schemas.RelayRequest);
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
  ["relay settings", relay, { name: "Edge", host: "edge.example.com", port: 40123, target_ipv4: "192.0.2.20" }, true],
  ["relay DNS destination", relay, { name: "Edge", host: "edge.example.com", port: 40123, target_host: "exit.example.com" }, true],
  ["relay requires one destination", relay, { name: "Edge", host: "edge.example.com", port: 40123, target_ipv4: "192.0.2.20", target_host: "exit.example.com" }, false],
  ["relay rejects IPv6 target", relay, { name: "Edge", host: "edge.example.com", port: 40123, target_ipv4: "2001:db8::1" }, false],
  ["create with relay", createClient, { tunnel_id: "tunnel-1", name: "phone", relay_id: "0123456789abcdef" }, true],
  ["clear relay", updateClient, { name: "phone", notes: "", expires_at: "", relay_id: "" }, true],
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
