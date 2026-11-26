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

test("WARP import rejects invalid replacements and allows a corrected retry", async ({ page, m, session }) => {
  await openMaintenance(page, m, session);
  await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.warp, exact: true }).click();
  const summary = await page.request.get("/api/warp");
  expect(summary.ok()).toBe(true);
  expect((await summary.json()).warp.configured).toBe(false);
  await page.getByText(m.warp.manualImport, { exact: true }).click();
  const input = page.getByLabel(m.warp.wireGuardConfig, { exact: true });
  const submit = page.getByRole("button", { name: m.warp.importConfig, exact: true });
  const privateKey = Buffer.alloc(32, 1).toString("base64");
  const peerKey = Buffer.alloc(32, 2).toString("base64");
  const original = "192.0.2.10:2408";
  const replacement = "192.0.2.20:2408";
  const config = `[Interface]
PrivateKey = ${privateKey}
Address = 172.16.0.2/32
Jc = 4
Jmin = 40
Jmax = 40
I1 = <b 0x0102>

[Peer]
PublicKey = ${peerKey}
Endpoint = ${original}
`;
  const corrected = config.replace(original, replacement);
  const importConfig = async (value: string) => {
    await input.fill(value);
    const [response] = await Promise.all([
      page.waitForResponse((res) => res.request().method() === "POST" && new URL(res.url()).pathname === "/api/warp/import"),
      submit.click(),
    ]);
    return response;
  };

  try {
    expect((await importConfig(config)).status()).toBe(200);
    await expect(page.getByText(original, { exact: true })).toBeVisible();

    for (const invalid of [corrected.replace("Jmin = 40", "Jmin = 41"), corrected.replace("0x0102", "0x123")]) {
      const response = await importConfig(invalid);
      expect(response.status()).toBe(400);
      const failure = await response.json();
      expect(failure.code).toBe("warp_import_failed");
      expect(JSON.stringify(failure).includes(privateKey)).toBe(false);
      await expect(page.locator(".toast")).toHaveText(failure.error);
      expect((await input.inputValue()) === invalid).toBe(true);
      await expect(page.getByText(original, { exact: true })).toBeVisible();
      const saved = await page.request.get("/api/warp");
      expect(saved.ok()).toBe(true);
      expect((await saved.json()).warp.endpoint).toBe(original);
    }

    expect((await importConfig(corrected)).status()).toBe(200);
    await expect(page.getByText(replacement, { exact: true })).toBeVisible();
    await openMaintenance(page, m, session);
    await page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.warp, exact: true }).click();
    await expect(page.getByText(replacement, { exact: true })).toBeVisible();
  } finally {
    const response = await page.request.delete("/api/warp", { headers: { Origin: new URL(page.url()).origin } });
    expect(response.ok()).toBe(true);
  }
});

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
