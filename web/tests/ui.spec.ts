import { randomBytes } from "node:crypto";
import { expect, test as base, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { messages, type Messages } from "../src/i18n";

type Session = Awaited<ReturnType<APIRequestContext["storageState"]>>;

const test = base.extend<{ m: Messages; noPageErrors: void }, { session: Session }>({
  session: [async ({ playwright }, use, workerInfo) => {
    const baseURL = workerInfo.project.use.baseURL!;
    const request = await playwright.request.newContext({ baseURL });
    try {
      const response = await request.post("/api/login", {
        headers: { Origin: baseURL },
        data: { password: "browser-test-only" },
      });
      expect(response.status()).toBe(200);
      // Reuse a real session in memory, without weakening the login limiter.
      await use(await request.storageState());
    } finally {
      await request.dispose();
    }
  }, { scope: "worker" }],
  m: async ({ locale }, use) => {
    await use(messages[locale?.startsWith("ru") ? "ru" : "en"]);
  },
  noPageErrors: [async ({ page }, use) => {
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await use();
    expect(errors).toEqual([]);
  }, { auto: true }],
});

async function login(page: Page, m: Messages) {
  await page.goto("/");
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
}

async function openDashboard(page: Page, m: Messages, session: Session) {
  await page.context().addCookies(session.cookies);
  await page.goto("/");
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
}

async function createTunnel(page: Page, m: Messages, profile: string) {
  const name = `ui-${randomBytes(4).toString("hex")}`;
  const suggestion = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/tunnels/suggestion" && url.searchParams.get("profile") === profile;
  });
  await page.getByRole("button", { name: m.common.createTunnel, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(m.forms.protocol, { exact: true }).selectOption(profile);
  const response = await suggestion;
  expect(response.status()).toBe(200);
  const defaults = await response.json();
  await expect(dialog.getByLabel(m.forms.listenPort, { exact: true })).toHaveValue(String(defaults.suggestion.port));
  await dialog.getByLabel(m.forms.nameInterface, { exact: true }).fill(name);
  await dialog.getByRole("button", { name: m.common.createTunnel, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const tunnel = page.getByRole("article").filter({ has: page.getByRole("heading", { name, exact: true }) });
  await expect(tunnel).toBeVisible();
  return tunnel;
}

async function createClient(page: Page, tunnel: Locator, m: Messages, name = "UI test client") {
  await tunnel.getByRole("button", { name: m.common.createClient, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(m.forms.clientName, { exact: true }).fill(name);
  await dialog.getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const client = tunnel.locator(".client-row").filter({ hasText: name });
  await expect(client).toBeVisible();
  return client;
}

async function deleteTunnel(page: Page, tunnel: Locator, m: Messages) {
  page.once("dialog", (dialog) => dialog.accept());
  await tunnel.getByRole("button", { name: m.common.delete, exact: true }).first().click();
  await expect(tunnel).not.toBeVisible();
}

async function expectContained(page: Page, container: Locator) {
  expect(await container.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
}

async function expectDecodedImage(image: Locator) {
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.complete && element.naturalWidth > 0)).toBe(true);
}

test("authentication, maintenance, saved preferences and logout", async ({ page, m, colorScheme }) => {
  await page.goto("/");
  expect((await page.request.get("/api/state")).status()).toBe(401);
  await page.getByLabel(m.login.password, { exact: true }).fill("incorrect-test-password");
  const rejected = page.waitForResponse((response) => response.url().endsWith("/api/login"));
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  expect((await rejected).status()).toBe(401);
  await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).not.toBeVisible();
  await login(page, m);

  await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: m.maintenance.tabs.support, exact: true }).click();
  await expect(dialog.getByRole("button", { name: m.maintenance.downloadSupport, exact: true })).toBeVisible();
  await expect(dialog.getByRole("region", { name: m.maintenance.tlsStatus, exact: true })).toBeVisible();
  await expect(dialog).not.toContainText("awg show");
  await expectContained(page, dialog);
  const traffic = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/traffic-summary");
  await dialog.getByRole("button", { name: m.maintenance.tabs.traffic, exact: true }).click();
  expect((await traffic).status()).toBe(200);
  await expect(dialog.getByText(m.maintenance.trafficDisabled, { exact: true })).not.toBeVisible();
  await expect(dialog.getByRole("heading", { name: m.maintenance.traffic, exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: m.common.close, exact: true }).click();
  await expect(page.getByRole("button", { name: m.common.maintenance, exact: true })).toBeFocused();

  await expect(page.locator("html")).toHaveAttribute("data-theme", colorScheme || "light");
  await page.getByRole("button", { name: m.aria.toggleTheme, exact: true }).click();
  await page.getByRole("button", { name: m.aria.toggleLanguage, exact: true }).click();
  const switched = m === messages.en ? messages.ru : messages.en;
  await expect(page.locator("html")).toHaveAttribute("lang", m === messages.en ? "ru" : "en");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", m === messages.en ? "ru" : "en");
  await expect(page.locator("html")).toHaveAttribute("data-theme", colorScheme === "dark" ? "light" : "dark");
  await page.getByRole("button", { name: switched.common.logOut, exact: true }).click();
  await expect(page.getByLabel(switched.login.password, { exact: true })).toBeVisible();
  expect((await page.request.get("/api/state")).status()).toBe(401);
});

test("server name can be set, changed and removed from the header", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const serverNameButton = page.getByRole("button", { name: m.serverName.add, exact: true });
  await expect(serverNameButton).toBeVisible();
  await serverNameButton.click();
  const dialog = page.getByRole("dialog");
  const input = dialog.getByLabel(m.serverName.name, { exact: true });
  await input.fill("Moscow02");
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(dialog).not.toBeVisible();

  const configuredName = page.getByRole("button", { name: m.serverName.edit, exact: true });
  await expect(configuredName).toHaveText("Moscow02");
  await configuredName.click();
  await expect(input).toHaveValue("Moscow02");
  await input.fill("Office7");
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(configuredName).toHaveText("Office7");

  await configuredName.click();
  await input.fill("");
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(page.getByRole("button", { name: m.serverName.add, exact: true })).toBeVisible();
});

test("AWG3 forms preserve input on error and support client and tunnel edits", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const tunnel = await createTunnel(page, m, "awg_3");
  const client = await createClient(page, tunnel, m);
  await expect(client).toHaveClass(/client-status-enabled/);
  await client.getByRole("button", { name: m.common.disable, exact: true }).click();
  await expect(client).toHaveClass(/client-status-disabled/);
  await client.getByRole("button", { name: m.common.enable, exact: true }).click();
  await expect(client).toHaveClass(/client-status-enabled/);

  await client.getByRole("button", { name: m.common.edit, exact: true }).click();
  const dialog = page.getByRole("dialog");
  const note = '<img src=x onerror="alert(1)">';
  await dialog.getByLabel(m.forms.notes, { exact: true }).fill(note);
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(client.getByText(note, { exact: true })).toBeVisible();
  await expect(client.locator("img")).toHaveCount(0);

  await tunnel.getByRole("button", { name: m.common.settings, exact: true }).click();
  const port = dialog.getByLabel(m.forms.listenPort, { exact: true });
  const originalPort = await port.inputValue();
  await port.fill("65536");
  await dialog.getByLabel("DNS", { exact: true }).fill("9.9.9.9");
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  const error = dialog.getByRole("alert");
  await expect(error).toBeVisible();
  await expect(port).toHaveValue("65536");
  await expect(dialog.getByLabel("DNS", { exact: true })).toHaveValue("9.9.9.9");
  await error.scrollIntoViewIfNeeded();
  expect(await error.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const top = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
    return top !== null && element.contains(top);
  })).toBe(true);
  await expectContained(page, dialog);
  await port.fill(originalPort);
  await dialog.getByLabel(m.common.enabled, { exact: true }).uncheck();
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await tunnel.getByRole("button", { name: m.common.settings, exact: true }).click();
  await expect(dialog.getByLabel("DNS", { exact: true })).toHaveValue("9.9.9.9");
  await expect(dialog.getByLabel(m.common.enabled, { exact: true })).not.toBeChecked();
  await dialog.getByLabel(m.common.enabled, { exact: true }).check();
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(dialog).not.toBeVisible();

  await tunnel.getByRole("button", { name: m.tunnel.protocol, exact: true }).click();
  await expect(dialog.getByText(m.forms.awg3Warning, { exact: true })).toBeVisible();
  await dialog.getByLabel("Jmin", { exact: true }).fill("9999");
  await dialog.getByRole("button", { name: m.forms.saveProtocol, exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await dialog.getByLabel("Jmin", { exact: true }).fill("10");
  await dialog.getByRole("button", { name: m.forms.saveProtocol, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await deleteTunnel(page, tunnel, m);
});

for (const profile of ["awg_2_0", "awg_3"]) {
  test(`${profile} config exports and QR switching do not show stale images`, async ({ page, m, session }) => {
    await openDashboard(page, m, session);
    const tunnel = await createTunnel(page, m, profile);
    const client = await createClient(page, tunnel, m);
    let vpnRequests = 0;
    let rawRequests = 0;
    page.on("request", (request) => {
      const path = new URL(request.url()).pathname;
      if (path.endsWith("/amnezia-vpn-qr")) vpnRequests++;
      if (path.endsWith("/qr")) rawRequests++;
    });
    let releaseRaw = () => {};
    const rawGate = new Promise<void>((resolve) => { releaseRaw = resolve; });
    await page.route("**/api/clients/*/qr", async (route) => {
      await rawGate;
      await route.continue();
    });
    await client.getByRole("button", { name: m.common.config, exact: true }).click();
    const dialog = page.locator("dialog.dialog");
    const image = dialog.locator(".qr-image");
    await expectDecodedImage(image);
    const vpnSource = await image.getAttribute("src");
    await expect(image).toHaveAttribute("alt", m.clientConfig.amneziaVPNAlt("UI test client"));
    const rawRequest = page.waitForRequest("**/api/clients/*/qr");
    try {
      await dialog.getByRole("tab", { name: "AmneziaWG", exact: true }).click();
      await rawRequest;
      await expect(image).toHaveCount(0);
      await expect(dialog.getByRole("status")).toBeVisible();
    } finally {
      releaseRaw();
    }
    await expectDecodedImage(image);
    const rawSource = await image.getAttribute("src");
    expect(rawSource !== vpnSource).toBe(true);
    await expect(image).toHaveAttribute("alt", m.clientConfig.amneziaWGAlt("UI test client"));
    await expectContained(page, dialog);
    for (let i = 0; i < 3; i++) {
      await dialog.getByRole("tab", { name: "AmneziaVPN", exact: true }).click();
      await expect(image).toHaveAttribute("src", vpnSource!);
      await dialog.getByRole("tab", { name: "AmneziaWG", exact: true }).click();
      await expect(image).toHaveAttribute("src", rawSource!);
    }
    expect({ vpnRequests, rawRequests }).toEqual({ vpnRequests: 1, rawRequests: 1 });

    const downloadEvent = page.waitForEvent("download");
    await dialog.getByRole("button", { name: m.clientConfig.downloadConf, exact: true }).click();
    const download = await downloadEvent;
    expect(download.suggestedFilename().endsWith(".conf")).toBe(true);
    const stream = await download.createReadStream();
    const chunks: Buffer[] = [];
    for await (const chunk of stream) chunks.push(Buffer.from(chunk));
    const config = Buffer.concat(chunks).toString("utf8");
    expect(config.includes("[Interface]") && config.includes("[Peer]")).toBe(true);
    expect(config.includes("HeaderProtectionKey = ")).toBe(profile === "awg_3");
    await dialog.getByRole("button", { name: m.clientConfig.copyVpnKey, exact: true }).click();
    const link = dialog.getByLabel(m.clientConfig.vpnImportLink, { exact: true });
    await expect(link).toBeVisible();
    // Keep secret-bearing values out of assertion output and CI logs.
    expect((await link.inputValue()).startsWith("vpn://")).toBe(true);
    await expectContained(page, dialog);
    await dialog.getByRole("button", { name: m.common.close, exact: true }).click();
    await expect(dialog).not.toBeVisible();

    // Cache is dialog-local: reopening must fetch fresh config, with a retry on failure.
    await page.route("**/api/clients/*/amnezia-vpn-qr?*", (route) => route.fulfill({ status: 503, body: "unavailable" }), { times: 1 });
    await client.getByRole("button", { name: m.common.config, exact: true }).click();
    await expect(dialog.getByRole("alert")).toBeVisible();
    await expect(image).toHaveCount(0);
    await dialog.getByRole("button", { name: m.common.retry, exact: true }).click();
    await expectDecodedImage(image);
    await dialog.getByRole("button", { name: m.common.close, exact: true }).click();
    await deleteTunnel(page, tunnel, m);
  });
}
