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
  let paginated = true;
  let releaseInventory: (() => void) | undefined;
  const pageRequests: string[] = [];
  await page.route((url) => url.pathname === "/api/controller/nodes", async (route) => {
    const cursor = new URL(route.request().url()).searchParams.get("after") || "";
    pageRequests.push(cursor);
    const data = !paginated ? { nodes } : cursor ? { nodes: [nodes[1]] } : { nodes: [nodes[0]], next_cursor: nodes[0].node_id };
    if (paginated && !cursor && pageRequests.length === 2) await new Promise<void>((resolve) => { releaseInventory = resolve; });
    return route.fulfill({ json: { controller_id: controller, ...data } }).catch(() => {});
  });
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
  await expect(selector.locator("option")).toHaveCount(2);
  await expect.poll(() => Boolean(releaseInventory), { timeout: 10_000 }).toBe(true);
  await page.getByRole("button", { name: m.fleet.nextPage, exact: true }).click();
  await expect(selector.locator("option")).toHaveCount(2);
  await expect(selector.locator("option").last()).toContainText("Remote 1");
  releaseInventory?.();
  await expect(selector.locator("option").last()).toContainText("Remote 1");
  await selector.selectOption(nodes[1].node_id);
  await expect(page.getByRole("heading", { name: "Remote 1", exact: true })).toBeVisible();
  paginated = false;
  await page.getByRole("button", { name: m.fleet.firstPage, exact: true }).click();
  await expect(selector).toHaveValue("");
  await expect(page.getByRole("heading", { name: "Remote 1", exact: true })).toHaveCount(0);
  await expect(selector.locator("option")).toHaveCount(3);
  expect(pageRequests).toContain(nodes[0].node_id);
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

test("node refresh waits for accepted correlation and fences delayed context", async ({ page }, testInfo) => {
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
  const node = { node_id: "11111111-1111-4111-8111-111111111111", name: "Refresh node", binding_epoch: 1, status: "online", state_epoch: controller, boot_id: controller, last_confirmed_at: new Date().toISOString(), application_version: "test", contract_version: 1, capabilities: ["snapshot.v1", "snapshot.refresh.v1"] };
  const requests: { operation_id: string; idempotency_key: string }[] = [];
  let phase = "queued";
  let sequence = 1;
  let delayed = false;
  let release: (() => void) | undefined;
  let generation = 0;
  let holdProjection = false;
  let releaseProjection: (() => void) | undefined;
  await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: controller, nodes: [node] } }));
  await page.route("**/api/controller/nodes/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const status = (id: string) => ({ controller_id: controller, node_id: node.node_id, operation_id: id, state_epoch: controller, binding_epoch: 1, expected_desired_generation: 0, status: phase, code: phase === "succeeded" ? "snapshot_accepted" : "", issued_at: new Date().toISOString(), expires_at: new Date(Date.now() + 120_000).toISOString(), completed_at: phase === "succeeded" ? new Date().toISOString() : null, snapshot_sequence: phase === "succeeded" ? 2 : 0 });
    if (path.endsWith("/refresh")) {
      const body = route.request().postDataJSON(); requests.push(body);
      if (delayed) await new Promise<void>((resolve) => { release = resolve; });
      return route.fulfill({ status: 202, json: status(body.operation_id) }).catch(() => {});
    }
    if (path.includes("/operations/")) return route.fulfill({ json: status(path.split("/").at(-1)!) }).catch(() => {});
    if (holdProjection) await new Promise<void>((resolve) => { releaseProjection = resolve; });
    return route.fulfill({ json: { controller_id: controller, node, state_epoch: controller, boot_id: controller, desired_generation: generation, snapshot_sequence: sequence, observed_at: new Date().toISOString(), received_at: new Date().toISOString(), stale: false, available: true, desired: { tunnels: [] }, observations: { tunnels: [], doctor: { scope: "runtime" } } } }).catch(() => {});
  });
  await page.goto(origin);
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  const selector = page.getByLabel(m.fleet.server, { exact: true });
  await expect(selector.locator("option")).toHaveCount(2);
  await selector.selectOption(node.node_id);
  const refresh = page.getByRole("button", { name: m.fleet.refresh, exact: true });
  await expect(refresh).toBeEnabled(); await refresh.click();
  await expect(page.getByText(m.fleet.refreshQueued, { exact: true })).toBeVisible();
  await expect(page.getByText(m.fleet.refreshSucceeded, { exact: true })).toHaveCount(0);
  await expect(refresh).toBeDisabled();
  phase = "leased"; await expect(page.getByText(m.fleet.refreshLeased, { exact: true })).toBeVisible();
  sequence = 2; phase = "succeeded";
  await expect(page.getByText(m.fleet.refreshSucceeded, { exact: true })).toBeVisible();
  await expect(refresh).toBeEnabled();
  phase = "conflict"; holdProjection = true; generation = 1;
  await refresh.click();
  const retry = page.getByRole("button", { name: m.fleet.retryRefresh, exact: true });
  await expect.poll(() => Boolean(releaseProjection)).toBe(true);
  await expect(retry).toBeDisabled();
  expect(requests).toHaveLength(2);
  holdProjection = false; releaseProjection?.();
  await expect(retry).toBeEnabled();
  delayed = true; phase = "queued"; await retry.click();
  await expect.poll(() => Boolean(release)).toBe(true);
  await selector.selectOption(""); release?.();
  await expect(page.getByText(m.fleet.refreshQueued, { exact: true })).toHaveCount(0);
  await expect(page.getByText(m.fleet.refreshSucceeded, { exact: true })).toHaveCount(0);
  await selector.selectOption(node.node_id); await expect(refresh).toBeEnabled();
  expect(requests).toHaveLength(3); expect(requests[0].operation_id).not.toEqual(requests[1].operation_id);
  expect(requests[2]).toMatchObject({ expected_desired_generation: 1 });
  const storage = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
  expect(storage).not.toContain(requests[0].operation_id);
  await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
  await expect(selector).toHaveCount(0);
  } finally {
    child.kill("SIGTERM");
    await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
  }
});
