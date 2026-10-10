import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

// Context races are injected here; the acceptance harness separately exercises
// real worker snapshots and registry-backed controller reads.
test("fleet views fence delayed responses, epochs and logout and remain read-only", async ({ page }, testInfo) => {
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
  const nodes = ["11111111-1111-4111-8111-111111111111", "33333333-3333-4333-8333-333333333333"].map((id, index) => ({
    node_id: id, name: `Remote ${index}`, binding_epoch: 1, status: "online", state_epoch: controller, boot_id: controller,
    last_confirmed_at: "2026-10-10T10:00:00Z", application_version: "test", contract_version: 1, capabilities: ["snapshot.v1"],
  }));
  let delayed = true;
  let release: (() => void) | undefined;
  let requested = false;
  const mutations: string[] = [];
  page.on("request", (request) => { if (request.url().includes("/api/controller/nodes") && request.method() !== "GET") mutations.push(request.method()); });
  await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: controller, nodes } }));
  await page.route("**/api/controller/nodes/*", async (route) => {
    const node = nodes.find((item) => route.request().url().endsWith(item.node_id))!;
    if (node === nodes[0] && delayed) { requested = true; await new Promise<void>((resolve) => { release = resolve; }); }
    await route.fulfill({ json: { controller_id: controller, node, state_epoch: controller, boot_id: controller, desired_generation: 0, snapshot_sequence: 1, observed_at: null, received_at: null, stale: true, available: false, desired: null, observations: null } }).catch(() => {});
  });
  await page.goto(origin);
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  const selector = page.getByLabel(m.fleet.server, { exact: true });
  await expect(selector.locator("option")).toHaveCount(3);
  await selector.selectOption(nodes[0].node_id);
  await expect.poll(() => requested).toBe(true);
  await selector.selectOption(nodes[1].node_id);
  await expect(page.getByRole("heading", { name: "Remote 1", exact: true })).toBeVisible();
  release?.(); delayed = false;
  await expect(page.getByRole("heading", { name: "Remote 0", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: m.common.maintenance, exact: true })).toHaveCount(0);
  await expect(page.getByText(m.fleet.noSnapshot, { exact: true })).toBeVisible();
  nodes[1].boot_id = "44444444-4444-4444-8444-444444444444";
  nodes[1].status = "offline";
  await expect(page.getByText(m.fleet.offline, { exact: true })).toBeVisible({ timeout: 12_000 });
  await selector.selectOption("");
  await expect(page.getByRole("heading", { name: "Remote 1", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: m.common.maintenance, exact: true })).toBeVisible();
  await selector.selectOption(nodes[1].node_id);
  let releaseLogout: (() => void) | undefined;
  await page.route("**/api/logout", async (route) => {
    const response = await route.fetch();
    await new Promise<void>((resolve) => { releaseLogout = resolve; });
    await route.fulfill({ response });
  });
  await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
  await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeDisabled();
  await expect.poll(() => Boolean(releaseLogout)).toBe(true);
  releaseLogout?.();
  await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeEnabled();
  await expect(selector).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Remote 1", exact: true })).toHaveCount(0);
  expect(mutations).toEqual([]);
  const storage = await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
  expect(storage).not.toContain(nodes[0].node_id);
  expect(storage).not.toContain(nodes[1].node_id);
  } finally {
    child.kill("SIGTERM");
    await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
  }
});
