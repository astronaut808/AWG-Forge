// deno-lint-ignore-file no-unused-vars -- classic scripts share UI symbols across files loaded by index.html.
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
  globalThis.setSafeHTML(modal, `<div class="modal-body">${html}</div>`);

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
  return escapeHTML(value).replaceAll("`", "&#96;");
}
