import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

test("remote one-use config export fences confirmation, validates delivery, and cancels pending work", async ({ page }, testInfo) => {
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
    const node = { node_id: "11111111-1111-4111-8111-111111111111", name: "Export node", binding_epoch: 1, status: "online", state_epoch: controller, boot_id: controller, last_confirmed_at: new Date().toISOString(), application_version: "test", contract_version: 1, capabilities: ["snapshot.v1", "client.config.export.v1"] };
    let generation = 1; let projectionBinding = 1; let stale = false; let prepares = 0; let cancels = 0; let projectionReads = 0; let downloadMode: "hold" | "bad" | "good" = "hold"; let held: (() => void) | undefined;
    const projection = () => ({ controller_id: controller, node: { ...node, binding_epoch: projectionBinding }, state_epoch: controller, boot_id: controller, desired_generation: generation, snapshot_sequence: 1, observed_at: new Date().toISOString(), received_at: new Date().toISOString(), stale, available: true, desired: { tunnels: [{ id: "tunnel-a", name: "Tunnel A", interface: "awg0", profile: "awg2", enabled: true, listen_port: 1, revision: 7, clients: [{ id: "client-a", name: "Existing client", enabled: true, address: "10.0.0.2" }] }] }, observations: { tunnels: [], doctor: { scope: "runtime", tun_available: true, forwarding: true, runtime_unknown: 0, tunnels_down: 0, apply_failures: 0 } } });
    await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: controller, nodes: [node] } }));
    await page.route("**/api/controller/nodes/**", (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/client-config-exports")) {
        prepares++; const body = route.request().postDataJSON();
        expect(body).toEqual({ expected_state_epoch: controller, expected_binding_epoch: 1, expected_desired_generation: generation, tunnel_id: "tunnel-a", client_id: "client-a", expected_tunnel_revision: 7 });
        return route.fulfill({ status: 202, json: { export_id: `33333333-3333-4333-8333-${String(prepares).padStart(12, "0")}`, node_id: node.node_id, tunnel_id: "tunnel-a", client_id: "client-a", expires_at: new Date(Date.now() + 60_000).toISOString() } });
      }
      if (path.endsWith("/client-config-exports/cancel")) { cancels++; return route.fulfill({ status: 204 }); }
      if (path.endsWith("/client-config-exports/download")) {
        if (downloadMode === "hold") return new Promise<void>((resolve) => { held = () => { void route.abort("failed"); resolve(); }; });
        if (downloadMode === "bad") return route.fulfill({ status: 200, headers: { "Content-Type": "application/json", "Cache-Control": "no-store", "Content-Disposition": "attachment; filename=unsafe.conf" }, body: "{}" });
        return route.fulfill({ status: 200, headers: { "Content-Type": "text/plain", "Cache-Control": "no-store", "Content-Disposition": "attachment; filename=ignored.conf" }, body: "[Interface]\nPrivateKey = test-only\n" });
      }
      projectionReads++; return route.fulfill({ json: projection() });
    });
    await page.goto(origin);
    await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
    await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    await page.getByLabel(m.fleet.server, { exact: true }).selectOption(node.node_id);
    const region = page.getByRole("region", { name: m.fleet.remoteConfigExport, exact: true });
    await region.getByRole("button", { name: m.fleet.remoteExport, exact: true }).click();
    await expect(region.getByText("Export node · Tunnel A · Existing client", { exact: false })).toBeVisible();
    const readsBeforeEpochConflict = projectionReads; projectionBinding = 2;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeEpochConflict);
    await page.getByRole("button", { name: m.fleet.remoteExportConfirm, exact: true }).click();
    await expect(region.getByText(m.fleet.remoteExportConflict, { exact: true })).toBeVisible();
    expect(prepares).toBe(0);
    const readsBeforeBindingReset = projectionReads; projectionBinding = 1;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeBindingReset);
    await region.getByRole("button", { name: m.fleet.remoteExport, exact: true }).click();
    const readsBeforeConflict = projectionReads; generation = 2;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeConflict);
    await page.getByRole("button", { name: m.fleet.remoteExportConfirm, exact: true }).click();
    await expect(region.getByText(m.fleet.remoteExportConflict, { exact: true })).toBeVisible();
    expect(prepares).toBe(0);
    const readsBeforeGenerationReset = projectionReads; generation = 1;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeGenerationReset);
    await region.getByRole("button", { name: m.fleet.remoteExport, exact: true }).click();
    await page.getByRole("button", { name: m.fleet.remoteExportConfirm, exact: true }).click();
    await expect(region.getByRole("button", { name: m.fleet.remoteExportCancel, exact: true })).toBeVisible();
    const readsBeforeCancellation = projectionReads; generation = 2;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeCancellation);
    await expect.poll(() => cancels).toBe(1);
    const readsBeforeReset = projectionReads; generation = 1;
    await expect.poll(() => projectionReads, { timeout: 7_000 }).toBeGreaterThan(readsBeforeReset);
    downloadMode = "bad";
    await region.getByRole("button", { name: m.fleet.remoteExport, exact: true }).click();
    await page.getByRole("button", { name: m.fleet.remoteExportConfirm, exact: true }).click();
    await expect(region.getByText(m.fleet.remoteExportMetadata, { exact: true })).toBeVisible();
    downloadMode = "good";
    await region.getByRole("button", { name: m.fleet.remoteExport, exact: true }).click();
    await page.getByRole("button", { name: m.fleet.remoteExportConfirm, exact: true }).click();
    await expect(region.getByText(m.fleet.remoteExportReady, { exact: true })).toBeVisible();
    held?.();
    await expect(region.getByText(m.fleet.remoteExportReady, { exact: true })).toBeVisible();
    stale = true;
    await expect(region.getByRole("button", { name: m.fleet.remoteExport, exact: true })).toBeDisabled({ timeout: 7_000 });
    node.status = "offline";
    await expect(region.getByText(m.fleet.remoteExportUnsafe, { exact: true })).toBeVisible({ timeout: 7_000 });
  } finally {
    child.kill("SIGTERM"); await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
  }
});
