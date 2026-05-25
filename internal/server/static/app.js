const app = document.querySelector("#app");
const modal = document.querySelector("#modal");
const toast = document.querySelector("#toast");

let state = null;
let activeProfile = localStorage.getItem("awg-forge.profile") || "awg_legacy_1_0";
let activeTheme = initialTheme();
const maintenanceState = {
  tab: "overview",
  doctor: null,
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

applyTheme(activeTheme);
initParallax();
initModalBehavior();
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
  app.innerHTML = `
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
  `;

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
    app.innerHTML = `
      <section class="panel empty">
        <span class="empty-icon">!</span>
        <strong>No profiles available</strong>
        <p class="muted">Backend returned an empty profile list.</p>
      </section>
    `;
    return;
  }

  const active = profiles.find((profile) => profile.id === activeProfile) || profiles[0];
  activeProfile = active.id;
  localStorage.setItem("awg-forge.profile", activeProfile);

  const allTunnels = state.tunnels || [];
  const tunnels = allTunnels.filter((tunnel) => tunnel.profile === activeProfile);

  app.innerHTML = `
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
  `;

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
        <div class="fact"><span>Clients</span><strong>${clients.filter((client) => client.enabled).length}/${clients.length}</strong></div>
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
  return `
    <div class="client-row">
      <div>
        <strong><span class="status-dot ${client.enabled ? "ok" : "off"}"></span>${escapeHTML(client.name)}</strong>
        <span class="muted"><span class="mono">${escapeHTML(client.address)}</span> · ${client.enabled ? "enabled" : "disabled"}${client.needs_new_config ? " · needs new config" : ""}</span>
        ${notes ? `<span class="client-notes">${escapeHTML(notes)}</span>` : ""}
      </div>
      <div class="actions row-actions">
        ${client.needs_new_config ? `<span class="badge warn">stale</span>` : ""}
        <a class="button" href="/clients/config/${encodeURIComponent(client.id)}">Config</a>
        <button data-action="import-key" data-client="${escapeAttr(client.id)}">Import key</button>
        <button data-action="edit-client" data-tunnel="${escapeAttr(tunnel.id)}" data-client="${escapeAttr(client.id)}">Edit</button>
        <button data-action="${client.enabled ? "disable-client" : "enable-client"}" data-client="${escapeAttr(client.id)}">${client.enabled ? "Disable" : "Enable"}</button>
        <button class="danger" data-action="delete-client" data-client="${escapeAttr(client.id)}">Delete</button>
      </div>
    </div>
  `;
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
      if (action === "doctor") await openDoctor();
      if (action === "backup") openBackup();
      if (action === "support-bundle") await downloadSupportBundle();
      if (action === "updates") await openUpdates();
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

async function openClientImportKey(clientID) {
  const res = await api(`/api/clients/${encodeURIComponent(clientID)}/import-key`, {
    method: "POST",
    idempotencyKey: newIdempotencyKey(),
  });
  const payload = await res.json().catch(() => ({}));
  if (!res.ok) {
    showToast(payload.error || "failed to create import key");
    return;
  }
  const key = String(payload.import_key || "");
  showModal(`
    <div class="modal-head">
      <div><h2>Experimental import key</h2><p class="muted">${escapeHTML(payload.client?.name || "Client")} · AmneziaVPN / DefaultVPN only</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <div class="form-grid">
      <div>
        <label>vpn:// key</label>
        <textarea class="mono import-key-text" readonly>${escapeHTML(key)}</textarea>
      </div>
      <p class="muted">Paste this key into AmneziaVPN or DefaultVPN where vpn:// text key import is supported. This is experimental; keep using the downloaded .conf for routers and production fallback.</p>
    </div>
    <div class="form-actions">
      <button type="button" data-copy-import-key>Copy key</button>
      <a class="button" href="/clients/config/${encodeURIComponent(clientID)}">Download .conf</a>
    </div>
  `);
  modal.querySelector("[data-copy-import-key]")?.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(key);
      showToast("import key copied");
    } catch {
      modal.querySelector(".import-key-text")?.select();
      showToast("select and copy the key manually");
    }
  });
}

function openClientSettings(tunnel, clientID) {
  const client = (tunnel.clients || []).find((item) => item.id === clientID);
  if (!client) {
    showToast("client not found");
    return;
  }

  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Client settings</h2><p class="muted">${escapeHTML(tunnel.name)} · <span class="mono">${escapeHTML(client.address)}</span></p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div class="form-grid single">
        <div><label>Client name</label><input name="name" value="${escapeAttr(client.name)}" autofocus></div>
        <div><label>Notes</label><textarea name="notes" maxlength="1000" placeholder="Admin-only note">${escapeHTML(client.notes || "")}</textarea></div>
      </div>
      <div class="form-actions"><button class="primary" type="submit">Save client</button></div>
    </form>
  `;

  showModal(body);

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const res = await api(`/api/clients/${encodeURIComponent(client.id)}/settings`, {
      method: "PATCH",
      idempotencyKey: formIdempotencyKey(event.currentTarget),
      body: {
        name: form.get("name"),
        notes: form.get("notes"),
      },
    });

    if (res.ok) {
      await closeModalAndReload(tunnel.profile);
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

function openCreateTunnel(profile) {
  const suggestion = nextTunnelSuggestion(profile);
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Create ${escapeHTML(profile.tab)} tunnel</h2><p class="muted">Each tunnel has its own port, subnet, keys, and clients.</p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div class="form-grid">
        <div><label>Name / interface</label><input name="name" value="${escapeAttr(suggestion.name)}" autofocus></div>
        <div><label>Listen port</label><input name="port" inputmode="numeric" value="${escapeAttr(suggestion.port)}"></div>
        <div><label>IPv4 subnet</label><input name="subnet" value="${escapeAttr(suggestion.subnet)}"></div>
      </div>
      <div class="form-actions"><button class="primary" type="submit">Create tunnel</button></div>
    </form>
  `;

  showModal(body);

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const res = await api("/api/tunnels", {
      method: "POST",
      idempotencyKey: formIdempotencyKey(event.currentTarget),
      body: {
        profile: profile.id,
        name: form.get("name"),
        port: Number(form.get("port")),
        subnet: form.get("subnet"),
      },
    });

    if (res.ok) {
      await closeModalAndReload(profile.id);
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

function nextTunnelSuggestion(profile) {
  const existing = (state.tunnels || []).filter((tunnel) => tunnel.profile === profile.id);
  if (existing.length === 0) {
    return {
      name: profile.suggested_name,
      port: profile.suggested_port,
      subnet: profile.suggested_subnet,
    };
  }

  const n = existing.length + 1;
  const baseName = String(profile.suggested_name || "awg");
  const subnet = nextSubnet(profile.suggested_subnet, n);

  return {
    name: `${baseName}-${n}`,
    port: Number(profile.suggested_port) + existing.length,
    subnet,
  };
}

function nextSubnet(suggestedSubnet, n) {
  const fallback = String(suggestedSubnet || "10.8.0.0/24");
  const match = fallback.match(/^(\d+)\.(\d+)\.(\d+)\.0\/24$/);
  if (!match) return fallback;

  const thirdOctet = Math.min(254, Number(match[3]) + n - 1);
  return `${match[1]}.${match[2]}.${thirdOctet}.0/24`;
}

function formatMTU(mtu) {
  return Number(mtu) > 0 ? String(mtu) : "Auto";
}

function isPresetMTU(mtu) {
  return [0, 1280, 1380, 1400, 1420].includes(Number(mtu || 0));
}

function mtuOption(current, value, label) {
  const selected = value === -1 ? !isPresetMTU(current) : Number(current || 0) === value;
  return `<option value="${value}" ${selected ? "selected" : ""}>${escapeHTML(label)}</option>`;
}

function selectedMTU(form) {
  const mode = Number(form.get("mtu_mode"));
  if (mode !== -1) return mode;
  return Number(form.get("mtu_custom") || 0);
}

function openCreateClient(tunnel) {
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Create client</h2><p class="muted">${escapeHTML(tunnel.name)} · ${escapeHTML(tunnel.profile)}</p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div><label>Client name</label><input name="name" autofocus></div>
      <div class="form-actions"><button class="primary" type="submit">Create client</button></div>
    </form>
  `;

  showModal(body);

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const res = await api("/api/clients", {
      method: "POST",
      idempotencyKey: formIdempotencyKey(event.currentTarget),
      body: {
        tunnel_id: tunnel.id,
        name: form.get("name"),
      },
    });

    if (res.ok) {
      const payload = await res.json();
      await closeModalAndReload(tunnel.profile);
      if (payload.client?.id) downloadClientConfig(payload.client.id);
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

function openSettings(tunnel) {
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Tunnel settings</h2><p class="muted">${escapeHTML(tunnel.profile)}</p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div class="form-grid">
        <div><label>Name / interface</label><input name="name" value="${escapeAttr(tunnel.name)}" autofocus></div>
        <div><label>Server host</label><input name="server_host" value="${escapeAttr(tunnel.server_host || "")}" placeholder="${escapeAttr(state.server_host || "")}"></div>
        <div><label>Listen port</label><input name="port" inputmode="numeric" value="${escapeAttr(tunnel.listen_port)}"></div>
        <div><label>IPv4 subnet</label><input name="subnet" value="${escapeAttr(tunnel.subnet)}"></div>
        <div><label>DNS</label><input name="dns" value="${escapeAttr(tunnel.dns)}"></div>
        <div><label>Allowed IPs</label><input name="allowed_ips" value="${escapeAttr(tunnel.allowed_ips)}"></div>
        <div><label>Persistent keepalive</label><input name="keepalive" inputmode="numeric" value="${escapeAttr(tunnel.keepalive)}"></div>
        <div>
          <label>MTU</label>
          <select name="mtu_mode">
            ${mtuOption(tunnel.mtu, 0, "Auto")}
            ${mtuOption(tunnel.mtu, 1280, "1280")}
            ${mtuOption(tunnel.mtu, 1380, "1380")}
            ${mtuOption(tunnel.mtu, 1400, "1400")}
            ${mtuOption(tunnel.mtu, 1420, "1420")}
            ${mtuOption(tunnel.mtu, -1, "Custom")}
          </select>
        </div>
        <div><label>Custom MTU</label><input name="mtu_custom" inputmode="numeric" value="${isPresetMTU(tunnel.mtu) ? "" : escapeAttr(tunnel.mtu)}"></div>
        <div><label><input name="enabled" type="checkbox" ${tunnel.enabled ? "checked" : ""}> Enabled</label></div>
      </div>
      <div class="form-actions"><button class="primary" type="submit">Save settings</button></div>
    </form>
  `;

  showModal(body);

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/settings`, {
      method: "PATCH",
      idempotencyKey: formIdempotencyKey(event.currentTarget),
      body: {
        name: form.get("name"),
        server_host: String(form.get("server_host") || "").trim(),
        port: Number(form.get("port")),
        subnet: form.get("subnet"),
        dns: form.get("dns"),
        allowed_ips: form.get("allowed_ips"),
        keepalive: Number(form.get("keepalive")),
        mtu: selectedMTU(form),
        enabled: form.get("enabled") === "on",
      },
    });

    if (res.ok) {
      await closeModalAndReload(tunnel.profile);
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

function openProtocol(tunnel) {
  const params = tunnel.params || [];
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Protocol parameters</h2><p class="muted">${escapeHTML(tunnel.name)} · ${escapeHTML(tunnel.profile)}</p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div class="form-grid">
        ${params.map(({ key, value }) => `
          <div>
            <label>${escapeHTML(key)}</label>
            ${String(key).startsWith("I") ? `<textarea name="${escapeAttr(key)}">${escapeHTML(value || "")}</textarea>` : `<input name="${escapeAttr(key)}" value="${escapeAttr(value || "")}">`}
          </div>
        `).join("")}
      </div>
      <div class="form-actions">
        <button type="button" data-regenerate>Regenerate</button>
        <button class="primary" type="submit">Save protocol</button>
      </div>
    </form>
  `;

  showModal(body);

  const regenerate = document.querySelector("[data-regenerate]");
  regenerate.addEventListener("click", async () => {
    if (!confirm("Regenerate protocol parameters? Clients in this tunnel must import fresh configs.")) return;

    const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/regenerate`, {
      method: "POST",
      idempotencyKey: newIdempotencyKey(),
      body: { profile: tunnel.profile },
    });

    if (res.ok) await closeModalAndReload(tunnel.profile);
  });

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const nextParams = {};
    for (const { key } of params) nextParams[key] = String(form.get(key) || "").trim();

    const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/protocol`, {
      method: "PATCH",
      idempotencyKey: formIdempotencyKey(event.currentTarget),
      body: {
        profile: tunnel.profile,
        params: nextParams,
      },
    });

    if (res.ok) {
      await closeModalAndReload(tunnel.profile);
      return;
    }

    resetSubmit(event.currentTarget);
  });
}

async function openDoctor() {
  const res = await api("/api/doctor");
  if (!res.ok) return;

  const payload = await res.json();
  const results = payload.results || [];
  const repairAvailable = Boolean(state?.apply_enabled);
  const repairReason = "Firewall repair is unavailable because APPLY_CONFIG=false";

  showModal(`
    <div class="modal-head">
      <div><h2>Doctor</h2><p class="muted">Runtime, tools, network, and tunnel checks.</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <div class="modal-actions">
      <button
        type="button"
        class="${repairAvailable ? "" : "is-disabled"}"
        data-modal-action="repair-firewall"
        aria-disabled="${repairAvailable ? "false" : "true"}"
        title="${repairAvailable ? "Repair managed firewall rules" : repairReason}"
      >Repair firewall</button>
    </div>
    <div class="client-list">
      ${results.map((result) => `
        <div class="client-row">
          <div>
            <strong>${escapeHTML(result.area)}</strong>
            <span class="muted doctor-message mono">${escapeHTML(result.message)}</span>
          </div>
          <span class="badge ${doctorBadgeClass(result.level)}">${escapeHTML(result.level)}</span>
        </div>
      `).join("")}
    </div>
  `);

  modal.querySelector("[data-modal-action='repair-firewall']")?.addEventListener("click", repairFirewall);
}

function openMaintenance(tab = maintenanceState.tab || "overview") {
  maintenanceState.tab = tab;
  const tabs = [
    ["overview", "Overview"],
    ["doctor", "Doctor"],
    ["firewall", "Firewall"],
    ["backup", "Backup"],
    ["restore", "Restore"],
    ["updates", "Updates"],
    ["support", "Support"],
    ["system", "System"],
  ];

  showModal(`
    <div class="modal-head">
      <div><h2>Maintenance</h2><p class="muted">Operations center for diagnostics, firewall, backups, restore checks, updates, and support.</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <div class="maintenance-tabs" role="tablist" aria-label="Maintenance sections">
      ${tabs.map(([id, label]) => `
        <button type="button" role="tab" class="${id === maintenanceState.tab ? "active" : ""}" data-maint-tab="${escapeAttr(id)}">${escapeHTML(label)}</button>
      `).join("")}
    </div>
    <section class="maintenance-panel">
      ${renderMaintenanceTab()}
    </section>
  `);
  modal.classList.add("maintenance-modal");

  bindMaintenanceEvents();
}

function renderMaintenanceTab() {
  if (maintenanceState.tab === "doctor") return renderMaintenanceDoctor();
  if (maintenanceState.tab === "firewall") return renderMaintenanceFirewall();
  if (maintenanceState.tab === "backup") return renderMaintenanceBackup();
  if (maintenanceState.tab === "restore") return renderMaintenanceRestore();
  if (maintenanceState.tab === "updates") return renderMaintenanceUpdates();
  if (maintenanceState.tab === "support") return renderMaintenanceSupport();
  if (maintenanceState.tab === "system") return renderMaintenanceSystem();
  return renderMaintenanceOverview();
}

function renderMaintenanceOverview() {
  const summary = maintenanceSummary();
  return `
    <div class="maintenance-hero">
      <div>
        <span class="badge ${summary.overallClass}">${escapeHTML(summary.overall)}</span>
        <h3>${escapeHTML(summary.title)}</h3>
        <p class="muted">${escapeHTML(summary.text)}</p>
      </div>
      <button type="button" class="primary" data-maint-action="run-doctor">${maintenanceState.loading.doctor ? "Running..." : "Run doctor"}</button>
    </div>
    <div class="maintenance-grid">
      ${maintenanceOverviewCard("Runtime", summary.runtimeBadge, summary.runtimeClass, [
        `${summary.upTunnels}/${summary.totalTunnels} tunnels up`,
        `${summary.enabledClients}/${summary.totalClients} clients enabled`,
      ], "doctor")}
      ${maintenanceOverviewCard("Clients", summary.clientsBadge, summary.clientsClass, [
        `${summary.staleClients} stale config(s)`,
        `${summary.noHandshakeClients} no handshake warning(s)`,
      ], "doctor")}
      ${maintenanceOverviewCard("Firewall", summary.firewallBadge, summary.firewallClass, [
        `${summary.firewallOk}/${summary.totalTunnels} tunnel checks ok`,
        state?.apply_enabled ? "Runtime repair enabled" : "Dry-run mode",
      ], "firewall")}
      ${maintenanceOverviewCard("Recovery", "backup", "ok", [
        "Encrypted backup",
        "Restore dry-run verification",
      ], "backup")}
    </div>
  `;
}

function maintenanceOverviewCard(title, badge, badgeClass, lines, tab) {
  return `
    <section class="maintenance-card compact">
      <div class="maintenance-card-head">
        <h3>${escapeHTML(title)}</h3>
        <span class="badge ${escapeAttr(badgeClass)}">${escapeHTML(badge)}</span>
      </div>
      <ul class="maintenance-list">
        ${lines.map((line) => `<li>${escapeHTML(line)}</li>`).join("")}
      </ul>
      <button type="button" data-maint-tab="${escapeAttr(tab)}">Open ${escapeHTML(title)}</button>
    </section>
  `;
}

function renderMaintenanceDoctor() {
  const results = maintenanceState.doctor || [];
  const hasResults = results.length > 0;
  const filtered = hasResults ? results : [];
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Doctor</h3>
        <p class="muted">Runtime tools, ports, tunnels, firewall, peers, handshakes, and stale configs.</p>
      </div>
      <button type="button" class="primary" data-maint-action="run-doctor">${maintenanceState.loading.doctor ? "Running..." : "Run doctor"}</button>
    </div>
    ${maintenanceState.lastRun.doctor ? `<p class="muted">Last run: ${escapeHTML(maintenanceState.lastRun.doctor)}</p>` : ""}
    ${hasResults ? `
      <div class="maintenance-filters">
        <span class="badge ok">${filtered.filter((item) => item.level === "ok").length} ok</span>
        <span class="badge warn">${filtered.filter((item) => item.level === "warn").length} warn</span>
        <span class="badge bad">${filtered.filter((item) => item.level === "fail").length} fail</span>
      </div>
      <div class="client-list">
        ${filtered.map((result) => `
          <div class="client-row">
            <div>
              <strong>${escapeHTML(result.area)}</strong>
              <span class="muted doctor-message mono">${escapeHTML(result.message)}</span>
            </div>
            <span class="badge ${doctorBadgeClass(result.level)}">${escapeHTML(result.level)}</span>
          </div>
        `).join("")}
      </div>
    ` : `<div class="empty-inline">Run Doctor to collect current diagnostics.</div>`}
  `;
}

function renderMaintenanceFirewall() {
  const tunnels = state?.tunnels || [];
  const repairAvailable = Boolean(state?.apply_enabled);
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Firewall</h3>
        <p class="muted">Managed NAT, INPUT, and FORWARD rules for enabled tunnels.</p>
      </div>
      <button type="button" class="primary ${repairAvailable ? "" : "is-disabled"}" data-maint-action="repair-firewall" aria-disabled="${repairAvailable ? "false" : "true"}" title="${repairAvailable ? "Repair managed firewall rules" : "APPLY_CONFIG=false"}">Repair firewall</button>
    </div>
    ${repairAvailable ? `<div class="notice">Repair reconciles only awg-forge managed rules. It does not change keys, protocol params, or client configs.</div>` : `<div class="notice">Runtime firewall repair is unavailable because APPLY_CONFIG=false.</div>`}
    <div class="maintenance-grid">
      ${tunnels.map((tunnel) => {
        const fw = tunnel.status?.firewall || {};
        return `
          <section class="maintenance-card compact">
            <div class="maintenance-card-head">
              <h3>${escapeHTML(tunnel.name)}</h3>
              <span class="badge ${escapeAttr(fw.level || "warn")}">${escapeHTML(fw.label || "firewall unknown")}</span>
            </div>
            <p class="muted">${escapeHTML(fw.message || "Managed firewall summary for this tunnel.")}</p>
            <ul class="maintenance-list">
              <li><span class="mono">${escapeHTML(tunnel.subnet || "")}</span></li>
              <li><span class="mono">${escapeHTML(tunnel.interface || tunnel.name || "")}</span></li>
              <li>${Number(tunnel.listen_port || 0)}/udp</li>
            </ul>
          </section>
        `;
      }).join("") || `<div class="empty-inline">No tunnels yet.</div>`}
    </div>
  `;
}

function renderMaintenanceBackup() {
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Encrypted backup</h3>
        <p class="muted">Export state, rendered configs, and metadata with a dedicated backup password.</p>
      </div>
    </div>
    <div class="notice">Use a dedicated backup password. It is required to restore this archive and is not stored by awg-forge.</div>
    <form id="maintenance-backup-form" class="form-grid single">
      <div><label>Backup password</label><input name="password" type="password" autocomplete="new-password" minlength="8"></div>
      <label class="checkbox-row"><input name="saved" type="checkbox"> I understand that this password is required to restore the backup.</label>
      <div class="form-actions"><button class="primary" type="submit">Create encrypted backup</button></div>
    </form>
  `;
}

function renderMaintenanceRestore() {
  const report = maintenanceState.restoreReport;
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Restore verify</h3>
        <p class="muted">Dry-run an encrypted backup before restoring it from CLI.</p>
      </div>
    </div>
    <div class="notice">This check decrypts and validates the backup without writing to CONFIG_DIR. Actual restore remains CLI-only for safety.</div>
    <form id="maintenance-restore-form" class="form-grid single">
      <div><label>Backup file</label><input name="backup" type="file" accept=".afbackup,application/octet-stream"></div>
      <div><label>Backup password</label><input name="password" type="password" autocomplete="current-password" minlength="8"></div>
      <div class="form-actions"><button class="primary" type="submit">Verify backup</button></div>
    </form>
    ${report ? renderRestoreReport(report) : ""}
    <div class="command-block mono">docker cp ./&lt;backup-file&gt;.afbackup awg-forge:/tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='...' awg-forge awg-forge restore verify /tmp/backup.afbackup
docker exec -e BACKUP_PASSWORD='...' awg-forge awg-forge restore /tmp/backup.afbackup
docker exec awg-forge awg-forge tunnel restart
docker exec awg-forge awg-forge firewall repair
docker exec awg-forge awg-forge doctor</div>
  `;
}

function renderRestoreReport(report) {
  const tunnels = report.Tunnels || report.tunnels || [];
  return `
    <div class="maintenance-result">
      <div class="maintenance-card-head">
        <h3>Backup verified</h3>
        <span class="badge ok">ok</span>
      </div>
      <div class="facts">
        ${fact("Format", report.Format || report.format || "")}
        ${fact("Schema", String(report.SchemaVersion || report.schema_version || ""))}
        ${fact("Files", String(report.FileCount || report.file_count || 0))}
        ${fact("Clients", String(report.ClientCount || report.client_count || 0))}
        ${fact("Server host", report.ServerHost || report.server_host || "")}
      </div>
      ${tunnels.length ? `<div class="client-list">${tunnels.map((tunnel) => `
        <div class="client-row">
          <div>
            <strong>${escapeHTML(tunnel.Name || tunnel.name)}</strong>
            <span class="muted"><span class="mono">${escapeHTML(tunnel.Interface || tunnel.interface || "")}</span> · ${escapeHTML(tunnel.Profile || tunnel.profile || "")} · ${escapeHTML(tunnel.Subnet || tunnel.subnet || "")}</span>
          </div>
          <span class="badge">${Number(tunnel.ListenPort || tunnel.listen_port || 0)}/udp</span>
        </div>
      `).join("")}</div>` : ""}
    </div>
  `;
}

function renderMaintenanceUpdates() {
  const report = maintenanceState.updates || {};
  const info = report.build_info || {};
  const components = report.components || [];
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Updates</h3>
        <p class="muted">Compare pinned AmneziaWG refs against upstream. Updates are manual.</p>
      </div>
      <button type="button" class="primary" data-maint-action="run-updates">${maintenanceState.loading.updates ? "Checking..." : "Check updates"}</button>
    </div>
    <div class="notice">awg-forge never updates AmneziaWG inside the running container. Update pinned refs in a PR, rebuild, test, and release a new image.</div>
    ${components.length ? `
      <p class="muted">awg-forge ${escapeHTML(info.version || "dev")} · ${maintenanceState.lastRun.updates ? `last checked ${escapeHTML(maintenanceState.lastRun.updates)}` : "not checked in this session"}</p>
      <div class="client-list">
        ${components.map((component) => `
          <div class="client-row">
            <div>
              <strong>${escapeHTML(component.name)}</strong>
              <span class="muted mono">${escapeHTML(component.repository)}</span>
              <span class="muted">pinned <span class="mono">${escapeHTML(shortRef(component.current_ref))}</span>${component.latest_ref ? ` · upstream <span class="mono">${escapeHTML(shortRef(component.latest_ref))}</span>` : ""}</span>
              ${component.error ? `<span class="muted">${escapeHTML(component.error)}</span>` : ""}
            </div>
            <span class="badge ${updateBadgeClass(component.status)}">${escapeHTML(updateLabel(component.status))}</span>
          </div>
        `).join("")}
      </div>
    ` : `<div class="empty-inline">Run update check to compare bundled refs with upstream.</div>`}
  `;
}

function renderMaintenanceSupport() {
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Support bundle</h3>
        <p class="muted">Download diagnostics that are safe to share.</p>
      </div>
      <button type="button" class="primary" data-maint-action="support-bundle">Download support bundle</button>
    </div>
    <div class="maintenance-grid">
      <section class="maintenance-card compact">
        <div class="maintenance-card-head"><h3>Included</h3><span class="badge ok">redacted</span></div>
        <ul class="maintenance-list">
          <li>Doctor output</li>
          <li>Runtime summaries</li>
          <li>State/config summaries</li>
          <li>File inventory</li>
        </ul>
      </section>
      <section class="maintenance-card compact">
        <div class="maintenance-card-head"><h3>Excluded</h3><span class="badge ok">secrets</span></div>
        <ul class="maintenance-list">
          <li>Private keys</li>
          <li>Preshared keys</li>
          <li>Passwords</li>
          <li>Full client configs</li>
        </ul>
      </section>
    </div>
  `;
}

function renderMaintenanceSystem() {
  const ports = state?.published_udp_ports || [];
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>System</h3>
        <p class="muted">Current UI/runtime context without secrets.</p>
      </div>
    </div>
    <div class="facts">
      ${fact("Server host", state?.server_host || "")}
      ${fact("Apply config", state?.apply_enabled ? "enabled" : "disabled")}
      ${fact("Tunnels", String((state?.tunnels || []).length))}
      ${fact("Profiles", String((state?.profiles || []).length))}
      ${fact("Published UDP", ports.length ? ports.join(", ") : "host networking / dynamic")}
    </div>
    <div class="command-block mono">docker exec awg-forge awg-forge doctor
docker exec awg-forge awg show
docker compose logs -f</div>
  `;
}

function bindMaintenanceEvents() {
  modal.querySelectorAll("[data-maint-tab]").forEach((button) => {
    button.addEventListener("click", () => openMaintenance(button.dataset.maintTab));
  });

  modal.querySelectorAll("[data-maint-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      const action = button.dataset.maintAction;
      if (button.getAttribute("aria-disabled") === "true") {
        showToast(button.title || "Action unavailable");
        return;
      }
      if (action === "run-doctor") await runMaintenanceDoctor();
      if (action === "repair-firewall") await repairFirewall({ after: "maintenance" });
      if (action === "run-updates") await runMaintenanceUpdates();
      if (action === "support-bundle") await downloadSupportBundle();
    });
  });

  modal.querySelector("#maintenance-backup-form")?.addEventListener("submit", submitMaintenanceBackup);
  modal.querySelector("#maintenance-restore-form")?.addEventListener("submit", submitMaintenanceRestoreVerify);
}

function maintenanceSummary() {
  const tunnels = state?.tunnels || [];
  const clients = tunnels.flatMap((tunnel) => tunnel.clients || []);
  const upTunnels = tunnels.filter((tunnel) => tunnel.status?.up).length;
  const staleClients = tunnels.reduce((sum, tunnel) => sum + Number(tunnel.status?.stale_clients || 0), 0);
  const firewallOk = tunnels.filter((tunnel) => tunnel.status?.firewall?.level === "ok").length;
  const noHandshakeClients = maintenanceState.doctor
    ? maintenanceState.doctor.filter((item) => item.area?.includes("handshake") && item.level === "warn").length
    : 0;
  const failures = maintenanceState.doctor ? maintenanceState.doctor.filter((item) => item.level === "fail").length : 0;
  const warnings = maintenanceState.doctor ? maintenanceState.doctor.filter((item) => item.level === "warn").length : 0;
  const overall = failures ? "needs attention" : warnings ? "warnings" : state?.apply_enabled ? "healthy" : "dry-run";
  return {
    overall,
    overallClass: failures ? "bad" : warnings ? "warn" : "ok",
    title: failures ? "Maintenance required" : warnings ? "Review warnings" : "System looks calm",
    text: maintenanceState.doctor ? "Summary is based on the latest Doctor run in this session." : "Run Doctor for live runtime diagnostics.",
    totalTunnels: tunnels.length,
    upTunnels,
    totalClients: clients.length,
    enabledClients: clients.filter((client) => client.enabled).length,
    staleClients,
    noHandshakeClients,
    firewallOk,
    runtimeBadge: upTunnels === tunnels.length ? "ok" : "check",
    runtimeClass: upTunnels === tunnels.length ? "ok" : "warn",
    clientsBadge: staleClients ? "stale" : "ok",
    clientsClass: staleClients ? "warn" : "ok",
    firewallBadge: firewallOk === tunnels.length ? "ok" : "check",
    firewallClass: firewallOk === tunnels.length ? "ok" : "warn",
  };
}

async function runMaintenanceDoctor() {
  maintenanceState.loading.doctor = true;
  openMaintenance("doctor");
  const res = await api("/api/doctor");
  maintenanceState.loading.doctor = false;
  if (!res.ok) {
    openMaintenance("doctor");
    return;
  }
  const payload = await res.json();
  maintenanceState.doctor = payload.results || [];
  maintenanceState.lastRun.doctor = new Date().toLocaleString();
  openMaintenance("doctor");
}

async function runMaintenanceUpdates() {
  maintenanceState.loading.updates = true;
  openMaintenance("updates");
  const res = await api("/api/updates");
  maintenanceState.loading.updates = false;
  if (!res.ok) {
    openMaintenance("updates");
    return;
  }
  const payload = await res.json();
  maintenanceState.updates = payload.updates || {};
  maintenanceState.lastRun.updates = new Date().toLocaleString();
  openMaintenance("updates");
}

async function submitMaintenanceBackup(event) {
  event.preventDefault();
  if (!beginSubmit(event.currentTarget)) return;

  const form = new FormData(event.currentTarget);
  if (form.get("saved") !== "on") {
    showToast("Confirm that you saved the backup password");
    resetSubmit(event.currentTarget);
    return;
  }
  const res = await fetch("/api/backup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password: form.get("password") }),
  });
  if (!res.ok) {
    showToast(await errorText(res));
    resetSubmit(event.currentTarget);
    return;
  }
  await downloadBlobResponse(res, "awg-forge-backup.afbackup");
  showToast("Backup downloaded");
  resetSubmit(event.currentTarget);
}

async function submitMaintenanceRestoreVerify(event) {
  event.preventDefault();
  if (!beginSubmit(event.currentTarget)) return;

  const form = new FormData(event.currentTarget);
  const res = await fetch("/api/restore/verify", {
    method: "POST",
    body: form,
  });
  if (!res.ok) {
    showToast(await errorText(res));
    resetSubmit(event.currentTarget);
    return;
  }
  const payload = await res.json();
  maintenanceState.restoreReport = payload.report || null;
  showToast("Backup verified");
  resetSubmit(event.currentTarget);
  openMaintenance("restore");
}

async function repairFirewall(options = {}) {
  if (!state?.apply_enabled) {
    showToast("Firewall repair is unavailable: APPLY_CONFIG=false");
    return;
  }
  if (!confirm("Repair managed firewall rules for enabled tunnels?")) return;
  const res = await api("/api/firewall/repair", { method: "POST", body: {} });
  if (!res.ok) return;
  const payload = await res.json();
  const firewall = payload.firewall || {};
  if (firewall.apply_enabled === false) {
    showToast("Firewall repair skipped: APPLY_CONFIG=false");
  } else {
    showToast("Firewall rules repaired");
  }
  await loadState();
  if (options.after === "maintenance") {
    openMaintenance("firewall");
  } else {
    await openDoctor();
  }
}

function openBackup() {
  showModal(`
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Encrypted backup</h2><p class="muted">Contains private keys and client PSKs. Store it safely.</p></div>
        <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
      </div>
      <div class="notice">Use a dedicated backup password. It is required to restore this archive and is not stored by awg-forge.</div>
      <div><label>Backup password</label><input name="password" type="password" autocomplete="new-password" minlength="8" autofocus></div>
      <div class="form-actions"><button class="primary" type="submit">Download backup</button></div>
    </form>
  `);

  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!beginSubmit(event.currentTarget)) return;

    const form = new FormData(event.currentTarget);
    const res = await fetch("/api/backup", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password: form.get("password") }),
    });
    if (!res.ok) {
      showToast(await errorText(res));
      resetSubmit(event.currentTarget);
      return;
    }
    await downloadBlobResponse(res, "awg-forge-backup.afbackup");
    modal.close();
  });
}

async function openUpdates() {
  showModal(`
    <div class="modal-head">
      <div><h2>Updates</h2><p class="muted">Checking pinned AmneziaWG refs against upstream. Updates are manual.</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <p class="muted">Contacting GitHub...</p>
  `);

  const res = await api("/api/updates");
  if (!res.ok) return;

  const payload = await res.json();
  const report = payload.updates || {};
  const info = report.build_info || {};
  const components = report.components || [];

  showModal(`
    <div class="modal-head">
      <div><h2>Updates</h2><p class="muted">awg-forge ${escapeHTML(info.version || "dev")} · manual updates only</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <div class="notice">awg-forge never updates AmneziaWG inside the running container. If upstream changed, update pinned refs in awg-forge, rebuild, test, and release a new image.</div>
    <div class="client-list">
      ${components.map((component) => `
        <div class="client-row">
          <div>
            <strong>${escapeHTML(component.name)}</strong>
            <span class="muted mono">${escapeHTML(component.repository)}</span>
            <span class="muted">pinned <span class="mono">${escapeHTML(shortRef(component.current_ref))}</span>${component.latest_ref ? ` · upstream <span class="mono">${escapeHTML(shortRef(component.latest_ref))}</span> (${escapeHTML(component.default_branch)})` : ""}</span>
            ${component.error ? `<span class="muted">${escapeHTML(component.error)}</span>` : ""}
          </div>
          <span class="badge ${updateBadgeClass(component.status)}">${escapeHTML(updateLabel(component.status))}</span>
        </div>
      `).join("")}
    </div>
  `);
}

async function openHealth(tunnel) {
  showModal(`
    <div class="modal-head">
      <div><h2>Clients health</h2><p class="muted">${escapeHTML(tunnel.name)} · sampling traffic for 2 seconds...</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <p class="muted">Reading runtime handshakes and transfer counters.</p>
  `);

  const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/health`);
  if (!res.ok) return;

  const payload = await res.json();
  const health = payload.health || {};
  const warnings = health.warnings || [];
  const clients = health.clients || [];

  showModal(`
    <div class="modal-head">
      <div><h2>Clients health</h2><p class="muted">${escapeHTML(health.name || tunnel.name)} · ${Number(health.sample_seconds || 0)} second sample</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    ${warnings.length ? `<div class="notice">${warnings.map(escapeHTML).join("<br>")}</div>` : ""}
    <div class="client-list">
      ${clients.length ? clients.map((client) => `
        <div class="client-row">
          <div>
            <strong>${escapeHTML(client.name)}</strong>
            <span class="muted"><span class="mono">${escapeHTML(client.address)}</span> · ${escapeHTML(client.status)}${client.latest_handshake ? ` · handshake ${escapeHTML(client.latest_handshake)}` : ""}</span>
            ${client.warning ? `<span class="muted">${escapeHTML(client.warning)}</span>` : ""}
          </div>
          <div class="actions">
            <span class="badge ${healthBadgeClass(client.status)}">rx +${formatBytes(client.rx_delta_bytes)}</span>
            <span class="badge ${healthBadgeClass(client.status)}">tx +${formatBytes(client.tx_delta_bytes)}</span>
          </div>
        </div>
      `).join("") : `<p class="muted">No clients in this tunnel.</p>`}
    </div>
  `);
}

function doctorBadgeClass(level) {
  if (level === "ok") return "ok";
  if (level === "warn") return "warn";
  return "bad";
}

function updateBadgeClass(status) {
  if (status === "current") return "ok";
  if (status === "newer_available") return "warn";
  return "bad";
}

function updateLabel(status) {
  if (status === "newer_available") return "newer available";
  if (status === "current") return "current";
  return "unknown";
}

function shortRef(ref) {
  const value = String(ref || "unknown");
  return value.length > 12 ? value.slice(0, 12) : value;
}

function healthBadgeClass(status) {
  if (status === "traffic flowing") return "ok";
  if (status === "disabled") return "";
  if (status === "idle, handshake ok") return "ok";
  return "bad";
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes >= 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(2)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(2)} KiB`;
  return `${bytes} B`;
}

function brandIcon() {
  return `
    <svg viewBox="0 0 32 32" focusable="false" aria-hidden="true">
      <path d="M16 3.5 25 7v7.2c0 5.9-3.6 11.2-9 14.1-5.4-2.9-9-8.2-9-14.1V7l9-3.5Z"></path>
      <path d="M11 17.2h10M11 12.6h10M13.7 21.8h4.6"></path>
    </svg>
  `;
}

function brandTitle() {
  return `
    <h1 class="brand-title">
      <span>awg-forge</span>
      <a class="brand-by" href="https://github.com/astronaut808" target="_blank" rel="noopener noreferrer" aria-label="Open astronaut808 GitHub profile">
        <span>by</span>
        <strong>astronaut808</strong>
      </a>
    </h1>
  `;
}

function themeToggleLabel() {
  return activeTheme === "dark" ? "Switch to light theme" : "Switch to dark theme";
}

function themeIcon() {
  if (activeTheme === "dark") {
    return `
      <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true">
        <circle cx="12" cy="12" r="4"></circle>
        <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"></path>
      </svg>
    `;
  }

  return `
    <svg viewBox="0 0 24 24" focusable="false" aria-hidden="true">
      <path d="M21 12.8A8.5 8.5 0 1 1 11.2 3 6.5 6.5 0 0 0 21 12.8Z"></path>
    </svg>
  `;
}

async function restartTunnel(tunnel) {
  const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/restart`, {
    method: "POST",
    idempotencyKey: newIdempotencyKey(),
    body: {},
  });

  if (res.ok) await closeModalAndReload(tunnel.profile);
}

async function deleteTunnel(tunnel) {
  if (!confirm(`Delete tunnel ${tunnel.name} and all its clients?`)) return;

  const res = await api(`/api/tunnels/${encodeURIComponent(tunnel.id)}/delete`, {
    method: "DELETE",
    idempotencyKey: newIdempotencyKey(),
  });

  if (res.ok) await closeModalAndReload(tunnel.profile);
}

async function setClient(clientID, enabled) {
  const res = await api(`/api/clients/${encodeURIComponent(clientID)}/${enabled ? "enable" : "disable"}`, {
    method: "POST",
    idempotencyKey: newIdempotencyKey(),
    body: {},
  });

  if (res.ok) await loadState();
}

async function deleteClient(clientID) {
  if (!confirm("Delete this client?")) return;

  const res = await api(`/api/clients/${encodeURIComponent(clientID)}/delete`, {
    method: "DELETE",
    idempotencyKey: newIdempotencyKey(),
  });

  if (res.ok) await loadState();
}

async function logout() {
  await api("/api/logout", {
    method: "POST",
    body: {},
  });

  state = null;
  renderLogin();
}

async function refreshState() {
  await loadState();
  showToast("Refreshed");
}

async function api(url, options = {}) {
  const init = {
    method: options.method || "GET",
    headers: {},
  };

  if (options.idempotencyKey) {
    init.headers["Idempotency-Key"] = options.idempotencyKey;
  }

  if (options.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }

  const res = await fetch(url, init);
  if (!res.ok) {
    const message = await errorText(res);
    const isApplyFailure = message.includes("apply failed");
    if (isApplyFailure && modal?.open) modal.close();
    showToast(message);
    if (isApplyFailure) window.setTimeout(() => loadState(), 100);
  }
  return res;
}

function beginSubmit(form) {
  if (form.dataset.submitting === "true") return false;

  form.dataset.submitting = "true";
  form.querySelectorAll("button").forEach((button) => {
    if (button.type !== "button") button.disabled = true;
  });

  return true;
}

function resetSubmit(form) {
  if (!form) return;

  form.dataset.submitting = "false";
  form.querySelectorAll("button").forEach((button) => {
    button.disabled = false;
  });
}

function formIdempotencyKey(form) {
  if (!form.dataset.idempotencyKey) {
    form.dataset.idempotencyKey = newIdempotencyKey();
  }

  return form.dataset.idempotencyKey;
}

function newIdempotencyKey() {
  if (crypto.randomUUID) return crypto.randomUUID();
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

async function errorText(res) {
  try {
    const data = await res.json();
    return data.error || `Request failed: ${res.status}`;
  } catch {
    return `Request failed: ${res.status}`;
  }
}

function showModal(html) {
  const wasOpen = modal.open;
  modal.className = "modal";
  modal.innerHTML = `<div class="modal-body">${html}</div>`;

  modal.querySelectorAll("[data-close]").forEach((button) => {
    button.addEventListener("click", () => modal.close());
  });

  if (!wasOpen) modal.showModal();

  const autofocus = modal.querySelector("[autofocus]");
  if (autofocus) autofocus.focus();
}

async function closeModalAndReload(profileID) {
  modal.close();
  if (profileID) activeProfile = profileID;
  await loadState();
}

function downloadClientConfig(clientID) {
  const link = document.createElement("a");
  link.href = `/clients/config/${encodeURIComponent(clientID)}`;
  link.download = "";
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}

async function downloadSupportBundle() {
  const res = await fetch("/api/support-bundle");
  if (!res.ok) {
    showToast(await errorText(res));
    return;
  }
  await downloadBlobResponse(res, "awg-forge-support.zip");
}

async function downloadBlobResponse(res, fallbackName) {
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filenameFromDisposition(res.headers.get("Content-Disposition")) || fallbackName;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function filenameFromDisposition(value) {
  const match = String(value || "").match(/filename="([^"]+)"/);
  return match ? match[1] : "";
}

function findTunnel(id) {
  return (state?.tunnels || []).find((tunnel) => tunnel.id === id);
}

function showToast(message) {
  window.clearTimeout(showToast.timer);
  toast.textContent = message;
  toast.classList.add("show");
  showToast.timer = window.setTimeout(() => toast.classList.remove("show"), 3600);
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[char]));
}

function escapeAttr(value) {
  return escapeHTML(value);
}
