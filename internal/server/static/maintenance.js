function openMaintenance(tab = maintenanceState.tab || "overview") {
  maintenanceState.tab = tab;
  const tabs = [
    ["overview", "Overview"],
    ["doctor", "Doctor"],
    ["firewall", "Firewall"],
    ["warp", "WARP"],
    ["backup", "Backup"],
    ["restore", "Restore"],
    ["updates", "Updates"],
    ["support", "Support"],
    ["logs", "Logs"],
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
  if (maintenanceState.tab === "warp") return renderMaintenanceWarp();
  if (maintenanceState.tab === "backup") return renderMaintenanceBackup();
  if (maintenanceState.tab === "restore") return renderMaintenanceRestore();
  if (maintenanceState.tab === "updates") return renderMaintenanceUpdates();
  if (maintenanceState.tab === "support") return renderMaintenanceSupport();
  if (maintenanceState.tab === "logs") return renderMaintenanceLogs();
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
      ${maintenanceOverviewCard("WARP", summary.warpBadge, summary.warpClass, [
        state?.warp?.configured ? `${state.warp.enabled_tunnel_count || 0} tunnel(s) via WARP` : "Not configured",
        state?.warp?.registered ? "Registered automatically" : (state?.warp?.endpoint || "Manual import"),
      ], "warp")}
      ${maintenanceOverviewCard("Recovery", "backup", "ok", [
        "Encrypted backup",
        "Restore dry-run verification",
      ], "backup")}
      ${maintenanceOverviewCard("Audit", summary.auditBadge, summary.auditClass, [
        `${summary.auditEvents} event(s) loaded`,
        maintenanceState.lastRun.logs ? `last refreshed ${maintenanceState.lastRun.logs}` : "manual refresh",
      ], "logs")}
    </div>
  `;
}

function renderMaintenanceWarp() {
  const warp = state?.warp || {};
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>WARP egress</h3>
        <p class="muted">Use automatic registration first. Manual import is only a fallback for configs generated outside awg-forge.</p>
      </div>
      <div class="actions">
        <button type="button" class="primary" data-maint-action="register-warp">${warp.configured ? "Re-register WARP" : "Register WARP"}</button>
        <button type="button" data-maint-action="restart-warp" ${warp.configured ? "" : "disabled"}>Restart WARP</button>
        <button type="button" class="danger" data-maint-action="delete-warp" ${warp.configured && Number(warp.enabled_tunnel_count || 0) === 0 ? "" : "disabled"}>Delete WARP</button>
      </div>
    </div>
    <section class="maintenance-card compact maintenance-warp-status">
      <div class="maintenance-card-head">
        <h3>Status</h3>
        <span class="badge ${warp.configured ? "ok" : "neutral"}">${warp.configured ? "configured" : "not configured"}</span>
      </div>
      <div class="maintenance-facts">
        <div><span>Registration</span><strong class="mono">${warp.registered ? "automatic" : "manual"}</strong></div>
        ${warp.client_id ? `<div><span>Client ID</span><strong class="mono">${escapeHTML(warp.client_id)}</strong></div>` : ""}
        <div><span>License</span><strong class="mono">${warp.license_set ? "set" : "not stored"}</strong></div>
        <div><span>Interface</span><strong class="mono">${escapeHTML(warp.interface_name || "warp0")}</strong></div>
        <div><span>Endpoint</span><strong class="mono">${escapeHTML(warp.endpoint || "-")}</strong></div>
        <div><span>Address</span><strong class="mono">${escapeHTML(warp.address_v4 || "-")}</strong></div>
        <div><span>Tunnels</span><strong class="mono">${Number(warp.enabled_tunnel_count || 0)} via WARP</strong></div>
      </div>
      ${warp.last_apply_error ? `<p class="bad-text">${escapeHTML(warp.last_apply_error)}</p>` : ""}
    </section>
    <details class="maintenance-details">
      <summary>Manual WARP config import</summary>
      <form id="maintenance-warp-import-form" class="form-grid single">
        <p class="muted">You normally do not need this. Use it only when you already have a Cloudflare WARP WireGuard/AmneziaWG config from another generator or WARP client tool.</p>
        <div><label>WARP WireGuard or AmneziaWG config</label><textarea name="config" rows="7" placeholder="[Interface]&#10;PrivateKey = ...&#10;Address = ...&#10;&#10;[Peer]&#10;PublicKey = ...&#10;Endpoint = ..."></textarea></div>
        <div class="form-actions"><button class="primary" type="submit">Import WARP config</button></div>
      </form>
    </details>
    <div class="notice">After registration or import, open tunnel settings and switch Egress from Server WAN to Cloudflare WARP. Existing client configs do not need to change.</div>
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

function renderMaintenanceLogs() {
  const events = maintenanceState.auditLog || [];
  return `
    <div class="maintenance-section-head">
      <div>
        <h3>Audit log</h3>
        <p class="muted">Recent safe operational events from the local audit log. Secrets and config-like values are redacted before storage.</p>
      </div>
      <button type="button" class="primary" data-maint-action="run-logs">${maintenanceState.loading.logs ? "Loading..." : "Refresh logs"}</button>
    </div>
    ${maintenanceState.lastRun.logs ? `<p class="muted">Last refresh: ${escapeHTML(maintenanceState.lastRun.logs)}</p>` : ""}
    ${events.length ? `
      <div class="client-list">
        ${events.map((event) => `
          <div class="client-row audit-row">
            <div>
              <strong>${escapeHTML(event.event || "event")}</strong>
              <span class="muted mono">${escapeHTML(formatAuditTime(event.time))}</span>
              ${event.message ? `<span class="muted">${escapeHTML(event.message)}</span>` : ""}
              ${event.error ? `<span class="muted bad-text">${escapeHTML(event.error)}</span>` : ""}
              ${event.fields && Object.keys(event.fields).length ? `<span class="muted mono">${escapeHTML(formatAuditFields(event.fields))}</span>` : ""}
            </div>
            <span class="badge ${auditBadgeClass(event.level)}">${escapeHTML(event.level || "info")}</span>
          </div>
        `).join("")}
      </div>
    ` : `<div class="empty-inline">Refresh logs to load the latest audit events.</div>`}
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
      if (action === "run-logs") await runMaintenanceLogs();
      if (action === "register-warp") await registerWarp();
      if (action === "restart-warp") await restartWarp();
      if (action === "delete-warp") await deleteWarp();
    });
  });

  modal.querySelector("#maintenance-backup-form")?.addEventListener("submit", submitMaintenanceBackup);
  modal.querySelector("#maintenance-restore-form")?.addEventListener("submit", submitMaintenanceRestoreVerify);
  modal.querySelector("#maintenance-warp-import-form")?.addEventListener("submit", submitMaintenanceWarpImport);
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
    warpBadge: state?.warp?.configured ? (state?.warp?.registered ? "registered" : "configured") : "manual",
    warpClass: state?.warp?.configured ? "ok" : "neutral",
    auditEvents: (maintenanceState.auditLog || []).length,
    auditBadge: maintenanceState.auditLog ? "loaded" : "manual",
    auditClass: "ok",
  };
}

function auditBadgeClass(level) {
  if (level === "error") return "bad";
  if (level === "warn") return "warn";
  return "ok";
}

function formatAuditTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatAuditFields(fields) {
  return Object.keys(fields).sort().map((key) => `${key}=${fields[key]}`).join(" · ");
}
