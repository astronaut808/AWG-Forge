const app = document.querySelector("#app");
const modal = document.querySelector("#modal");
const toast = document.querySelector("#toast");

let state = null;
let activeProfile = localStorage.getItem("awg-forge.profile") || "awg_legacy_1_0";

const profileTitles = {
  awg_legacy_1_0: "AmneziaWG Legacy / 1.0",
  awg_1_5: "AmneziaWG 1.5",
  awg_2_0: "AmneziaWG 2.0",
};

init();

async function init() {
  await loadState();
}

async function loadState() {
  const res = await fetch("/api/state");
  if (res.status === 401) {
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
    <section class="panel login">
      <div class="brand">
        <h1>awg-forge</h1>
        <p>Secure admin access</p>
      </div>
      <form id="login-form" class="form-grid" style="margin-top:18px">
        <div>
          <label for="password">Password</label>
          <input id="password" name="password" type="password" autocomplete="current-password" autofocus>
        </div>
        <div class="form-actions">
          <button class="primary" type="submit">Log in</button>
        </div>
      </form>
    </section>
  `;
  document.querySelector("#login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const password = document.querySelector("#password").value;
    const res = await api("/api/login", { method: "POST", body: { password } });
    if (res.ok) await loadState();
  });
}

function renderApp() {
  const profiles = state.profiles || [];
  const active = profiles.find((profile) => profile.id === activeProfile) || profiles[0];
  activeProfile = active.id;
  localStorage.setItem("awg-forge.profile", activeProfile);
  const tunnels = state.tunnels.filter((tunnel) => tunnel.profile === activeProfile);
  app.innerHTML = `
    <header class="topbar">
      <div class="brand">
        <h1>awg-forge</h1>
        <p>${escapeHTML(state.server_host)} · ${state.tunnels.length} tunnel(s)</p>
      </div>
      <div class="toolbar">
        <button class="ghost" data-action="refresh">Refresh</button>
        <button class="ghost" data-action="logout">Log out</button>
      </div>
    </header>
    <nav class="tabs" aria-label="Protocol profiles">
      ${profiles.map((profile) => `
        <button class="tab ${profile.id === activeProfile ? "active" : ""}" data-profile="${profile.id}">
          <strong>${escapeHTML(profile.tab)}</strong>
          <span>${escapeHTML(profile.label)}</span>
        </button>
      `).join("")}
    </nav>
    <section class="panel">
      <div class="section-head">
        <div>
          <h2>${escapeHTML(profileTitles[activeProfile] || activeProfile)}</h2>
          <p class="muted">${active.available ? "Create and manage independent tunnels for this profile." : "This profile is reserved for the next protocol implementation."}</p>
        </div>
        <button class="primary" data-action="create-tunnel" ${active.available ? "" : "disabled"}>Create tunnel</button>
      </div>
      ${renderTunnels(active, tunnels)}
    </section>
  `;
  bindAppEvents(active, tunnels);
}

function renderTunnels(profile, tunnels) {
  if (!profile.available) {
    return `<div class="empty"><strong>2.0 is not enabled yet</strong><p class="muted">We will enable creation after exact upstream syntax, ranges, and golden tests are implemented.</p></div>`;
  }
  if (tunnels.length === 0) {
    return `<div class="empty"><strong>No tunnels for ${escapeHTML(profile.tab)} yet</strong><p class="muted">Create a tunnel first, then add clients inside it.</p><button class="primary" data-action="create-tunnel">Create tunnel</button></div>`;
  }
  return `<div class="grid">${tunnels.map(renderTunnelCard).join("")}</div>`;
}

function renderTunnelCard(tunnel) {
  const clients = tunnel.clients || [];
  const up = tunnel.status?.up;
  return `
    <article class="card">
      <div class="card-head">
        <div>
          <h3>${escapeHTML(tunnel.name)}</h3>
          <p class="muted">${escapeHTML(tunnel.interface)} · ${escapeHTML(tunnel.profile)}</p>
        </div>
        <span class="badge ${up ? "ok" : "bad"}">${up ? "up" : "down"}</span>
      </div>
      <div class="facts">
        <div class="fact"><span>Endpoint</span><strong>${escapeHTML(state.server_host)}:${tunnel.listen_port}</strong></div>
        <div class="fact"><span>Subnet</span><strong>${escapeHTML(tunnel.subnet)}</strong></div>
        <div class="fact"><span>DNS</span><strong>${escapeHTML(tunnel.dns)}</strong></div>
        <div class="fact"><span>MTU</span><strong>${formatMTU(tunnel.mtu)}</strong></div>
        <div class="fact"><span>Clients</span><strong>${clients.filter((client) => client.enabled).length}/${clients.length}</strong></div>
      </div>
      <div class="actions">
        <button class="primary" data-action="create-client" data-tunnel="${tunnel.id}">Create client</button>
        <button data-action="settings" data-tunnel="${tunnel.id}">Settings</button>
        <button data-action="protocol" data-tunnel="${tunnel.id}">Protocol</button>
        <button data-action="restart" data-tunnel="${tunnel.id}">Restart</button>
        <button class="danger" data-action="delete-tunnel" data-tunnel="${tunnel.id}">Delete</button>
      </div>
      <div class="client-list">
        ${clients.length ? clients.map((client) => renderClientRow(tunnel, client)).join("") : `<p class="muted">No clients yet.</p>`}
      </div>
      ${tunnel.status?.last_error ? `<p class="badge bad">${escapeHTML(tunnel.status.last_error)}</p>` : ""}
    </article>
  `;
}

function renderClientRow(tunnel, client) {
  return `
    <div class="client-row">
      <div>
        <strong>${escapeHTML(client.name)}</strong>
        <span class="muted">${escapeHTML(client.address)} · ${client.enabled ? "enabled" : "disabled"}</span>
      </div>
      <div class="actions">
        <a class="button" href="/clients/config/${client.id}">Config</a>
        <button data-action="qr" data-client="${client.id}">QR</button>
        <button data-action="${client.enabled ? "disable-client" : "enable-client"}" data-client="${client.id}">${client.enabled ? "Disable" : "Enable"}</button>
        <button class="danger" data-action="delete-client" data-client="${client.id}">Delete</button>
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
      const tunnel = findTunnel(node.dataset.tunnel);
      if (action === "refresh") await loadState();
      if (action === "logout") await logout();
      if (action === "create-tunnel") openCreateTunnel(active);
      if (action === "create-client") openCreateClient(tunnel);
      if (action === "settings") openSettings(tunnel);
      if (action === "protocol") openProtocol(tunnel);
      if (action === "restart") await restartTunnel(tunnel);
      if (action === "delete-tunnel") await deleteTunnel(tunnel);
      if (action === "qr") openQR(node.dataset.client);
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
        <button type="button" data-close>Close</button>
      </div>
      <div class="form-grid">
        <div><label>Name / interface</label><input name="name" value="${escapeAttr(suggestion.name)}"></div>
        <div><label>Listen port</label><input name="port" inputmode="numeric" value="${suggestion.port}"></div>
        <div><label>IPv4 subnet</label><input name="subnet" value="${escapeAttr(suggestion.subnet)}"></div>
      </div>
      <div class="form-actions"><button class="primary" type="submit">Create tunnel</button></div>
    </form>
  `;
  showModal(body);
  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const res = await api("/api/tunnels", {
      method: "POST",
      body: {
        profile: profile.id,
        name: form.get("name"),
        port: Number(form.get("port")),
        subnet: form.get("subnet"),
      },
    });
    if (res.ok) closeModalAndReload(profile.id);
  });
}

function nextTunnelSuggestion(profile) {
  const existing = state.tunnels.filter((tunnel) => tunnel.profile === profile.id);
  if (existing.length === 0) {
    return { name: profile.suggested_name, port: profile.suggested_port, subnet: profile.suggested_subnet };
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
  return `<option value="${value}" ${selected ? "selected" : ""}>${label}</option>`;
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
        <button type="button" data-close>Close</button>
      </div>
      <div><label>Client name</label><input name="name" autofocus></div>
      <div class="form-actions"><button class="primary" type="submit">Create client</button></div>
    </form>
  `;
  showModal(body);
  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const res = await api("/api/clients", { method: "POST", body: { tunnel_id: tunnel.id, name: form.get("name") } });
    if (res.ok) {
      const payload = await res.json();
      await loadState();
      openQR(payload.client.id);
    }
  });
}

function openSettings(tunnel) {
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Tunnel settings</h2><p class="muted">${escapeHTML(tunnel.profile)}</p></div>
        <button type="button" data-close>Close</button>
      </div>
      <div class="form-grid">
        <div><label>Name / interface</label><input name="name" value="${escapeAttr(tunnel.name)}"></div>
        <div><label>Listen port</label><input name="port" inputmode="numeric" value="${tunnel.listen_port}"></div>
        <div><label>IPv4 subnet</label><input name="subnet" value="${escapeAttr(tunnel.subnet)}"></div>
        <div><label>DNS</label><input name="dns" value="${escapeAttr(tunnel.dns)}"></div>
        <div><label>Allowed IPs</label><input name="allowed_ips" value="${escapeAttr(tunnel.allowed_ips)}"></div>
        <div><label>Persistent keepalive</label><input name="keepalive" inputmode="numeric" value="${tunnel.keepalive}"></div>
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
        <div><label>Custom MTU</label><input name="mtu_custom" inputmode="numeric" value="${isPresetMTU(tunnel.mtu) ? "" : tunnel.mtu}"></div>
        <div><label><input name="enabled" type="checkbox" ${tunnel.enabled ? "checked" : ""}> Enabled</label></div>
      </div>
      <div class="form-actions"><button class="primary" type="submit">Save settings</button></div>
    </form>
  `;
  showModal(body);
  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const res = await api(`/api/tunnels/${tunnel.id}/settings`, {
      method: "PATCH",
      body: {
        name: form.get("name"),
        port: Number(form.get("port")),
        subnet: form.get("subnet"),
        dns: form.get("dns"),
        allowed_ips: form.get("allowed_ips"),
        keepalive: Number(form.get("keepalive")),
        mtu: selectedMTU(form),
        enabled: form.get("enabled") === "on",
      },
    });
    if (res.ok) closeModalAndReload(tunnel.profile);
  });
}

function openProtocol(tunnel) {
  const params = tunnel.params || [];
  const body = `
    <form id="modal-form">
      <div class="modal-head">
        <div><h2>Protocol parameters</h2><p class="muted">${escapeHTML(tunnel.name)} · ${escapeHTML(tunnel.profile)}</p></div>
        <button type="button" data-close>Close</button>
      </div>
      <div class="form-grid">
        ${params.map(({ key, value }) => `
          <div>
            <label>${escapeHTML(key)}</label>
            ${key.startsWith("I") ? `<textarea name="${escapeAttr(key)}">${escapeHTML(value || "")}</textarea>` : `<input name="${escapeAttr(key)}" value="${escapeAttr(value || "")}">`}
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
  document.querySelector("[data-regenerate]").addEventListener("click", async () => {
    if (!confirm("Regenerate protocol parameters? Clients in this tunnel must import fresh configs.")) return;
    const res = await api(`/api/tunnels/${tunnel.id}/regenerate`, { method: "POST", body: { profile: tunnel.profile } });
    if (res.ok) closeModalAndReload(tunnel.profile);
  });
  document.querySelector("#modal-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const nextParams = {};
    for (const { key } of params) nextParams[key] = String(form.get(key) || "").trim();
    const res = await api(`/api/tunnels/${tunnel.id}/protocol`, { method: "PATCH", body: { profile: tunnel.profile, params: nextParams } });
    if (res.ok) closeModalAndReload(tunnel.profile);
  });
}

function openQR(clientID) {
  showModal(`
    <div class="modal-head">
      <div><h2>Client QR</h2><p class="muted">Scan or download the latest config.</p></div>
      <button type="button" data-close>Close</button>
    </div>
    <img class="qr" alt="Client QR code" src="/api/clients/${clientID}/qr.png?ts=${Date.now()}">
    <div class="form-actions">
      <a class="button primary" href="/clients/config/${clientID}">Download config</a>
    </div>
  `);
}

async function restartTunnel(tunnel) {
  const res = await api(`/api/tunnels/${tunnel.id}/restart`, { method: "POST", body: {} });
  if (res.ok) closeModalAndReload(tunnel.profile);
}

async function deleteTunnel(tunnel) {
  if (!confirm(`Delete tunnel ${tunnel.name} and all its clients?`)) return;
  const res = await api(`/api/tunnels/${tunnel.id}/delete`, { method: "DELETE" });
  if (res.ok) closeModalAndReload(tunnel.profile);
}

async function setClient(clientID, enabled) {
  const res = await api(`/api/clients/${clientID}/${enabled ? "enable" : "disable"}`, { method: "POST", body: {} });
  if (res.ok) await loadState();
}

async function deleteClient(clientID) {
  if (!confirm("Delete this client?")) return;
  const res = await api(`/api/clients/${clientID}/delete`, { method: "DELETE" });
  if (res.ok) await loadState();
}

async function logout() {
  await api("/api/logout", { method: "POST", body: {} });
  state = null;
  renderLogin();
}

async function api(url, options = {}) {
  const init = { method: options.method || "GET", headers: {} };
  if (options.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(options.body);
  }
  const res = await fetch(url, init);
  if (!res.ok) showToast(await errorText(res));
  return res;
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
  modal.querySelectorAll("[data-close]").forEach((button) => button.addEventListener("click", () => modal.close()));
  modal.showModal();
}

async function closeModalAndReload(profileID) {
  modal.close();
  if (profileID) activeProfile = profileID;
  await loadState();
}

function findTunnel(id) {
  return state.tunnels.find((tunnel) => tunnel.id === id);
}

function showToast(message) {
  toast.textContent = message;
  toast.classList.add("show");
  window.setTimeout(() => toast.classList.remove("show"), 3600);
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
