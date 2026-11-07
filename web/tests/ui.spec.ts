import { randomBytes } from "node:crypto";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
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
    const cspErrors: string[] = [];
    page.on("console", (message) => {
      if (message.type() === "error" && /Content Security Policy|violates.*directive/i.test(message.text())) cspErrors.push(message.text());
    });
    page.on("pageerror", (error) => errors.push(error.message));
    await use();
    expect(errors).toEqual([]);
    expect(cspErrors).toEqual([]);
  }, { auto: true }],
});

async function expectActionLabels(page: Page) {
  const defects = await page.locator(".button").evaluateAll((buttons) => buttons.filter((button) => button.getBoundingClientRect().width > 0).flatMap((button) => {
    const lines = new Set<number>();
    const walker = document.createTreeWalker(button, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node; node = walker.nextNode()) {
      if (!node.textContent?.trim()) continue;
      const range = document.createRange(); range.selectNodeContents(node);
      for (const rect of Array.from(range.getClientRects())) if (rect.width > 0) lines.add(Math.round(rect.top));
    }
    return lines.size > 1 || button.scrollWidth > button.clientWidth + 1 ? [button.textContent?.trim()] : [];
  }));
  expect(defects).toEqual([]);
}

async function login(page: Page, m: Messages) {
  await page.goto("/");
  await expect(page.getByLabel(m.ui.username, { exact: true })).toHaveValue("admin");
  await page.getByLabel(m.login.password, { exact: true }).fill("browser-test-only");
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
  const preferences = await page.locator(".sidebar-preference").evaluateAll((rows) => rows.map((row) => {
    const label = row.querySelector("span")!, select = row.querySelector("select")!;
    const a = label.getBoundingClientRect(), b = select.getBoundingClientRect();
    const range = document.createRange(); range.selectNodeContents(label);
    return { clipped: range.getBoundingClientRect().width > a.width + 1, overlap: a.right > b.left + 1 && a.bottom > b.top + 1 };
  }));
  expect(preferences).toEqual([{ clipped: false, overlap: false }, { clipped: false, overlap: false }]);
}

async function openDashboard(page: Page, m: Messages, session: Session) {
  await page.context().addCookies(session.cookies);
  await page.goto("/");
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).toBeVisible();
  await expectActionLabels(page);
}

async function selectTunnel(page: Page, m: Messages, name: string) {
  const picker = page.getByRole("combobox", { name: m.ui.selectedTunnel, exact: true });
  if (page.viewportSize()!.width <= 760) {
    await expect(picker).toBeVisible();
    const id = await picker.locator("option").evaluateAll((options, target) => (options.find((option) => option.textContent?.startsWith(`${target} ·`)) as HTMLOptionElement | undefined)?.value, name);
    expect(Boolean(id)).toBe(true);
    await picker.selectOption(id!);
  } else {
    await page.locator(".tunnel-choice").filter({ hasText: name }).click();
  }
}

async function openTunnelTools(page: Page, m: Messages) {
  const toggle = page.getByRole("button", { name: m.ui.searchAndFilter, exact: true });
  if (await toggle.isVisible() && await toggle.getAttribute("aria-expanded") === "false") await toggle.click();
}

async function createTunnel(page: Page, m: Messages, profile: string) {
  const name = `ui-${randomBytes(4).toString("hex")}`;
  const suggestion = page.waitForResponse((response) => {
    const url = new URL(response.url());
    return url.pathname === "/api/tunnels/suggestion" && url.searchParams.get("profile") === profile;
  });
  await page.getByRole("button", { name: m.common.createTunnel, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: m.forms.createTunnelTitle, exact: true })).toHaveCount(1);
  await dialog.getByLabel(m.forms.protocol, { exact: true }).selectOption(profile);
  const response = await suggestion;
  expect(response.status()).toBe(200);
  const defaults = await response.json();
  await expect(dialog.getByLabel(m.forms.listenPort, { exact: true })).toHaveValue(String(defaults.suggestion.port));
  if (page.viewportSize()!.width > 760) {
    const expectAlignedFields = async () => {
      for (const [left, right] of [[m.forms.protocol, m.forms.nameInterface], [m.forms.portSelection, m.forms.listenPort], [m.forms.ipv4Subnet, m.forms.egress]]) {
        const a = (await dialog.getByLabel(left, { exact: true }).boundingBox())!;
        const b = (await dialog.getByLabel(right, { exact: true }).boundingBox())!;
        expect(Math.abs(a.y - b.y)).toBeLessThanOrEqual(1);
        expect(a.x).toBeLessThan(b.x);
      }
    };
    await expectAlignedFields();
    await dialog.getByLabel(m.forms.portSelection, { exact: true }).selectOption("manual");
    await expectAlignedFields();
    await dialog.getByLabel(m.forms.portSelection, { exact: true }).selectOption("automatic");
  }
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
  await client.getByRole("button", { name: m.ui.clientActions, exact: true }).click();
  await expectActionLabels(page);
  await expect(client.getByRole("button", { name: m.ui.connectClient, exact: true })).toHaveCount(1);
  await expect(client.locator(".client-expansion button")).toHaveCount(4);
  await expect(client.getByRole("button", { name: /QR Amnezia|vpn:\/\/|^\.conf$/ })).toHaveCount(0);
  return client;
}

async function deleteTunnel(page: Page, tunnel: Locator, m: Messages) {
  const name = await tunnel.locator(".tunnel-head").getByRole("heading", { level: 2 }).innerText();
  await tunnel.getByRole("button", { name: m.ui.connection, exact: true }).click();
  await tunnel.getByRole("button", { name: m.common.delete, exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: m.common.delete, exact: true }).click();
  await expect(page.locator(".tunnel-choice").filter({ hasText: name })).toHaveCount(0);
  const response = await page.request.get("/api/state");
  const state = await response.json();
  expect(state.tunnels.some((item: { name: string }) => item.name === name)).toBe(false);
}

async function expectContained(page: Page, container: Locator) {
  expect(await container.evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1)).toBe(true);
}

async function expectLoginAlignment(page: Page) {
  const geometry = await page.locator(".login-stack").evaluate((stack) => {
    const card = stack.querySelector(".login-card")!.getBoundingClientRect();
    const footer = stack.querySelector(".app-footer")!;
    const items = Array.from(footer.children).map((item) => item.getBoundingClientRect());
    const left = Math.min(...items.map((rect) => rect.left));
    const right = Math.max(...items.map((rect) => rect.right));
    const centers = items.map((rect) => rect.top + rect.height / 2);
    const requiredMarkers = Array.from(stack.querySelectorAll(".mantine-InputWrapper-required")).map((marker) => {
      const label = marker.parentElement!.getBoundingClientRect();
      const rect = marker.getBoundingClientRect();
      return rect.top >= label.top && rect.bottom <= label.bottom + 1;
    });
    const controls = Array.from(stack.querySelectorAll(".mantine-InputWrapper-root"));
    const heights = controls.map((control) => control.querySelector(".mantine-Input-input")!.getBoundingClientRect().height);
    const labelSizes = controls.map((control) => Number.parseFloat(getComputedStyle(control.querySelector("label")!).fontSize));
    return { centerOffset: Math.abs((left + right - card.left - card.right) / 2), rowOffset: Math.max(...centers) - Math.min(...centers), requiredMarkers, heightDelta: Math.max(...heights) - Math.min(...heights), labelSizeDelta: Math.max(...labelSizes) - Math.min(...labelSizes) };
  });
  expect(geometry.centerOffset).toBeLessThanOrEqual(1);
  expect(geometry.rowOffset).toBeLessThanOrEqual(1);
  expect(geometry.requiredMarkers).toEqual([true, true]);
  expect(geometry.heightDelta).toBeLessThanOrEqual(1);
  expect(geometry.labelSizeDelta).toBe(0);
}

async function expectDecodedImage(image: Locator) {
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.complete && element.naturalWidth > 0)).toBe(true);
}

test("authentication, maintenance, saved preferences and logout", async ({ page, m, colorScheme }) => {
  await page.goto("/");
  await expectLoginAlignment(page);
  expect((await page.request.get("/api/state")).status()).toBe(401);
  await page.getByLabel(m.login.password, { exact: true }).fill("incorrect-test-password");
  const rejected = page.waitForResponse((response) => response.url().endsWith("/api/login"));
  await page.getByRole("button", { name: m.login.logIn, exact: true }).click();
  expect((await rejected).status()).toBe(401);
  await expect(page.getByRole("button", { name: m.login.logIn, exact: true })).toBeEnabled();
  await expect(page.getByRole("button", { name: m.common.logOut, exact: true })).not.toBeVisible();
  await login(page, m);

  await page.getByRole("button", { name: m.common.maintenance, exact: true }).click();
  const maintenance = page.locator(".content-page");
  await maintenance.getByRole("button", { name: m.maintenance.tabs.support, exact: true }).click();
  await expect(maintenance.getByRole("button", { name: m.maintenance.downloadSupport, exact: true })).toBeVisible();
  await expect(maintenance.getByRole("region", { name: m.maintenance.tlsStatus, exact: true })).toBeVisible();
  await expect(maintenance).not.toContainText("awg show");
  await expectContained(page, maintenance);
  const traffic = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/traffic-summary");
  await page.locator(".main-nav").getByRole("button", { name: m.maintenance.tabs.traffic, exact: true }).click();
  expect((await traffic).status()).toBe(200);
  await expect(page.getByText(m.maintenance.trafficDisabled, { exact: true })).not.toBeVisible();
  await expect(page.getByRole("heading", { name: m.maintenance.traffic, exact: true })).toBeVisible();

  await expect(page.locator("html")).toHaveAttribute("data-theme", colorScheme || "light");
  await page.getByRole("combobox", { name: m.aria.toggleTheme, exact: true }).selectOption(colorScheme === "dark" ? "light" : "dark");
  await page.getByRole("combobox", { name: m.aria.toggleLanguage, exact: true }).selectOption(m === messages.en ? "ru" : "en");
  const switched = m === messages.en ? messages.ru : messages.en;
  await expect(page.locator("html")).toHaveAttribute("lang", m === messages.en ? "ru" : "en");
  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("lang", m === messages.en ? "ru" : "en");
  await expect(page.locator("html")).toHaveAttribute("data-theme", colorScheme === "dark" ? "light" : "dark");
  await page.getByRole("button", { name: switched.common.logOut, exact: true }).click();
  await expect(page.getByLabel(switched.login.password, { exact: true })).toBeVisible();
  await expectLoginAlignment(page);
  expect((await page.request.get("/api/state")).status()).toBe(401);
});

test("AWG1.5 protocol layout preserves its actual parameter set at desktop and mobile widths", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const tunnel = await createTunnel(page, m, "awg_1_5");
  await tunnel.getByRole("button", { name: m.ui.protocolParameters, exact: true }).click();
  const viewport = page.viewportSize()!;
  const state = await (await page.request.get("/api/state")).json();
  const name = await tunnel.locator(".tunnel-head h2").innerText();
  const expected = state.tunnels.find((item: { name: string }) => item.name === name).params.map((param: { key: string }) => param.key).sort();
  for (const width of [2534, 900, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    if (width === 390) await expect(page.getByRole("combobox", { name: m.ui.selectedTunnel, exact: true })).toBeVisible();
    else await expect(tunnel.locator(".tunnel-facts")).toHaveAttribute("open", "");
    const { column, signatures } = await tunnel.locator(".protocol-layout").evaluate((element) => ({ column: element.querySelector(".protocol-column")!.getBoundingClientRect().toJSON(), signatures: element.querySelector(":scope>.protocol-group")!.getBoundingClientRect().toJSON() }));
    if (width === 2534) {
      expect(Math.abs(column.y - signatures.y)).toBeLessThanOrEqual(1);
      expect(signatures.x - column.x - column.width).toBeCloseTo(16, 0);
    } else {
      expect(signatures.y - column.y - column.height).toBeCloseTo(16, 0);
    }
    expect(await tunnel.locator(".protocol-group [name]").evaluateAll((fields) => fields.map((field) => field.getAttribute("name")).sort())).toEqual(expected);
    await expectContained(page, tunnel);
  }
  await page.setViewportSize(viewport);
  await deleteTunnel(page, tunnel, m);
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

  await tunnel.getByRole("button", { name: m.ui.connection, exact: true }).click();
  const port = tunnel.getByLabel(m.forms.listenPort, { exact: true });
  const originalPort = await port.inputValue();
  await port.fill("65536");
  await tunnel.getByLabel("DNS", { exact: true }).fill("9.9.9.9");
  await tunnel.getByRole("button", { name: m.common.save, exact: true }).click();
  const error = tunnel.getByRole("alert");
  await expect(error).toBeVisible();
  await expect(port).toHaveValue("65536");
  await expect(tunnel.getByLabel("DNS", { exact: true })).toHaveValue("9.9.9.9");
  await error.scrollIntoViewIfNeeded();
  expect(await error.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    const top = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2);
    return top !== null && element.contains(top);
  })).toBe(true);
  await expectContained(page, tunnel);
  await port.fill(originalPort);
  await tunnel.getByLabel("MTU", { exact: true }).selectOption("custom");
  await tunnel.getByLabel(m.forms.customMTU, { exact: true }).fill("1360");
  await tunnel.getByLabel(m.common.enabled, { exact: true }).uncheck();
  await tunnel.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(error).not.toBeVisible();
  // Visit another pane and return to confirm the saved server values.
  await tunnel.getByRole("button", { name: m.tunnel.clients, exact: true }).click();
  await tunnel.getByRole("button", { name: m.ui.connection, exact: true }).click();
  await expect(tunnel.getByLabel("DNS", { exact: true })).toHaveValue("9.9.9.9");
  await expect(tunnel.getByLabel(m.forms.customMTU, { exact: true })).toHaveValue("1360");
  await expect(tunnel.getByLabel(m.common.enabled, { exact: true })).not.toBeChecked();
  await tunnel.getByLabel(m.common.enabled, { exact: true }).check();
  await tunnel.getByRole("button", { name: m.common.save, exact: true }).click();
  await tunnel.getByRole("button", { name: m.ui.protocolParameters, exact: true }).click();
  await expect(tunnel.getByText(m.forms.awg3Warning, { exact: true })).toBeVisible();
  await tunnel.getByLabel("Jmin", { exact: true }).fill("9999");
  await tunnel.getByRole("button", { name: m.forms.saveProtocol, exact: true }).click();
  await expect(tunnel.getByRole("alert")).toBeVisible();
  await tunnel.getByLabel("Jmin", { exact: true }).fill("10");
  await tunnel.getByRole("button", { name: m.forms.saveProtocol, exact: true }).click();
  await expect(tunnel.getByRole("alert")).not.toBeVisible();
  // The workspace grows on wide displays, while groups retain contiguous spacing.
  const viewport = page.viewportSize()!;
  await page.setViewportSize({ width: 2534, height: 1320 });
  const workspace = (await page.locator(".tunnel-workspace").boundingBox())!;
  const main = (await page.locator(".workspace-main").boundingBox())!;
  expect(workspace.width).toBeGreaterThan(2000);
  expect(Math.abs(main.x + main.width - workspace.x - workspace.width - 32)).toBeLessThanOrEqual(1);
  await expectContained(page, tunnel);
  const numericGaps = await tunnel.locator(".protocol-column").evaluate((column) => Array.from(column.children).slice(1).map((group) => group.getBoundingClientRect().top - group.previousElementSibling!.getBoundingClientRect().bottom));
  expect(numericGaps).toEqual([16, 16]);
  const state = await (await page.request.get("/api/state")).json();
  const tunnelName = await tunnel.locator(".tunnel-head h2").innerText();
  const currentTunnel = state.tunnels.find((item: { name: string }) => item.name === tunnelName);
  const renderedKeys = await tunnel.locator(".protocol-group [name]").evaluateAll((fields) => fields.map((field) => field.getAttribute("name")).sort());
  expect(renderedKeys).toEqual(currentTunnel.params.map((param: { key: string }) => param.key).sort());
  expect(renderedKeys).toHaveLength(25);
  // At tablet width, wrapped protocol labels must preserve each row's input baseline.
  await page.setViewportSize({ width: 900, height: 1000 });
  const rowOffsets = await tunnel.locator(".protocol-fields").evaluateAll((groups) => groups.flatMap((group) => {
    const rows = new Map<number, number[]>();
    for (const wrapper of Array.from(group.children)) {
      const top = Math.round(wrapper.getBoundingClientRect().top);
      const input = wrapper.querySelector(".mantine-Input-input")!;
      rows.set(top, [...(rows.get(top) || []), input.getBoundingClientRect().top]);
    }
    return Array.from(rows.values(), (tops) => Math.max(...tops) - Math.min(...tops));
  }));
  expect(Math.max(...rowOffsets)).toBeLessThanOrEqual(1);
  await page.setViewportSize(viewport);
  // Protocol-form confirmation retains context and supports an inline API failure/retry.
  await page.route("**/api/tunnels/*/regenerate", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "request failed" }) }), { times: 1 });
  await tunnel.locator(".regenerate-section").getByRole("button", { name: m.forms.regenerate, exact: true }).click();
  const confirmation = page.getByRole("dialog");
  await expect(confirmation).toContainText(await tunnel.locator(".tunnel-head h2").innerText());
  await confirmation.getByRole("button", { name: m.forms.regenerate, exact: true }).click();
  await expect(confirmation.getByRole("alert")).toBeVisible();
  await expect(tunnel.getByLabel("Jmin", { exact: true })).toHaveValue("10");
  const regenerated = page.waitForResponse((response) => response.url().endsWith("/regenerate"));
  await confirmation.getByRole("button", { name: m.forms.regenerate, exact: true }).click();
  expect((await regenerated).status()).toBe(200);
  await expect(confirmation).not.toBeVisible();
  // Show a single regeneration entry per pane; the header shortcut remains elsewhere.
  await expect(tunnel.getByRole("button", { name: m.forms.regenerate, exact: true })).toHaveCount(1);
  await expect(tunnel.locator(".header-actions").getByRole("button", { name: m.forms.regenerate, exact: true })).toHaveCount(0);
  await tunnel.getByRole("button", { name: m.tunnel.clients, exact: true }).click();
  await tunnel.locator(".header-actions").getByRole("button", { name: m.forms.regenerate, exact: true }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
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
    await client.getByRole("button", { name: m.ui.connectClient, exact: true }).click();
    const dialog = page.locator('[role="dialog"].dialog');
    const image = dialog.locator(".qr-image");
    await expectDecodedImage(image);
    const vpnSource = await image.getAttribute("src");
    await expect(image).toHaveAttribute("alt", m.clientConfig.amneziaVPNAlt("UI test client"));
    const rawRequest = page.waitForRequest("**/api/clients/*/qr");
    try {
      await dialog.getByRole("tab", { name: "AmneziaWG", exact: true }).click();
      await rawRequest;
      await expect(image).toHaveCount(0);
      await expect(dialog.locator(".qr-image-loading")).toBeVisible();
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
    // Enlarged QR is a nested dialog: Escape closes only that layer and restores focus.
    const enlarge = dialog.getByRole("button", { name: m.clientConfig.openLarger(m.clientConfig.amneziaWGQR), exact: true });
    await enlarge.click();
    await expect(page.getByRole("dialog")).toHaveCount(2);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(1);
    await expect(enlarge).toBeFocused();

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
    await client.getByRole("button", { name: m.ui.connectClient, exact: true }).click();
    await expect(dialog.getByRole("alert")).toBeVisible();
    await expect(image).toHaveCount(0);
    await dialog.getByRole("button", { name: m.common.retry, exact: true }).click();
    await expectDecodedImage(image);
    await dialog.getByRole("button", { name: m.common.close, exact: true }).click();
    await deleteTunnel(page, tunnel, m);
  });
}


test("navigation, client search, quota editing and unified connection preserve tunnel context", async ({ page, m, session }, testInfo) => {
  await openDashboard(page, m, session);
  const tunnel = await createTunnel(page, m, "awg_2_0");
  const tunnelName = await tunnel.locator(".tunnel-head").getByRole("heading", { level: 2 }).innerText();
  for (const name of ["MacBook", "Work laptop", "Tablet", "Home computer", "Search target"]) await createClient(page, tunnel, m, name);
  const target = tunnel.locator(".client-row").filter({ hasText: "Search target" });
  await target.getByRole("button", { name: m.common.edit, exact: true }).click();
  const dialog = page.locator('[role="dialog"].dialog');
  await dialog.getByLabel(m.forms.trafficLimit, { exact: true }).selectOption("limit");
  await dialog.getByLabel(m.forms.trafficLimitValue, { exact: true }).fill("2");
  await dialog.getByLabel(m.forms.trafficLimitUnit, { exact: true }).selectOption("gib");
  await dialog.getByLabel(m.forms.trafficLimitPeriod, { exact: true }).selectOption("rolling_30d");
  await dialog.getByLabel(m.forms.expiration, { exact: true }).selectOption("7d");
  await dialog.getByRole("button", { name: m.common.save, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(target).toContainText("2.00 GiB");
  const search = tunnel.getByRole("searchbox", { name: m.ui.searchClients, exact: true });
  await search.fill("Search target");
  await expect(tunnel.locator(".client-row")).toHaveCount(1);
  await target.getByRole("button", { name: m.common.delete, exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText(tunnelName);
  await expect(page.getByRole("dialog")).toContainText(await target.locator(".client-identity .mono").innerText());
  await page.getByRole("dialog").getByRole("button", { name: m.common.delete, exact: true }).click();
  await expect(search).toBeVisible();
  await search.fill("");
  await expect(tunnel.locator(".client-row")).toHaveCount(4);
  const firstClient = tunnel.locator(".client-row").filter({ hasText: "MacBook" });
  await firstClient.getByRole("button", { name: m.ui.clientActions, exact: true }).click();
  const evidenceDirectory = process.env.AWG_UI_EVIDENCE_DIR;
  if (evidenceDirectory) {
    mkdirSync(evidenceDirectory, { recursive: true, mode: 0o700 });
    if (testInfo.project.name.startsWith("desktop")) await page.setViewportSize({ width: 1586, height: 992 });
    await page.screenshot({ path: join(evidenceDirectory, `${testInfo.project.name}.png`), fullPage: true });
  }
  await firstClient.getByRole("button", { name: m.ui.connectClient, exact: true }).click();
  await expect(dialog.getByRole("tab", { name: "AmneziaVPN", exact: true })).toHaveAttribute("aria-selected", "true");
  await dialog.getByRole("tab", { name: "AmneziaWG", exact: true }).click();
  await expect(dialog.getByRole("tab", { name: "AmneziaWG", exact: true })).toHaveAttribute("aria-selected", "true");
  await expectDecodedImage(dialog.locator(".qr-image"));
  await dialog.getByRole("button", { name: m.common.close, exact: true }).click();
  // Explicit overview selection must override a previously active protocol filter.
  const other = await createTunnel(page, m, "awg_3");
  const otherName = await other.getByRole("heading", { level: 2 }).innerText();
  await openTunnelTools(page, m);
  await page.getByLabel(m.aria.tunnelFilters, { exact: true }).selectOption("awg_2_0");
  await page.locator(".main-nav").getByRole("button", { name: m.ui.overview, exact: true }).click();
  await page.locator(".overview-card").filter({ hasText: otherName }).click();
  await expect(page.getByRole("article").getByRole("heading", { name: otherName, exact: true })).toBeVisible();
  await page.locator(".main-nav").getByRole("button", { name: m.tunnel.clients, exact: true }).click();
  await page.getByRole("searchbox", { name: m.ui.searchClients, exact: true }).fill("Work laptop");
  await expect(page.locator(".client-row")).toHaveCount(1);
  await expect(page.locator(".client-row")).toContainText("Work laptop");
  await page.locator(".main-nav").getByRole("button", { name: m.dashboard.tunnels, exact: true }).click();
  await deleteTunnel(page, page.getByRole("article"), m);
  await selectTunnel(page, m, tunnelName);
  await deleteTunnel(page, page.getByRole("article"), m);
});


test("global client creation requires a choice and preserves editable tunnel context", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const first = await createTunnel(page, m, "awg_2_0");
  const firstName = await first.locator(".tunnel-head h2").innerText();
  const second = await createTunnel(page, m, "awg_3");
  const secondName = await second.locator(".tunnel-head h2").innerText();
  const before = await (await page.request.get("/api/state")).json();
  const firstID = before.tunnels.find((item: { name: string }) => item.name === firstName).id;
  const secondID = before.tunnels.find((item: { name: string }) => item.name === secondName).id;
  await page.locator(".main-nav").getByRole("button", { name: m.tunnel.clients, exact: true }).click();
  await page.locator(".all-clients").getByRole("button", { name: m.common.createClient, exact: true }).click();
  const dialog = page.getByRole("dialog");
  const picker = dialog.getByRole("combobox", { name: m.ui.clientTunnel, exact: true });
  await expect(picker).toHaveValue("");
  const firstClose = dialog.getByRole("button", { name: m.common.close, exact: true }).first();
  await firstClose.focus();
  await page.keyboard.press("Shift+Tab");
  await expect(dialog.getByRole("button", { name: m.common.createClient, exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(firstClose).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(picker).toBeFocused();
  await expect.poll(() => picker.evaluate((input) => { const style = getComputedStyle(input); return style.outlineStyle !== "none" || style.boxShadow !== "none"; })).toBe(true);
  await dialog.getByLabel(m.forms.clientName, { exact: true }).fill("Chosen tunnel client");
  let requests = 0;
  page.on("request", (request) => { if (new URL(request.url()).pathname === "/api/clients" && request.method() === "POST") requests++; });
  await dialog.getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(picker).toBeFocused();
  expect(requests).toBe(0);
  await picker.selectOption(firstID);
  await dialog.getByLabel(m.forms.expiration, { exact: true }).selectOption("7d");
  await page.route("**/api/clients", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "request failed" }) }), { times: 1 });
  await dialog.getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(dialog.getByRole("alert")).toBeVisible();
  await picker.selectOption(secondID);
  await expect(dialog.getByLabel(m.forms.clientName, { exact: true })).toHaveValue("Chosen tunnel client");
  await expect(dialog.getByLabel(m.forms.expiration, { exact: true })).toHaveValue("7d");
  await dialog.getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  const after = await (await page.request.get("/api/state")).json();
  expect(after.tunnels.find((item: { id: string }) => item.id === secondID).clients.some((client: { name: string }) => client.name === "Chosen tunnel client")).toBe(true);
  expect(after.tunnels.find((item: { id: string }) => item.id === firstID).clients.some((client: { name: string }) => client.name === "Chosen tunnel client")).toBe(false);
  // A deliberate filter is a default, never a locked destination.
  await page.getByRole("combobox", { name: m.aria.tunnelFilters, exact: true }).selectOption(firstID);
  await page.locator(".all-clients").getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(picker).toHaveValue(firstID);
  await picker.selectOption(secondID);
  await dialog.getByLabel(m.forms.clientName, { exact: true }).fill("Moved from filtered context");
  await dialog.getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByRole("combobox", { name: m.aria.tunnelFilters, exact: true })).toHaveValue(secondID);
  await expect(page.locator(".client-row").filter({ hasText: "Moved from filtered context" })).toBeVisible();
  await page.locator(".main-nav").getByRole("button", { name: m.dashboard.tunnels, exact: true }).click();
  await selectTunnel(page, m, firstName);
  await page.getByRole("article").getByRole("button", { name: m.common.createClient, exact: true }).click();
  await expect(picker).toHaveValue(firstID);
  await picker.selectOption(secondID);
  await page.keyboard.press("Escape");
  await deleteTunnel(page, page.getByRole("article"), m);
  await selectTunnel(page, m, secondName);
  await deleteTunnel(page, page.getByRole("article"), m);
});

test("backup file picker supports upload, password retry and dry-run verification", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  await page.locator(".main-nav").getByRole("button", { name: m.common.maintenance, exact: true }).click();
  await page.getByRole("button", { name: m.maintenance.tabs.backup, exact: true }).click();
  const backup = page.locator(".maintenance-section").first();
  const restore = page.locator(".maintenance-section").nth(1);
  await restore.getByRole("button", { name: m.backup.verify, exact: true }).click();
  await expect(restore.getByRole("alert")).toHaveText(m.backup.fileRequired);
  await backup.getByLabel(m.backup.password, { exact: true }).fill("disposable-backup-passphrase");
  const downloaded = page.waitForEvent("download");
  await backup.getByRole("button", { name: m.backup.create, exact: true }).click();
  const download = await downloaded;
  const stream = await download.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  await restore.locator('input[type="file"]').setInputFiles({ name: "validation.afbackup", mimeType: "application/octet-stream", buffer: Buffer.concat(chunks) });
  await restore.getByLabel(m.backup.password, { exact: true }).fill("incorrect-disposable-passphrase");
  await restore.getByRole("button", { name: m.backup.verify, exact: true }).click();
  await expect(restore.getByRole("alert")).toHaveText(m.backup.verifyFailed);
  await expect(restore).toContainText("validation.afbackup");
  await restore.getByLabel(m.backup.password, { exact: true }).fill("disposable-backup-passphrase");
  const verified = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/restore/verify");
  await restore.getByRole("button", { name: m.backup.verify, exact: true }).click();
  const result = await (await verified).json();
  await expect(restore.locator(".fact").filter({ has: page.getByText(m.backup.schema, { exact: true }) }).locator("strong")).toHaveText(String(result.report.schema_version));
  await expect(restore.getByRole("alert")).not.toBeVisible();
  await expect(restore.locator(".metric-grid")).toBeVisible();
  await expectContained(page, restore);
});


test("client creation without tunnels explains the prerequisite", async ({ page, m, session }) => {
  await page.context().addCookies(session.cookies);
  const current = await (await page.request.get("/api/state")).json();
  await page.route("**/api/state", (route) => route.fulfill({ json: { ...current, tunnels: [] } }));
  await page.route("**/api/events", (route) => route.abort());
  await page.goto("/#/clients");
  await expect(page.getByRole("button", { name: m.common.createClient, exact: true })).toBeDisabled();
  await expect(page.getByText(m.dashboard.noTunnelsYet, { exact: true })).toBeVisible();
  await page.getByRole("button", { name: m.common.createTunnel, exact: true }).click();
  await expect(page.getByRole("dialog").getByLabel(m.forms.protocol, { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
});

test("review regressions preserve runtime meaning, compact selection and readable errors", async ({ page, m, session }) => {
  await page.context().addCookies(session.cookies);
  const fixture = await (await page.request.get("/api/state")).json();
  fixture.tunnels[0].enabled = true;
  fixture.tunnels[0].status.up = false;
  const primaryID = fixture.tunnels[0].id;
  const unknown = { ...fixture.tunnels[0], id: "review-unknown", name: "Unknown runtime", clients: [], status: null };
  fixture.tunnels.push(unknown, ...Array.from({ length: 4 }, (_, index) => ({ ...unknown, id: `spare-${index}`, name: `Spare ${index}` })));
  await page.route("**/api/events", (route) => route.fulfill({ status: 204 }));
  await page.route("**/api/state", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(fixture) }));
  await openDashboard(page, m, session);
  await expect(page.locator(".metric-lead")).toContainText(m.ui.noRuntimeData);
  if (m === messages.ru) await expect(page.locator(".metric-lead")).not.toContainText("выключены");
  await page.setViewportSize({ width: 1280, height: 900 });
  expect(await page.locator(".overview-table").evaluate((element) => element.scrollWidth <= element.clientWidth + 1)).toBe(true);
  await page.locator(".main-nav").getByRole("button", { name: m.dashboard.tunnels, exact: true }).click();
  await selectTunnel(page, m, fixture.tunnels[0].name);
  await expect(page.locator(".tunnel-title")).toContainText(m.status.down);
  await page.locator(".tunnel-tabs").getByRole("button", { name: m.ui.connection, exact: true }).click();
  await expect(page.getByLabel(m.common.enabled, { exact: true })).toBeChecked();
  const toggleOffset = await page.locator(".toggle-row").evaluate((element) => Math.abs(element.querySelector(".mantine-Switch-track")!.getBoundingClientRect().top - element.querySelector(".mantine-Switch-label")!.getBoundingClientRect().top));
  expect(toggleOffset).toBeLessThanOrEqual(5);
  await expect(page.locator(".tunnel-facts")).toHaveAttribute("open", "");
  await selectTunnel(page, m, unknown.name);
  await expect(page.locator(".tunnel-title")).toContainText(m.ui.runtimeUnknown);
  await page.setViewportSize({ width: 390, height: 844 });
  const picker = page.getByRole("combobox", { name: m.ui.selectedTunnel, exact: true });
  await expect(picker).toBeVisible();
  await expect(page.locator(".tunnel-nav")).toHaveCount(0);
  await picker.selectOption(primaryID);
  await expect(page.locator(".tunnel-facts")).not.toHaveAttribute("open", "");
  expect((await page.locator(".tunnel-title").boundingBox())!.y).toBeLessThan(744);
  await page.locator(".tunnel-facts>summary").click();
  await expect(page.locator(".tunnel-facts")).toHaveAttribute("open", "");
  await expectContained(page, page.locator(".tunnel-workspace"));
  await page.locator(".main-nav").getByRole("button", { name: m.common.maintenance, exact: true }).click();
  const backupTab = page.locator(".subtabs").getByRole("button", { name: m.maintenance.tabs.backup, exact: true });
  await backupTab.click();
  await expect(backupTab).toHaveAttribute("aria-current", "page");
  await page.locator(".maintenance-section").nth(1).getByRole("button", { name: m.backup.verify, exact: true }).click();
  const ratio = await page.getByRole("alert").evaluate((element) => {
    const style = getComputedStyle(element);
    const luminance = (value: string) => value.match(/[\d.]+/g)!.slice(0, 3).map(Number).map((channel) => channel / 255).map((channel) => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4).reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0);
    const foreground = luminance(style.color), background = luminance(style.backgroundColor);
    return (Math.max(foreground, background) + .05) / (Math.min(foreground, background) + .05);
  });
  expect(ratio).toBeGreaterThanOrEqual(4.5);
});

test("unified connection keeps import keys explicit and enlarged multipart QR retries in place", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const tunnel = await createTunnel(page, m, "awg_2_0");
  const client = await createClient(page, tunnel, m);
  let qrRequests = 0, importRequests = 0, nextAttempts = 0;
  page.on("request", (request) => {
    if (/\/(qr|amnezia-vpn-qr|amnezia-vpn-qr-series)(\?|$)/.test(request.url())) qrRequests++;
    if (request.url().endsWith("/import-key")) importRequests++;
  });
  await page.route("**/api/clients/*/import-key", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ import_key: "vpn://BROWSER-TEST-FIXTURE", warning: "" }) }));
  await client.getByRole("button", { name: m.ui.connectClient, exact: true }).click();
  const linkDialog = page.getByRole("dialog");
  await expect(linkDialog.getByRole("button", { name: m.clientConfig.copyVpnKey, exact: true })).toBeVisible();
  await expectDecodedImage(linkDialog.locator(".qr-image"));
  expect(qrRequests).toBeGreaterThan(0);
  expect(importRequests).toBe(0);
  await expect(linkDialog.locator(".qr-config-option")).toHaveCount(1);
  await linkDialog.getByRole("button", { name: m.clientConfig.copyVpnKey, exact: true }).click();
  await expect(linkDialog.getByLabel(m.clientConfig.vpnImportLink, { exact: true })).toBeVisible();
  expect(importRequests).toBe(1);
  await page.keyboard.press("Escape");
  await page.route("**/api/clients/*/amnezia-vpn-qr-series", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ chunks: 2 }) }));
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=", "base64");
  await page.route("**/api/clients/*/amnezia-vpn-qr?*", (route) => {
    if (new URL(route.request().url()).searchParams.get("chunk") === "1" && ++nextAttempts === 1) return route.fulfill({ status: 503, body: "unavailable" });
    return route.fulfill({ contentType: "image/png", body: png });
  });
  await client.getByRole("button", { name: m.ui.connectClient, exact: true }).click();
  const parent = page.getByRole("dialog");
  await expectDecodedImage(parent.locator(".qr-image"));
  await parent.getByRole("button", { name: m.clientConfig.openLarger(m.clientConfig.amneziaVPNQR), exact: true }).click();
  const enlarged = page.getByRole("dialog").last();
  await enlarged.getByRole("button", { name: m.clientConfig.next, exact: true }).click();
  await expect(enlarged.getByRole("alert")).toBeVisible();
  await expect(enlarged.locator("img")).toHaveCount(0);
  await enlarged.getByRole("button", { name: m.common.retry, exact: true }).click();
  await expectDecodedImage(enlarged.locator(".qr-lightbox-image"));
  expect(nextAttempts).toBe(2);
  const enlargedClose = enlarged.getByRole("button", { name: m.clientConfig.closePreview, exact: true });
  await enlargedClose.focus();
  await page.keyboard.press("Shift+Tab");
  await expect(enlarged.getByRole("link", { name: m.clientConfig.downloadQR, exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(enlargedClose).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect(page.getByRole("dialog").getByRole("button", { name: m.clientConfig.openLarger(m.clientConfig.amneziaVPNQR), exact: true })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await deleteTunnel(page, tunnel, m);
});

test("Legacy retains all nine fields in a full-width protocol layout", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  const tunnel = await createTunnel(page, m, "awg_legacy_1_0");
  await tunnel.getByRole("button", { name: m.ui.protocolParameters, exact: true }).click();
  const names = await tunnel.locator(".protocol-group [name]").evaluateAll((fields) => fields.map((field) => field.getAttribute("name")));
  expect([...names].sort()).toEqual(["Jc", "Jmin", "Jmax", "S1", "S2", "H1", "H2", "H3", "H4"].sort());
  expect(names.slice(0, 3)).toEqual(["Jc", "Jmin", "Jmax"]);
  const viewport = page.viewportSize()!;
  await page.setViewportSize({ width: 2534, height: 1000 });
  const widths = await tunnel.locator(".protocol-basic").evaluate((element) => ({ layout: element.clientWidth, column: element.querySelector(".protocol-column")!.clientWidth }));
  expect(widths.layout).toBe(widths.column);
  await page.setViewportSize(viewport);
  await deleteTunnel(page, tunnel, m);
});


test("empty tunnels expose one create action and filtered emptiness offers recovery", async ({ page, m, session }) => {
  await openDashboard(page, m, session);
  await page.locator(".main-nav").getByRole("button", { name: m.dashboard.tunnels, exact: true }).click();
  await openTunnelTools(page, m);
  await page.getByRole("searchbox", { name: m.ui.searchTunnels, exact: true }).fill("no-such-tunnel-ui-test");
  await expect(page.getByRole("button", { name: m.common.createTunnel, exact: true })).toHaveCount(1);
  await expect(page.getByRole("button", { name: m.ui.clearTunnelFilters, exact: true })).toHaveCount(1);
  await page.getByRole("button", { name: m.ui.clearTunnelFilters, exact: true }).click();
  await expect(page.getByRole("article")).toBeVisible();
  await page.route("**/api/state", async (route) => {
    const response = await route.fetch();
    const state = await response.json();
    await route.fulfill({ response, json: { ...state, tunnels: [] } });
  });
  await page.reload();
  await expect(page.getByRole("button", { name: m.common.createTunnel, exact: true })).toHaveCount(1);
  await page.getByRole("button", { name: m.common.createTunnel, exact: true }).click();
  await expect(page.getByRole("dialog").getByLabel(m.forms.protocol, { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
});
