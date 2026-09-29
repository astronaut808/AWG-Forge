import { createHmac } from "node:crypto";
import { spawn, type ChildProcess } from "node:child_process";
import { createServer } from "node:net";
import { expect, test, type BrowserContext } from "@playwright/test";
import { messages } from "../src/i18n";

test("controller activation, TOTP login, recovery, reauthentication and mobile layout", async ({ page, browser }, testInfo) => {
  test.setTimeout(180_000);
  const m = messages[testInfo.project.use.locale?.startsWith("ru") ? "ru" : "en"];
  const port = await freePort();
  const origin = `http://127.0.0.1:${port}`;
  const child = spawn(process.execPath, ["web/tests/server.mjs"], {
    cwd: process.cwd(),
    env: { ...process.env, WEBUI_PORT: String(port) },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let startupOutput = "";
  let observerContext: BrowserContext | null = null;
  child.stderr?.on("data", (chunk: Buffer) => { startupOutput = (startupOutput + chunk.toString()).slice(-4000); });
  try {
    await waitForServer(origin, child, () => startupOutput);
    let statusRequests = 0;
    await page.route("**/api/auth/status", async (route) => {
      statusRequests++;
      if (statusRequests === 1) {
        await route.fulfill({ status: 200, contentType: "application/json", body: '{"mode":"activating"}' });
      } else {
        await route.continue();
      }
    });
    await page.goto(origin);
    await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeDisabled();
    await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeEnabled({ timeout: 10_000 });
    expect(statusRequests).toBeGreaterThan(1);
    await page.unroute("**/api/auth/status");
    await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
    observerContext = await browser.newContext({ locale: testInfo.project.use.locale || "en-US" });
    const observer = await observerContext.newPage();
    await observer.goto(origin);
    await expect(observer.getByLabel(m.login.password, { exact: true })).toBeVisible();
    await expect(observer.getByLabel(m.controller.username, { exact: true })).toHaveCount(0);

    await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
    let dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: m.maintenance.tabs.controller, exact: true }).click();
    await dialog.getByLabel(m.controller.username, { exact: true }).fill("admin");
    await dialog.getByLabel(m.controller.password, { exact: true }).fill("correct horse battery staple");
    await dialog.getByRole("button", { name: m.controller.generateMFA }).click();
    const secret = (await dialog.locator(".controller-secret").textContent())?.trim() || "";
    expect(secret).toMatch(/^[A-Z2-7]+$/);
    await expect(dialog.locator(".controller-qr")).toBeVisible();
    await waitForSafeTOTPStep();
    const confirmationStep = Math.floor(Date.now() / 30_000) - 1;
    await dialog.getByLabel(m.controller.code, { exact: true }).fill(totp(secret, confirmationStep));
    await page.route("**/api/controller/activate", async (route) => {
      const request = route.request();
      const response = await fetch(request.url(), {
        method: request.method(),
        headers: request.headers(),
        body: request.postData(),
      });
      const body = await response.text();
      await new Promise((resolve) => setTimeout(resolve, 1500));
      await route.fulfill({ status: response.status, headers: Object.fromEntries(response.headers), body });
    });
    await dialog.getByRole("button", { name: m.controller.activate }).click();
    await expect(dialog.locator(".controller-codes code")).toHaveCount(10);
    await expect(observer.getByLabel(m.controller.username, { exact: true })).toBeVisible({ timeout: 10_000 });
    const firstCodes = await dialog.locator(".controller-codes code").allTextContents();
    expect(firstCodes).toHaveLength(10);
    await dialog.getByRole("button", { name: m.common.close }).click();
    await expect(dialog.locator(".controller-codes code")).toHaveCount(10);
    await dialog.getByLabel(m.controller.codesSaved).check();
    await dialog.getByRole("button", { name: m.controller.finish }).click();

    await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
    await page.getByLabel(m.controller.username, { exact: true }).fill("admin");
    await page.getByLabel(m.controller.password, { exact: true }).fill("correct horse battery staple");
    await page.getByLabel(m.controller.code, { exact: true }).fill(totp(secret, Math.floor(Date.now() / 30_000)));
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();

    await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
    dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: m.maintenance.tabs.controller, exact: true }).click();
    await dialog.getByLabel(m.controller.password, { exact: true }).fill("correct horse battery staple");
    await dialog.getByRole("button", { name: m.controller.useRecovery }).click();
    await dialog.getByLabel(m.controller.recoveryCode, { exact: true }).fill(firstCodes[0]);
    await dialog.getByRole("button", { name: m.controller.reauth }).click();
    await dialog.getByRole("button", { name: m.controller.rotateCodes }).click();
    await expect(dialog.locator(".controller-codes code")).toHaveCount(10);
    const replacementCodes = await dialog.locator(".controller-codes code").allTextContents();
    expect(replacementCodes).toHaveLength(10);
    expect(replacementCodes[0]).not.toBe(firstCodes[0]);
    await dialog.getByLabel(m.controller.codesSaved).check();
    await dialog.getByRole("button", { name: m.controller.finish }).click();

    await page.getByRole("button", { name: m.common.logOut, exact: true }).click();
    const oldCodeAttempt = await page.request.post(`${origin}/api/controller/login/recovery`, {
      headers: { Origin: origin },
      data: { username: "admin", password: "correct horse battery staple", code: firstCodes[1] },
    });
    expect(oldCodeAttempt.status()).toBe(401);
    await page.getByLabel(m.controller.username, { exact: true }).fill("admin");
    await page.getByLabel(m.controller.password, { exact: true }).fill("correct horse battery staple");
    await page.getByRole("button", { name: m.controller.useRecovery }).click();
    await page.getByLabel(m.controller.recoveryCode, { exact: true }).fill(replacementCodes[0]);
    await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
    await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  } finally {
    await observerContext?.close();
    await stopServer(child);
  }
});

function totp(secret: string, step: number): string {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0;
  let value = 0;
  const bytes: number[] = [];
  for (const character of secret) {
    value = (value << 5) | alphabet.indexOf(character);
    bits += 5;
    if (bits >= 8) { bits -= 8; bytes.push((value >>> bits) & 255); }
  }
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(step));
  const digest = createHmac("sha1", Buffer.from(bytes)).update(counter).digest();
  const offset = digest[digest.length - 1] & 15;
  const number = (digest.readUInt32BE(offset) & 0x7fffffff) % 1_000_000;
  return String(number).padStart(6, "0");
}

async function waitForSafeTOTPStep(): Promise<void> {
  while (true) {
    const second = Math.floor(Date.now() / 1000) % 30;
    if (second >= 2 && second <= 15) return;
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
}

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("no test port");
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return address.port;
}

async function waitForServer(origin: string, child: ChildProcess, output: () => string): Promise<void> {
  const until = Date.now() + 90_000;
  while (Date.now() < until) {
    if (child.exitCode !== null) throw new Error(`controller test server exited: ${output()}`);
    try { if ((await fetch(origin)).ok) return; } catch { /* server is still starting */ }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`controller test server did not start: ${output()}`);
}

async function stopServer(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) return;
  child.kill("SIGTERM");
  await new Promise<void>((resolve) => {
    const timer = setTimeout(() => { child.kill("SIGKILL"); resolve(); }, 10_000);
    child.once("exit", () => { clearTimeout(timer); resolve(); });
  });
}
