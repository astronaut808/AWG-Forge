import { expect, test } from "@playwright/test";
import { messages } from "../src/i18n";

const invitationID = "12345678-1234-4234-8234-123456789abc";
const enrollmentID = "22345678-1234-4234-8234-123456789abc";
const controllerID = "32345678-1234-4234-8234-123456789abc";
const nodeID = "42345678-1234-4234-8234-123456789abc";
const syntheticSecret = "onboarding-canary-".padEnd(43, "x");

test("node onboarding compares explicitly, waits for presence, rejects XSS and keeps secret out of public artifacts", async ({ page }, testInfo) => {
  const m = messages[testInfo.project.use.locale?.startsWith("ru") ? "ru" : "en"];
  await page.goto("/");
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
  let phase = "waiting";
  let connected = false;
  let decisions = 0;
  let polls = 0;
  const publicRequests: string[] = [];
  const name = "Node ' ; $(touch should-not-exist) <img src=x onerror=alert(1)>";
  const expires = new Date(Date.now() + 600_000).toISOString();
  await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
  await page.route("**/api/auth/session", (route) => route.fulfill({ json: { mode: "controller", authenticated: true, username: "admin", recent_auth: true, expires_at: "2099-01-01T00:00:00Z" } }));
  await page.route("**/api/controller/**", (route) => {
    const request = route.request();
    publicRequests.push(request.url() + JSON.stringify(request.headers()) + (request.postData() || ""));
    const path = new URL(request.url()).pathname;
    const headers = { "Cache-Control": "no-store" };
    if (path.endsWith("/control/status")) return route.fulfill({ headers, json: { controller_id: controllerID, control: { enabled: true, bind_ip: "192.0.2.1", advertised: "controller.example", port: 9443, ca_pin: "sha256:" + "a".repeat(64) } } });
    if (path.endsWith("/installer-info")) return route.fulfill({ headers, json: { supported: false, reason: "unpublished_build" } });
    if (path.endsWith("/invite")) return route.fulfill({ headers, json: { invitation_id: invitationID, secret: syntheticSecret, controller_url: "https://controller.example:9443", ca_pin: "sha256:" + "a".repeat(64), ca_cert_pem: "public CA", expires_at: expires } });
    if (path.endsWith("/enrollments/status")) { polls++; return route.fulfill({ headers, json: { invitation_id: invitationID, controller_id: controllerID, enrollment_id: phase === "waiting" ? undefined : enrollmentID, node_id: nodeID, status: phase, connected, expires_at: expires } }); }
    if (path.endsWith("/review")) return route.fulfill({ headers, json: { enrollment_id: enrollmentID, requested_name: name, verification_code: "ABCD-EFGH", status: "pending", expires_at: expires } });
    if (path.endsWith("/decide")) { decisions++; expect(request.postDataJSON()).toEqual({ enrollment_id: enrollmentID, verification_code: "ABCD-EFGH", approve: true }); phase = "approved"; return route.fulfill({ headers, json: { approved: true } }); }
    return route.fulfill({ status: 404, json: { error: "unknown" } });
  });
  await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: m.maintenance.tabs.controller, exact: true }).click();
  await dialog.getByRole("button", { name: m.onboarding.add, exact: true }).click();
  await dialog.getByLabel(m.onboarding.name, { exact: true }).fill(name);
  await dialog.getByRole("button", { name: m.onboarding.invite, exact: true }).click();
  await expect(dialog.getByLabel(m.onboarding.secret, { exact: true })).toHaveValue(syntheticSecret);
  await expect(dialog.getByText(m.onboarding.unpublished, { exact: true })).toBeVisible();
  const publicArgs = await dialog.getByText(/--mode /).textContent();
  expect(publicArgs).not.toContain(syntheticSecret);
  expect(publicArgs).toContain("'\\''");
  expect(await page.evaluate(() => document.querySelectorAll("img[src='x']").length)).toBe(0);
  phase = "pending";
  const approve = dialog.getByRole("button", { name: m.onboarding.approve, exact: true });
  await expect(approve).toBeDisabled();
  expect(decisions).toBe(0);
  await dialog.getByLabel(m.onboarding.matched, { exact: true }).check();
  await approve.click();
  await expect(dialog.getByText(m.onboarding.presence, { exact: true })).toBeVisible();
  await expect(dialog.getByText(m.onboarding.connected, { exact: true })).toHaveCount(0);
  expect(decisions).toBe(1);
  connected = true;
  await expect(dialog.getByText(m.onboarding.connected, { exact: true })).toBeVisible({ timeout: 10_000 });
  await expect(dialog.getByLabel(m.onboarding.secret, { exact: true })).toHaveCount(0);
  expect(publicRequests.join("\n")).not.toContain(syntheticSecret);
  expect(await page.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage }, url: location.href }))).not.toContain(syntheticSecret);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
  await dialog.getByRole("button", { name: m.onboarding.cancel, exact: true }).click();
  const stoppedPolls = polls;
  await page.waitForTimeout(1700);
  expect(polls).toBe(stoppedPolls);
});

test("node onboarding discards replies and secret when closed or account changes", async ({ page }, testInfo) => {
  const m = messages[testInfo.project.use.locale?.startsWith("ru") ? "ru" : "en"];
  await page.goto("/");
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
  let username = "admin";
  let pendingInvitation: (() => Promise<void>) | null = null;
  await page.route("**/api/auth/status", (route) => route.fulfill({ json: { mode: "controller" } }));
  await page.route("**/api/auth/session", (route) => route.fulfill({ json: { mode: "controller", authenticated: true, username, recent_auth: true, expires_at: "2099-01-01T00:00:00Z" } }));
  await page.route("**/api/controller/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/control/status")) return route.fulfill({ json: { controller_id: controllerID, control: { enabled: true, bind_ip: "127.0.0.1", advertised: "127.0.0.1", port: 9443, ca_pin: "sha256:" + "a".repeat(64) } } });
    if (path.endsWith("/installer-info")) return route.fulfill({ json: { supported: false, reason: "unpublished_build" } });
    if (path.endsWith("/invite")) { await new Promise<void>((resolve) => { pendingInvitation = async () => { await route.fulfill({ json: { invitation_id: invitationID, secret: syntheticSecret, controller_url: "https://127.0.0.1:9443", ca_pin: "sha256:" + "a".repeat(64), ca_cert_pem: "public", expires_at: new Date(Date.now() + 600_000).toISOString() } }); resolve(); }; }); return; }
    return route.fulfill({ json: { invitation_id: invitationID, controller_id: controllerID, status: "waiting", connected: false, expires_at: new Date(Date.now() + 600_000).toISOString() } });
  });
  await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: m.maintenance.tabs.controller, exact: true }).click();
  await dialog.getByRole("button", { name: m.onboarding.add, exact: true }).click();
  await dialog.getByRole("button", { name: m.onboarding.invite, exact: true }).click();
  await expect.poll(() => Boolean(pendingInvitation)).toBe(true);
  await dialog.getByRole("button", { name: m.onboarding.cancel, exact: true }).click();
  if (pendingInvitation) await (pendingInvitation as () => Promise<void>)().catch(() => {});
  await expect(dialog.getByLabel(m.onboarding.secret, { exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: m.onboarding.add, exact: true }).click();
  pendingInvitation = null;
  await dialog.getByRole("button", { name: m.onboarding.invite, exact: true }).click();
  await expect.poll(() => Boolean(pendingInvitation)).toBe(true);
  username = "other-admin";
  if (pendingInvitation) await (pendingInvitation as () => Promise<void>)();
  await expect(dialog.getByLabel(m.onboarding.name, { exact: true })).toHaveCount(0);
  await expect(dialog.getByLabel(m.onboarding.secret, { exact: true })).toHaveCount(0);
});
