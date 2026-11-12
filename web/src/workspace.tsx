import { Button, ChoiceGroup, SelectField } from "./ui";
import { useMediaQuery } from "@mantine/hooks";
import * as api from "./api";
import { useI18n } from "./context";
import { errorMessage, toneFromLevel } from "./errors";
import { versionLabel } from "./form-values";
import type { Locale } from "./i18n";
import { ForgeMark, Icon } from "./icons";
import { ServerNameForm } from "./forms";
import type { IconName } from "./icons";
import { MaintenanceCenter, TrafficPanel } from "./maintenance";
import { navigate, navigationEvent } from "./navigation";
import { Badge, Dialog, Empty } from "./shared";
import type { AppState, Client, Profile, TrafficSummary, Tunnel } from "./types";
import { activeLabel, classNames, dateOnly, formatBytes, isExperimentalProfile, profileTitle, relativeTime } from "./utils";
import { Activity, Fragment, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

type ShellProps = {
  state: AppState;
  theme: string;
  setTheme: (theme: string) => void;
  locale: Locale;
  setLocale: (locale: Locale) => void;
  logout: () => void;
  openMaintenance: () => void;
  resetTunnelFilter: () => void;
  notify: (message: string) => void;
  reload: () => Promise<void>;
  renderClients?: () => ReactNode;
  renderClient?: (client: Client, tunnel: Tunnel) => ReactNode;
  onCreateTunnel?: () => void;
  onCreateClient?: (tunnel?: Tunnel) => void;
  overlays?: ReactNode;
  children: ReactNode;
};

export function Shell(props: ShellProps) {
  const { state, theme, setTheme, locale, setLocale, logout, children } = props;
  const { m } = useI18n();
  const [view, setView] = useState(() => {
    const section = currentSection();
    return { section, visited: [section] };
  });
  const { section, visited } = view;
  const activeSection = useRef(section);
  const sectionScroll = useRef(new Map<string, number>());
  const [tunnelNavigation, setTunnelNavigation] = useState(0);
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const preferencesTrigger = useRef<HTMLButtonElement | null>(null);
  const [serverNameOpen, setServerNameOpen] = useState(false);
  const serverNameTrigger = useRef<HTMLButtonElement | null>(null);
  const panelAddress = globalThis.location.host;
  const editServerName = state.server_name ? m.serverName.edit : m.serverName.add;
  useLayoutEffect(() => {
    globalThis.scrollTo({ top: sectionScroll.current.get(section) || 0, behavior: "instant" });
  }, [section]);
  useEffect(() => {
    const previousRestoration = globalThis.history.scrollRestoration;
    globalThis.history.scrollRestoration = "manual";
    const onHash = () => {
      const next = currentSection();
      if (next !== activeSection.current) {
        sectionScroll.current.set(activeSection.current, globalThis.scrollY);
        activeSection.current = next;
      }
      setView((current) => current.section === next ? current : {
        section: next, visited: current.visited.includes(next) ? current.visited : [...current.visited, next],
      });
    };
    globalThis.addEventListener("hashchange", onHash);
    globalThis.addEventListener("popstate", onHash);
    globalThis.addEventListener(navigationEvent, onHash);
    return () => {
      globalThis.history.scrollRestoration = previousRestoration;
      globalThis.removeEventListener("hashchange", onHash);
      globalThis.removeEventListener("popstate", onHash);
      globalThis.removeEventListener(navigationEvent, onHash);
    };
  }, []);
  const navigation: Array<{ id: string; label: string; icon: IconName }> = [
    { id: "overview", label: m.ui.overview, icon: "home" },
    { id: "tunnels", label: m.dashboard.tunnels, icon: "tunnels" },
    { id: "clients", label: m.tunnel.clients, icon: "clients" },
    { id: "traffic", label: m.maintenance.tabs.traffic, icon: "traffic" },
    { id: "maintenance", label: m.common.maintenance, icon: "settings" },
  ];
  function openTunnel(tunnel: Tunnel) {
    localStorage.setItem("awg-forge.selected-tunnel", tunnel.id);
    props.resetTunnelFilter();
    // An explicit overview selection opens its target without a stale local search.
    setTunnelNavigation((value) => value + 1);
    sectionScroll.current.set("tunnels", 0);
    navigate("tunnels");
  }
  return <main className="app-shell">
    <aside className="sidebar">
      <div className="sidebar-top"><Brand /><Button className="server-scope" aria-label={editServerName} title={editServerName} onClick={(event) => { serverNameTrigger.current = event.currentTarget; setServerNameOpen(true); }}><Icon name="server" /><div><strong title={state.server_name || m.ui.server}>{state.server_name || m.ui.server}</strong><span title={m.serverName.panelAddress}>{panelAddress}</span></div><Icon name="edit" /></Button></div>
      <nav className="main-nav" aria-label={m.aria.globalActions}>{navigation.map((item) => <Button key={item.id} type="button" className={classNames(section === item.id && "active")} aria-current={section === item.id ? "page" : undefined} onClick={() => navigate(item.id)}><Icon name={item.icon} />{item.label}</Button>)}</nav>
      <div className="sidebar-bottom">
        <Button className="sidebar-setting" type="button" aria-label={m.ui.interfaceSettings} title={m.ui.interfaceSettings} onClick={(event) => { preferencesTrigger.current = event.currentTarget; setPreferencesOpen(true); }}><Icon name={theme === "dark" ? "moon" : "sun"} /><span>{m.ui.appearance}</span></Button>
        <Button className="sidebar-setting" type="button" aria-label={m.common.logOut} title={m.common.logOut} onClick={logout}><Icon name="logout" /><span>{m.common.logOut}</span></Button>
        <FooterLinks />
      </div>
    </aside>
    <div className="workspace-main">
      {visited.map((id) => <Activity key={id} mode={section === id ? "visible" : "hidden"}>
        {id === "tunnels" ? <Fragment key={tunnelNavigation}>{children}</Fragment> : <>
          <div className="page-heading"><div><h1>{id === "overview" ? (locale === "ru" ? "Обзор сервера" : "Server overview") : navigation.find((item) => item.id === id)?.label}</h1>{id === "overview" && <p className="page-description">{locale === "ru" ? "Туннели, клиенты и трафик" : "Tunnels, clients and traffic"}</p>}</div>{id === "overview" && props.onCreateTunnel && <Button className="button primary" type="button" disabled={!state.profiles.some((profile) => profile.available)} onClick={props.onCreateTunnel}><Icon name="plus" />{m.common.createTunnel}</Button>}</div>
          <div className="content-page">{id === "overview" ? <Overview state={state} openTunnel={openTunnel} navigate={navigate} renderClient={props.renderClient} onCreateClient={props.onCreateClient} /> : id === "clients" ? props.renderClients?.() : id === "traffic" ? <TrafficView state={state} /> : <MaintenanceCenter embedded state={state} notify={props.notify} reload={props.reload} close={() => navigate("tunnels")} />}</div>
        </>}
      </Activity>)}
      {props.overlays}
      {preferencesOpen && <Dialog title={m.ui.interfaceSettings} size="30rem" returnFocusTo={preferencesTrigger.current} onClose={() => setPreferencesOpen(false)}>
        <div className="interface-settings">
          <p className="note">{m.ui.interfaceSettingsHelp}</p>
          <ChoiceGroup label={m.ui.theme} value={theme} onChange={setTheme} options={[{ value: "light", label: <><Icon name="sun" />{m.ui.lightTheme}</> }, { value: "dark", label: <><Icon name="moon" />{m.ui.darkTheme}</> }]} />
          <ChoiceGroup label={m.ui.language} value={locale} onChange={(value) => setLocale(value as Locale)} options={[{ value: "ru", label: "Русский" }, { value: "en", label: "English" }]} />
          <div className="interface-server"><span className="field-title">{m.serverName.title}</span><Button className="button server-name-action" onClick={() => { serverNameTrigger.current = preferencesTrigger.current; setPreferencesOpen(false); setServerNameOpen(true); }}><Icon name="server" /><span className="server-name-text">{state.server_name || m.serverName.add}</span><Icon name="edit" /></Button><p className="help">{m.serverName.storage}</p></div>
          <div className="interface-footer"><FooterLinks showProjectLabel /><Button className="button" type="button" onClick={() => setPreferencesOpen(false)}>{m.common.close}</Button></div>
        </div>
      </Dialog>}
      {serverNameOpen && <Dialog title={m.serverName.title} size="30rem" returnFocusTo={serverNameTrigger.current} onClose={() => setServerNameOpen(false)}><ServerNameForm state={state} save={async (name) => { await api.updateServerName(name); await props.reload(); setServerNameOpen(false); props.notify(m.serverName.saved); }} /></Dialog>}
    </div>
  </main>;
}

function currentSection(): string {
  const section = globalThis.location.hash.replace(/^#\/?/, "").split("/")[0];
  return ["overview", "tunnels", "clients", "traffic", "maintenance"].includes(section) ? section : "overview";
}

function Overview({ state, openTunnel, navigate, renderClient, onCreateClient }: {
  state: AppState; openTunnel: (tunnel: Tunnel) => void; navigate: (section: string) => void;
  renderClient?: (client: Client, tunnel: Tunnel) => ReactNode; onCreateClient?: (tunnel?: Tunnel) => void;
}) {
  const { m, locale } = useI18n();
  const [traffic, setTraffic] = useState<TrafficSummary | null>(null);
  const [trafficError, setTrafficError] = useState("");
  const [loading, setLoading] = useState(true);
  const hasTraffic = useRef(false);
  const requestID = useRef(0);
  const mounted = useRef(false);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; requestID.current += 1; };
  }, []);
  const copy = locale === "ru" ? {
    allTunnels: "Все туннели", allClients: "Все клиенты", recorded: "Записанный трафик", period: "Сумма за период",
    traffic30d: "Трафик за 30 дней", trafficPair: "Получено и отправлено", history: "История трафика",
    running: "работают", stopped: "не работают", active: "активны сейчас", name: "Название", egress: "Выход",
  } : {
    allTunnels: "All tunnels", allClients: "All clients", recorded: "Recorded traffic", period: "Total per period",
    traffic30d: "Traffic in 30 days", trafficPair: "Received and sent", history: "Traffic history",
    running: "up", stopped: "down", active: "active now", name: "Name", egress: "Egress",
  };
  const loadTraffic = useCallback(async () => {
    if (!state.database?.enabled) { hasTraffic.current = false; setLoading(false); setTraffic(null); setTrafficError(""); return; }
    const id = ++requestID.current;
    if (!hasTraffic.current) setLoading(true);
    try {
      const next = await api.trafficSummary();
      if (!mounted.current || id !== requestID.current) return;
      hasTraffic.current = true;
      setTraffic(next); setTrafficError("");
    } catch (err) {
      if (mounted.current && id === requestID.current) setTrafficError(errorMessage(err, m.common.requestFailed));
    } finally {
      if (mounted.current && id === requestID.current) setLoading(false);
    }
  }, [state.database?.enabled, m.common.requestFailed]);
  useEffect(() => { void loadTraffic(); }, [loadTraffic]);
  const allClients = state.tunnels.flatMap((tunnel) => tunnel.clients.map((client) => ({ client, tunnel })));
  const activeCount = allClients.filter(({ client }) => client.enabled && !client.expired && activeLabel(client.runtime?.last_seen_at || client.last_seen_at) === "active now").length;
  const totals = (traffic?.rows || []).reduce((sum, row) => ({ today: sum.today + row.rx_today + row.tx_today, week: sum.week + row.rx_7d + row.tx_7d, month: sum.month + row.rx_30d + row.tx_30d }), { today: 0, week: 0, month: 0 });
  const hasHistory = Boolean(traffic?.enabled && traffic.rows?.length);
  const periods = [{ label: m.maintenance.today, value: totals.today }, { label: m.maintenance.last7d, value: totals.week }, { label: m.maintenance.last30d, value: totals.month }];
  const scale = Math.max(...periods.map((period) => period.value), 1);
  return <div className="overview">
    <div className="overview-metrics">
      <section className="overview-metric metric-lead"><Icon name="tunnels" /><div><span>{m.dashboard.tunnels}</span><strong>{state.tunnels.length}</strong><small>{state.tunnels.filter((tunnel) => tunnel.status?.up === true).length} {copy.running} · {state.tunnels.filter((tunnel) => tunnel.status?.up === false).length} {copy.stopped}{state.tunnels.some((tunnel) => !tunnel.status) && <> · {state.tunnels.filter((tunnel) => !tunnel.status).length} {m.ui.noRuntimeData}</>}</small></div></section>
      <section className="overview-metric"><Icon name="clients" /><div><span>{m.tunnel.clients}</span><strong>{allClients.length}</strong><small>{activeCount} {copy.active}</small></div></section>
      <section className="overview-metric"><Icon name="traffic" /><div><span>{copy.traffic30d}</span><strong aria-busy={loading}>{hasHistory ? formatBytes(totals.month) : "—"}</strong><small>{copy.trafficPair}</small></div></section>
    </div>
    <div className="overview-middle">
      <section className="overview-tunnels"><div className="section-head"><h2>{m.dashboard.tunnels}</h2><Button className="text-link" type="button" onClick={() => navigate("tunnels")}>{copy.allTunnels}<Icon name="chevron" /></Button></div>
        {!state.tunnels.length ? <Empty title={m.dashboard.noTunnelsYet} text={m.dashboard.createFirstTunnel} /> : <div className="overview-table"><div className="overview-table-head" aria-hidden="true"><span>{copy.name}</span><span>{m.tunnel.protocol}</span><span className="numeric">UDP</span><span>{copy.egress}</span><span>{m.ui.status}</span><span /></div>{state.tunnels.slice(0, 5).map((tunnel) => <Button className="overview-table-row overview-card" type="button" key={tunnel.id} onClick={() => openTunnel(tunnel)} aria-label={`${m.ui.viewTunnel}: ${tunnel.name}`}><strong>{tunnel.name}</strong><span className="overview-protocol">{profileTitle(tunnel.profile)}{isExperimentalProfile(tunnel.profile) && <Badge tone="brand">{m.common.experimental}</Badge>}</span><span className="numeric"><span className="mobile-label">UDP </span>{tunnel.listen_port}</span><span>{tunnel.egress_mode === "warp" ? "WARP" : m.ui.server}</span><span className="table-status"><span className={classNames("status-dot", tunnel.status?.up ? "up" : "down")} />{tunnel.status ? tunnel.status.up ? m.status.up : m.status.down : m.ui.runtimeUnknown}</span><Icon name="chevron" /></Button>)}</div>}
      </section>
      <section className="overview-traffic"><div className="section-head"><div><h2>{copy.recorded}</h2><p>{copy.period}</p></div></div>
        {loading ? <output className="overview-traffic-state">{m.common.loading}</output> : trafficError ? <div className="overview-traffic-state"><p className="form-error" role="alert">{trafficError}</p><Button className="button" type="button" onClick={() => void loadTraffic()}>{m.common.retry}</Button></div> : !state.database?.enabled || !traffic?.enabled ? <p className="overview-traffic-state note">{m.maintenance.trafficDisabled}</p> : !hasHistory ? <p className="overview-traffic-state note">{m.maintenance.noTrafficData}</p> : <div className="period-bars">{periods.map((period) => <div className="period-row" key={period.label}><span>{period.label}</span><div className="period-track" aria-hidden="true"><div className="period-fill" style={{ width: `${period.value / scale * 100}%` }} /></div><strong>{formatBytes(period.value)}</strong></div>)}</div>}
        <Button className="text-link traffic-history-link" type="button" onClick={() => navigate("traffic")}>{copy.history}<Icon name="chevron" /></Button>
      </section>
    </div>
    <section className="overview-clients"><div className="section-head"><div><h2>{m.tunnel.clients}</h2><p>{m.tunnel.clientTotal(allClients.length)}</p></div><Button className="text-link" type="button" onClick={() => navigate("clients")}>{copy.allClients}<Icon name="chevron" /></Button></div>
      {!allClients.length ? <Empty title={m.tunnel.noClientsYet} text={state.tunnels.length ? m.tunnel.createFirstClient : m.dashboard.createFirstTunnel} action={onCreateClient && state.tunnels.length ? <Button className="button primary" onClick={() => onCreateClient()}><Icon name="plus" />{m.common.createClient}</Button> : undefined} /> : renderClient ? <div className="client-table has-tunnel"><ClientTableHead withTunnel />{allClients.slice(0, 4).map(({ client, tunnel }) => renderClient(client, tunnel))}</div> : <Button className="button" type="button" onClick={() => navigate("clients")}>{copy.allClients}</Button>}
    </section>
  </div>;
}

function TrafficView({ state }: { state: AppState }) {
  return <TrafficPanel state={state} />;
}

export function AllClients({ state, renderClient, onCreateClient, onCreateTunnel, tunnelFilter, setTunnelFilter }: {
  state: AppState; renderClient: (client: Client, tunnel: Tunnel) => ReactNode; onCreateClient?: (tunnel?: Tunnel) => void; tunnelFilter: string; setTunnelFilter: (value: string) => void; onCreateTunnel: () => void;
}) {
  const { m, locale } = useI18n();
  const [query, setQuery] = useState("");
  const effectiveFilter = state.tunnels.some((tunnel) => tunnel.id === tunnelFilter) ? tunnelFilter : "all";
  const clients = state.tunnels.flatMap((tunnel) => tunnel.clients.map((client) => ({ client, tunnel }))).filter(({ client, tunnel }) => (effectiveFilter === "all" || tunnel.id === effectiveFilter) && `${client.name} ${client.address} ${client.notes} ${tunnel.name}`.toLowerCase().includes(query.toLowerCase()));
  const clientTunnel = state.tunnels.find((tunnel) => tunnel.id === effectiveFilter);
  return <section className="clients all-clients">
    <div className="clients-toolbar"><span>{m.tunnel.clientTotal(clients.length)}</span>{onCreateClient && <Button className="button primary" type="button" disabled={!state.tunnels.length} onClick={() => onCreateClient(clientTunnel)}><Icon name="plus" />{m.common.createClient}</Button>}</div>
    <div className="client-filters"><input className="client-search" type="search" aria-label={m.ui.searchClients} placeholder={m.ui.searchClients} value={query} onInput={(event) => setQuery(event.currentTarget.value)} /><SelectField className="toolbar-select" aria-label={m.aria.tunnelFilters} value={effectiveFilter} onChange={(value) => setTunnelFilter(value!)} data={[{ value: "all", label: locale === "ru" ? "Все туннели" : "All tunnels" }, ...state.tunnels.map((tunnel) => ({ value: tunnel.id, label: tunnel.name }))]} /></div>
    {clients.length ? <div className="client-table has-tunnel"><ClientTableHead withTunnel />{clients.map(({ client, tunnel }) => renderClient(client, tunnel))}</div> : <Empty title={!state.tunnels.length ? m.dashboard.noTunnelsYet : query ? m.ui.noMatches : m.tunnel.noClientsYet} text={query ? m.ui.searchClients : state.tunnels.length ? m.tunnel.createFirstClient : m.dashboard.createFirstTunnel} action={!state.tunnels.length ? <Button className="button primary" onClick={onCreateTunnel}>{m.common.createTunnel}</Button> : undefined} />}
  </section>;
}

export function Brand({ subtitle }: { subtitle?: ReactNode }) {
  return <div className="brand"><span className="brand-mark"><ForgeMark /></span><div><strong>AWG-Forge</strong>{subtitle && <p>{subtitle}</p>}</div></div>;
}

export function FooterLinks({ version, extra, showProjectLabel = false }: { version?: string; extra?: ReactNode; showProjectLabel?: boolean }) {
  const { locale, m } = useI18n();
  const label = version ? versionLabel(version) : "";
  const docsPath = locale === "ru" ? "ru" : "en";
  return (
    <footer className="app-footer" aria-label={m.aria.projectLinks}>
      <a className="footer-link icon" href="https://github.com/astronaut808/awg-forge" target="_blank" rel="noreferrer" aria-label="awg-forge on GitHub">
        <svg aria-hidden="true" viewBox="0 0 24 24">
          <path d="M12 2a10 10 0 0 0-3.16 19.49c.5.09.68-.22.68-.48v-1.69c-2.78.6-3.37-1.18-3.37-1.18-.45-1.15-1.11-1.46-1.11-1.46-.91-.62.07-.61.07-.61 1 .07 1.53 1.03 1.53 1.03.89 1.52 2.34 1.08 2.91.82.09-.65.35-1.08.63-1.33-2.22-.25-4.56-1.11-4.56-4.94 0-1.09.39-1.98 1.03-2.68-.1-.25-.45-1.27.1-2.64 0 0 .84-.27 2.75 1.02A9.57 9.57 0 0 1 12 7.01c.85 0 1.71.11 2.51.34 1.91-1.29 2.75-1.02 2.75-1.02.55 1.37.2 2.39.1 2.64.64.7 1.03 1.59 1.03 2.68 0 3.84-2.34 4.69-4.57 4.94.36.31.68.92.68 1.86v2.56c0 .27.18.58.69.48A10 10 0 0 0 12 2Z" />
        </svg>
        {showProjectLabel && <span>GitHub</span>}
      </a>
      <a className="footer-link" href={`https://github.com/astronaut808/awg-forge/tree/master/docs/${docsPath}`} target="_blank" rel="noreferrer">{m.common.docs}</a>
      {version && <span className="footer-version" title="awg-forge version">{label}</span>}
      {extra}
    </footer>
  );
}

export function TunnelFirstDashboard({ profiles, tunnels, filter, setFilter, requestedTunnelID, onRequestedTunnelOpened, onCreateTunnel, renderTunnel }: {
  profiles: Profile[]; tunnels: Tunnel[]; filter: string; setFilter: (filter: string) => void;
  requestedTunnelID: string | null; onRequestedTunnelOpened: () => void;
  onCreateTunnel: (profile?: Profile) => void; renderTunnel: (tunnel: Tunnel) => ReactNode;
}) {
  const { m } = useI18n();
  const [selectedID, setSelectedID] = useState(localStorage.getItem("awg-forge.selected-tunnel") || "");
  const [query, setQuery] = useState("");
  const compact = useMediaQuery("(max-width:760px)", undefined, { getInitialValueInEffect: false });
  const [showTools, setShowTools] = useState(false);
  useEffect(() => {
    if (!requestedTunnelID || !tunnels.some((tunnel) => tunnel.id === requestedTunnelID)) return;
    setSelectedID(requestedTunnelID);
    setFilter("all");
    setQuery("");
    onRequestedTunnelOpened();
  }, [tunnels, setFilter, requestedTunnelID, onRequestedTunnelOpened]);
  const availableFilters = profiles.filter((profile) => tunnels.some((tunnel) => tunnel.profile === profile.id));
  const effectiveFilter = availableFilters.some((profile) => profile.id === filter) ? filter : "all";
  const visible = tunnels.filter((tunnel) => (effectiveFilter === "all" || tunnel.profile === effectiveFilter) && `${tunnel.name} ${profileTitle(tunnel.profile)} ${tunnel.listen_port}`.toLowerCase().includes(query.toLowerCase()));
  const selected = visible.find((tunnel) => tunnel.id === selectedID) || visible[0];
  function select(id: string) { setSelectedID(id); localStorage.setItem("awg-forge.selected-tunnel", id); setShowTools(false); }
  return <>
    <div className="page-heading"><div><h1>{m.dashboard.tunnels}</h1><span>{m.dashboard.tunnelCount(tunnels.length)}</span></div>{tunnels.length > 0 && <Button className="button primary" type="button" disabled={!profiles.some((profile) => profile.available)} onClick={() => onCreateTunnel()}><Icon name="plus" />{m.common.createTunnel}</Button>}</div>
    <section className="tunnel-workspace">
      {compact && <div className="tunnel-mobile-picker"><label htmlFor="mobile-tunnel">{m.ui.selectedTunnel}</label><div><SelectField id="mobile-tunnel" value={selected?.id || ""} onChange={(value) => select(value!)} data={[...(!selected ? [{ value: "", label: m.ui.chooseTunnel }] : []), ...visible.map((tunnel) => ({ value: tunnel.id, label: `${tunnel.name} · ${profileTitle(tunnel.profile)}` }))]} /><Button className="button tunnel-tool-toggle" aria-label={m.ui.searchAndFilter} title={m.ui.searchAndFilter} aria-expanded={showTools} aria-controls="tunnel-tools" onClick={() => setShowTools(!showTools)}><Icon name="search" /></Button></div>{(effectiveFilter !== "all" || query) && <div className="tunnel-filter-state"><span className="help">{effectiveFilter !== "all" ? profileTitle(effectiveFilter) : ""}{query ? ` · ${query}` : ""}</span><Button className="button text" onClick={() => { setFilter("all"); setQuery(""); }}>{m.ui.clearTunnelFilters}</Button></div>}</div>}
      {(!compact || showTools) && <aside className="tunnel-nav" id="tunnel-tools" aria-label={m.dashboard.tunnels}>
        <div className="tunnel-nav-toolbar"><input type="search" aria-label={m.ui.searchTunnels} placeholder={m.ui.searchTunnels} value={query} onInput={(event) => setQuery(event.currentTarget.value)} />
          {availableFilters.length > 1 && <SelectField className="toolbar-select" aria-label={m.aria.tunnelFilters} value={effectiveFilter} onChange={(value) => setFilter(value!)} data={[{ value: "all", label: m.common.all }, ...availableFilters.map((profile) => ({ value: profile.id, label: profileTitle(profile.id) }))]} />}
        </div>
        {visible.map((tunnel) => <Button key={tunnel.id} className={classNames("tunnel-choice", tunnel.id === selected?.id && "active")} type="button" aria-current={tunnel.id === selected?.id ? "true" : undefined} onClick={() => select(tunnel.id)}><span className="tunnel-choice-head"><span className={classNames("status-dot", tunnel.status?.up ? "up" : "down")} /><strong>{tunnel.name}</strong><Icon name="chevron" /></span><span className="tunnel-choice-meta">{profileTitle(tunnel.profile)} · {tunnel.listen_port}</span></Button>)}
      </aside>}
      <div className="tunnel-details">{selected ? renderTunnel(selected) : <Empty title={tunnels.length ? m.dashboard.noTunnelsInFilter : m.dashboard.noTunnelsYet} text={tunnels.length ? m.ui.clearTunnelFilters : m.dashboard.createFirstTunnel} action={!tunnels.length ? <Button className="button primary" type="button" disabled={!profiles.some((profile) => profile.available)} onClick={() => onCreateTunnel()}>{m.common.createTunnel}</Button> : !compact ? <Button className="button" onClick={() => { setFilter("all"); setQuery(""); }}>{m.ui.clearTunnelFilters}</Button> : undefined} />}</div>
    </section>
  </>;
}

export function TunnelCard(props: {
  tunnel: Tunnel; onCreateClient: () => void; onSettings: () => void; onProtocol: () => void;
  onRestart: () => void; onRegenerate: () => void; onDelete: () => void;
  renderSettings: () => ReactNode; renderProtocol: () => ReactNode;
  onClientConfig: (client: Client) => void; onClientSettings: (client: Client) => void;
  onClientToggle: (client: Client) => void; onClientDelete: (client: Client) => void;
}) {
  const { m } = useI18n();
  const { tunnel } = props;
  const compact = useMediaQuery("(max-width:760px)", undefined, { getInitialValueInEffect: false });
  const [factsOpen, setFactsOpen] = useState(!compact);
  useEffect(() => { setFactsOpen(!compact); }, [compact, tunnel.id]);
  const [tab, setTab] = useState("clients");
  const [query, setQuery] = useState("");
  const clients = tunnel.clients.filter((client) => `${client.name} ${client.address} ${client.notes}`.toLowerCase().includes(query.toLowerCase()));
  return <article className="tunnel-card">
      <div className="tunnel-head"><div className="tunnel-title"><h2>{tunnel.name}</h2><Badge tone={tunnel.status?.up ? "ok" : "neutral"}>{!tunnel.status ? m.ui.runtimeUnknown : tunnel.status.up ? m.status.up : m.status.down}</Badge></div>
      <div className="header-actions">{tab !== "protocol" && <Button className="button text" type="button" onClick={props.onRegenerate}><Icon name="refresh" />{m.forms.regenerate}</Button>}<Button className="button text" type="button" onClick={props.onRestart}><Icon name="play" />{m.common.restart}</Button></div>
    </div>
    <nav className="tunnel-tabs" aria-label={m.ui.selectedTunnel}>
      <Button className={classNames(tab === "clients" && "active")} type="button" aria-current={tab === "clients" ? "page" : undefined} onClick={() => setTab("clients")}>{m.tunnel.clients}</Button>
      <Button className={classNames(tab === "connection" && "active")} type="button" aria-current={tab === "connection" ? "page" : undefined} onClick={() => setTab("connection")}>{m.ui.connection}</Button>
      <Button className={classNames(tab === "protocol" && "active")} type="button" aria-current={tab === "protocol" ? "page" : undefined} onClick={() => setTab("protocol")}>{m.ui.protocolParameters}</Button>
    </nav>
    <details className="tunnel-facts" open={!compact || factsOpen} onToggle={(event) => { if (compact) setFactsOpen(event.currentTarget.open); }}><summary>{m.ui.connectionSummary}<Icon name="chevron" /></summary><div className="facts">
      <Fact label={m.tunnel.protocol} value={profileTitle(tunnel.profile)} />
      <Fact label={m.forms.listenPort} value={String(tunnel.listen_port)} />
      <Fact label={m.tunnel.subnet} value={tunnel.subnet} />
      <Fact label="MTU" value={tunnel.mtu > 0 ? String(tunnel.mtu) : m.forms.auto} />
      <Fact label={m.tunnel.dns} value={tunnel.dns} />
      <Fact label={m.ui.internet} value={tunnel.egress_mode === "warp" ? "WARP → Internet" : m.ui.directEgress} />
    </div></details>
    <div className="badges">
      {tunnel.status?.firewall?.label && <Badge tone={toneFromLevel(tunnel.status.firewall.level)} title={tunnel.status.firewall.message}>{m.ui.firewallLabels[tunnel.status.firewall.label as keyof typeof m.ui.firewallLabels] || m.ui.firewallLabels["firewall unknown"]}</Badge>}
      {Number(tunnel.status?.stale_clients) > 0 && <Badge tone="warn">{m.tunnel.staleConfig(tunnel.status.stale_clients)}</Badge>}
      {isExperimentalProfile(tunnel.profile) && <Badge tone="warn">{m.common.experimental}</Badge>}
    </div>
    <div className="tunnel-content">
      {tab === "connection" ? <>{props.renderSettings()}<div className="form-actions"><Button className="button danger" type="button" onClick={props.onDelete}>{m.common.delete}</Button></div></> : tab === "protocol" ? props.renderProtocol() : <section className="clients">
        <div className="clients-toolbar"><div><h3>{m.tunnel.clients}</h3><span>{m.tunnel.clientTotal(tunnel.clients.length)}</span></div><Button className="button primary" type="button" onClick={props.onCreateClient}><Icon name="plus" />{m.common.createClient}</Button></div>
        {(tunnel.clients.length > 4 || query !== "") && <input className="client-search" type="search" aria-label={m.ui.searchClients} placeholder={m.ui.searchClients} value={query} onInput={(event) => setQuery(event.currentTarget.value)} />}
        {tunnel.clients.length === 0 ? <div className="empty compact">{m.tunnel.createFirstClient}</div> : <div className="client-table">
          <ClientTableHead />
          {clients.map((client) => <ClientRow key={client.id} client={client} onConfig={() => props.onClientConfig(client)} onSettings={() => props.onClientSettings(client)} onToggle={() => props.onClientToggle(client)} onDelete={() => props.onClientDelete(client)} />)}
          {!clients.length && <p className="empty compact">{m.ui.noMatches}</p>}
        </div>}
      </section>}
    </div>
  </article>;
}

function ClientTableHead({ withTunnel = false }: { withTunnel?: boolean }) {
  const { m } = useI18n();
  return <div className={classNames("client-table-head", withTunnel && "with-tunnel")} aria-hidden="true"><span>{m.ui.device}</span>{withTunnel && <span>{m.dashboard.tunnels}</span>}<span>{m.ui.status}</span><span>{m.tunnel.received}</span><span>{m.tunnel.sent}</span><span>{m.ui.lastActivity}</span><span>{m.ui.actions}</span></div>;
}

export function ClientRow({ client, tunnelName, onConfig, onSettings, onToggle, onDelete }: { client: Client; tunnelName?: string; onConfig: () => void; onSettings: () => void; onToggle: () => void; onDelete: () => void }) {
  const { m, locale } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const lastSeen = client.runtime?.last_seen_at || client.last_seen_at;
  const online = client.enabled && !client.expired && activeLabel(lastSeen) === "active now";
  const visualState = online ? "online" : client.enabled && !client.expired ? "enabled" : "disabled";
  const visualStatus = client.expired ? m.status.expired : online ? m.status.activeNow : client.enabled ? m.common.enabled : m.common.disabled;
  const enableBlocked = !client.enabled && Boolean(client.traffic?.exceeded);
  return <div className={classNames("client-row", `client-status-${visualState}`, tunnelName && "with-tunnel", expanded && "expanded")}>
    <div className="client-summary">
      <div className="client-identity"><Icon name="clients" /><div><strong className="client-name" title={client.name}>{client.name}</strong><span className="mono">{client.address}</span></div></div>
      {tunnelName && <span className="client-tunnel-name" title={tunnelName}><span className="mobile-label">{m.dashboard.tunnels}: </span>{tunnelName}</span>}
      <div className="client-status-cell"><Badge tone={client.expired ? "warn" : online ? "ok" : "neutral"}>{visualStatus}</Badge>{client.traffic?.exceeded && <Badge tone="bad">{m.status.limitExceeded}</Badge>}{client.needs_new_config && <Badge tone="warn">{m.common.stale}</Badge>}</div>
      <span className="client-counter rx"><span className="sr-only">{m.tunnel.received}: </span><span className="mobile-label" aria-hidden="true">{m.tunnel.received} </span><span className="counter-value">{client.runtime?.present ? formatBytes(client.runtime.rx_bytes) : "—"}</span></span>
      <span className="client-counter tx"><span className="sr-only">{m.tunnel.sent}: </span><span className="mobile-label" aria-hidden="true">{m.tunnel.sent} </span><span className="counter-value">{client.runtime?.present ? formatBytes(client.runtime.tx_bytes) : "—"}</span></span>
      <span className="client-lastseen"><span className="sr-only">{m.ui.lastActivity}: </span>{relativeTime(lastSeen, m.time, locale)}</span>
      <div className="client-actions"><Button className="button" type="button" aria-label={m.ui.clientActions} title={m.ui.clientActions} aria-expanded={expanded} aria-controls={`export-${client.id}`} onClick={() => setExpanded(!expanded)}><Icon name="settings" /><span className="client-action-label">{m.ui.clientActions}</span><span className={classNames("config-chevron", expanded && "expanded")}><Icon name="chevron" /></span></Button></div>
    </div>
    {client.expires_at && <p className="client-detail">{m.tunnel.expires} {dateOnly(client.expires_at, locale)}</p>}
    {client.traffic?.enabled && (expanded || Boolean(client.traffic.limit_bytes)) && <p className="client-detail">{m.tunnel.totalTraffic} {formatBytes(client.traffic.rx_total + client.traffic.tx_total)}{client.traffic.limit_bytes ? <> · {client.traffic.limit_period === "rolling_30d" ? m.forms.trafficLimitRolling30Days : m.forms.trafficLimitLifetime}: {formatBytes(client.traffic.limit_usage_bytes)} / {formatBytes(client.traffic.limit_bytes)}</> : null}</p>}
    {client.traffic?.exceeded && <p className="client-detail note">{client.expired ? m.ui.expiredQuotaHelp : client.traffic.limit_period === "rolling_30d" ? m.forms.trafficLimitRolling30DaysExceededHelp : m.forms.trafficLimitExceededHelp}</p>}
    {client.notes && <p className="client-detail note">{client.notes}</p>}
    {expanded && <div className="client-expansion" id={`export-${client.id}`}>
      <div className="actions"><Button className="button" type="button" onClick={onConfig}><Icon name="qr" />{m.ui.connectClient}</Button><Button className="button" type="button" onClick={onSettings}>{m.common.edit}</Button><Button className="button" type="button" disabled={enableBlocked} title={enableBlocked ? m.forms.trafficLimitEnableBlocked : undefined} onClick={onToggle}>{client.enabled ? m.common.disable : m.common.enable}</Button><Button className="button danger" type="button" onClick={onDelete}>{m.common.delete}</Button></div>
    </div>}
  </div>;
}

function Fact({ label, value, extra }: { label: string; value: string; extra?: string }) {
  return <div className="fact"><span>{label}</span><strong className="mono">{value}</strong>{extra && <em>{extra}</em>}</div>;
}
