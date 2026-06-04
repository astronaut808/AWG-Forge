// deno-lint-ignore-file no-unused-vars -- classic scripts share UI symbols across files loaded by index.html.
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
