import { createContext, render } from "preact";
import { useCallback, useContext, useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import { initialLocale, localeStorageKey, messages } from "./i18n";
import type { Locale, Messages } from "./i18n";
import type {
  AppState,
  AuditEvent,
  Client,
  DoctorResult,
  FirewallReport,
  Level,
  Profile,
  RestoreReport,
  TrafficSummary,
  TrafficSummaryRow,
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
  | { kind: "client-config"; client: Client }
  | { kind: "maintenance" };

type MaintenanceTab = "overview" | "doctor" | "firewall" | "warp" | "backup" | "restore" | "updates" | "support" | "logs" | "traffic" | "system";
type QRImportMode = "amneziavpn" | "amneziawg";

const themeKey = "awg-forge.theme";
const dashboardFilterKey = "awg-forge.dashboard-filter";
let parallaxInitialized = false;

const I18nContext = createContext<{ locale: Locale; setLocale: (locale: Locale) => void; m: Messages }>({
  locale: "en",
  setLocale: () => {},
  m: messages.en,
});

function useI18n() {
  return useContext(I18nContext);
}

function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [modal, setModal] = useState<Modal | null>(null);
  const [toast, setToast] = useState("");
  const [theme, setTheme] = useState(initialTheme);
  const [locale, setLocale] = useState<Locale>(initialLocale);
  const [dashboardFilter, setDashboardFilterValue] = useState(localStorage.getItem(dashboardFilterKey) || "all");
  const liveUpdatesEnabled = state !== null;
  const m = messages[locale];
  const messagesRef = useRef(m);
  const authenticatedRef = useRef(false);
  messagesRef.current = m;

  const load = useCallback(async (options: { quiet?: boolean } = {}) => {
    try {
      const next = await api.state();
      authenticatedRef.current = true;
      setState(next);
      setAuthChecked(true);
    } catch (err) {
      setAuthChecked(true);
      if (err instanceof api.APIError && err.status === 401) {
        const wasAuthenticated = authenticatedRef.current;
        authenticatedRef.current = false;
        setModal(null);
        setState(null);
        if (wasAuthenticated) notify(messagesRef.current.common.sessionExpired);
        return;
      }
      if (!options.quiet) notify(errorMessage(err, messagesRef.current.common.requestFailed));
    }
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(themeKey, theme);
  }, [theme]);

  useEffect(() => {
    document.documentElement.lang = locale;
    localStorage.setItem(localeStorageKey, locale);
  }, [locale]);

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
        notify(messagesRef.current.events.liveUpdateFailed);
      }
    });
    events.onerror = () => {
      events.close();
      void load({ quiet: true });
      fallback = globalThis.setInterval(() => void load({ quiet: true }), 5000);
    };
    const authCheck = globalThis.setInterval(() => void load({ quiet: true }), 60000);
    return () => {
      events.close();
      if (fallback) globalThis.clearInterval(fallback);
      globalThis.clearInterval(authCheck);
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
      const message = errorMessage(err, m.common.requestFailed);
      if (message.includes("apply failed")) {
        setModal(null);
        await load({ quiet: true });
      }
      notify(message);
    }
  }

  const i18n = { locale, setLocale, m };
  const shellProps = { theme, setTheme, locale, setLocale };

  if (!authChecked) return <I18nContext.Provider value={i18n}><Splash /></I18nContext.Provider>;
  if (!state) {
    return (
      <I18nContext.Provider value={i18n}>
        <Login onLogin={() => load()} notify={notify} {...shellProps} />
        <Toast message={toast} />
      </I18nContext.Provider>
    );
  }
  if (!active) return <I18nContext.Provider value={i18n}><Shell state={state} {...shellProps} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}><Empty title={m.dashboard.noProfiles} text={m.dashboard.noProfilesText} /></Shell></I18nContext.Provider>;

  const renderTunnel = (tunnel: Tunnel) => (
    <TunnelCard
      key={tunnel.id}
      tunnel={tunnel}
      onCreateClient={() => setModal({ kind: "create-client", tunnel })}
      onSettings={() => setModal({ kind: "settings", tunnel })}
      onProtocol={() => setModal({ kind: "protocol", tunnel })}
      onRestart={() => runAction(m.actions.tunnelRestarted, () => api.restartTunnel(tunnel.id))}
      onDelete={() => confirm(m.actions.deleteTunnelConfirm(tunnel.name)) && runAction(m.actions.tunnelDeleted, () => api.deleteTunnel(tunnel.id))}
      onClientConfig={(client) => setModal({ kind: "client-config", client })}
      onClientSettings={(client) => setModal({ kind: "client-settings", tunnel, client })}
      onClientToggle={(client) => {
        if (!client.enabled && client.traffic?.exceeded) {
          notify(m.forms.trafficLimitEnableBlocked);
          return;
        }
        void runAction(client.enabled ? m.actions.clientDisabled : m.actions.clientEnabled, () => api.setClientEnabled(client.id, !client.enabled));
      }}
      onClientDelete={(client) => confirm(m.actions.deleteClientConfirm(client.name)) && runAction(m.actions.clientDeleted, () => api.deleteClient(client.id))}
    />
  );

  return (
    <I18nContext.Provider value={i18n}>
    <Shell state={state} {...shellProps} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}>
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
    </I18nContext.Provider>
  );
}

function Login({ onLogin, notify, theme, setTheme, locale, setLocale }: { onLogin: () => Promise<void>; notify: (message: string) => void; theme: string; setTheme: (theme: string) => void; locale: Locale; setLocale: (locale: Locale) => void }) {
  const { m } = useI18n();
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
                notify(errorMessage(err, m.common.requestFailed));
              } finally {
                setBusy(false);
              }
            }}
          >
            <label>{m.login.password}<input aria-label={m.login.password} type="password" autocomplete="current-password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label>
            <button class="button primary wide" disabled={busy} type="submit">{busy ? m.login.loggingIn : m.login.logIn}</button>
          </form>
        </section>
        <FooterLinks
          extra={(
            <>
              <button class="footer-link icon" type="button" title={m.aria.toggleTheme} aria-label={m.aria.toggleTheme} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
                {theme === "dark" ? "☼" : "☾"}
              </button>
              <button class="footer-link" type="button" title={m.aria.toggleLanguage} aria-label={m.aria.toggleLanguage} onClick={() => setLocale(locale === "en" ? "ru" : "en")}>
                {locale === "en" ? "RU" : "EN"}
              </button>
            </>
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
  locale: Locale;
  setLocale: (locale: Locale) => void;
  logout: () => void;
  openMaintenance: () => void;
  children: preact.ComponentChildren;
};

function Shell(props: ShellProps) {
  const { state, theme, setTheme, locale, setLocale, logout, openMaintenance, children } = props;
  const { m } = useI18n();
  return (
    <main class="app-shell">
      <header class="topbar panel">
        <Brand subtitle={<><span class="mono">{state.server_host}</span> · {m.dashboard.tunnelCount(state.tunnels.length)}</>} />
        <nav class="toolbar" aria-label={m.aria.globalActions}>
          <button class="button icon" type="button" title={m.aria.toggleTheme} aria-label={m.aria.toggleTheme} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>{theme === "dark" ? "☼" : "☾"}</button>
          <button class="button" type="button" title={m.aria.toggleLanguage} aria-label={m.aria.toggleLanguage} onClick={() => setLocale(locale === "en" ? "ru" : "en")}>{locale === "en" ? "RU" : "EN"}</button>
          <button class="button" type="button" onClick={openMaintenance}>{m.common.maintenance}</button>
          <button class="button" type="button" onClick={logout}>{m.common.logOut}</button>
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
  const { locale, m } = useI18n();
  const label = version ? versionLabel(version) : "";
  const docsPath = locale === "ru" ? "ru" : "en";
  return (
    <footer class="app-footer" aria-label={m.aria.projectLinks}>
      <a class="footer-link icon" href="https://github.com/astronaut808/awg-forge" target="_blank" rel="noreferrer" aria-label="awg-forge on GitHub">
        <svg aria-hidden="true" viewBox="0 0 24 24">
          <path d="M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.69c-2.78.6-3.37-1.18-3.37-1.18-.45-1.15-1.11-1.46-1.11-1.46-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.89 1.52 2.34 1.08 2.91.82.09-.65.35-1.08.63-1.33-2.22-.25-4.56-1.11-4.56-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.64 0 0 .84-.27 2.75 1.02A9.57 9.57 0 0 1 12 7.01c.85 0 1.71.11 2.51.34 1.91-1.29 2.75-1.02 2.75-1.02.55 1.37.2 2.39.1 2.64.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.86v2.56c0 .27.18.58.69.48A10 10 0 0 0 12 2Z" />
        </svg>
      </a>
      <a class="footer-link" href={`https://github.com/astronaut808/awg-forge/tree/master/docs/${docsPath}`} target="_blank" rel="noreferrer">{m.common.docs}</a>
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
  const { m } = useI18n();
  const effectiveFilter = filter === "all" || profiles.some((profile) => profile.id === filter) ? filter : "all";
  const filteredProfile = profiles.find((profile) => profile.id === effectiveFilter);
  const visibleProfiles = profiles.filter((profile) => effectiveFilter === "all" || profile.id === effectiveFilter);
  const visibleTunnels = effectiveFilter === "all" ? tunnels : tunnels.filter((tunnel) => tunnel.profile === effectiveFilter);
  const countFor = (profileID: string) => tunnels.filter((tunnel) => tunnel.profile === profileID).length;
  return (
    <section class="panel stack dashboard-v2">
      <div class="section-head">
        <div>
          <h2>{m.dashboard.tunnels}</h2>
          <div class="filter-row" aria-label={m.aria.tunnelFilters}>
            <button className={classNames("filter-pill", effectiveFilter === "all" && "active")} type="button" onClick={() => setFilter("all")}><span class="filter-label">{m.common.all}</span><span class="filter-count">{tunnels.length}</span></button>
            {profiles.map((profile) => (
              <button key={profile.id} className={classNames("filter-pill", countFor(profile.id) === 0 && "is-empty", effectiveFilter === profile.id && "active")} type="button" onClick={() => setFilter(profile.id)} title={`${profileTitle(profile.id)} · ${m.dashboard.tunnelCount(countFor(profile.id))}`}>
                <span class="filter-label">{profile.tab}</span><span class="filter-count">{countFor(profile.id)}</span>
              </button>
            ))}
          </div>
        </div>
        <button class="button primary" type="button" disabled={profiles.length === 0} onClick={() => onCreateTunnel()}>{m.common.createTunnel}</button>
      </div>
      {visibleTunnels.length === 0 ? (
        <Empty
          title={tunnels.length === 0 ? m.dashboard.noTunnelsYet : m.dashboard.noTunnelsInFilter}
          text={tunnels.length === 0 ? m.dashboard.createFirstTunnel : filteredProfile ? m.dashboard.createTunnelForProfile(profileTitle(filteredProfile.id)) : m.dashboard.createTunnelForSelected}
          action={<button class="button primary" type="button" onClick={() => onCreateTunnel(filteredProfile)}>{m.common.createTunnel}</button>}
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
                  <span>{m.dashboard.tunnelCount(group.length)}</span>
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
  onClientConfig: (client: Client) => void;
  onClientSettings: (client: Client) => void;
  onClientToggle: (client: Client) => void;
  onClientDelete: (client: Client) => void;
}) {
  const { m } = useI18n();
  const { tunnel } = props;
  const stale = Number(tunnel.status?.stale_clients || 0);
  return (
    <article class="tunnel-card panel">
      <div class="tunnel-head">
        <div>
          <h3>{tunnel.name}</h3>
          <p>{tunnel.interface} · {profileTitle(tunnel.profile)}</p>
        </div>
        <Badge tone={tunnel.status?.up ? "ok" : "bad"}>{tunnel.status?.up ? m.status.up : m.status.down}</Badge>
      </div>
      <div class="facts">
        <Fact label={m.tunnel.endpoint} value={`${tunnel.server_host}:${tunnel.listen_port}`} extra={tunnel.egress_mode === "warp" ? "WARP" : ""} />
        <Fact label={m.tunnel.subnet} value={tunnel.subnet} />
        <Fact label={m.tunnel.dns} value={tunnel.dns} />
        <Fact label="MTU" value={Number(tunnel.mtu) > 0 ? String(tunnel.mtu) : m.forms.auto} />
        <Fact label={m.tunnel.clients} value={`${tunnel.clients.filter((client) => client.enabled).length}/${tunnel.clients.length}`} />
      </div>
      <div class="badges">
        {tunnel.status?.firewall?.label && <Badge tone={toneFromLevel(tunnel.status.firewall.level)} title={tunnel.status.firewall.message}>{tunnel.status.firewall.label}</Badge>}
        {stale > 0 && <Badge tone="warn">{m.tunnel.staleConfig(stale)}</Badge>}
        {tunnel.egress_mode === "warp" && <Badge tone="brand">{m.tunnel.warpEgress}</Badge>}
      </div>
      <div class="actions">
        <button class="button primary" type="button" onClick={props.onCreateClient}>{m.common.createClient}</button>
        <button class="button" type="button" onClick={props.onSettings}>{m.common.settings}</button>
        <button class="button" type="button" onClick={props.onProtocol}>{m.tunnel.protocol}</button>
        <button class="button" type="button" onClick={props.onRestart}>{m.common.restart}</button>
        <button class="button danger" type="button" onClick={props.onDelete}>{m.common.delete}</button>
      </div>
      <section class="clients">
        <div class="list-head"><h4>{m.tunnel.clients}</h4><span>{tunnel.clients.length ? m.tunnel.clientTotal(tunnel.clients.length) : m.tunnel.noClientsYet}</span></div>
        {tunnel.clients.length === 0 ? <div class="empty compact">{m.tunnel.createFirstClient}</div> : tunnel.clients.map((client) => (
          <ClientRow
            key={client.id}
            client={client}
            onConfig={() => props.onClientConfig(client)}
            onSettings={() => props.onClientSettings(client)}
            onToggle={() => props.onClientToggle(client)}
            onDelete={() => props.onClientDelete(client)}
          />
        ))}
      </section>
    </article>
  );
}

function ClientRow({ client, onConfig, onSettings, onToggle, onDelete }: { client: Client; onConfig: () => void; onSettings: () => void; onToggle: () => void; onDelete: () => void }) {
  const { m, locale } = useI18n();
  const lastSeen = client.runtime?.last_seen_at || client.last_seen_at;
  const status = client.expired ? "expired" : activeLabel(lastSeen);
  const statusText = clientStatusText(status, m);
  const enableBlockedByTrafficLimit = !client.enabled && Boolean(client.traffic?.exceeded);
  return (
    <div className={classNames("client-row", !client.active && "dimmed")}>
      <div class="client-main">
        <div class="client-title">
          <span class="status-dot" />
          <strong>{client.name}</strong>
          <Badge tone={client.active ? "ok" : "neutral"}>{client.expired ? m.status.expired : client.enabled ? m.common.enabled : m.common.disabled}</Badge>
          {client.traffic?.exceeded && <Badge tone="bad">{m.status.limitExceeded}</Badge>}
          <Badge tone={status === "active now" ? "ok" : status === "seen recently" ? "neutral" : "muted"}>{statusText}</Badge>
          {client.needs_new_config && <Badge tone="warn">{m.common.stale}</Badge>}
        </div>
        <p class="mono">{client.address}</p>
        <p>
          {m.tunnel.lastSeen} {relativeTime(lastSeen, m.time, locale)}
          {client.runtime?.present ? <> · {m.tunnel.received} {formatBytes(client.runtime.rx_bytes)} · {m.tunnel.sent} {formatBytes(client.runtime.tx_bytes)}</> : null}
          {client.expires_at ? <> · {m.tunnel.expires} {dateOnly(client.expires_at, locale)}</> : null}
        </p>
        {client.traffic?.enabled && <p>{m.tunnel.totalTraffic} {formatBytes(client.traffic.rx_total + client.traffic.tx_total)} / {client.traffic.limit_bytes ? formatBytes(client.traffic.limit_bytes) : "∞"}</p>}
        {client.traffic?.exceeded && <p class="note">{m.forms.trafficLimitExceededHelp}</p>}
        {client.notes && <p class="note">{client.notes}</p>}
      </div>
      <div class="actions">
        <button class="button" type="button" onClick={onConfig}>{m.common.config}</button>
        <button class="button" type="button" onClick={onSettings}>{m.common.edit}</button>
        <button class="button" type="button" disabled={enableBlockedByTrafficLimit} title={enableBlockedByTrafficLimit ? m.forms.trafficLimitEnableBlocked : undefined} onClick={onToggle}>{client.enabled ? m.common.disable : m.common.enable}</button>
        <button class="button danger" type="button" onClick={onDelete}>{m.common.delete}</button>
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
  if (modal.kind === "create-client") return <CreateClientForm tunnel={modal.tunnel} runAction={runAction} />;
  if (modal.kind === "client-settings") return <ClientSettingsForm client={modal.client} runAction={runAction} />;
  if (modal.kind === "client-config") return <ClientConfigPanel client={modal.client} notify={notify} />;
  return <MaintenanceCenter state={state} notify={notify} close={close} reload={reload} />;
}

function CreateTunnelForm({ state, profile, runAction }: { state: AppState; profile: Profile; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  const { m } = useI18n();
  const profiles = state.profiles.length ? state.profiles : [profile];
  const [profileID, setProfileID] = useState(profile.id);
  const selected = profiles.find((item) => item.id === profileID) || profiles[0] || profile;
  return <Form title={m.forms.createTunnelTitle} subtitle={m.forms.createTunnelSubtitle} submit={m.common.createTunnel} onSubmit={(form) => runAction(m.forms.tunnelCreated, () => api.createTunnel({ profile: field(form, "profile"), name: field(form, "name"), port: Number(field(form, "port")), subnet: field(form, "subnet"), egress_mode: field(form, "egress_mode") }))}>
    <label>{m.forms.protocol}<select aria-label={m.forms.protocol} name="profile" value={selected.id} onInput={(event) => setProfileID((event.currentTarget as HTMLSelectElement).value)}>{profiles.map((item) => <option key={item.id} value={item.id}>{profileTitle(item.id)}</option>)}</select></label>
    <label>{m.forms.nameInterface}<input key={`${selected.id}-name`} aria-label={m.forms.nameInterface} name="name" defaultValue={selected.suggested_name || "awg0"} /></label>
    <label>{m.forms.listenPort}<input key={`${selected.id}-port`} aria-label={m.forms.listenPort} name="port" inputMode="numeric" defaultValue={selected.suggested_port || 51820} /></label>
    <label>{m.forms.ipv4Subnet}<input key={`${selected.id}-subnet`} aria-label={m.forms.ipv4Subnet} name="subnet" defaultValue={selected.suggested_subnet || "10.8.0.0/24"} /></label>
    <label>{m.forms.egress}<select aria-label={m.forms.egress} name="egress_mode" defaultValue="wan"><option value="wan">{m.forms.serverWAN}</option><option value="warp">{m.forms.cloudflareWARP}</option></select></label>
    {!state.warp?.configured && <small class="form-note">{m.forms.warpAutoRegister}</small>}
  </Form>;
}

function TunnelSettingsForm({ state, tunnel, runAction }: { state: AppState; tunnel: Tunnel; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  const { m } = useI18n();
  const [egressMode, setEgressMode] = useState(tunnel.egress_mode || "wan");
  useEffect(() => {
    setEgressMode(tunnel.egress_mode || "wan");
  }, [tunnel.id, tunnel.egress_mode]);
  return <Form title={m.forms.tunnelSettingsTitle} subtitle={`${tunnel.name} · ${profileTitle(tunnel.profile)}`} submit={m.common.save} onSubmit={(form) => runAction(m.forms.settingsSaved, () => api.updateTunnel(tunnel.id, {
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
    <label>{m.forms.nameInterface}<input aria-label={m.forms.nameInterface} name="name" defaultValue={tunnel.name} /></label>
    <label>{m.forms.serverHost}<input aria-label={m.forms.serverHost} name="server_host" defaultValue={tunnel.server_host || ""} placeholder={state.server_host} /></label>
    <label>{m.forms.egress}<select aria-label={m.forms.egress} name="egress_mode" value={egressMode} onInput={(event) => setEgressMode((event.currentTarget as HTMLSelectElement).value)}><option value="wan">{m.forms.serverWAN}</option><option value="warp">{m.forms.cloudflareWARP}</option></select></label>
    <label>{m.forms.listenPort}<input aria-label={m.forms.listenPort} name="port" inputMode="numeric" defaultValue={tunnel.listen_port} /></label>
    {!state.warp?.configured && <small class="form-note">{m.forms.warpAutoRegister}</small>}
    <label>{m.forms.ipv4Subnet}<input aria-label={m.forms.ipv4Subnet} name="subnet" defaultValue={tunnel.subnet} /></label>
    <label>{m.tunnel.dns}<input aria-label={m.tunnel.dns} name="dns" defaultValue={tunnel.dns} /></label>
    <label>{m.forms.allowedIPs}<input aria-label={m.forms.allowedIPs} name="allowed_ips" defaultValue={tunnel.allowed_ips} /></label>
    <label>{m.forms.persistentKeepalive}<input aria-label={m.forms.persistentKeepalive} name="keepalive" inputMode="numeric" defaultValue={tunnel.keepalive} /></label>
    <MTUField value={tunnel.mtu || 0} />
    <label class="toggle-row full">
      <span><strong>{m.common.enabled}</strong><small>{m.forms.renderTunnelEnabled}</small></span>
      <input aria-label={m.common.enabled} name="enabled" type="checkbox" defaultChecked={tunnel.enabled} />
    </label>
  </Form>;
}

function ProtocolForm({ tunnel, runAction }: { tunnel: Tunnel; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  const { m } = useI18n();
  return <Form title={m.forms.protocolTitle} subtitle={`${tunnel.name} · ${tunnel.profile}`} submit={m.forms.saveProtocol} secondary={<button class="button" type="button" onClick={() => confirm(m.forms.regenerateConfirm) && void runAction(m.forms.protocolRegenerated, () => api.regenerateProtocol(tunnel.id, tunnel.profile))}>{m.forms.regenerate}</button>} onSubmit={(form) => {
    const params: Record<string, string> = {};
    for (const item of tunnel.params) params[item.key] = field(form, item.key).trim();
    return runAction(m.forms.protocolSaved, () => api.updateProtocol(tunnel.id, tunnel.profile, params));
  }}>
    {tunnel.params.map((item) => item.key.startsWith("I") ? (
      <label key={item.key}>{item.key}<textarea aria-label={item.key} name={item.key} defaultValue={item.value || ""} /></label>
    ) : (
      <label key={item.key}>{item.key}<input aria-label={item.key} name={item.key} defaultValue={item.value || ""} /></label>
    ))}
  </Form>;
}

function CreateClientForm({ tunnel, runAction }: { tunnel: Tunnel; runAction: (label: string, fn: () => Promise<unknown>, options?: { reload?: boolean; close?: boolean }) => Promise<void> }) {
  const { m } = useI18n();
  return <Form title={m.forms.createClientTitle} subtitle={`${tunnel.name} · ${tunnel.profile}`} submit={m.common.createClient} onSubmit={(form) => runAction(m.forms.clientCreatedOpenConfig, async () => {
    await api.createClient(tunnel.id, field(form, "name"), expirationFromForm(form));
  })}>
    <label>{m.forms.clientName}<input aria-label={m.forms.clientName} name="name" /></label>
    <ExpirationField />
  </Form>;
}

function ClientSettingsForm({ client, runAction }: { client: Client; runAction: (label: string, fn: () => Promise<unknown>) => Promise<void> }) {
  const { m } = useI18n();
  return <Form title={m.forms.clientSettingsTitle} subtitle={`${client.name} · ${client.address}`} submit={m.common.save} onSubmit={(form) => runAction(m.forms.clientSaved, async () => {
    await api.updateClient(client.id, { name: field(form, "name"), notes: field(form, "notes"), expires_at: expirationFromForm(form, client.expires_at) });
    if (client.traffic?.enabled) await api.updateClientTrafficLimit(client.id, trafficLimitBytesFromForm(form, m.forms.trafficLimitInvalid));
  })}>
    <label>{m.forms.clientName}<input aria-label={m.forms.clientName} name="name" defaultValue={client.name} /></label>
    <ExpirationField current={client.expires_at} keepCurrent />
    {client.traffic?.enabled && <label>{m.forms.trafficLimit}<input aria-label={m.forms.trafficLimit} name="traffic_limit_gib" type="number" min="0.001" step="0.001" inputMode="decimal" defaultValue={trafficLimitGiBValue(client.traffic.limit_bytes)} placeholder="∞" /><span class="help">{client.traffic.exceeded ? m.forms.trafficLimitExceededHelp : m.forms.trafficLimitHelp}</span></label>}
    <label class="full">{m.forms.notes}<textarea aria-label={m.forms.notes} name="notes" maxLength={1000} defaultValue={client.notes || ""} /></label>
  </Form>;
}

function MTUField({ value }: { value: number }) {
  const { m } = useI18n();
  const normalized = Number(value || 0);
  const preset = normalized === 0 || normalized === 1280 || normalized === 1420 ? String(normalized) : "custom";
  const [mode, setMode] = useState(preset);
  return <>
    <label>MTU<select aria-label="MTU" name="mtu_mode" value={mode} onInput={(event) => setMode((event.currentTarget as HTMLSelectElement).value)}>
      <option value="0">{m.forms.auto}</option>
      <option value="1280">1280</option>
      <option value="1420">1420</option>
      <option value="custom">{m.common.custom}</option>
    </select></label>
    {mode === "custom" && <label>{m.forms.customMTU}<input aria-label={m.forms.customMTU} name="mtu_custom" inputMode="numeric" defaultValue={normalized || 1280} /></label>}
  </>;
}

function ExpirationField({ current, keepCurrent = false }: { current?: string; keepCurrent?: boolean }) {
  const { m, locale } = useI18n();
  const [mode, setMode] = useState(keepCurrent ? "keep" : "");
  return <>
    <label>{m.forms.expiration}<select aria-label={m.forms.expiration} name="expires_mode" value={mode} onInput={(event) => setMode((event.currentTarget as HTMLSelectElement).value)}>
      {keepCurrent && <option value="keep">{current ? m.forms.keepCurrentValue(dateOnly(current, locale)) : m.forms.keepCurrent}</option>}
      <option value="">{m.forms.neverExpires}</option>
      <option value="1h">{m.forms.oneHour}</option>
      <option value="1d">{m.forms.oneDay}</option>
      <option value="7d">{m.forms.sevenDays}</option>
      <option value="30d">{m.forms.thirtyDays}</option>
      <option value="custom">{m.forms.customDate}</option>
    </select></label>
    {mode === "custom" && <label>{m.forms.customExpiration}<input aria-label={m.forms.customExpiration} name="expires_custom" type="datetime-local" min={dateTimeLocalValue()} defaultValue={dateTimeLocalValue(current || expirationValue("1d"))} /></label>}
  </>;
}

function ClientConfigPanel({ client, notify }: { client: Client; notify: (message: string) => void }) {
  const { m } = useI18n();
  const awgQRURL = api.clientQRCodeURL(client.id);
  const notifyRef = useRef(notify);
  const [importKey, setImportKey] = useState("");
  const [importWarning, setImportWarning] = useState("");
  const [vpnQRChunks, setVPNQRChunks] = useState(1);
  const [vpnQRChunk, setVPNQRChunk] = useState(0);
  const [qrMode, setQRMode] = useState<QRImportMode>("amneziavpn");
  const [expandedQR, setExpandedQR] = useState<"" | QRImportMode>("");
  const [busy, setBusy] = useState(false);
  const vpnQRURL = api.clientAmneziaVPNQRCodeURL(client.id, vpnQRChunk);
  const expandedQRURL = expandedQR === "amneziavpn" ? vpnQRURL : awgQRURL;
  const expandedQRTitle = expandedQR === "amneziavpn" ? m.clientConfig.amneziaVPNQR : m.clientConfig.amneziaWGQR;
  const hasVPNQRSeries = vpnQRChunks > 1;
  const vpnQRDownloadName = hasVPNQRSeries ? `${client.name}-amneziavpn-${vpnQRChunk + 1}-of-${vpnQRChunks}.png` : `${client.name}-amneziavpn.png`;
  const activeQRURL = qrMode === "amneziavpn" ? vpnQRURL : awgQRURL;
  const activeQRTitle = qrMode === "amneziavpn" ? m.clientConfig.amneziaVPNQR : m.clientConfig.amneziaWGQR;
  const activeQRDescription = qrMode === "amneziavpn"
    ? m.clientConfig.amneziaVPNDescription
    : m.clientConfig.amneziaWGDescription;
  const activeQRAlt = qrMode === "amneziavpn" ? m.clientConfig.amneziaVPNAlt(client.name) : m.clientConfig.amneziaWGAlt(client.name);
  const activeQRDownloadName = qrMode === "amneziavpn" ? vpnQRDownloadName : `${client.name}-amneziawg.png`;
  const messagesRef = useRef(m);
  messagesRef.current = m;

  useEffect(() => {
    notifyRef.current = notify;
  }, [notify]);

  useEffect(() => {
    let cancelled = false;
    setVPNQRChunks(1);
    setVPNQRChunk(0);
    api.clientAmneziaVPNQRSeries(client.id)
      .then((res) => {
        if (cancelled) return;
        setVPNQRChunks(Math.max(1, res.chunks));
      })
      .catch((err) => notifyRef.current(errorMessage(err, messagesRef.current.common.requestFailed)));
    return () => {
      cancelled = true;
    };
  }, [client.id]);

  useEffect(() => {
    if (!expandedQR) return;
    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") setExpandedQR("");
    }
    globalThis.addEventListener("keydown", closeOnEscape);
    return () => globalThis.removeEventListener("keydown", closeOnEscape);
  }, [expandedQR]);

  async function loadImportKey(): Promise<string> {
    if (importKey) return importKey;
    if (busy) return "";
    setBusy(true);
    try {
      const res = await api.clientImportKey(client.id);
      setImportKey(res.import_key);
      setImportWarning(res.warning);
      return res.import_key;
    } catch (err) {
      notify(errorMessage(err, m.common.requestFailed));
      return "";
    } finally {
      setBusy(false);
    }
  }

  async function copyImportKey() {
    const key = importKey || await loadImportKey();
    if (!key) return;
    try {
      await navigator.clipboard.writeText(key);
      notify(m.clientConfig.vpnLinkCopied);
    } catch {
      notify(m.clientConfig.clipboardUnavailable);
    }
  }

  return <PanelTitle title={m.clientConfig.title} subtitle={`${client.name} · ${client.address}`}>
    <div class="config-options">
      <section class="config-option qr-config-option">
        <div>
          <h3>{activeQRTitle}</h3>
          <p>{activeQRDescription}</p>
        </div>
        <div class="segmented qr-mode-tabs" role="tablist" aria-label={m.clientConfig.qrImportMode}>
          <button class={qrMode === "amneziavpn" ? "active" : ""} type="button" role="tab" aria-selected={qrMode === "amneziavpn"} onClick={() => setQRMode("amneziavpn")}>AmneziaVPN</button>
          <button class={qrMode === "amneziawg" ? "active" : ""} type="button" role="tab" aria-selected={qrMode === "amneziawg"} onClick={() => setQRMode("amneziawg")}>AmneziaWG</button>
        </div>
        <div class="qr-panel">
          <button class="qr-image-button" type="button" onClick={() => setExpandedQR(qrMode)} aria-label={m.clientConfig.openLarger(activeQRTitle)}>
            <img class="qr-image" src={activeQRURL} alt={activeQRAlt} />
          </button>
          {qrMode === "amneziavpn" && hasVPNQRSeries && <div class="qr-series">
            <button class="button" type="button" disabled={vpnQRChunk === 0} onClick={() => setVPNQRChunk((value) => Math.max(0, value - 1))}>{m.clientConfig.previous}</button>
            <span>{m.clientConfig.qrCounter(vpnQRChunk + 1, vpnQRChunks)}</span>
            <button class="button" type="button" disabled={vpnQRChunk + 1 >= vpnQRChunks} onClick={() => setVPNQRChunk((value) => Math.min(vpnQRChunks - 1, value + 1))}>{m.clientConfig.next}</button>
          </div>}
        </div>
        <div class="action-row">
          <a class="button" href={activeQRURL} download={activeQRDownloadName}>{m.clientConfig.downloadQR} {qrMode === "amneziavpn" && hasVPNQRSeries ? `${vpnQRChunk + 1}` : ""}</a>
        </div>
      </section>
      <section class="config-option">
        <div>
          <h3>{m.clientConfig.importOptions}</h3>
          <p>{m.clientConfig.importOptionsText}</p>
        </div>
        <div class="config-actions">
          <button class="button primary" type="button" onClick={() => downloadConfig(client.id)}>{m.clientConfig.downloadConf}</button>
          <button class="button" disabled={busy} type="button" onClick={copyImportKey}>{m.clientConfig.copyVpnKey}</button>
        </div>
        {importKey && <textarea class="mono import-key" aria-label={m.clientConfig.vpnImportLink} readOnly value={importKey} />}
        {importWarning && <p class="note">{importWarning}</p>}
      </section>
    </div>
    <p class="note">{m.clientConfig.secretWarning}</p>
    {expandedQR && <dialog open class="qr-lightbox" aria-label={expandedQRTitle}>
      <button class="qr-lightbox-backdrop-button" type="button" onClick={() => setExpandedQR("")} aria-label={m.clientConfig.closePreview} />
      <div class="qr-lightbox-card">
        <div class="qr-lightbox-head">
          <div>
            <h3>{expandedQRTitle}</h3>
            <p>{expandedQR === "amneziavpn" ? (hasVPNQRSeries ? m.clientConfig.qrCounter(vpnQRChunk + 1, vpnQRChunks) : m.clientConfig.amneziaVPNImportQR) : m.clientConfig.rawConfQR}</p>
          </div>
          <button class="button icon" type="button" onClick={() => setExpandedQR("")} aria-label={m.clientConfig.closePreview}>×</button>
        </div>
        <img class="qr-lightbox-image" src={expandedQRURL} alt={m.clientConfig.expandedAlt(expandedQRTitle, client.name)} />
        {expandedQR === "amneziavpn" && hasVPNQRSeries && <div class="qr-series">
          <button class="button" type="button" disabled={vpnQRChunk === 0} onClick={() => setVPNQRChunk((value) => Math.max(0, value - 1))}>{m.clientConfig.previous}</button>
          <span>{m.clientConfig.qrCounter(vpnQRChunk + 1, vpnQRChunks)}</span>
          <button class="button" type="button" disabled={vpnQRChunk + 1 >= vpnQRChunks} onClick={() => setVPNQRChunk((value) => Math.min(vpnQRChunks - 1, value + 1))}>{m.clientConfig.next}</button>
        </div>}
        <a class="button" href={expandedQRURL} download={expandedQR === "amneziavpn" ? vpnQRDownloadName : `${client.name}-amneziawg.png`}>{m.clientConfig.downloadQR}</a>
      </div>
    </dialog>}
  </PanelTitle>;
}

function MaintenanceCenter({ state, notify, reload }: { state: AppState; notify: (message: string) => void; close: () => void; reload: () => Promise<void> }) {
  const { m } = useI18n();
  const [tab, setTab] = useState<MaintenanceTab>("overview");
  const [doctorResults, setDoctorResults] = useState<DoctorResult[] | null>(null);
  const [firewall, setFirewall] = useState<FirewallReport | null>(null);
  const [updateReport, setUpdateReport] = useState<UpdatesReport | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [traffic, setTraffic] = useState<TrafficSummary | null>(null);
  const [restore, setRestore] = useState<RestoreReport | null>(null);
  const [busyAction, setBusyAction] = useState("");
  const notifyRef = useRef(notify);
  const messagesRef = useRef(m);
  messagesRef.current = m;

  useEffect(() => {
    notifyRef.current = notify;
  }, [notify]);

  useEffect(() => {
    if (tab !== "logs") return;
    let closed = false;

    async function loadAuditLog(quiet: boolean) {
      try {
        const res = await api.auditLog();
        if (!closed) setEvents(sortNewestFirst(res.events));
      } catch (err) {
        if (!quiet && !closed) notifyRef.current(errorMessage(err, messagesRef.current.common.requestFailed));
      }
    }

    void loadAuditLog(true);
    const timer = globalThis.setInterval(() => void loadAuditLog(true), 3500);
    return () => {
      closed = true;
      globalThis.clearInterval(timer);
    };
  }, [tab]);

  useEffect(() => {
    if (tab !== "traffic") return;
    let closed = false;
    api.trafficSummary()
      .then((res) => {
        if (!closed) setTraffic(res);
      })
      .catch((err) => {
        if (!closed) notifyRef.current(errorMessage(err, messagesRef.current.common.requestFailed));
      });
    return () => {
      closed = true;
    };
  }, [tab]);

  async function action(key: string, label: string, fn: () => Promise<void>) {
    if (busyAction) return;
    setBusyAction(key);
    try {
      await fn();
      notify(label);
      await reload();
    } catch (err) {
      notify(errorMessage(err, m.common.requestFailed));
    } finally {
      setBusyAction("");
    }
  }

  return <PanelTitle title={m.maintenance.title} subtitle={m.maintenance.subtitle}>
    <nav class="subtabs">{(["overview", "doctor", "firewall", "warp", "backup", "restore", "updates", "support", "logs", "traffic", "system"] as MaintenanceTab[]).map((item) => <button key={item} className={classNames("button", tab === item && "active")} type="button" onClick={() => setTab(item)}>{m.maintenance.tabs[item]}</button>)}</nav>
    {tab === "overview" && <div class="metric-grid"><Metric title={m.dashboard.tunnels} value={String(state.tunnels.length)} /><Metric title="WARP" value={state.warp.configured ? `${state.warp.enabled_tunnel_count} ${m.common.enabled}` : m.maintenance.notConfigured} /><Metric title={m.maintenance.applyConfig} value={state.apply_enabled ? m.common.enabled : m.common.manual} /><Metric title={m.common.profiles} value={String(state.profiles.length)} /></div>}
    {tab === "doctor" && <div class="stack"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("doctor", m.maintenance.doctorCompleted, async () => setDoctorResults((await api.doctor()).results))}><ButtonContent busy={busyAction === "doctor"}>{m.maintenance.runDoctor}</ButtonContent></button><ResultList results={doctorResults} /></div>}
    {tab === "firewall" && <div class="stack"><button class="button primary" disabled={!state.apply_enabled || Boolean(busyAction)} type="button" onClick={() => action("firewall", m.maintenance.firewallRepaired, async () => setFirewall((await api.firewallRepair()).firewall))}><ButtonContent busy={busyAction === "firewall"}>{m.maintenance.repairFirewall}</ButtonContent></button>{firewall ? <ResultList results={firewall.results.map((item) => ({ level: item.status === "ok" ? "ok" : item.status === "duplicate" ? "warn" : "fail", area: `${item.tunnel}/${item.name}`, message: item.message || item.rule }))} /> : <p>{m.maintenance.firewallNote}</p>}</div>}
    {tab === "warp" && <WarpPanel state={state} action={action} busyAction={busyAction} />}
    {tab === "backup" && <BackupPanel notify={notify} />}
    {tab === "restore" && <RestorePanel report={restore} setReport={setRestore} notify={notify} />}
    {tab === "updates" && <div class="stack"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("updates", m.maintenance.updatesChecked, async () => setUpdateReport((await api.updates()).updates))}><ButtonContent busy={busyAction === "updates"}>{m.maintenance.checkUpdates}</ButtonContent></button>{updateReport && <ResultList results={updateReport.components.map((item) => ({ level: item.status === "current" ? "ok" : "warn", area: item.name, message: `${item.current_ref} → ${item.latest_ref}` }))} />}</div>}
    {tab === "support" && <div class="stack"><p>{m.maintenance.supportText}</p><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("support", m.maintenance.supportDownloadStarted, async () => { const res = await fetch("/api/support-bundle"); if (!res.ok) throw new Error(m.maintenance.supportDownloadFailed); await downloadResponse(res, "awg-forge-support.zip"); })}><ButtonContent busy={busyAction === "support"}>{m.maintenance.downloadSupport}</ButtonContent></button></div>}
    {tab === "logs" && <div class="stack"><p class="note">{m.maintenance.auditAutoRefresh}</p><div class="list">{events.length === 0 ? <div class="empty compact">{m.maintenance.noAuditEvents}</div> : events.map((event) => <div class="row" key={`${event.time}-${event.event}`}><strong>{event.event}</strong><p>{event.time} · {event.level} · {event.message}{event.error ? ` · ${event.error}` : ""}</p></div>)}</div></div>}
    {tab === "traffic" && <TrafficPanel state={state} traffic={traffic} reload={async () => setTraffic(await api.trafficSummary())} />}
    {tab === "system" && <SystemPanel state={state} />}
  </PanelTitle>;
}

function TrafficPanel({ state, traffic, reload }: { state: AppState; traffic: TrafficSummary | null; reload: () => Promise<void> }) {
  const { m } = useI18n();
  const [busy, setBusy] = useState(false);
  const rows = trafficRowsWithNames(state, traffic?.rows || []);
  const totals = trafficTotals(rows);
  const topClients = [...rows].sort((a, b) => (b.rx_total + b.tx_total) - (a.rx_total + a.tx_total)).slice(0, 5);

  async function refresh() {
    setBusy(true);
    try {
      await reload();
    } finally {
      setBusy(false);
    }
  }

  return <div class="stack">
    <div>
      <h3>{m.maintenance.traffic}</h3>
      <p class="note">{traffic?.enabled ? m.maintenance.trafficText : m.maintenance.trafficDisabled}</p>
    </div>
    <button class="button primary" disabled={busy} type="button" onClick={() => void refresh()}><ButtonContent busy={busy}>{m.maintenance.refreshTraffic}</ButtonContent></button>
    {!traffic ? <p>{m.maintenance.noResults}</p> : !traffic.enabled ? <div class="empty compact">{m.maintenance.trafficDisabled}</div> : rows.length === 0 ? <div class="empty compact">{m.maintenance.noTrafficData}</div> : <>
      <div class="metric-grid">
        <Metric title={m.maintenance.totalTraffic} value={formatBytes(totals.rxTotal + totals.txTotal)} />
        <Metric title={m.maintenance.today} value={formatTrafficPair(totals.rxToday, totals.txToday, m)} />
        <Metric title={m.maintenance.last7d} value={formatTrafficPair(totals.rx7d, totals.tx7d, m)} />
        <Metric title={m.maintenance.last30d} value={formatTrafficPair(totals.rx30d, totals.tx30d, m)} />
      </div>
      <div>
        <h3>{m.maintenance.topClients}</h3>
        <div class="list">
          {topClients.map((row) => <div class="row" key={`${row.tunnel_id}-${row.client_id}`}>
        <div>
          <strong>{row.clientName}</strong>
          <p>{row.tunnelName} · {m.maintenance.totalTraffic}: {formatBytes(row.rx_total + row.tx_total)}</p>
          <p>{m.maintenance.today}: {formatTrafficPair(row.rx_today, row.tx_today, m)} · {m.maintenance.last7d}: {formatTrafficPair(row.rx_7d, row.tx_7d, m)}</p>
        </div>
          </div>)}
        </div>
      </div>
    </>}
  </div>;
}

function trafficRowsWithNames(state: AppState, rows: TrafficSummaryRow[]) {
  return rows.map((row) => {
    const tunnel = state.tunnels.find((item) => item.id === row.tunnel_id);
    const client = tunnel?.clients.find((item) => item.id === row.client_id);
    return {
      ...row,
      tunnelName: tunnel?.name || row.tunnel_id,
      clientName: client?.name || row.client_id,
    };
  });
}

function formatTrafficPair(rxBytes: number, txBytes: number, m: Messages): string {
  return `${formatBytes(rxBytes)} ${m.tunnel.received} / ${formatBytes(txBytes)} ${m.tunnel.sent}`;
}

function trafficTotals(rows: TrafficSummaryRow[]) {
  return rows.reduce((acc, row) => ({
    rxTotal: acc.rxTotal + row.rx_total,
    txTotal: acc.txTotal + row.tx_total,
    rxToday: acc.rxToday + row.rx_today,
    txToday: acc.txToday + row.tx_today,
    rx7d: acc.rx7d + row.rx_7d,
    tx7d: acc.tx7d + row.tx_7d,
    rx30d: acc.rx30d + row.rx_30d,
    tx30d: acc.tx30d + row.tx_30d,
  }), { rxTotal: 0, txTotal: 0, rxToday: 0, txToday: 0, rx7d: 0, tx7d: 0, rx30d: 0, tx30d: 0 });
}

function SystemPanel({ state }: { state: AppState }) {
  const { m } = useI18n();
  const clients = state.tunnels.flatMap((tunnel) => tunnel.clients);
  const enabledClients = clients.filter((client) => client.enabled && !client.expired).length;
  const upTunnels = state.tunnels.filter((tunnel) => tunnel.status?.up).length;
  const ports = state.published_udp_ports.length ? state.published_udp_ports.join(", ") : m.maintenance.hostNetworkingDynamic;
  const build = state.build;

  return <div class="stack">
    <div>
      <h3>{m.maintenance.system}</h3>
      <p class="note">{m.maintenance.systemText}</p>
    </div>
    <div class="metric-grid">
      <Metric title={m.forms.serverHost} value={state.server_host || "-"} />
      <Metric title={m.maintenance.applyConfig} value={state.apply_enabled ? m.common.enabled : m.common.manual} />
      <Metric title={m.maintenance.database} value={databaseLabel(state.database)} />
      <Metric title={m.dashboard.tunnels} value={`${upTunnels}/${state.tunnels.length} ${m.status.up}`} />
      <Metric title={m.tunnel.clients} value={`${enabledClients}/${clients.length} ${m.common.enabled}`} />
      <Metric title={m.common.profiles} value={String(state.profiles.length)} />
      <Metric title={m.maintenance.publishedUDP} value={ports} />
      <Metric title="WARP" value={state.warp.configured ? m.warp.tunnelCount(state.warp.enabled_tunnel_count) : m.maintenance.notConfigured} />
      <Metric title={m.maintenance.version} value={build?.version || "dev"} />
      <Metric title={m.maintenance.commit} value={shortCommit(build?.commit)} />
      <Metric title={m.maintenance.updateMode} value={build?.amneziawg_update_mode || "-"} />
      <Metric title="amneziawg-go" value={shortCommit(build?.amneziawg_go_ref)} />
      <Metric title="amneziawg-tools" value={shortCommit(build?.amneziawg_tools_ref)} />
    </div>
    <pre class="command-block">{`docker exec awg-forge awg-forge doctor
docker exec awg-forge awg show
docker compose logs -f`}</pre>
  </div>;
}

function databaseLabel(database: AppState["database"]): string {
  if (!database?.mode) return "off";
  return database.enabled ? database.mode : "off";
}

function WarpPanel({ state, action, busyAction }: { state: AppState; action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const { m } = useI18n();
  return <div class="stack">
    <div class="metric-grid">
      <Metric title={m.warp.registration} value={state.warp.registered ? m.common.automatic : state.warp.configured ? m.common.manual : m.common.none} />
      <Metric title={m.warp.interface} value={state.warp.interface_name || "warp0"} />
      <Metric title={m.warp.endpoint} value={state.warp.endpoint || "-"} />
      <Metric title={m.dashboard.tunnels} value={m.warp.tunnelsViaWARP(state.warp.enabled_tunnel_count || 0)} />
    </div>
    <div class="actions"><button class="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("warp-register", state.warp.configured ? m.warp.reregistered : m.warp.registered, async () => { await api.registerWarp(); })}><ButtonContent busy={busyAction === "warp-register"}>{state.warp.configured ? m.warp.reregister : m.warp.register}</ButtonContent></button><button class="button" disabled={!state.warp.configured || Boolean(busyAction)} type="button" onClick={() => action("warp-restart", m.warp.restarted, async () => { await api.restartWarp(); })}><ButtonContent busy={busyAction === "warp-restart"}>{m.warp.restart}</ButtonContent></button><button class="button danger" disabled={!state.warp.configured || Number(state.warp.enabled_tunnel_count || 0) > 0 || Boolean(busyAction)} type="button" onClick={() => confirm(m.warp.deleteConfirm) && action("warp-delete", m.warp.deleted, async () => { await api.deleteWarp(); })}><ButtonContent busy={busyAction === "warp-delete"}>{m.warp.delete}</ButtonContent></button></div>
    <details><summary>{m.warp.manualImport}</summary><WarpImport action={action} busyAction={busyAction} /></details>
  </div>;
}

function WarpImport({ action, busyAction }: { action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const { m } = useI18n();
  const [value, setValue] = useState("");
  return <form class="form single" onSubmit={(event) => { event.preventDefault(); void action("warp-import", m.warp.imported, async () => { await api.importWarp(value); }); }}><label>{m.warp.wireGuardConfig}<textarea aria-label={m.warp.wireGuardConfig} value={value} onInput={(event) => setValue((event.currentTarget as HTMLTextAreaElement).value)} placeholder="[Interface]&#10;PrivateKey = ..." /></label><button class="button primary" disabled={Boolean(busyAction)} type="submit"><ButtonContent busy={busyAction === "warp-import"}>{m.warp.importConfig}</ButtonContent></button></form>;
}

function BackupPanel({ notify }: { notify: (message: string) => void }) {
  const { m } = useI18n();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return <form class="form single" onSubmit={async (event) => { event.preventDefault(); setBusy(true); try { const res = await api.backup(password); await downloadResponse(res, "awg-forge-backup.afbackup"); notify(m.backup.downloadStarted); } catch (err) { notify(errorMessage(err, m.common.requestFailed)); } finally { setBusy(false); } }}><label>{m.backup.password}<input aria-label={m.backup.password} type="password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label><button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{m.backup.create}</ButtonContent></button></form>;
}

function RestorePanel({ report, setReport, notify }: { report: RestoreReport | null; setReport: (report: RestoreReport | null) => void; notify: (message: string) => void }) {
  const { m } = useI18n();
  const file = useRef<File | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  return <div class="stack"><form class="form single" onSubmit={async (event) => { event.preventDefault(); if (!file.current) { notify(m.backup.fileRequired); return; } setBusy(true); try { setReport((await api.restoreVerify(file.current, password)).report); notify(m.backup.verified); } catch (err) { notify(errorMessage(err, m.common.requestFailed)); } finally { setBusy(false); } }}><label>{m.backup.file}<input aria-label={m.backup.file} type="file" accept=".afbackup,application/octet-stream" onInput={(event) => { file.current = (event.currentTarget as HTMLInputElement).files?.[0] || null; }} /></label><label>{m.common.password}<input aria-label={m.common.password} type="password" value={password} onInput={(event) => setPassword((event.currentTarget as HTMLInputElement).value)} /></label><button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{m.backup.verify}</ButtonContent></button></form>{report && <div class="metric-grid"><Metric title={m.backup.format} value={report.format} /><Metric title={m.backup.schema} value={String(report.schema)} /><Metric title={m.dashboard.tunnels} value={String(report.tunnels.length)} /><Metric title={m.tunnel.clients} value={String(report.client_count)} /></div>}</div>;
}

function Form({ title, subtitle, submit, secondary, onSubmit, children }: { title: string; subtitle: string; submit: string; secondary?: preact.ComponentChildren; onSubmit: (form: HTMLFormElement) => Promise<void>; children: preact.ComponentChildren }) {
  const [busy, setBusy] = useState(false);
  return <PanelTitle title={title} subtitle={subtitle}><form class="form" onSubmit={async (event) => { event.preventDefault(); setBusy(true); try { await onSubmit(event.currentTarget as HTMLFormElement); } finally { setBusy(false); } }}>{children}<div class="form-actions">{secondary}<button class="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{submit}</ButtonContent></button></div></form></PanelTitle>;
}

function Dialog({ onClose, children }: { onClose: () => void; children: preact.ComponentChildren }) {
  const { m } = useI18n();
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
        <button class="button icon close" type="button" onClick={onClose} aria-label={m.common.close}>×</button>
        {children}
      </dialog>
    </div>
  );
}

function PanelTitle({ title, subtitle, children }: { title: string; subtitle: string; children: preact.ComponentChildren }) {
  return <div class="stack"><div class="modal-head"><div><h2>{title}</h2><p>{subtitle}</p></div></div>{children}</div>;
}

function ResultList({ results }: { results: Array<{ level: Level; area: string; message: string }> | null }) {
  const { m } = useI18n();
  if (!results) return <p>{m.maintenance.noResults}</p>;
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
  const { m } = useI18n();
  return <>{busy && <span class="spinner" aria-hidden="true" />}<span>{busy ? m.common.working : children}</span></>;
}

function Toast({ message }: { message: string }) {
  return <div className={classNames("toast", message && "show")}>{message}</div>;
}

function Splash() {
  const { m } = useI18n();
  return <main class="login-shell"><section class="panel login-card"><Brand /><p>{m.login.loading}</p></section></main>;
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

function trafficLimitGiBValue(value: number | null | undefined): string {
  if (!value) return "";
  return String(Number((value / 1024 / 1024 / 1024).toFixed(3)));
}

function trafficLimitBytesFromForm(form: HTMLFormElement, invalidMessage: string): number | null {
  const raw = field(form, "traffic_limit_gib").replace(",", ".").trim();
  if (!raw) return null;
  const gib = Number(raw);
  if (!Number.isFinite(gib) || gib <= 0) throw new Error(invalidMessage);
  const bytes = Math.round(gib * 1024 * 1024 * 1024);
  if (bytes <= 0) throw new Error(invalidMessage);
  return bytes;
}

function errorMessage(err: unknown, fallback = "request failed"): string {
  return err instanceof Error ? err.message : fallback;
}

function clientStatusText(status: ReturnType<typeof activeLabel> | "expired", m: Messages): string {
  if (status === "active now") return m.status.activeNow;
  if (status === "seen recently") return m.status.seenRecently;
  if (status === "never seen") return m.status.neverSeen;
  if (status === "expired") return m.status.expired;
  return m.status.offline;
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
