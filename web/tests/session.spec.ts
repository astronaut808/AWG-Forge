import { createHmac } from "node:crypto";
import { spawn } from "node:child_process";
import { createServer, request as proxyRequest, type ServerResponse } from "node:http";
import { createServer as createPortServer } from "node:net";
import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

// Hold real logout headers at a local proxy, so Chromium must cancel the network
// request before a delayed Set-Cookie can affect a subsequent real login.
for (const mode of ["standalone", "controller"] as const) {
  test(`${mode} logout deadline cancels late cookie clearing before another login`, async ({ page }, testInfo) => {
    test.setTimeout(60_000);
    const m = messages[testInfo.project.use.locale?.startsWith("ru") ? "ru" : "en"];
    let held: { response: ServerResponse; release: () => void } | undefined;
    let aborted = false;
    const portServer = createPortServer();
    await new Promise<void>((resolve) => portServer.listen(0, "127.0.0.1", resolve));
    const backendAddress = portServer.address();
    if (!backendAddress || typeof backendAddress === "string") throw new Error("backend port unavailable");
    const backend = `http://127.0.0.1:${backendAddress.port}`;
    await new Promise<void>((resolve) => portServer.close(() => resolve()));
    const child = spawn(process.execPath, ["web/tests/server.mjs"], { env: { ...process.env, WEBUI_PORT: String(backendAddress.port) }, stdio: "ignore" });
    const proxy = createServer((req, res) => {
      const upstream = proxyRequest(new URL(req.url || "/", backend), { method: req.method, headers: req.headers }, (response) => {
        if (req.url !== "/api/logout") {
          res.writeHead(response.statusCode || 502, response.headers); response.pipe(res); return;
        }
        const chunks: Buffer[] = [];
        response.on("data", (chunk: Buffer) => chunks.push(chunk));
        response.on("end", () => {
          held = { response: res, release: () => { res.writeHead(response.statusCode || 502, response.headers); res.end(Buffer.concat(chunks)); } };
        });
        res.on("close", () => { if (!res.writableFinished) aborted = true; });
      });
      upstream.on("error", () => { res.writeHead(502); res.end(); });
      req.pipe(upstream);
    });
    await new Promise<void>((resolve) => proxy.listen(0, "127.0.0.1", resolve));
    const address = proxy.address();
    if (!address || typeof address === "string") throw new Error("proxy port unavailable");
    const origin = `http://127.0.0.1:${address.port}`;
    const username = "admin";
    const password = "correct horse battery staple";
    let recovery = "";
    try {
      await expect.poll(async () => { try { return (await fetch(backend + "/api/auth/status")).status; } catch { return 0; } }, { timeout: 30_000 }).toBe(200);
      const login = await page.request.post(origin + "/api/login", { headers: { Origin: origin }, data: { password: "browser-test-only" } });
      expect(login.status()).toBe(200);
      if (mode === "controller") {
        const setup = await page.request.post(origin + "/api/controller/setup", { headers: { Origin: origin }, data: { username } });
        expect(setup.status()).toBe(200);
        const secret = (await setup.json()).totp_secret as string;
        const activation = await page.request.post(origin + "/api/controller/activate", {
          headers: { Origin: origin }, data: { username, password, totp_secret: secret, confirmation_code: totp(secret) },
        });
        expect(activation.status()).toBe(200);
        recovery = (await activation.json()).recovery_codes[0];
        const node = { node_id: "11111111-1111-4111-8111-111111111111", name: "Previous remote node", binding_epoch: 1, status: "online", state_epoch: "22222222-2222-4222-8222-222222222222", boot_id: "33333333-3333-4333-8333-333333333333", last_confirmed_at: null, application_version: "test", contract_version: 1, capabilities: ["snapshot.v1"] };
        await page.route("**/api/controller/nodes", (route) => route.fulfill({ json: { controller_id: node.state_epoch, nodes: [node] } }));
      }
      await page.goto(origin);
      await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
      if (mode === "controller") {
        await page.getByLabel(m.fleet.server, { exact: true }).selectOption("11111111-1111-4111-8111-111111111111");
        await expect(page.getByRole("heading", { name: "Previous remote node", exact: true })).toBeVisible();
      }
      await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
      const submit = page.getByRole("button", { name: m.login.logIn, exact: true });
      await expect(submit).toBeDisabled();
      await expect.poll(() => Boolean(held)).toBe(true);
      await expect(page.getByRole("heading", { name: "Previous remote node", exact: true })).toHaveCount(0);
      await expect(submit).toBeEnabled({ timeout: 15_000 });
      await expect.poll(() => aborted).toBe(true);
      if (mode === "controller") {
        await page.getByLabel(m.controller.username, { exact: true }).fill(username);
        await page.getByLabel(m.controller.password, { exact: true }).fill(password);
        await page.getByRole("button", { name: m.controller.useRecovery, exact: true }).click();
        await page.getByLabel(m.controller.recoveryCode, { exact: true }).fill(recovery);
      } else {
        await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
      }
      await submit.click();
      await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
      const cookieNames = (await page.context().cookies(origin)).map((cookie) => cookie.name);
      expect(cookieNames.length).toBeGreaterThan(0);
      held!.release();
      await expect.poll(async () => (await page.request.get(origin + "/api/state")).status()).toBe(200);
      await page.reload();
      await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
      expect((await page.context().cookies(origin)).map((cookie) => cookie.name)).toEqual(cookieNames);
      if (mode === "controller") await expect(page.getByLabel(m.fleet.server, { exact: true })).toHaveValue("");
    } finally {
      held?.response.destroy();
      proxy.closeAllConnections();
      await new Promise<void>((resolve) => proxy.close(() => resolve()));
      child.kill("SIGTERM");
      await new Promise<void>((resolve) => { if (child.exitCode !== null) resolve(); else child.once("exit", () => resolve()); });
    }
  });
}

function totp(secret: string): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0, value = 0;
  const bytes: number[] = [];
  for (const character of secret) {
    value = (value << 5) | alphabet.indexOf(character); bits += 5;
    if (bits >= 8) { bits -= 8; bytes.push((value >>> bits) & 255); }
  }
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30_000)));
  const digest = createHmac("sha1", Buffer.from(bytes)).update(counter).digest();
  return String((digest.readUInt32BE(digest[digest.length - 1] & 15) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}
