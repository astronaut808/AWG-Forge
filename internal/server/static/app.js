// deno-lint-ignore-file no-unused-vars -- classic scripts share UI symbols across files loaded by index.html.
const app = document.querySelector("#app");
const modal = document.querySelector("#modal");
const toast = document.querySelector("#toast");

let state = null;
let activeProfile = localStorage.getItem("awg-forge.profile") || "awg_legacy_1_0";
let activeTheme = initialTheme();
const maintenanceState = {
  tab: "overview",
  doctor: null,
  auditLog: null,
  updates: null,
  restoreReport: null,
  lastRun: {},
  loading: {},
};

const profileTitles = {
  awg_legacy_1_0: "AmneziaWG Legacy / 1.0",
  awg_1_5: "AmneziaWG 1.5",
  awg_2_0: "AmneziaWG 2.0",
};
init();

function initialTheme() {
  const stored = localStorage.getItem("awg-forge.theme");
  if (stored === "light" || stored === "dark") return stored;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyTheme(theme) {
  activeTheme = theme === "dark" ? "dark" : "light";
  document.documentElement.dataset.theme = activeTheme;
}

function toggleTheme() {
  applyTheme(activeTheme === "dark" ? "light" : "dark");
  localStorage.setItem("awg-forge.theme", activeTheme);
  if (state) renderApp();
}

function initParallax() {
  const reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const finePointer = window.matchMedia("(pointer: fine)");
  if (reduceMotion.matches || !finePointer.matches) return;

  let frame = 0;
  let nextX = 0;
  let nextY = 0;

  const apply = () => {
    frame = 0;
    document.documentElement.style.setProperty("--parallax-glow-x", `${(-nextX * 14).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-glow-y", `${(-nextY * 10).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-grid-x", `${(nextX * 6).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-grid-y", `${(nextY * 4).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-surface-x", `${(nextX * 2.4).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-surface-y", `${(nextY * 1.8).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-card-x", `${(-nextX * 1.4).toFixed(2)}px`);
    document.documentElement.style.setProperty("--parallax-card-y", `${(-nextY * 1.1).toFixed(2)}px`);
  };

  window.addEventListener("pointermove", (event) => {
    nextX = event.clientX / window.innerWidth - 0.5;
    nextY = event.clientY / window.innerHeight - 0.5;
    if (!frame) frame = requestAnimationFrame(apply);
  }, { passive: true });

  window.addEventListener("pointerleave", () => {
    nextX = 0;
    nextY = 0;
    if (!frame) frame = requestAnimationFrame(apply);
  }, { passive: true });
}

function initModalBehavior() {
  if (!modal) return;

  modal.addEventListener("click", (event) => {
    if (event.target === modal) modal.close();
  });

  modal.addEventListener("cancel", () => {
    // Native Esc close is fine. This hook is left intentionally simple.
  });
}

async function init() {
  await loadState();
}

async function loadState() {
  const res = await fetch("/api/state");
  if (res.status === 401) {
    state = null;
    renderLogin();
    return;
  }
  if (!res.ok) {
    showToast(await errorText(res));
    return;
  }
  state = await res.json();
  renderApp();
}

function renderLogin() {
  globalThis.setSafeHTML(app, `
    <section class="login-shell">
      <div class="panel login">
        <div class="brand">
          <span class="brand-mark" aria-hidden="true">${brandIcon()}</span>
          <div>
            ${brandTitle()}
            <p>Secure admin access</p>
          </div>
        </div>
        <form id="login-form" class="form-grid login-form">
          <div>
            <label for="password">Password</label>
            <input id="password" name="password" type="password" autocomplete="current-password" autofocus>
          </div>
          <div class="form-actions">
            <button class="primary wide" type="submit">Log in</button>
          </div>
        </form>
      </div>
    </section>
  `);

  const form = document.querySelector("#login-form");
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const password = document.querySelector("#password").value;
    const res = await api("/api/login", {
      method: "POST",
      body: { password },
    });

    if (res.ok) {
      resetSubmit(event.currentTarget);
      await loadState();
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

function renderApp() {
  if (!state) {
    renderLogin();
    return;
  }

  const profiles = state.profiles || [];
  if (profiles.length === 0) {
    globalThis.setSafeHTML(app, `
      <section class="panel empty">
        <span class="empty-icon">!</span>
        <strong>No profiles available</strong>
        <p class="muted">Backend returned an empty profile list.</p>
      </section>
    `);
    return;
  }

  const active = profiles.find((profile) => profile.id === activeProfile) || profiles[0];
  activeProfile = active.id;
  localStorage.setItem("awg-forge.profile", activeProfile);

  const allTunnels = state.tunnels || [];
  const tunnels = allTunnels.filter((tunnel) => tunnel.profile === activeProfile);

  globalThis.setSafeHTML(app, `
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true">${brandIcon()}</span>
        <div>
          ${brandTitle()}
          <p><span class="mono">${escapeHTML(state.server_host)}</span> · ${allTunnels.length} tunnel(s)</p>
        </div>
      </div>
      <div class="toolbar">
        <button class="ghost theme-toggle" data-action="theme" aria-label="${themeToggleLabel()}" title="${themeToggleLabel()}">${themeIcon()}</button>
        <button class="ghost" data-action="maintenance">Maintenance</button>
        <button class="ghost" data-action="refresh">Refresh</button>
        <button class="ghost" data-action="logout">Log out</button>
      </div>
    </header>
    <nav class="tabs" aria-label="Protocol profiles">
      ${profiles.map((profile) => `
        <button class="tab ${profile.id === activeProfile ? "active" : ""}" data-profile="${escapeAttr(profile.id)}">
          <strong>${escapeHTML(profile.tab)}</strong>
          <span>${escapeHTML(profile.label)}</span>
        </button>
      `).join("")}
    </nav>
    <section class="panel">
      <div class="section-head">
        <div>
          <h2>${escapeHTML(profileTitles[activeProfile] || activeProfile)}</h2>
          <p class="muted">${profileHelp(active)}</p>
        </div>
        <button class="primary" data-action="create-tunnel" ${active.available ? "" : "disabled"}>Create tunnel</button>
      </div>
      ${renderTunnels(active, tunnels)}
    </section>
  `);

  bindAppEvents(active);
}

function renderTunnels(profile, tunnels) {
  if (!profile.available) {
    return `<div class="empty"><span class="empty-icon">!</span><strong>This profile is not enabled yet</strong><p class="muted">Creation is disabled until syntax, ranges, and golden tests are ready.</p></div>`;
  }

  if (tunnels.length === 0) {
    return `<div class="empty"><span class="empty-icon">+</span><strong>No tunnels for ${escapeHTML(profile.tab)} yet</strong><p class="muted">Create a tunnel first, then add clients inside it.</p></div>`;
  }

  return `<div class="grid ${tunnels.length === 1 ? "single" : ""}">${tunnels.map(renderTunnelCard).join("")}</div>`;
}

function profileHelp(profile) {
  if (!profile.available) return "This profile is reserved for the next protocol implementation.";
  if (profile.id === "awg_2_0") return "Create AWG 2.0 tunnels. Use .conf import for production clients.";
  return "Create and manage independent tunnels for this profile.";
}

function renderTunnelCard(tunnel) {
  const clients = tunnel.clients || [];

  return `
    <article class="card">
      <div class="card-head">
        <div>
          <h3>${escapeHTML(tunnel.name)}</h3>
          <p class="muted"><span class="mono">${escapeHTML(tunnel.interface)}</span> · ${escapeHTML(profileTitles[tunnel.profile] || tunnel.profile)}</p>
        </div>
        ${renderRuntimeSummary(tunnel)}
      </div>
      <div class="facts">
        <div class="fact"><span>Endpoint</span><div class="endpoint-value"><strong class="mono">${escapeHTML(tunnelEndpointHost(tunnel))}:${escapeHTML(tunnel.listen_port)}</strong><small>${tunnel.server_host ? "custom" : "inherited"}</small></div></div>
        <div class="fact"><span>Subnet</span><strong class="mono">${escapeHTML(tunnel.subnet)}</strong></div>
        <div class="fact"><span>DNS</span><strong class="mono">${escapeHTML(tunnel.dns)}</strong></div>
        <div class="fact"><span>MTU</span><strong class="mono">${formatMTU(tunnel.mtu)}</strong></div>
        <div class="fact"><span>Egress</span><strong class="mono">${escapeHTML(tunnel.egress_mode === "warp" ? "WARP" : "WAN")}</strong></div>
        <div class="fact"><span>Clients</span><strong>${clients.filter((client) => client.active).length}/${clients.length}</strong></div>
      </div>
      <div class="actions card-actions">
        <button class="primary" data-action="create-client" data-tunnel="${escapeAttr(tunnel.id)}">Create client</button>
        <button data-action="settings" data-tunnel="${escapeAttr(tunnel.id)}">Settings</button>
        <button data-action="protocol" data-tunnel="${escapeAttr(tunnel.id)}">Protocol</button>
        <button data-action="health" data-tunnel="${escapeAttr(tunnel.id)}">Health</button>
        <button data-action="restart" data-tunnel="${escapeAttr(tunnel.id)}">Restart</button>
        <button class="danger" data-action="delete-tunnel" data-tunnel="${escapeAttr(tunnel.id)}">Delete</button>
      </div>
      <div class="client-list">
        <div class="client-list-head">
          <strong>Clients</strong>
          <span class="muted">${clients.length ? `${clients.length} total` : "No clients yet"}</span>
        </div>
        ${clients.length ? clients.map((client) => renderClientRow(tunnel, client)).join("") : `<div class="empty-inline">Create the first client for this tunnel.</div>`}
      </div>
      ${portWarning(tunnel)}
      ${tunnel.status?.last_error ? `<p class="badge bad">${escapeHTML(tunnel.status.last_error)}</p>` : ""}
    </article>
  `;
}

function fact(label, value) {
  return `<div class="fact"><span>${escapeHTML(label)}</span><strong class="mono">${escapeHTML(value || "-")}</strong></div>`;
}

function renderRuntimeSummary(tunnel) {
  const clients = tunnel.clients || [];
  const staleClients = Number(tunnel.status?.stale_clients || 0);
  const firewall = tunnel.status?.firewall || {};
  const runtime = tunnel.enabled === false
    ? { label: "runtime disabled", level: "neutral" }
    : tunnel.status?.up
      ? { label: "runtime up", level: "ok" }
      : { label: "runtime down", level: "bad" };
  const configs = clients.length === 0
    ? { label: "no clients", level: "neutral" }
    : staleClients > 0
      ? { label: `${staleClients} stale config${staleClients === 1 ? "" : "s"}`, level: "warn" }
      : { label: "configs fresh", level: "ok" };
  const items = [
    runtime,
    { label: firewall.label || "firewall unknown", level: firewall.level || "warn", title: firewall.message || "" },
    configs,
  ];

  return `
    <div class="runtime-summary">
      ${items.map((item) => `<span class="badge ${statusBadgeClass(item.level)}" aria-label="${escapeAttr(item.title || item.label)}">${escapeHTML(item.label)}</span>`).join("")}
    </div>
  `;
}

function statusBadgeClass(level) {
  if (level === "ok" || level === "bad" || level === "warn" || level === "neutral") return level;
  return "warn";
}

function portWarning(tunnel) {
  if (!state.published_udp_ports || portInRanges(tunnel.listen_port, state.published_udp_ports)) return "";
  return `<p class="badge warn">Port ${escapeHTML(tunnel.listen_port)} is outside published UDP range ${escapeHTML(state.published_udp_ports)}</p>`;
}

function portInRanges(port, spec) {
  const numericPort = Number(port);

  return String(spec).split(",").some((part) => {
    const trimmed = part.trim();
    if (!trimmed) return false;

    if (trimmed.includes("-")) {
      const [min, max] = trimmed.split("-").map((value) => Number(value.trim()));
      return numericPort >= min && numericPort <= max;
    }

    return numericPort === Number(trimmed);
  });
}

function tunnelEndpointHost(tunnel) {
  return tunnel.server_host || state.server_host || "";
}

function renderClientRow(tunnel, client) {
  const notes = String(client.notes || "").trim();
  const runtime = clientRuntimeSummary(client);
  const expiration = clientExpirationSummary(client);
  const enabledTitle = client.enabled ? "Client is rendered unless expired." : "Client is disabled and not rendered.";
  const details = [expiration.details, runtime.details].filter(Boolean).map(escapeHTML).join(" · ");
  return `
    <div class="client-row">
      <div>
        <div class="client-title-row">
          <strong>${escapeHTML(client.name)}</strong>
          <span class="client-meta">
            ${clientBadge(client.enabled ? "enabled" : "disabled", client.enabled ? "ok" : "neutral", enabledTitle)}
            ${expiration.badge ? clientBadge(expiration.badge, expiration.level, expiration.title) : ""}
            ${clientBadge(runtime.label, runtime.level, runtime.title)}
            ${client.needs_new_config ? clientBadge("stale config", "warn", "Download a fresh .conf after tunnel or protocol changes.") : ""}
          </span>
        </div>
        <span class="muted"><span class="mono">${escapeHTML(client.address)}</span></span>
        ${details ? `<span class="muted">${details}</span>` : ""}
        ${notes ? `<span class="client-notes">${escapeHTML(notes)}</span>` : ""}
      </div>
      <div class="actions row-actions">
        <a class="button" href="/clients/config/${encodeURIComponent(client.id)}">Config</a>
        <button data-action="import-key" data-client="${escapeAttr(client.id)}">Import key</button>
        <button data-action="edit-client" data-tunnel="${escapeAttr(tunnel.id)}" data-client="${escapeAttr(client.id)}">Edit</button>
        <button data-action="${client.enabled ? "disable-client" : "enable-client"}" data-client="${escapeAttr(client.id)}">${client.enabled ? "Disable" : "Enable"}</button>
        <button class="danger" data-action="delete-client" data-client="${escapeAttr(client.id)}">Delete</button>
      </div>
    </div>
  `;
}

function clientBadge(label, level, title = "") {
  const text = String(label || "").trim();
  const hint = String(title || text).trim();
  return `<span class="badge ${statusBadgeClass(level)}" title="${escapeAttr(hint)}" aria-label="${escapeAttr(hint)}">${escapeHTML(text)}</span>`;
}

function clientExpirationSummary(client) {
  if (!client.expires_at) return { badge: "", level: "neutral", details: "" };
  const date = formatDateOnly(client.expires_at);
  if (client.expired) {
    return {
      badge: "expired",
      level: "warn",
      title: "Expired clients stay visible, but are not rendered as server peers.",
      details: date ? `not rendered since ${date}` : "not rendered",
    };
  }
  return { badge: "", level: "neutral", title: "", details: date ? `expires ${date}` : "" };
}

function clientRuntimeSummary(client) {
  if (!client.enabled) {
    return { level: "neutral", label: "not active", title: "Disabled clients are not rendered as server peers.", details: "" };
  }
  const runtime = client.runtime || {};
  const latest = String(runtime.latest_handshake || "").trim();
  const transfer = runtimeTransfer(runtime);
  const rememberedLastSeen = runtime.last_seen_at || client.last_seen_at || "";
  if (client.expired) {
    const seen = latest ? lastSeenDetail(formatHandshakeAge(latest)) : lastSeenDetail(formatDateOnly(rememberedLastSeen));
    return {
      level: "neutral",
      label: "not active",
      title: "Expired clients are kept for history but are not rendered as server peers.",
      details: [seen, transfer].filter(Boolean).join(" · "),
    };
  }
  if (!runtime.present) {
    if (client.ever_connected && rememberedLastSeen) {
      return {
        level: "neutral",
        label: "last seen",
        title: "Runtime peer is not visible now; this is the saved last successful handshake date.",
        details: lastSeenDetail(formatDateOnly(rememberedLastSeen)),
      };
    }
    return {
      level: "neutral",
      label: "status unknown",
      title: "Runtime information is unavailable for this client.",
      details: transfer,
    };
  }
  if (!latest) {
    if (client.ever_connected && rememberedLastSeen) {
      return {
        level: "neutral",
        label: "last seen",
        title: "Runtime peer is present, but it has no fresh handshake yet.",
        details: [lastSeenDetail(formatDateOnly(rememberedLastSeen)), transfer].filter(Boolean).join(" · "),
      };
    }
    return {
      level: "warn",
      label: "never connected",
      title: "The peer exists in runtime, but no successful handshake has been observed.",
      details: transfer,
    };
  }
  const recent = handshakeLooksRecent(latest);
  return {
    level: recent ? "ok" : "neutral",
    label: recent ? "active now" : "seen recently",
    title: recent
      ? "Latest handshake is recent. This is an approximate online indicator."
      : "Latest handshake exists, but it is older than the active window.",
    details: [lastSeenDetail(formatHandshakeAge(latest)), transfer].filter(Boolean).join(" · "),
  };
}

function handshakeLooksRecent(value) {
  const seconds = handshakeAgeSeconds(value);
  return seconds !== null && seconds < 180;
}

function handshakeAgeSeconds(value) {
  const text = String(value || "").toLowerCase();
  let total = 0;
  let matched = false;
  const units = [
    [/(\d+)\s+day/, 86400],
    [/(\d+)\s+hour/, 3600],
    [/(\d+)\s+minute/, 60],
    [/(\d+)\s+second/, 1],
  ];
  units.forEach(([pattern, multiplier]) => {
    const match = text.match(pattern);
    if (!match) return;
    matched = true;
    total += Number(match[1]) * multiplier;
  });
  return matched ? total : null;
}

function formatHandshakeAge(value) {
  const seconds = handshakeAgeSeconds(value);
  if (seconds === null) return String(value || "").trim();
  if (seconds < 180) return String(value || "").trim();
  if (seconds < 3600) return `${Math.floor(seconds / 60)} minutes ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} hours ago`;
  return `${Math.floor(seconds / 86400)} days ago`;
}

function runtimeTransfer(runtime) {
  const rx = Number(runtime.rx_bytes || 0);
  const tx = Number(runtime.tx_bytes || 0);
  if (!rx && !tx) return "";
  return `received ${formatBytes(rx)} · sent ${formatBytes(tx)}`;
}

function formatBytes(bytes) {
  const value = Number(bytes || 0);
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let current = value;
  let unit = 0;
  while (current >= 1024 && unit < units.length - 1) {
    current /= 1024;
    unit += 1;
  }
  const digits = unit === 0 || current >= 10 ? 0 : 1;
  return `${current.toFixed(digits)} ${units[unit]}`;
}

function formatDateOnly(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleDateString();
}

function lastSeenDetail(value) {
  return value ? `last seen ${value}` : "";
}

function bindAppEvents(active) {
  document.querySelectorAll("[data-profile]").forEach((button) => {
    button.addEventListener("click", () => {
      activeProfile = button.dataset.profile;
      renderApp();
    });
  });

  document.querySelectorAll("[data-action]").forEach((node) => {
    node.addEventListener("click", async () => {
      const action = node.dataset.action;
      const tunnel = node.dataset.tunnel ? findTunnel(node.dataset.tunnel) : null;

      if (action === "refresh") await refreshState();
      if (action === "theme") toggleTheme();
      if (action === "maintenance") openMaintenance();
      if (action === "support-bundle") await downloadSupportBundle();
      if (action === "logout") await logout();
      if (action === "create-tunnel") openCreateTunnel(active);
      if (action === "create-client" && tunnel) openCreateClient(tunnel);
      if (action === "settings" && tunnel) openSettings(tunnel);
      if (action === "protocol" && tunnel) openProtocol(tunnel);
      if (action === "health" && tunnel) await openHealth(tunnel);
      if (action === "restart" && tunnel) await restartTunnel(tunnel);
      if (action === "delete-tunnel" && tunnel) await deleteTunnel(tunnel);
      if (action === "edit-client" && tunnel) openClientSettings(tunnel, node.dataset.client);
      if (action === "import-key") await openClientImportKey(node.dataset.client);
      if (action === "enable-client") await setClient(node.dataset.client, true);
      if (action === "disable-client") await setClient(node.dataset.client, false);
      if (action === "delete-client") await deleteClient(node.dataset.client);
    });
  });
}
