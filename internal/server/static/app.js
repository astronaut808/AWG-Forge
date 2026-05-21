const app = document.querySelector("#app");
const modal = document.querySelector("#modal");
const toast = document.querySelector("#toast");

let state = null;
let activeProfile = localStorage.getItem("awg-forge.profile") || "awg_legacy_1_0";
let activeTheme = initialTheme();

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
  const up = tunnel.status?.up;

  return `
    <article class="card">
      <div class="card-head">
        <div>
          <h3>${escapeHTML(tunnel.name)}</h3>
          <p class="muted"><span class="mono">${escapeHTML(tunnel.interface)}</span> · ${escapeHTML(profileTitles[tunnel.profile] || tunnel.profile)}</p>
        </div>
        <span class="badge ${up ? "ok" : "bad"}">${up ? "up" : "down"}</span>
      </div>
      <div class="facts">
        <div class="fact"><span>Endpoint</span><strong class="mono">${escapeHTML(tunnelEndpointHost(tunnel))}:${escapeHTML(tunnel.listen_port)}</strong></div>
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
  return `
    <div class="client-row">
      <div>
        <strong><span class="status-dot ${client.enabled ? "ok" : "off"}"></span>${escapeHTML(client.name)}</strong>
        <span class="muted"><span class="mono">${escapeHTML(client.address)}</span> · ${client.enabled ? "enabled" : "disabled"}${client.needs_new_config ? " · needs new config" : ""}</span>
      </div>
      <div class="actions row-actions">
        ${client.needs_new_config ? `<span class="badge warn">stale</span>` : ""}
        <a class="button" href="/clients/config/${encodeURIComponent(client.id)}">Config</a>
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
      if (action === "enable-client") await setClient(node.dataset.client, true);
      if (action === "disable-client") await setClient(node.dataset.client, false);
      if (action === "delete-client") await deleteClient(node.dataset.client);
    });
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

function openMaintenance() {
  const repairAvailable = Boolean(state?.apply_enabled);
  const repairReason = "APPLY_CONFIG=false";
  const items = [
    {
      action: "doctor",
      title: "Doctor",
      badge: "check",
      badgeClass: "warn",
      text: "Runtime tools, ports, tunnels, firewall, peers, handshakes, and stale configs.",
      button: "Open Doctor",
    },
    {
      action: "repair-firewall",
      title: "Firewall repair",
      badge: repairAvailable ? "live" : "disabled",
      badgeClass: repairAvailable ? "ok" : "warn",
      text: repairAvailable
        ? "Reconcile managed NAT, INPUT, and FORWARD rules for enabled tunnels."
        : "Runtime firewall repair is disabled in dry-run mode.",
      button: "Repair firewall",
      disabled: !repairAvailable,
      reason: repairReason,
    },
    {
      action: "backup",
      title: "Encrypted backup",
      badge: "encrypted",
      badgeClass: "ok",
      text: "Export state, rendered configs, and metadata with a dedicated backup password.",
      button: "Download backup",
    },
    {
      action: "support-bundle",
      title: "Support bundle",
      badge: "redacted",
      badgeClass: "ok",
      text: "Download diagnostics without private keys, PSKs, session secrets, or full configs.",
      button: "Download bundle",
    },
    {
      action: "updates",
      title: "Updates",
      badge: "manual",
      badgeClass: "warn",
      text: "Compare pinned AmneziaWG refs against upstream. Running containers are never updated automatically.",
      button: "Check updates",
    },
    {
      action: "restore",
      title: "Restore",
      badge: "CLI only",
      badgeClass: "warn",
      text: "Restore remains CLI-only for safety. Use BACKUP_PASSWORD with awg-forge restore.",
      button: "CLI only",
      disabled: true,
      reason: "Restore is intentionally available only from CLI",
    },
  ];

  showModal(`
    <div class="modal-head">
      <div><h2>Maintenance</h2><p class="muted">Production operations, diagnostics, backups, and update checks.</p></div>
      <button class="icon-button" type="button" data-close aria-label="Close">&times;</button>
    </div>
    <div class="maintenance-grid">
      ${items.map((item) => `
        <section class="maintenance-card">
          <div class="maintenance-card-head">
            <h3>${escapeHTML(item.title)}</h3>
            <span class="badge ${item.badgeClass}">${escapeHTML(item.badge)}</span>
          </div>
          <p class="muted">${escapeHTML(item.text)}</p>
          <button
            type="button"
            class="${item.disabled ? "is-disabled" : ""}"
            data-maintenance-action="${escapeAttr(item.action)}"
            aria-disabled="${item.disabled ? "true" : "false"}"
            title="${escapeAttr(item.disabled ? item.reason : item.title)}"
          >${escapeHTML(item.button)}</button>
        </section>
      `).join("")}
    </div>
  `);

  modal.querySelectorAll("[data-maintenance-action]").forEach((button) => {
    button.addEventListener("click", async () => {
      const action = button.dataset.maintenanceAction;
      if (button.getAttribute("aria-disabled") === "true") {
        showToast(button.title || "Action unavailable");
        return;
      }
      if (action === "doctor") await openDoctor();
      if (action === "repair-firewall") await repairFirewall();
      if (action === "backup") openBackup();
      if (action === "support-bundle") await downloadSupportBundle();
      if (action === "updates") await openUpdates();
    });
  });
}

async function repairFirewall() {
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
  await openDoctor();
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
  modal.innerHTML = `<div class="modal-body">${html}</div>`;

  modal.querySelectorAll("[data-close]").forEach((button) => {
    button.addEventListener("click", () => modal.close());
  });

  modal.showModal();

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
