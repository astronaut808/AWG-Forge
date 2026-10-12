import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

type CreateIntent = { operation_id: string; idempotency_key: string; expected_desired_generation: number; tunnel_id: string; client_id: string; name: string };

test("remote client CRUD confirms immutable intent, retries it, and clears it on context changes", async ({ page }, testInfo) => {
  test.setTimeout(90_000);
  const port = await new Promise<number>((resolve, reject) => {
    const server = createServer(); server.on("error", reject);
    server.listen(0, "127.0.0.1", () => { const address = server.address(); if (!address || typeof address === "string") return reject(new Error("port unavailable")); server.close(() => resolve(address.port)); });
  });
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(process.execPath, ["web/tests/server.mjs"], { env: { ...process.env, WEBUI_PORT: String(port) }, stdio: "ignore" });
  try {
    await expect.poll(async () => { try { return (await fetch(origin + "/api/auth/status")).status; } catch { return 0; } }, { timeout: 30_000 }).toBe(200);
    const m = messages[testInfo.project.use.locale?.startsWith("ru") ? "ru" : "en"];
    const controller = "22222222-2222-4222-8222-222222222222";
    const node = { node_id: "11111111-1111-4111-8111-111111111111", name: "Client node", binding_epoch: 1, status: "online", state_epoch: controller, boot_id: controller, last_confirmed_at: new Date().toISOString(), application_version: "test", contract_version: 1, capabilities: ["snapshot.v1", "client.create.v1", "client.delete.v1"] };
    let generation = 0; let stale = false; let creates = 0; let projectionReads = 0; let create: CreateIntent | undefined; let retry: CreateIntent | undefined; let phase: "reconciling" | "succeeded" = "reconciling"; let mismatchedOnce = false; let delayed: (() => void) | undefined;
    const clients = [{ id: "client-a", name: "Existing client", enabled: true, address: "10.0.0.2" }];
    const status = (intent: CreateIntent, current = phase) => ({ controller_id: controller, node_id: node.node_id, operation_id: intent.operation_id, state_epoch: controller, binding_epoch: 1, expected_desired_generation: intent.expected_desired_generation, kind: "client.create", status: current, code: current === "succeeded" ? "client_created_committed" : "", issued_at: new Date().toISOString(), expires_at: new Date(Date.now() + 120_000).toISOString(), completed_at: current === "succeeded" ? new Date().toISOString() : null, snapshot_sequence: 1, create_result: current === "succeeded" ? { committed_desired_generation: intent.expected_desired_generation + 1, tunnel_id: intent.tunnel_id, client_id: intent.client_id, name: intent.name } : undefined });
    await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: controller, nodes: [node] } }));
    await page.route("**/api/controller/nodes/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/client-create")) {
        const body = route.request().postDataJSON() as CreateIntent; if (!create) create = body; else retry = body; creates++;
        if (creates === 1) return route.abort("connectionreset");
        return route.fulfill({ status: 202, json: status(body) });
      }
      if (path.includes("/operations/")) {
        if (delayed) await new Promise<void>((resolve) => { delayed = resolve; });
        if (mismatchedOnce) { mismatchedOnce = false; const wrong = status(create!, "succeeded"); wrong.create_result!.client_id = "wrong-client"; return route.fulfill({ json: wrong }); }
        return route.fulfill({ json: status(create!) });
      }
      projectionReads++;
      return route.fulfill({ json: { controller_id: controller, node, state_epoch: controller, boot_id: controller, desired_generation: generation, snapshot_sequence: 1, observed_at: new Date().toISOString(), received_at: new Date().toISOString(), stale, available: true, desired: { tunnels: [{ id: "tunnel-a", name: "Tunnel A", interface: "awg0", profile: "awg2", enabled: true, listen_port: 1, revision: 1, clients }] }, observations: { tunnels: [], doctor: { scope: "runtime", tun_available: true, forwarding: true, runtime_unknown: 0, tunnels_down: 0, apply_failures: 0 } } } });
    });
    await page.goto(origin);
    await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
    await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    const selector = page.getByLabel(m.fleet.server, { exact: true });
    await expect(selector.locator("option")).toHaveCount(2); await selector.selectOption(node.node_id);
    const region = page.getByRole("region", { name: m.fleet.remoteClientManagement, exact: true });
    const clientName = region.getByLabel(m.fleet.remoteClientName, { exact: true });
    await clientName.fill("a".repeat(64));
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeEnabled();
    await clientName.evaluate((input) => { (input as HTMLInputElement).value = "a".repeat(65); input.dispatchEvent(new Event("input", { bubbles: true })); });
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeDisabled();
    await clientName.fill("Remote client");
    await region.getByRole("button", { name: m.fleet.remoteCreate, exact: true }).click();
    await expect(region.getByText("Client node · Tunnel A · Remote client", { exact: false })).toBeVisible();
    generation = 1; clients.push({ id: "other-client", name: "Other", enabled: true, address: "10.0.0.3" });
    await expect.poll(() => projectionReads, { timeout: 6_000 }).toBeGreaterThan(1);
    await page.getByRole("button", { name: m.fleet.clientConfirm, exact: true }).click();
    await expect(page.getByText(m.fleet.refreshConflict, { exact: true })).toBeVisible();
    expect(creates).toBe(0);
    await region.getByLabel(m.fleet.remoteClientName, { exact: true }).fill("Remote client");
    await region.getByRole("button", { name: m.fleet.remoteCreate, exact: true }).click();
    await page.getByRole("button", { name: m.fleet.clientConfirm, exact: true }).click();
    await expect(page.getByText(m.fleet.clientUnknown, { exact: true })).toBeVisible();
    await page.getByRole("button", { name: m.fleet.clientRetry, exact: true }).click();
    expect(creates).toBe(2); expect(create?.client_id).toMatch(/^[a-f0-9]{16}$/); expect(retry).toEqual(create);
    phase = "succeeded"; mismatchedOnce = true;
    await expect(page.getByText(m.fleet.clientUnknown, { exact: true })).toBeVisible({ timeout: 6_000 });
    await expect(page.getByText(m.fleet.clientCommitted, { exact: true })).toBeVisible({ timeout: 6_000 });
    generation = 2; clients.push({ id: create!.client_id, name: create!.name, enabled: true, address: "10.0.0.4" });
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeEnabled({ timeout: 6_000 });
    await region.getByLabel(m.fleet.remoteExistingClient, { exact: true }).selectOption("client-a");
    await region.getByRole("button", { name: m.fleet.remoteDelete, exact: true }).click();
    await expect(region.getByText("Client node · Tunnel A · Existing client", { exact: false })).toBeVisible();
    await page.getByRole("button", { name: m.fleet.clientCancel, exact: true }).click();
    node.status = "offline";
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeDisabled({ timeout: 7_000 });
    node.status = "online"; stale = true;
    await expect(page.getByText(m.fleet.stale, { exact: true })).toBeVisible({ timeout: 7_000 });
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeDisabled({ timeout: 7_000 });
    stale = false; node.capabilities = ["snapshot.v1"];
    await expect(region.getByText(m.fleet.clientUnsupported, { exact: true })).toBeVisible({ timeout: 7_000 });
    await expect(region.getByRole("button", { name: m.fleet.remoteCreate, exact: true })).toBeDisabled({ timeout: 7_000 });
    await selector.selectOption("");
    await expect(region).toHaveCount(0); await selector.selectOption(node.node_id);
    await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
    await expect(selector).toHaveCount(0);
  } finally {
    child.kill("SIGTERM"); await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
  }
});
