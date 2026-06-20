import { render } from "preact";
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type {
  AppState,
  AuditEvent,
  Client,
  DoctorResult,
  FirewallReport,
  Level,
  Profile,
  RestoreReport,
  Tunnel,
  TunnelHealth,
  UpdatesReport,
} from "./types";
import {
  activeLabel,
  classNames,
  dateTimeLocalToISO,
  dateTimeLocalValue,
  dateOnly,
  downloadConfig,
  downloadResponse,
  expirationValue,
  formatBytes,
  profileDescription,
  profileTitle,
  relativeTime,
} from "./utils";
import "./styles.css";

type Modal =
  | { kind: "create-tunnel"; profile: Profile }
  | { kind: "settings"; tunnel: Tunnel }
  | { kind: "protocol"; tunnel: Tunnel }
  | { kind: "create-client"; tunnel: Tunnel }
  | { kind: "client-settings"; tunnel: Tunnel; client: Client }
  | { kind: "health"; tunnel: Tunnel; health?: TunnelHealth }
  | { kind: "import-key"; client: Client; key: string; warning: string }
  | { kind: "maintenance" };

type MaintenanceTab = "overview" | "doctor" | "firewall" | "warp" | "backup" | "restore" | "updates" | "support" | "logs" | "system";

const profileKey = "awg-forge.profile";
const themeKey = "awg-forge.theme";

function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [activeProfile, setActiveProfile] = useState(localStorage.getItem(profileKey) || "awg_legacy_1_0");
  const [modal, setModal] = useState<Modal | null>(null);
  const [toast, setToast] = useState("");
  const [theme, setTheme] = useState(initialTheme);
  const liveUpdatesEnabled = state !== null;

  const load = useCallback(async (options: { quiet?: boolean } = {}) => {
    try {
      const next = await api.state();
      setState(next);
      setAuthChecked(true);
      if (!next.profiles.find((profile) => profile.id === activeProfile) && next.profiles[0]) {
        setActiveProfile(next.profiles[0].id);
      }
    } catch (err) {
      setAuthChecked(true);
      if (err instanceof api.APIError && err.status === 401) {
        setState(null);
        return;
      }
      if (!options.quiet) notify(errorMessage(err));
    }
  }, [activeProfile]);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(themeKey, theme);
  }, [theme]);

  useEffect(() => {
    initParallax();
    void load();
  }, [load]);

  useEffect(() => {
    if (!liveUpdatesEnabled) return undefined;
    let fallback = 0;
    const events = new EventSource("/api/events");
    events.addEventListener("state", (event) => {
      try {
        setState(JSON.parse((event as MessageEvent).data) as AppState);
      } catch {
        notify("live update failed");
      }
    });
    events.onerror = () => {
      events.close();
      fallback = globalThis.setInterval(() => void load({ quiet: true }), 5000);
    };
    return () => {
      events.close();
      if (fallback) globalThis.clearInterval(fallback);
    };
  }, [liveUpdatesEnabled, load]);

  const profiles = state?.profiles || [];
  const active = profiles.find((profile) => profile.id === activeProfile) || profiles[0];
  const allTunnels = state?.tunnels || [];
  const tunnels = active ? allTunnels.filter((tunnel) => tunnel.profile === active.id) : [];

  function selectProfile(id: string) {
    localStorage.setItem(profileKey, id);
    setActiveProfile(id);
  }

  function notify(message: string) {
    setToast(message);
    globalThis.setTimeout(() => setToast((current) => (current === message ? "" : current)), 3600);
  }

  async function runAction(label: string, fn: () => Promise<unknown>, options: { reload?: boolean; close?: boolean } = { reload: true }) {
    try {
      await fn();
      if (options.close !== false) setModal(null);
      if (options.reload !== false) await load({ quiet: true });
      notify(label);
    } catch (err) {
      const message = errorMessage(err);
      if (message.includes("apply failed")) {
        setModal(null);
        await load({ quiet: true });
      }
      notify(message);
    }
  }

  if (!authChecked) return <Splash />;
  if (!state) return <Login onLogin={() => load()} notify={notify} />;
  if (!active) return <Shell state={state} theme={theme} setTheme={setTheme} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}><Empty title="No profiles" text="Backend returned no protocol profiles." /></Shell>;

  return (
    <Shell state={state} theme={theme} setTheme={setTheme} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}>
      <ProfileTabs profiles={profiles} active={active.id} onSelect={selectProfile} />
      <section class="panel stack">
        <div class="section-head">
          <div>
            <h2>{profileTitle(active.id)}</h2>
            <p>{profileDescription(active.id)}</p>
          </div>
          <button class="button primary" type="button" disabled={!active.available} onClick={() => setModal({ kind: "create-tunnel", profile: active })}>Create tunnel</button>
        </div>
        {tunnels.length === 0 ? (
          <Empty title={`No tunnels for ${active.tab} yet`} text="Create a tunnel first, then add clients inside it." action={<button class="button primary" type="button" onClick={() => setModal({ kind: "create-tunnel", profile: active })}>Create tunnel</button>} />
        ) : (
          <div className={classNames("tunnel-grid", tunnels.length === 1 && "single")}>
            {tunnels.map((tunnel) => (
              <TunnelCard
                key={tunnel.id}
                tunnel={tunnel}
                onCreateClient={() => setModal({ kind: "create-client", tunnel })}
                onSettings={() => setModal({ kind: "settings", tunnel })}
                onProtocol={() => setModal({ kind: "protocol", tunnel })}
                onHealth={async () => {
                  setModal({ kind: "health", tunnel });
                  try {
                    const res = await api.tunnelHealth(tunnel.id);
                    setModal({ kind: "health", tunnel, health: res.health });
                  } catch (err) {
                    notify(errorMessage(err));
                  }
                }}
                onRestart={() => runAction("tunnel restarted", () => api.restartTunnel(tunnel.id))}
                onDelete={() => confirm(`Delete tunnel ${tunnel.name} and all its clients?`) && runAction("tunnel deleted", () => api.deleteTunnel(tunnel.id))}
                onClientConfig={downloadConfig}
                onClientKey={async (client) => {
                  try {
                    const res = await api.clientImportKey(client.id);
                    setModal({ kind: "import-key", client, key: res.import_key, warning: res.warning });
                  } catch (err) {
                    notify(errorMessage(err));
                  }
                }}
                onClientSettings={(client) => setModal({ kind: "client-settings", tunnel, client })}
                onClientToggle={(client) => runAction(client.enabled ? "client disabled" : "client enabled", () => api.setClientEnabled(client.id, !client.enabled))}
                onClientDelete={(client) => confirm(`Delete client ${client.name}?`) && runAction("client deleted", () => api.deleteClient(client.id))}
              />
            ))}
          </div>
        )}
      </section>
      {modal && (
        <Dialog onClose={() => setModal(null)}>
          <ModalContent modal={modal} state={state} notify={notify} close={() => setModal(null)} reload={() => load({ quiet: true })} runAction={runAction} />
        </Dialog>
      )}
      <Toast message={toast} />
    </Shell>
  );
}

function Login({ onLogin, notify }: { onLogin: () => Promise<void>; notify: (message: string) => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <main class="login-shell">
      <section class="panel login-card">
        <Brand />
        <form
          class="form single"
          onSubmit={async (event) => {
            event.preventDefault();
            setBusy(true);
            try {
              await api.login(password);
              await onLogin();
            } catch (err) {
              notify(errorMessage(err));
            } finally {
              setBusy(false);
            }
          }}
        >
          <label>Password<input aria-label="Password" type="password" autocomplete="current-password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label>
          <button class="button primary wide" disabled={busy} type="submit">{busy ? "Logging in..." : "Log in"}</button>
        </form>
      </section>
    </main>
  );
}

type ShellProps = {
  state: AppState;
  theme: string;
  setTheme: (theme: string) => void;
  logout: () => void;
  openMaintenance: () => void;
  children: preact.ComponentChildren;
};

function Shell(props: ShellProps) {
  const { state, theme, setTheme, logout, openMaintenance, children } = props;
  return (
    <main class="app-shell">
      <header class="topbar panel">
        <Brand subtitle={<><span class="mono">{state.server_host}</span> · {state.tunnels.length} tunnel(s)</>} />
        <nav class="toolbar" aria-label="Global actions">
          <button class="button icon" type="button" title="Toggle theme" aria-label="Toggle theme" onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>{theme === "dark" ? "☼" : "☾"}</button>
          <button class="button" type="button" onClick={openMaintenance}>Maintenance</button>
          <button class="button" type="button" onClick={logout}>Log out</button>
        </nav>
      </header>
      {children}
      <FooterLinks version={state.build?.version || "dev"} />
    </main>
  );
}

function Brand({ subtitle }: { subtitle?: preact.ComponentChildren }) {
  return (
    <div class="brand">
      <span class="brand-mark" aria-hidden="true">
        <svg viewBox="0 0 32 32"><path d="M16 3.5 25 7v7.2c0 5.9-3.6 11.2-9 14.1-5.4-2.9-9-8.2-9-14.1V7l9-3.5Z" /><path d="M11 17.2h10M11 12.6h10M13.7 21.8h4.6" /></svg>
      </span>
      <div>
        <h1><span>awg-forge</span><a href="https://github.com/astronaut808" target="_blank" rel="noreferrer">by <strong>astronaut808</strong></a></h1>
        {subtitle && <p>{subtitle}</p>}
      </div>
    </div>
  );
}

function FooterLinks({ version }: { version: string }) {
  const label = versionLabel(version);
  return (
    <footer class="app-footer" aria-label="Project links">
      <a class="footer-link icon" href="https://github.com/astronaut808/awg-forge" target="_blank" rel="noreferrer" aria-label="awg-forge on GitHub">
        <svg aria-hidden="true" viewBox="0 0 24 24">
          <path d="M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.69c-2.78.6-3.37-1.18-3.37-1.18-.45-1.15-1.11-1.46-1.11-1.46-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.89 1.52 2.34 1.08 2.91.82.09-.65.35-1.08.63-1.33-2.22-.25-4.56-1.11-4.56-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.64 0 0 .84-.27 2.75 1.02A9.57 9.57 0 0 1 12 7.01c.85 0 1.71.11 2.51.34 1.91-1.29 2.75-1.02 2.75-1.02.55 1.37.2 2.39.1 2.64.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.86v2.56c0 .27.18.58.69.48A10 10 0 0 0 12 2Z" />
        </svg>
      </a>
      <a class="footer-link" href="https://github.com/astronaut808/awg-forge/tree/master/docs/ru" target="_blank" rel="noreferrer">Docs</a>
      <span class="footer-version" title="awg-forge version">{label}</span>
    </footer>
  );
}

function ProfileTabs({ profiles, active, onSelect }: { profiles: Profile[]; active: string; onSelect: (id: string) => void }) {
  return (
    <nav class="tabs panel" aria-label="Protocol profiles">
      {profiles.map((profile) => (
        <button key={profile.id} className={classNames("tab", profile.id === active && "active")} type="button" onClick={() => onSelect(profile.id)}>
          <strong>{profile.tab}</strong>
          <span>{profile.label}</span>
        </button>
      ))}
    </nav>
  );
}

function TunnelCard(props: {
  tunnel: Tunnel;
  onCreateClient: () => void;
  onSettings: () => void;
  onProtocol: () => void;
  onHealth: () => void;
  onRestart: () => void;
  onDelete: () => void;
  onClientConfig: (id: string) => void;
  onClientKey: (client: Client) => void;
  onClientSettings: (client: Client) => void;
  onClientToggle: (client: Client) => void;
  onClientDelete: (client: Client) => void;
}) {
  const { tunnel } = props;
  const stale = Number(tunnel.status?.stale_clients || 0);
  return (
    <article class="tunnel-card panel">
      <div class="tunnel-head">
        <div>
          <h3>{tunnel.name}</h3>
          <p>{tunnel.interface} · {profileTitle(tunnel.profile)}</p>
        </div>
        <Badge tone={tunnel.status?.up ? "ok" : "bad"}>{tunnel.status?.up ? "up" : "down"}</Badge>
      </div>
      <div class="facts">
        <Fact label="Endpoint" value={`${tunnel.server_host}:${tunnel.listen_port}`} extra={tunnel.egress_mode === "warp" ? "WARP" : ""} />
        <Fact label="Subnet" value={tunnel.subnet} />
        <Fact label="DNS" value={tunnel.dns} />
        <Fact label="MTU" value={Number(tunnel.mtu) > 0 ? String(tunnel.mtu) : "Auto"} />
        <Fact label="Clients" value={`${tunnel.clients.filter((client) => client.enabled).length}/${tunnel.clients.length}`} />
      </div>
      <div class="badges">
        {tunnel.status?.firewall?.label && <Badge tone={toneFromLevel(tunnel.status.firewall.level)} title={tunnel.status.firewall.message}>{tunnel.status.firewall.label}</Badge>}
        {stale > 0 && <Badge tone="warn">{stale} stale config</Badge>}
        {tunnel.egress_mode === "warp" && <Badge tone="brand">WARP egress</Badge>}
      </div>
      <div class="actions">
        <button class="button primary" type="button" onClick={props.onCreateClient}>Create client</button>
        <button class="button" type="button" onClick={props.onSettings}>Settings</button>
        <button class="button" type="button" onClick={props.onProtocol}>Protocol</button>
        <button class="button" type="button" onClick={props.onHealth}>Health</button>
        <button class="button" type="button" onClick={props.onRestart}>Restart</button>
        <button class="button danger" type="button" onClick={props.onDelete}>Delete</button>
      </div>
      <section class="clients">
        <div class="list-head"><h4>Clients</h4><span>{tunnel.clients.length ? `${tunnel.clients.length} total` : "No clients yet"}</span></div>
        {tunnel.clients.length === 0 ? <div class="empty compact">Create the first client for this tunnel.</div> : tunnel.clients.map((client) => (
          <ClientRow
            key={client.id}
            client={client}
            onConfig={() => props.onClientConfig(client.id)}
            onKey={() => props.onClientKey(client)}
            onSettings={() => props.onClientSettings(client)}
            onToggle={() => props.onClientToggle(client)}
            onDelete={() => props.onClientDelete(client)}
          />
        ))}
      </section>
    </article>
  );
}

function ClientRow({ client, onConfig, onKey, onSettings, onToggle, onDelete }: { client: Client; onConfig: () => void; onKey: () => void; onSettings: () => void; onToggle: () => void; onDelete: () => void }) {
  const lastSeen = client.runtime?.last_seen_at || client.last_seen_at;
  const status = client.expired ? "expired" : activeLabel(lastSeen);
  return (
    <div className={classNames("client-row", !client.active && "dimmed")}>
      <div class="client-main">
        <div class="client-title">
          <span class="status-dot" />
          <strong>{client.name}</strong>
          <Badge tone={client.active ? "ok" : "neutral"}>{client.expired ? "expired" : client.enabled ? "enabled" : "disabled"}</Badge>
          <Badge tone={status === "active now" ? "ok" : status === "seen recently" ? "neutral" : "muted"}>{status}</Badge>
          {client.needs_new_config && <Badge tone="warn">stale</Badge>}
        </div>
        <p class="mono">{client.address}</p>
        <p>
          last seen {relativeTime(lastSeen)}
          {client.runtime?.present ? <> · received {formatBytes(client.runtime.rx_bytes)} · sent {formatBytes(client.runtime.tx_bytes)}</> : null}
          {client.expires_at ? <> · expires {dateOnly(client.expires_at)}</> : null}
        </p>
        {client.notes && <p class="note">{client.notes}</p>}
      </div>
      <div class="actions">
        <button class="button" type="button" onClick={onConfig}>Config</button>
        <button class="button" type="button" onClick={onKey}>Import key</button>
        <button class="button" type="button" onClick={onSettings}>Edit</button>
        <button class="button" type="button" onClick={onToggle}>{client.enabled ? "Disable" : "Enable"}</button>
        <button class="button danger" type="button" onClick={onDelete}>Delete</button>
      </div>
    </div>
  );
}

function Fact({ label, value, extra }: { label: string; value: string; extra?: string }) {
  return <div class="fact"><span>{label}</span><strong class="mono">{value}</strong>{extra && <em>{extra}</em>}</div>;
}

function ModalContent({ modal, state, notify, close, reload, runAction }: {
  modal: Modal;
  state: AppState;
  notify: (message: string) => void;
  close: () => void;
  reload: () => Promise<void>;
  runAction: (label: string, fn: () => Promise<unknown>, options?: { reload?: boolean; close?: boolean }) => Promise<void>;
}) {
  if (modal.kind === "create-tunnel") return <CreateTunnelForm state={state} profile={modal.profile} runAction={runAction} />;
  if (modal.kind === "settings") return <TunnelSettingsForm state={state} tunnel={modal.tunnel} runAction={runAction} />;
  if (modal.kind === "protocol") return <ProtocolForm tunnel={modal.tunnel} runAction={runAction} />;
  if (modal.kind === "create-client") return <CreateClientForm tunnel={modal.tunnel} notify={notify} runAction={runAction} />;
  if (modal.kind === "client-settings") return <ClientSettingsForm client={modal.client} runAction={runAction} />;
  if (modal.kind === "health") return <HealthPanel tunnel={modal.tunnel} health={modal.health} />;
  if (modal.kind === "import-key") return <ImportKeyPanel modal={modal} notify={notify} />;
  return <MaintenanceCenter state={state} notify={notify} close={close} reload={reload} />;
}

function CreateTunnelForm({ state, profile, runAction }: { state: AppState; profile: Profile; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  return <Form title={`Create ${profile.tab} tunnel`} subtitle="Each tunnel has its own interface, port, subnet, keys, and clients." submit="Create tunnel" onSubmit={(form) => runAction("tunnel created", () => api.createTunnel({ profile: profile.id, name: field(form, "name"), port: Number(field(form, "port")), subnet: field(form, "subnet"), egress_mode: field(form, "egress_mode") }))}>
    <label>Name / interface<input aria-label="Name / interface" name="name" defaultValue={profile.suggested_name || "awg0"} /></label>
    <label>Listen port<input aria-label="Listen port" name="port" inputMode="numeric" defaultValue={profile.suggested_port || 51820} /></label>
    <label>IPv4 subnet<input aria-label="IPv4 subnet" name="subnet" defaultValue={profile.suggested_subnet || "10.8.0.0/24"} /></label>
    <label>Egress<select aria-label="Egress" name="egress_mode" defaultValue="wan"><option value="wan">Server WAN</option><option value="warp">Cloudflare WARP</option></select></label>
    {!state.warp?.configured && <small class="form-note">WARP will be registered automatically if Cloudflare WARP is selected.</small>}
  </Form>;
}

function TunnelSettingsForm({ state, tunnel, runAction }: { state: AppState; tunnel: Tunnel; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  return <Form title="Tunnel settings" subtitle={`${tunnel.name} · ${profileTitle(tunnel.profile)}`} submit="Save settings" onSubmit={(form) => runAction("settings saved", () => api.updateTunnel(tunnel.id, {
    name: field(form, "name"),
    server_host: field(form, "server_host"),
    egress_mode: field(form, "egress_mode"),
    port: Number(field(form, "port")),
    subnet: field(form, "subnet"),
    dns: field(form, "dns"),
    allowed_ips: field(form, "allowed_ips"),
    keepalive: Number(field(form, "keepalive")),
    mtu: mtuValue(form),
    enabled: (form.elements.namedItem("enabled") as HTMLInputElement)?.checked || false,
  }))}>
    <label>Name / interface<input aria-label="Name / interface" name="name" defaultValue={tunnel.name} /></label>
    <label>Server host<input aria-label="Server host" name="server_host" defaultValue={tunnel.server_host || ""} placeholder={state.server_host} /></label>
    <label>Egress<select aria-label="Egress" name="egress_mode" defaultValue={tunnel.egress_mode || "wan"}><option value="wan">Server WAN</option><option value="warp">Cloudflare WARP</option></select></label>
    <label>Listen port<input aria-label="Listen port" name="port" inputMode="numeric" defaultValue={tunnel.listen_port} /></label>
    {!state.warp?.configured && <small class="form-note">WARP will be registered automatically if Cloudflare WARP is selected.</small>}
    <label>IPv4 subnet<input aria-label="IPv4 subnet" name="subnet" defaultValue={tunnel.subnet} /></label>
    <label>DNS<input aria-label="DNS" name="dns" defaultValue={tunnel.dns} /></label>
    <label>Allowed IPs<input aria-label="Allowed IPs" name="allowed_ips" defaultValue={tunnel.allowed_ips} /></label>
    <label>Persistent keepalive<input aria-label="Persistent keepalive" name="keepalive" inputMode="numeric" defaultValue={tunnel.keepalive} /></label>
    <MTUField value={tunnel.mtu || 0} />
    <label class="toggle-row full">
      <span><strong>Enabled</strong><small>Render this tunnel and keep it available for clients.</small></span>
      <input aria-label="Enabled" name="enabled" type="checkbox" defaultChecked={tunnel.enabled} />
    </label>
  </Form>;
}

function ProtocolForm({ tunnel, runAction }: { tunnel: Tunnel; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  return <Form title="Protocol parameters" subtitle={`${tunnel.name} · ${tunnel.profile}`} submit="Save protocol" secondary={<button class="button" type="button" onClick={() => confirm("Regenerate protocol parameters? Clients must import fresh configs.") && void runAction("protocol regenerated", () => api.regenerateProtocol(tunnel.id, tunnel.profile))}>Regenerate</button>} onSubmit={(form) => {
    const params: Record<string, string> = {};
    for (const item of tunnel.params) params[item.key] = field(form, item.key).trim();
    return runAction("protocol saved", () => api.updateProtocol(tunnel.id, tunnel.profile, params));
  }}>
    {tunnel.params.map((item) => item.key.startsWith("I") ? (
      <label key={item.key}>{item.key}<textarea aria-label={item.key} name={item.key} defaultValue={item.value || ""} /></label>
    ) : (
      <label key={item.key}>{item.key}<input aria-label={item.key} name={item.key} defaultValue={item.value || ""} /></label>
    ))}
  </Form>;
}

function CreateClientForm({ tunnel, notify, runAction }: { tunnel: Tunnel; notify: (message: string) => void; runAction: (label: string, fn: () => Promise<unknown>, options?: { reload?: boolean; close?: boolean }) => Promise<void> }) {
  return <Form title="Create client" subtitle={`${tunnel.name} · ${tunnel.profile}`} submit="Create client" onSubmit={(form) => runAction("client created", async () => {
    const res = await api.createClient(tunnel.id, field(form, "name"), expirationFromForm(form));
    if (res.client?.id) downloadConfig(res.client.id);
    notify("client config download started");
  })}>
    <label>Client name<input aria-label="Client name" name="name" /></label>
    <ExpirationField />
  </Form>;
}

function ClientSettingsForm({ client, runAction }: { client: Client; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  return <Form title="Client settings" subtitle={`${client.name} · ${client.address}`} submit="Save client" onSubmit={(form) => runAction("client saved", () => api.updateClient(client.id, { name: field(form, "name"), notes: field(form, "notes"), expires_at: expirationFromForm(form, client.expires_at) }))}>
    <label>Client name<input aria-label="Client name" name="name" defaultValue={client.name} /></label>
    <ExpirationField current={client.expires_at} keepCurrent />
    <label class="full">Notes<textarea aria-label="Notes" name="notes" maxLength={1000} defaultValue={client.notes || ""} /></label>
  </Form>;
}

function MTUField({ value }: { value: number }) {
  const normalized = Number(value || 0);
  const preset = normalized === 0 || normalized === 1280 || normalized === 1420 ? String(normalized) : "custom";
  const [mode, setMode] = useState(preset);
  return <>
    <label>MTU<select aria-label="MTU" name="mtu_mode" value={mode} onInput={(event) => setMode((event.currentTarget as HTMLSelectElement).value)}>
      <option value="0">Auto</option>
      <option value="1280">1280</option>
      <option value="1420">1420</option>
      <option value="custom">Custom</option>
    </select></label>
    {mode === "custom" && <label>Custom MTU<input aria-label="Custom MTU" name="mtu_custom" inputMode="numeric" defaultValue={normalized || 1280} /></label>}
  </>;
}

function ExpirationField({ current, keepCurrent = false }: { current?: string; keepCurrent?: boolean }) {
  const [mode, setMode] = useState(keepCurrent ? "keep" : "");
  return <>
    <label>Expiration<select aria-label="Expiration" name="expires_mode" value={mode} onInput={(event) => setMode((event.currentTarget as HTMLSelectElement).value)}>
      {keepCurrent && <option value="keep">{current ? `Keep current (${dateOnly(current)})` : "Keep current"}</option>}
      <option value="">Never expires</option>
      <option value="1h">1 hour</option>
      <option value="1d">1 day</option>
      <option value="7d">7 days</option>
      <option value="30d">30 days</option>
      <option value="custom">Custom date</option>
    </select></label>
    {mode === "custom" && <label>Custom expiration<input aria-label="Custom expiration" name="expires_custom" type="datetime-local" min={dateTimeLocalValue()} defaultValue={dateTimeLocalValue(current || expirationValue("1d"))} /></label>}
  </>;
}

function HealthPanel({ tunnel, health }: { tunnel: Tunnel; health?: TunnelHealth }) {
  return <PanelTitle title="Clients health" subtitle={health ? `${health.name || tunnel.name} · ${health.sample_seconds} second sample` : `${tunnel.name} · sampling traffic for 2 seconds...`}>
    {!health ? <p>Reading runtime handshakes and transfer counters.</p> : (
      <div class="list">
        {health.warnings?.map((warning) => <div class="notice" key={warning}>{warning}</div>)}
        {health.clients.length ? health.clients.map((client) => (
          <div class="client-row" key={client.id}>
            <div><strong>{client.name}</strong><p class="mono">{client.address}</p><p>{client.status}{client.latest_handshake ? ` · handshake ${client.latest_handshake}` : ""}</p>{client.warning && <p>{client.warning}</p>}</div>
            <div class="actions"><Badge tone={healthTone(client.status)}>received +{formatBytes(client.rx_delta_bytes)}</Badge><Badge tone={healthTone(client.status)}>sent +{formatBytes(client.tx_delta_bytes)}</Badge></div>
          </div>
        )) : <Empty title="No clients" text="This tunnel has no clients yet." />}
      </div>
    )}
  </PanelTitle>;
}

function ImportKeyPanel({ modal, notify }: { modal: Extract<Modal, { kind: "import-key" }>; notify: (message: string) => void }) {
  async function copyImportKey() {
    try {
      await navigator.clipboard.writeText(modal.key);
      notify("import key copied");
    } catch {
      notify("clipboard is unavailable; select and copy the key manually");
    }
  }

  return <PanelTitle title="Experimental import key" subtitle={`${modal.client.name} · AmneziaVPN / DefaultVPN only`}>
    <textarea class="mono import-key" aria-label="Import key" readOnly value={modal.key} />
    <p>{modal.warning}</p>
    <div class="actions"><button class="button primary" type="button" onClick={copyImportKey}>Copy key</button><button class="button" type="button" onClick={() => downloadConfig(modal.client.id)}>Download .conf</button></div>
  </PanelTitle>;
}

function MaintenanceCenter({ state, notify, reload }: { state: AppState; notify: (message: string) => void; close: () => void; reload: () => Promise<void> }) {
  const [tab, setTab] = useState<MaintenanceTab>("overview");
  const [doctorResults, setDoctorResults] = useState<DoctorResult[] | null>(null);
  const [firewall, setFirewall] = useState<FirewallReport | null>(null);
  const [updateReport, setUpdateReport] = useState<UpdatesReport | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [restore, setRestore] = useState<RestoreReport | null>(null);
  const [busyAction, setBusyAction] = useState("");
  const notifyRef = useRef(notify);

  useEffect(() => {
    notifyRef.current = notify;
  }, [notify]);

  useEffect(() => {
    let closed = false;

    async function loadAuditLog(quiet: boolean) {
      try {
        const res = await api.auditLog();
        if (!closed) setEvents(sortNewestFirst(res.events));
      } catch (err) {
        if (!quiet && !closed) notifyRef.current(errorMessage(err));
      }
    }

    void loadAuditLog(true);
    const timer = globalThis.setInterval(() => void loadAuditLog(true), 3500);
    return () => {
      closed = true;
      globalThis.clearInterval(timer);
    };
  }, []);

  async function action(key: string, label: string, fn: () => Promise<void>) {
    if (busyAction) return;
    setBusyAction(key);
    try {
      await fn();
      notify(label);
      await reload();
    } catch (err) {
      notify(errorMessage(err));
    } finally {
      setBusyAction("");
    }
  }

  return <PanelTitle title="Maintenance" subtitle="Operations center for diagnostics, firewall, backups, restore checks, updates, support, logs, and WARP.">
    <nav class="subtabs">{(["overview", "doctor", "firewall", "warp", "backup", "restore", "updates", "support", "logs", "system"] as MaintenanceTab[]).map((item) => <button key={item} className={classNames("button", tab === item && "active")} type="button" onClick={() => setTab(item)}>{item}</button>)}</nav>
    {tab === "overview" && <div class="metric-grid"><Metric title="Tunnels" value={String(state.tunnels.length)} /><Metric title="WARP" value={state.warp.configured ? `${state.warp.enabled_tunnel_count} enabled` : "not configured"} /><Metric title="Apply" value={state.apply_enabled ? "enabled" : "manual"} /><Metric title="Profiles" value={String(state.profiles.length)} /></div>}
    {tab === "doctor" && <div class="stack"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("doctor", "doctor completed", async () => setDoctorResults((await api.doctor()).results))}><ButtonContent busy={busyAction === "doctor"}>Run doctor</ButtonContent></button><ResultList results={doctorResults} /></div>}
    {tab === "firewall" && <div class="stack"><button class="button primary" disabled={!state.apply_enabled || Boolean(busyAction)} type="button" onClick={() => action("firewall", "firewall repaired", async () => setFirewall((await api.firewallRepair()).firewall))}><ButtonContent busy={busyAction === "firewall"}>Repair firewall</ButtonContent></button>{firewall ? <ResultList results={firewall.results.map((item) => ({ level: item.status === "ok" ? "ok" : item.status === "duplicate" ? "warn" : "fail", area: `${item.tunnel}/${item.name}`, message: item.message || item.rule }))} /> : <p>Repair reconciles only awg-forge managed rules.</p>}</div>}
    {tab === "warp" && <WarpPanel state={state} action={action} busyAction={busyAction} />}
    {tab === "backup" && <BackupPanel notify={notify} />}
    {tab === "restore" && <RestorePanel report={restore} setReport={setRestore} notify={notify} />}
    {tab === "updates" && <div class="stack"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("updates", "updates checked", async () => setUpdateReport((await api.updates()).updates))}><ButtonContent busy={busyAction === "updates"}>Check updates</ButtonContent></button>{updateReport && <ResultList results={updateReport.components.map((item) => ({ level: item.status === "current" ? "ok" : "warn", area: item.name, message: `${item.current_ref} → ${item.latest_ref}` }))} />}</div>}
    {tab === "support" && <div class="stack"><p>Create a sanitized support bundle without private keys or client configs.</p><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("support", "support bundle download started", async () => { const res = await fetch("/api/support-bundle"); if (!res.ok) throw new Error("support bundle failed"); await downloadResponse(res, "awg-forge-support.zip"); })}><ButtonContent busy={busyAction === "support"}>Download support bundle</ButtonContent></button></div>}
    {tab === "logs" && <div class="stack"><p class="note">Audit log updates automatically while Maintenance is open.</p><div class="list">{events.length === 0 ? <div class="empty compact">No audit events yet.</div> : events.map((event) => <div class="row" key={`${event.time}-${event.event}`}><strong>{event.event}</strong><p>{event.time} · {event.level} · {event.message}{event.error ? ` · ${event.error}` : ""}</p></div>)}</div></div>}
    {tab === "system" && <pre class="command-block">{`docker exec awg-forge awg-forge doctor
docker exec awg-forge awg-forge firewall repair
docker compose logs -f`}</pre>}
  </PanelTitle>;
}

function WarpPanel({ state, action, busyAction }: { state: AppState; action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  return <div class="stack">
    <div class="metric-grid">
      <Metric title="Registration" value={state.warp.registered ? "automatic" : state.warp.configured ? "manual" : "none"} />
      <Metric title="Interface" value={state.warp.interface_name || "warp0"} />
      <Metric title="Endpoint" value={state.warp.endpoint || "-"} />
      <Metric title="Tunnels" value={`${state.warp.enabled_tunnel_count || 0} via WARP`} />
    </div>
    <div class="actions"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("warp-register", state.warp.configured ? "WARP re-registered" : "WARP registered", async () => { await api.registerWarp(); })}><ButtonContent busy={busyAction === "warp-register"}>{state.warp.configured ? "Re-register WARP" : "Register WARP"}</ButtonContent></button><button class="button" disabled={!state.warp.configured || Boolean(busyAction)} type="button" onClick={() => action("warp-restart", "WARP restarted", async () => { await api.restartWarp(); })}><ButtonContent busy={busyAction === "warp-restart"}>Restart WARP</ButtonContent></button><button class="button danger" disabled={!state.warp.configured || Number(state.warp.enabled_tunnel_count || 0) > 0 || Boolean(busyAction)} type="button" onClick={() => confirm("Delete WARP config?") && action("warp-delete", "WARP deleted", async () => { await api.deleteWarp(); })}><ButtonContent busy={busyAction === "warp-delete"}>Delete WARP</ButtonContent></button></div>
    <details><summary>Manual WARP config import</summary><WarpImport action={action} busyAction={busyAction} /></details>
  </div>;
}

function WarpImport({ action, busyAction }: { action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const [value, setValue] = useState("");
  return <form class="form single" onSubmit={(event) => { event.preventDefault(); void action("warp-import", "WARP imported", async () => { await api.importWarp(value); }); }}><label>WARP WireGuard config<textarea aria-label="WARP WireGuard config" value={value} onInput={(event) => setValue((event.currentTarget as HTMLTextAreaElement).value)} placeholder="[Interface]&#10;PrivateKey = ..." /></label><button class="button primary" disabled={Boolean(busyAction)} type="submit"><ButtonContent busy={busyAction === "warp-import"}>Import WARP config</ButtonContent></button></form>;
}

function BackupPanel({ notify }: { notify: (message: string) => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return <form class="form single" onSubmit={async (event) => { event.preventDefault(); setBusy(true); try { const res = await api.backup(password); await downloadResponse(res, "awg-forge-backup.afbackup"); notify("backup download started"); } catch (err) { notify(errorMessage(err)); } finally { setBusy(false); } }}><label>Backup password<input aria-label="Backup password" type="password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label><button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>Create encrypted backup</ButtonContent></button></form>;
}

function RestorePanel({ report, setReport, notify }: { report: RestoreReport | null; setReport: (report: RestoreReport | null) => void; notify: (message: string) => void }) {
  const file = useRef<File | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return <div class="stack"><form class="form single" onSubmit={async (event) => { event.preventDefault(); if (!file.current) { notify("backup file is required"); return; } setBusy(true); try { setReport((await api.restoreVerify(file.current, password)).report); notify("backup verified"); } catch (err) { notify(errorMessage(err)); } finally { setBusy(false); } }}><label>Backup file<input aria-label="Backup file" type="file" accept=".afbackup,application/octet-stream" onInput={(event) => { file.current = (event.currentTarget as HTMLInputElement).files?.[0] || null; }} /></label><label>Password<input aria-label="Password" type="password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label><button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>Verify backup</ButtonContent></button></form>{report && <div class="metric-grid"><Metric title="Format" value={report.format} /><Metric title="Schema" value={String(report.schema)} /><Metric title="Tunnels" value={String(report.tunnels.length)} /><Metric title="Clients" value={String(report.client_count)} /></div>}</div>;
}

function Form({ title, subtitle, submit, secondary, onSubmit, children }: { title: string; subtitle: string; submit: string; secondary?: preact.ComponentChildren; onSubmit: (form: HTMLFormElement) => Promise<void>; children: preact.ComponentChildren }) {
  const [busy, setBusy] = useState(false);
  return <PanelTitle title={title} subtitle={subtitle}><form class="form" onSubmit={async (event) => { event.preventDefault(); setBusy(true); try { await onSubmit(event.currentTarget as HTMLFormElement); } finally { setBusy(false); } }}>{children}<div class="form-actions">{secondary}<button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{submit}</ButtonContent></button></div></form></PanelTitle>;
}

function Dialog({ onClose, children }: { onClose: () => void; children: preact.ComponentChildren }) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const previousFocus = useRef<Element | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    previousFocus.current = document.activeElement;
    if (dialog && !dialog.open) {
      try {
        dialog.showModal();
      } catch {
        dialog.setAttribute("open", "");
      }
    }

    return () => {
      const target = previousFocus.current;
      if (target instanceof HTMLElement) target.focus();
    };
  }, []);

  return (
    <div class="dialog-backdrop" role="presentation">
      <dialog class="dialog" ref={dialogRef} aria-modal="true" onCancel={(event) => { event.preventDefault(); onClose(); }}>
        <button class="button icon close" type="button" onClick={onClose} aria-label="Close">×</button>
        {children}
      </dialog>
    </div>
  );
}

function PanelTitle({ title, subtitle, children }: { title: string; subtitle: string; children: preact.ComponentChildren }) {
  return <div class="stack"><div class="modal-head"><div><h2>{title}</h2><p>{subtitle}</p></div></div>{children}</div>;
}

function ResultList({ results }: { results: Array<{ level: Level; area: string; message: string }> | null }) {
  if (!results) return <p>No results yet.</p>;
  return <div class="list">{results.map((item) => <div class="row" key={`${item.area}-${item.message}`}><div><strong>{item.area}</strong><p>{item.message}</p></div><Badge tone={toneFromLevel(item.level)}>{item.level}</Badge></div>)}</div>;
}

function Empty({ title, text, action }: { title: string; text: string; action?: preact.ComponentChildren }) {
  return <div class="empty"><strong>{title}</strong><p>{text}</p>{action}</div>;
}

function Metric({ title, value }: { title: string; value: string }) {
  return <div class="fact"><span>{title}</span><strong class="mono">{value}</strong></div>;
}

function Badge({ tone, title, children }: { tone: string; title?: string; children: preact.ComponentChildren }) {
  return <span className={`badge ${tone}`} title={title}>{children}</span>;
}

function ButtonContent({ busy, children }: { busy: boolean; children: preact.ComponentChildren }) {
  return <>{busy && <span class="spinner" aria-hidden="true" />}<span>{busy ? "Working..." : children}</span></>;
}

function Toast({ message }: { message: string }) {
  return <div className={classNames("toast", message && "show")}>{message}</div>;
}

function Splash() {
  return <main class="login-shell"><section class="panel login-card"><Brand /><p>Loading...</p></section></main>;
}

function field(form: HTMLFormElement, name: string): string {
  return String(new FormData(form).get(name) || "");
}

function versionLabel(version: string): string {
  const trimmed = version.trim();
  if (!trimmed || trimmed === "dev") return "dev";
  return trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
}

function mtuValue(form: HTMLFormElement): number {
  const mode = field(form, "mtu_mode");
  if (mode === "custom") return Number(field(form, "mtu_custom"));
  return Number(mode || 0);
}

function expirationFromForm(form: HTMLFormElement, current = ""): string {
  const mode = field(form, "expires_mode");
  if (mode === "keep") return current;
  if (mode === "custom") return dateTimeLocalToISO(field(form, "expires_custom"));
  return expirationValue(mode);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : "request failed";
}

function toneFromLevel(level: Level): string {
  if (level === "ok") return "ok";
  if (level === "warn") return "warn";
  if (level === "neutral") return "neutral";
  if (level === "bad" || level === "fail") return "bad";
  return "muted";
}

function healthTone(status: string): string {
  if (status.includes("flowing") || status.includes("ok")) return "ok";
  if (status.includes("disabled")) return "neutral";
  return "bad";
}

function sortNewestFirst(events: AuditEvent[]): AuditEvent[] {
  return [...events].sort((left, right) => new Date(right.time).getTime() - new Date(left.time).getTime());
}

function initialTheme(): string {
  const stored = localStorage.getItem(themeKey);
  if (stored === "light" || stored === "dark") return stored;
  return globalThis.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function initParallax() {
  if (globalThis.matchMedia("(prefers-reduced-motion: reduce)").matches || !globalThis.matchMedia("(pointer: fine)").matches) return;
  globalThis.addEventListener("pointermove", (event) => {
    const x = event.clientX / globalThis.innerWidth - 0.5;
    const y = event.clientY / globalThis.innerHeight - 0.5;
    document.documentElement.style.setProperty("--px", `${(x * 10).toFixed(2)}px`);
    document.documentElement.style.setProperty("--py", `${(y * 8).toFixed(2)}px`);
  }, { passive: true });
}

async function doLogout(setState: (state: AppState | null) => void) {
  try {
    await api.logout();
  } finally {
    setState(null);
  }
}

render(<App />, document.querySelector("#app")!);
