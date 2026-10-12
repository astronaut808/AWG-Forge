import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

type Intent = { operation_id: string; idempotency_key: string; expected_desired_generation: number; tunnel_id: string; client_id: string; enabled: boolean };

test("remote client access preserves its exact intent and fences unconfirmed contexts", async ({ page }, testInfo) => {
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
    const node = { node_id: "11111111-1111-4111-8111-111111111111", name: "Client node", binding_epoch: 1, status: "online", state_epoch: controller, boot_id: controller, last_confirmed_at: new Date().toISOString(), application_version: "test", contract_version: 1, capabilities: ["snapshot.v1", "client.enabled.set.v1"] };
    let phase: "reconciling" | "succeeded" = "reconciling";
    let projectionGeneration = 0;
    let clientEnabled = true;
    let projectionReads = 0;
    let creates = 0;
    let firstIntent: Intent | undefined;
    let currentIntent: Intent | undefined;
    let delayedStatus: (() => void) | undefined;
    let delayStatus = false;
    const status = (body: Intent, current = phase) => ({
      controller_id: controller, node_id: node.node_id, operation_id: body.operation_id, state_epoch: controller, binding_epoch: 1,
      expected_desired_generation: body.expected_desired_generation, kind: "client.enabled.set", status: current, code: current === "succeeded" ? "client_enabled_committed" : "",
      issued_at: new Date().toISOString(), expires_at: new Date(Date.now() + 120_000).toISOString(), completed_at: current === "succeeded" ? new Date().toISOString() : null,
      snapshot_sequence: 0, client_result: current === "succeeded" ? { tunnel_id: "tunnel-a", client_id: "client-a", enabled: body.enabled, committed_desired_generation: body.expected_desired_generation + 1 } : undefined,
    });
    await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: controller, nodes: [node] } }));
    await page.route("**/api/controller/nodes/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/client-enabled")) {
        const body = route.request().postDataJSON() as Intent;
        firstIntent ||= body; currentIntent = body;
        creates++;
        if (creates === 1) return route.abort("connectionreset");
        if (creates === 3) {
          const malformed = status(body, "succeeded");
          malformed.client_result!.enabled = !body.enabled;
          return route.fulfill({ status: 202, json: malformed });
        }
        return route.fulfill({ status: 202, json: status(body) });
      }
      if (path.includes("/operations/")) {
        if (delayStatus) await new Promise<void>((resolve) => { delayedStatus = resolve; });
        return route.fulfill({ json: status(currentIntent!) }).catch(() => {});
      }
      projectionReads++;
      return route.fulfill({ json: {
        controller_id: controller, node, state_epoch: controller, boot_id: controller, desired_generation: projectionGeneration, snapshot_sequence: 1,
        observed_at: new Date().toISOString(), received_at: new Date().toISOString(), stale: false, available: true,
        desired: { tunnels: [{ id: "tunnel-a", name: "Tunnel A", interface: "awg0", profile: "awg2", enabled: true, listen_port: 1, revision: 1, clients: [{ id: "client-a", name: "Enabled client", enabled: clientEnabled, address: "10.0.0.2" }] }] },
        observations: { tunnels: [], doctor: { scope: "runtime", tun_available: true, forwarding: true, runtime_unknown: 0, tunnels_down: 0, apply_failures: 0 } },
      } }).catch(() => {});
    });
    await page.goto(origin);
    await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
    await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    const selector = page.getByLabel(m.fleet.server, { exact: true });
    await expect(selector.locator("option")).toHaveCount(2);
    await selector.selectOption(node.node_id);
    const access = page.getByRole("region", { name: m.fleet.clientAccess, exact: true }).getByRole("combobox");
    await expect(access).toHaveValue("tunnel-a/client-a");
    await page.getByRole("button", { name: m.fleet.clientDisable, exact: true }).click();
    projectionGeneration = 1; clientEnabled = false;
    await expect.poll(() => projectionReads, { timeout: 6_000 }).toBeGreaterThan(1);
    await page.getByRole("button", { name: m.fleet.clientConfirm, exact: true }).click();
    await expect(page.getByText(m.fleet.refreshConflict, { exact: true })).toBeVisible();
    expect(creates).toBe(0);
    await expect(access).toHaveValue("tunnel-a/client-a");
    await page.getByRole("button", { name: m.fleet.clientEnable, exact: true }).click();
    await page.getByRole("button", { name: m.fleet.clientConfirm, exact: true }).click();
    await expect(page.getByText(m.fleet.clientUnknown, { exact: true })).toBeVisible();
    expect(creates).toBe(1);
    await page.getByRole("button", { name: m.fleet.clientRetry, exact: true }).click();
    await expect(page.getByText(m.fleet.clientReconciling, { exact: true })).toBeVisible();
    expect(creates).toBe(2);
    expect(currentIntent).toEqual(firstIntent);
    expect(currentIntent).toMatchObject({ tunnel_id: "tunnel-a", client_id: "client-a", enabled: true, expected_desired_generation: 1 });
    phase = "succeeded";
    await expect(page.getByText(m.fleet.clientCommitted, { exact: true })).toBeVisible({ timeout: 6_000 });
    await expect(page.getByRole("button", { name: m.fleet.clientEnable, exact: true })).toBeDisabled();
    projectionGeneration = 2; clientEnabled = true;
    const disable = page.getByRole("button", { name: m.fleet.clientDisable, exact: true });
    await expect(disable).toBeEnabled({ timeout: 6_000 });

    await disable.click(); await page.getByRole("button", { name: m.fleet.clientConfirm, exact: true }).click();
    await expect(page.getByText(m.fleet.clientUnknown, { exact: true })).toBeVisible();
    await expect(page.getByText(m.fleet.clientCommitted, { exact: true })).toHaveCount(0);

    phase = "reconciling"; delayStatus = true;
    await page.getByRole("button", { name: m.fleet.clientRetry, exact: true }).click();
    await expect.poll(() => Boolean(delayedStatus)).toBe(true);
    await selector.selectOption(""); delayedStatus?.();
    await expect(page.getByText(m.fleet.clientReconciling, { exact: true })).toHaveCount(0);
    await selector.selectOption(node.node_id);
    await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
    await expect(selector).toHaveCount(0);
  } finally {
    child.kill("SIGTERM");
    await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
  }
});
