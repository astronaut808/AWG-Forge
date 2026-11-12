import { expect, test as base, type APIRequestContext, type Page } from "@playwright/test";
import { messages, type Messages } from "../src/i18n";

type Session = Awaited<ReturnType<APIRequestContext["storageState"]>>;

const test = base.extend<{ m: Messages; noPageErrors: void }, { session: Session }>({
  session: [async ({ playwright }, use, workerInfo) => {
    const request = await playwright.request.newContext({ baseURL: workerInfo.project.use.baseURL });
    try {
      const response = await request.post("/api/login", { headers: { Origin: workerInfo.project.use.baseURL! }, data: { password: "browser-test-only" } });
      expect(response.status()).toBe(200);
      await use(await request.storageState());
    } finally {
      await request.dispose();
    }
  }, { scope: "worker" }],
  m: ({ locale }, use) => use(messages[locale?.startsWith("ru") ? "ru" : "en"]),
  noPageErrors: [async ({ page }, use) => {
    const errors: string[] = [];
    const cspErrors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => {
      if (message.type() === "error" && /Content Security Policy|violates.*directive/i.test(message.text())) cspErrors.push(message.text());
    });
    await use();
    expect(errors).toEqual([]);
    expect(cspErrors).toEqual([]);
  }, { auto: true }],
});

async function openMaintenance(page: Page, m: Messages, session: Session) {
  await page.context().addCookies(session.cookies);
  await page.goto("/");
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
  await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
}

test("audit retry exposes initial failure instead of an empty log", async ({ page, m, session }) => {
  let attempts = 0;
  await page.route("**/api/audit-log?tail=100", async (route) => {
    attempts += 1;
    await route.fulfill(attempts === 1 ? { status: 503, body: "unavailable" } : {
      status: 200, contentType: "application/json", body: JSON.stringify({ events: [{ time: "2026-11-03T12:00:00Z", level: "warn", event: "tunnel.updated", message: "Configuration updated" }] }),
    });
  });
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.logs, exact: true }).click();
  await expect(page.locator(".resource-error")).toBeVisible();
  await expect(page.locator(".audit-row")).toHaveCount(0);
  await page.locator(".resource-error").getByRole("button").click();
  await expect(page.locator(".audit-row")).toHaveCount(1);
  await expect(page.locator(".audit-row")).toContainText("tunnel.updated");
});

test("audit keeps last successful data after a polling failure", async ({ page, m, session }) => {
  let attempts = 0;
  await page.route("**/api/audit-log?tail=100", async (route) => {
    attempts += 1;
    await route.fulfill(attempts === 1 ? {
      status: 200, contentType: "application/json", body: JSON.stringify({ events: [{ time: "2026-11-03T12:00:00Z", level: "ok", event: "tunnel.created", message: "Created" }] }),
    } : { status: 503, body: "unavailable" });
  });
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.logs, exact: true }).click();
  await expect(page.locator(".audit-row")).toHaveCount(1);
  await expect(page.locator(".resource-error")).toContainText(/last successful|последнего успешного/i, { timeout: 8_000 });
  await expect(page.locator(".audit-row")).toHaveCount(1);
});

test("audit polling waits for a slow request before starting the next refresh", async ({ page, m, session }) => {
  let attempts = 0;
  let releaseFirst: (() => void) | undefined;
  const firstResponse = new Promise<void>((resolve) => { releaseFirst = resolve; });
  await page.route("**/api/audit-log?tail=100", async (route) => {
    attempts += 1;
    if (attempts === 1) await firstResponse;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ events: [{ time: "2026-11-03T12:00:00Z", level: "info", event: "tunnel.updated", message: "Updated" }] }) });
  });
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.logs, exact: true }).click();
  await expect.poll(() => attempts).toBe(1);
  await page.waitForTimeout(3_800);
  expect(attempts).toBe(1);
  releaseFirst?.();
  await expect(page.locator(".audit-row")).toHaveCount(1);
  await expect.poll(() => attempts, { timeout: 5_000 }).toBeGreaterThanOrEqual(2);
});

test("traffic controls wait for the initial panel response", async ({ page, m, session }) => {
  let attempts = 0;
  let releasePanel: (() => void) | undefined;
  const panelResponse = new Promise<void>((resolve) => { releasePanel = resolve; });
  await page.route("**/api/traffic-summary", async (route) => {
    attempts += 1;
    if (attempts === 2) await panelResponse;
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ enabled: true, rows: [] }) });
  });
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.traffic, exact: true }).click();
  const refresh = page.locator(".resource-toolbar").getByRole("button", { name: m.maintenance.refreshTraffic, exact: true });
  await expect(refresh).toBeDisabled();
  releasePanel?.();
  await expect(refresh).toBeEnabled();
});

test("traffic retries safely and preserves successful data on a failed refresh", async ({ page, m, session }) => {
  let attempts = 0;
  const summary = { enabled: true, rows: [{ tunnel_id: "missing-tunnel", client_id: "missing-client", rx_total: 42, tx_total: 24, rx_today: 4, tx_today: 2, rx_7d: 8, tx_7d: 4, rx_30d: 12, tx_30d: 6, limit_bytes: null }] };
  await page.route("**/api/traffic-summary", async (route) => {
    attempts += 1;
    // Overview loads its own compact traffic summary before the Maintenance panel opens.
    await route.fulfill(attempts === 2 || attempts === 4 ? { status: 503, body: "unavailable" } : { status: 200, contentType: "application/json", body: JSON.stringify(summary) });
  });
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.traffic, exact: true }).click();
  await expect(page.locator(".resource-error")).toBeVisible();
  await expect(page.locator(".resource-error").getByRole("button")).toHaveCount(0);
  const refreshOrRetry = page.locator(".resource-toolbar").getByRole("button");
  await expect(refreshOrRetry).toHaveCount(1);
  await expect(refreshOrRetry).toHaveText(/Retry|Повторить/);
  await refreshOrRetry.click();
  await expect(page.getByRole("heading", { name: m.maintenance.topClients, exact: true })).toBeVisible();
  await page.locator(".resource-toolbar").getByRole("button", { name: m.maintenance.refreshTraffic, exact: true }).click();
  await expect(page.locator(".resource-error")).toContainText(/last successful|последнего успешного/i);
  await expect(page.getByRole("heading", { name: m.maintenance.topClients, exact: true })).toBeVisible();
  await expect(refreshOrRetry).toHaveText(/Retry|Повторить/);
  await refreshOrRetry.click();
  await expect(page.locator(".resource-error")).toHaveCount(0);
  await expect(refreshOrRetry).toHaveText(m.maintenance.refreshTraffic);
  expect(attempts).toBe(5);
});
