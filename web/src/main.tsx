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
  | { kind: "import-key"; client: Client; key: string; warning: string }
  | { kind: "maintenance" };

type MaintenanceTab = "overview" | "doctor" | "firewall" | "warp" | "backup" | "restore" | "updates" | "support" | "logs" | "system";

const themeKey = "awg-forge.theme";
const dashboardFilterKey = "awg-forge.dashboard-filter";
let parallaxInitialized = false;

function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [modal, setModal] = useState<Modal | null>(null);
  const [toast, setToast] = useState("");
  const [theme, setTheme] = useState(initialTheme);
  const [dashboardFilter, setDashboardFilterValue] = useState(localStorage.getItem(dashboardFilterKey) || "all");
  const liveUpdatesEnabled = state !== null;

  const load = useCallback(async (options: { quiet?: boolean } = {}) => {
    try {
      const next = await api.state();
      setState(next);
      setAuthChecked(true);
    } catch (err) {
      setAuthChecked(true);
      if (err instanceof api.APIError && err.status === 401) {
        setState(null);
        return;
      }
      if (!options.quiet) notify(errorMessage(err));
    }
  }, []);

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
  const active = defaultCreateProfile(profiles);
  const allTunnels = state?.tunnels || [];

  function setDashboardFilter(filter: string) {
    localStorage.setItem(dashboardFilterKey, filter);
    setDashboardFilterValue(filter);
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
  if (!state) {
    return (
      <>
        <Login onLogin={() => load()} notify={notify} theme={theme} setTheme={setTheme} />
        <Toast message={toast} />
      </>
    );
  }
  if (!active) return <Shell state={state} theme={theme} setTheme={setTheme} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}><Empty title="No profiles" text="Backend returned no protocol profiles." /></Shell>;

  const renderTunnel = (tunnel: Tunnel) => (
    <TunnelCard
      key={tunnel.id}
      tunnel={tunnel}
      onCreateClient={() => setModal({ kind: "create-client", tunnel })}
      onSettings={() => setModal({ kind: "settings", tunnel })}
      onProtocol={() => setModal({ kind: "protocol", tunnel })}
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
  );

  return (
    <Shell state={state} theme={theme} setTheme={setTheme} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}>
      <TunnelFirstDashboard
        profiles={profiles}
        tunnels={allTunnels}
        filter={dashboardFilter}
        setFilter={setDashboardFilter}
        onCreateTunnel={(profile) => setModal({ kind: "create-tunnel", profile: profile || active })}
        renderTunnel={renderTunnel}
      />
      {modal && (
        <Dialog onClose={() => setModal(null)}>
          <ModalContent modal={modal} state={state} notify={notify} close={() => setModal(null)} reload={() => load({ quiet: true })} runAction={runAction} />
        </Dialog>
      )}
      <Toast message={toast} />
    </Shell>
  );
}

function Login({ onLogin, notify, theme, setTheme }: { onLogin: () => Promise<void>; notify: (message: string) => void; theme: string; setTheme: (theme: string) => void }) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <main class="login-shell">
      <div class="login-stack">
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
        <FooterLinks
          extra={(
            <button class="footer-link icon" type="button" title="Toggle theme" aria-label="Toggle theme" onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
              {theme === "dark" ? "☼" : "☾"}
            </button>
          )}
        />
      </div>
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

function FooterLinks({ version, extra }: { version?: string; extra?: preact.ComponentChildren }) {
  const label = version ? versionLabel(version) : "";
  return (
    <footer class="app-footer" aria-label="Project links">
      <a class="footer-link icon" href="https://github.com/astronaut808/awg-forge" target="_blank" rel="noreferrer" aria-label="awg-forge on GitHub">
        <svg aria-hidden="true" viewBox="0 0 24 24">
          <path d="M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.69c-2.78.6-3.37-1.18-3.37-1.18-.45-1.15-1.11-1.46-1.11-1.46-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.89 1.52 2.34 1.08 2.91.82.09-.65.35-1.08.63-1.33-2.22-.25-4.56-1.11-4.56-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.64 0 0 .84-.27 2.75 1.02A9.57 9.57 0 0 1 12 7.01c.85 0 1.71.11 2.51.34 1.91-1.29 2.75-1.02 2.75-1.02.55 1.37.2 2.39.1 2.64.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.86v2.56c0 .27.18.58.69.48A10 10 0 0 0 12 2Z" />
        </svg>
      </a>
      <a class="footer-link" href="https://github.com/astronaut808/awg-forge/tree/master/docs/ru" target="_blank" rel="noreferrer">Docs</a>
      {version && <span class="footer-version" title="awg-forge version">{label}</span>}
      {extra}
    </footer>
  );
}

function TunnelFirstDashboard({ profiles, tunnels, filter, setFilter, onCreateTunnel, renderTunnel }: {
  profiles: Profile[];
  tunnels: Tunnel[];
  filter: string;
  setFilter: (filter: string) => void;
  onCreateTunnel: (profile?: Profile) => void;
  renderTunnel: (tunnel: Tunnel) => preact.ComponentChildren;
}) {
  const effectiveFilter = filter === "all" || profiles.some((profile) => profile.id === filter) ? filter : "all";
  const filteredProfile = profiles.find((profile) => profile.id === effectiveFilter);
  const visibleProfiles = profiles.filter((profile) => effectiveFilter === "all" || profile.id === effectiveFilter);
  const visibleTunnels = effectiveFilter === "all" ? tunnels : tunnels.filter((tunnel) => tunnel.profile === effectiveFilter);
  const countFor = (profileID: string) => tunnels.filter((tunnel) => tunnel.profile === profileID).length;
  return (
    <section class="panel stack dashboard-v2">
      <div class="section-head">
        <div>
          <h2>Tunnels</h2>
          <div class="filter-row" aria-label="Tunnel filters">
            <button className={classNames("filter-pill", effectiveFilter === "all" && "active")} type="button" onClick={() => setFilter("all")}><span class="filter-label">All</span><span class="filter-count">{tunnels.length}</span></button>
            {profiles.map((profile) => (
              <button key={profile.id} className={classNames("filter-pill", countFor(profile.id) === 0 && "is-empty", effectiveFilter === profile.id && "active")} type="button" onClick={() => setFilter(profile.id)} title={`${profileTitle(profile.id)} · ${countFor(profile.id)} tunnel(s)`}>
                <span class="filter-label">{profile.tab}</span><span class="filter-count">{countFor(profile.id)}</span>
              </button>
            ))}
          </div>
        </div>
        <button class="button primary" type="button" disabled={profiles.length === 0} onClick={() => onCreateTunnel()}>Create tunnel</button>
      </div>
      {visibleTunnels.length === 0 ? (
        <Empty
          title={tunnels.length === 0 ? "No tunnels yet" : "No tunnels in this filter"}
          text={tunnels.length === 0 ? "Create the first AmneziaWG tunnel." : filteredProfile ? `Create an ${profileTitle(filteredProfile.id)} tunnel.` : "Create a tunnel for the selected protocol."}
          action={<button class="button primary" type="button" onClick={() => onCreateTunnel(filteredProfile)}>Create tunnel</button>}
        />
      ) : (
        <div class="profile-groups">
          {visibleProfiles.map((profile) => {
            const group = tunnels.filter((tunnel) => tunnel.profile === profile.id);
            if (group.length === 0) return null;
            return (
              <section class="profile-group" key={profile.id}>
                <div class="profile-group-head">
                  <h3>{profileTitle(profile.id)}</h3>
                  <span>{group.length} tunnel(s)</span>
                </div>
                <div className={classNames("tunnel-grid", group.length === 1 && "single")}>
                  {group.map(renderTunnel)}
                </div>
              </section>
            );
          })}
        </div>
      )}
    </section>
  );
}

function TunnelCard(props: {
  tunnel: Tunnel;
  onCreateClient: () => void;
  onSettings: () => void;
  onProtocol: () => void;
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
  if (modal.kind === "import-key") return <ImportKeyPanel modal={modal} notify={notify} />;
  return <MaintenanceCenter state={state} notify={notify} close={close} reload={reload} />;
}

function CreateTunnelForm({ state, profile, runAction }: { state: AppState; profile: Profile; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  const profiles = state.profiles.length ? state.profiles : [profile];
  const [profileID, setProfileID] = useState(profile.id);
  const selected = profiles.find((item) => item.id === profileID) || profiles[0] || profile;
  return <Form title="Create tunnel" subtitle="Choose a protocol version, then create an independent tunnel." submit="Create tunnel" onSubmit={(form) => runAction("tunnel created", () => api.createTunnel({ profile: field(form, "profile"), name: field(form, "name"), port: Number(field(form, "port")), subnet: field(form, "subnet"), egress_mode: field(form, "egress_mode") }))}>
    <label>Protocol<select aria-label="Protocol" name="profile" value={selected.id} onInput={(event) => setProfileID((event.currentTarget as HTMLSelectElement).value)}>{profiles.map((item) => <option key={item.id} value={item.id}>{profileTitle(item.id)}</option>)}</select></label>
    <label>Name / interface<input key={`${selected.id}-name`} aria-label="Name / interface" name="name" defaultValue={selected.suggested_name || "awg0"} /></label>
    <label>Listen port<input key={`${selected.id}-port`} aria-label="Listen port" name="port" inputMode="numeric" defaultValue={selected.suggested_port || 51820} /></label>
    <label>IPv4 subnet<input key={`${selected.id}-subnet`} aria-label="IPv4 subnet" name="subnet" defaultValue={selected.suggested_subnet || "10.8.0.0/24"} /></label>
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
    {tab === "system" && <SystemPanel state={state} />}
  </PanelTitle>;
}

function SystemPanel({ state }: { state: AppState }) {
  const clients = state.tunnels.flatMap((tunnel) => tunnel.clients);
  const enabledClients = clients.filter((client) => client.enabled && !client.expired).length;
  const upTunnels = state.tunnels.filter((tunnel) => tunnel.status?.up).length;
  const ports = state.published_udp_ports.length ? state.published_udp_ports.join(", ") : "host networking / dynamic";
  const build = state.build;

  return <div class="stack">
    <div>
      <h3>System</h3>
      <p class="note">Current UI/runtime context without secrets.</p>
    </div>
    <div class="metric-grid">
      <Metric title="Server host" value={state.server_host || "-"} />
      <Metric title="Apply config" value={state.apply_enabled ? "enabled" : "manual"} />
      <Metric title="Tunnels" value={`${upTunnels}/${state.tunnels.length} up`} />
      <Metric title="Clients" value={`${enabledClients}/${clients.length} enabled`} />
      <Metric title="Profiles" value={String(state.profiles.length)} />
      <Metric title="Published UDP" value={ports} />
      <Metric title="WARP" value={state.warp.configured ? `${state.warp.enabled_tunnel_count} tunnel(s)` : "not configured"} />
      <Metric title="Version" value={build?.version || "dev"} />
      <Metric title="Commit" value={shortCommit(build?.commit)} />
      <Metric title="Update mode" value={build?.amneziawg_update_mode || "-"} />
      <Metric title="amneziawg-go" value={shortCommit(build?.amneziawg_go_ref)} />
      <Metric title="amneziawg-tools" value={shortCommit(build?.amneziawg_tools_ref)} />
    </div>
    <pre class="command-block">{`docker exec awg-forge awg-forge doctor
docker exec awg-forge awg show
docker compose logs -f`}</pre>
  </div>;
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

function defaultCreateProfile(profiles: Profile[], fallback?: Profile): Profile | undefined {
  return profiles.find((profile) => profile.id === "awg_2_0" && profile.available) || profiles.find((profile) => profile.available) || fallback;
}

function versionLabel(version: string): string {
  const trimmed = version.trim();
  if (!trimmed || trimmed === "dev") return "dev";
  return trimmed.startsWith("v") ? trimmed : `v${trimmed}`;
}

function shortCommit(value = ""): string {
  const trimmed = value.trim();
  if (!trimmed || trimmed === "unknown") return "-";
  return trimmed.length > 12 ? trimmed.slice(0, 12) : trimmed;
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

function sortNewestFirst(events: AuditEvent[]): AuditEvent[] {
  return [...events].sort((left, right) => new Date(right.time).getTime() - new Date(left.time).getTime());
}

function initialTheme(): string {
  const stored = localStorage.getItem(themeKey);
  if (stored === "light" || stored === "dark") return stored;
  return globalThis.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function initParallax() {
  if (parallaxInitialized) return;
  if (globalThis.matchMedia("(prefers-reduced-motion: reduce)").matches || !globalThis.matchMedia("(pointer: fine)").matches) return;
  parallaxInitialized = true;
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
