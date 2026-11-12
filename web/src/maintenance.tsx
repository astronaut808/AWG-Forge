import { FileInput, PasswordInput } from "@mantine/core";
import { Icon } from "./icons";
import { Button } from "./ui";
import * as api from "./api";
import { useI18n } from "./context";
import { backupErrorMessage, errorMessage, toneFromLevel } from "./errors";
import type { Messages } from "./i18n";
import { maintenanceCopy, maintenanceDate } from "./maintenance-copy";
import { Badge, ButtonContent, Metric, PanelTitle } from "./shared";
import type { AppState, AuditEvent, DoctorResult, FirewallReport, Level, RestoreReport, TrafficSummary, TrafficSummaryRow } from "./types";
import type { MaintenanceTab } from "./ui-types";
import { classNames, downloadResponse, formatBytes } from "./utils";
import { useCallback, useEffect, useRef, useState } from "react";

type ResultListItem = { level: Level; category?: string; area: string; message: string };
const doctorCategoryOrder = ["system", "security", "database", "network", "firewall", "tunnels", "clients", "warp"];

export function MaintenanceCenter({ state, notify, reload, embedded = false }: { embedded?: boolean; state: AppState; notify: (message: string) => void; close: () => void; reload: () => Promise<void> }) {
  const { m, locale } = useI18n();
  const [tab, setTab] = useState<MaintenanceTab>("overview");
  const [doctorResults, setDoctorResults] = useState<DoctorResult[] | null>(null);
  const [firewall, setFirewall] = useState<FirewallReport | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [auditLoading, setAuditLoading] = useState(false);
  const [auditError, setAuditError] = useState("");
  const [auditStale, setAuditStale] = useState(false);
  const [lastAuditSuccess, setLastAuditSuccess] = useState<Date | null>(null);
  const [auditRefreshRequest, setAuditRefreshRequest] = useState(0);
  const [restore, setRestore] = useState<RestoreReport | null>(null);
  const [busyAction, setBusyAction] = useState("");
  const messagesRef = useRef(m);
  const auditSucceeded = useRef(false);
  messagesRef.current = m;

  useEffect(() => {
    if (tab !== "logs") return;
    let closed = false;
    let inFlight = false;
    let requestID = 0;

    async function loadAuditLog() {
      if (inFlight) return;
      inFlight = true;
      const id = ++requestID;
      setAuditLoading(true);
      try {
        const res = await api.auditLog();
        if (closed || id !== requestID) return;
        setEvents(sortNewestFirst(res.events));
        setAuditError("");
        setAuditStale(false);
        setLastAuditSuccess(new Date());
        auditSucceeded.current = true;
      } catch (err) {
        if (closed || id !== requestID) return;
        setAuditError(errorMessage(err, messagesRef.current.common.requestFailed));
        setAuditStale(auditSucceeded.current);
      } finally {
        inFlight = false;
        if (!closed && id === requestID) setAuditLoading(false);
      }
    }

    void loadAuditLog();
    const timer = globalThis.setInterval(() => void loadAuditLog(), 3500);
    return () => {
      closed = true;
      globalThis.clearInterval(timer);
    };
  }, [tab, auditRefreshRequest]);

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

  const repairableFirewallIssue = state.apply_enabled && Boolean(doctorResults?.some((result) => result.category === "firewall" && result.level !== "ok"));

  return <PanelTitle title={m.maintenance.title} hideTitle={embedded} subtitle={embedded ? "" : m.maintenance.subtitle}>
    <nav className="subtabs">{(["overview", "doctor", "warp", "backup", "traffic", "logs", "support"] as MaintenanceTab[]).map((item) => <Button key={item} className={classNames("button", tab === item && "active")} type="button" aria-current={tab === item ? "page" : undefined} onClick={() => setTab(item)}>{m.maintenance.tabs[item]}</Button>)}</nav>
    {tab === "overview" && <MaintenanceOverview state={state} />}
    {tab === "doctor" && <div className="stack"><div><p className="note">{maintenanceCopy(locale).doctorHelp}</p><Button className="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("doctor", m.maintenance.doctorCompleted, async () => { const report = await api.doctor(); setDoctorResults(report.results); setFirewall(null); })}><ButtonContent busy={busyAction === "doctor"}>{m.maintenance.runDoctor}</ButtonContent></Button></div><ResultList results={doctorResults} />{repairableFirewallIssue && <section className="stack maintenance-action"><div><h3>{m.maintenance.firewall}</h3><p className="note">{m.maintenance.firewallNote}</p></div><Button className="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("firewall", m.maintenance.firewallRepaired, async () => setFirewall((await api.firewallRepair()).firewall))}><ButtonContent busy={busyAction === "firewall"}>{m.maintenance.repairFirewall}</ButtonContent></Button>{firewall && <ResultList results={firewall.results.map((item) => ({ level: item.status === "ok" ? "ok" : item.status === "duplicate" ? "warn" : "fail", area: `${item.tunnel}/${item.name}`, message: item.message || item.rule }))} />}</section>}</div>}
    {tab === "warp" && <WarpPanel state={state} action={action} busyAction={busyAction} />}
    {tab === "backup" && <BackupRestorePanel report={restore} setReport={setRestore} notify={notify} />}
    {tab === "support" && <SupportPanel state={state} action={action} busyAction={busyAction} />}
    {tab === "logs" && <AuditPanel events={events} loading={auditLoading} error={auditError} stale={auditStale} lastSuccess={lastAuditSuccess} retry={() => setAuditRefreshRequest((value) => value + 1)} />}
    {tab === "traffic" && <TrafficPanel state={state} />}
  </PanelTitle>;
}

function AuditPanel({ events, loading, error, stale, lastSuccess, retry }: { events: AuditEvent[]; loading: boolean; error: string; stale: boolean; lastSuccess: Date | null; retry: () => void }) {
  const { locale, m } = useI18n();
  const copy = maintenanceCopy(locale);
  return <div className="stack"><p className="note">{m.maintenance.auditAutoRefresh}</p>
    {lastSuccess && <p className="resource-meta">{copy.lastUpdated}: {maintenanceDate(lastSuccess, locale)}</p>}
    {error && <div className="resource-error" role="alert"><p>{copy.auditUnavailable}</p><p className="resource-meta">{error}</p><Button className="button" disabled={loading} type="button" onClick={retry}>{copy.retry}</Button>{stale && <p>{copy.staleAudit}</p>}</div>}
    {loading && !events.length ? <p>{copy.loadingAudit}</p> : !events.length && !error ? <div className="empty compact">{m.maintenance.noAuditEvents}</div> : <div className="list">{events.map((event) => <AuditRow event={event} key={`${event.time}-${event.event}`} />)}</div>}
  </div>;
}

function AuditRow({ event }: { event: AuditEvent }) {
  const { locale } = useI18n();
  const copy = maintenanceCopy(locale);
  const level = event.level.toLowerCase() as keyof typeof copy.severity;
  const label = copy.events[event.event as keyof typeof copy.events] || event.event.replace(/[._-]+/g, " ");
  const severity = copy.severity[level] || copy.severity.info;
  const tone: Level = level === "error" || level === "bad" ? "bad" : level === "fail" ? "fail" : level === "warn" ? "warn" : level === "ok" ? "ok" : "neutral";
  return <div className="row audit-row"><div><strong>{label}</strong><p>{maintenanceDate(event.time, locale)} · <span className={`audit-severity audit-severity-${level}`}>{severity}</span></p><p className="resource-meta">{event.event}</p>{event.message && <p>{event.message}</p>}{event.error && <p className="audit-error">{event.error}</p>}</div><Badge tone={tone}>{severity}</Badge></div>;
}

export function MaintenanceOverview({ state }: { state: AppState }) {
  const { m } = useI18n();
  const clients = state.tunnels.flatMap((tunnel) => tunnel.clients);
  const enabledClients = clients.filter((client) => client.enabled && !client.expired).length;
  const upTunnels = state.tunnels.filter((tunnel) => tunnel.status?.up).length;

  return <div className="metric-grid">
    <Metric title={m.dashboard.tunnels} value={`${upTunnels}/${state.tunnels.length} ${m.status.up}`} />
    <Metric title={m.tunnel.clients} value={`${enabledClients}/${clients.length} ${m.common.enabled}`} />
    {!state.apply_enabled && <Metric title={m.maintenance.applyConfig} value={m.common.manual} />}
    {state.warp.enabled_tunnel_count > 0 && <Metric title="WARP" value={m.warp.tunnelCount(state.warp.enabled_tunnel_count)} />}
  </div>;
}

export function TrafficPanel({ state }: { state: AppState }) {
  const { m, locale } = useI18n();
  const [traffic, setTraffic] = useState<TrafficSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(Boolean(state.database?.enabled));
  const [error, setError] = useState("");
  const [stale, setStale] = useState(false);
  const [lastSuccess, setLastSuccess] = useState<Date | null>(null);
  const requestID = useRef(0);
  const mounted = useRef(true);
  const hasTraffic = useRef(false);
  const requestFailed = useRef(m.common.requestFailed);
  requestFailed.current = m.common.requestFailed;
  const databaseEnabled = Boolean(state.database?.enabled);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; requestID.current += 1; };
  }, []);
  const loadTraffic = useCallback(async (manual = false) => {
    if (!databaseEnabled) { setLoading(false); return; }
    const id = ++requestID.current;
    if (!manual && !hasTraffic.current) setLoading(true);
    if (manual) setBusy(true);
    try {
      const next = await api.trafficSummary();
      if (!mounted.current || id !== requestID.current) return;
      hasTraffic.current = true;
      setTraffic(next); setError(""); setStale(false); setLastSuccess(new Date());
    } catch (err) {
      if (!mounted.current || id !== requestID.current) return;
      setError(errorMessage(err, requestFailed.current)); setStale(hasTraffic.current);
    } finally {
      if (mounted.current && id === requestID.current) { setLoading(false); setBusy(false); }
    }
  }, [databaseEnabled]);
  useEffect(() => {
    requestID.current += 1;
    if (!databaseEnabled) { hasTraffic.current = false; setTraffic(null); setError(""); setStale(false); setLoading(false); setBusy(false); setLastSuccess(null); return; }
    void loadTraffic();
  }, [databaseEnabled, loadTraffic]);
  const rows = trafficRowsWithNames(state, traffic?.rows || []);
  const totals = trafficTotals(rows);
  const topClients = [...rows].sort((a, b) => (b.rx_total + b.tx_total) - (a.rx_total + a.tx_total)).slice(0, 5);

  return <div className="stack">
    <div>
      <h3>{m.maintenance.traffic}</h3>
      {(!state.database?.enabled || traffic?.enabled === false) ? <p className="note">{m.maintenance.trafficDisabled}</p> : traffic?.enabled ? <p className="note">{m.maintenance.trafficText}</p> : null}
    </div>
    {state.database?.enabled && <div className="resource-toolbar"><Button className="button" disabled={busy || loading} type="button" onClick={() => { void loadTraffic(true); }}><ButtonContent busy={busy}>{error ? maintenanceCopy(locale).retry : m.maintenance.refreshTraffic}</ButtonContent></Button>{lastSuccess && <span className="resource-meta">{maintenanceCopy(locale).lastUpdated}: {maintenanceDate(lastSuccess, locale)}</span>}</div>}
    {error && <div className="resource-error" role="alert"><p>{maintenanceCopy(locale).trafficUnavailable}</p><p className="resource-meta">{error}</p>{stale && <p>{maintenanceCopy(locale).staleTraffic}</p>}</div>}
    {loading && !traffic ? <p>{maintenanceCopy(locale).loadingTraffic}</p> : !state.database?.enabled || (traffic && !traffic.enabled) ? <div className="empty compact">{m.maintenance.trafficDisabled}</div> : !traffic ? null : rows.length === 0 ? <div className="empty compact">{m.maintenance.noTrafficData}</div> : <>
      <div className="metric-grid">
        <Metric title={m.maintenance.totalTraffic} value={formatBytes(totals.rxTotal + totals.txTotal)} />
        <Metric title={m.maintenance.today} value={formatTrafficPair(totals.rxToday, totals.txToday, m)} />
        <Metric title={m.maintenance.last7d} value={formatTrafficPair(totals.rx7d, totals.tx7d, m)} />
        <Metric title={m.maintenance.last30d} value={formatTrafficPair(totals.rx30d, totals.tx30d, m)} />
      </div>
      <div>
        <h3>{m.maintenance.topClients}</h3>
        <div className="list">
          {topClients.map((row) => <div className="row" key={`${row.tunnel_id}-${row.client_id}`}>
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

function SupportPanel({ state, action, busyAction }: { state: AppState; action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const { m, locale } = useI18n();
  const tunnelHosts = new Set(state.tunnels.map((tunnel) => tunnel.server_host?.trim() || state.server_host).filter(Boolean));
  const ports = state.published_udp_ports.length ? state.published_udp_ports.join(", ") : m.maintenance.hostNetworkingDynamic;
  const build = state.build;

  return <div className="stack">
    <div>
      <h3>{m.maintenance.tabs.support}</h3>
      <p className="note">{m.maintenance.supportText}</p>
    </div>
    <Button className="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("support", m.maintenance.supportDownloadStarted, async () => { const res = await fetch("/api/support-bundle"); if (!res.ok) throw new Error(m.maintenance.supportDownloadFailed); await downloadResponse(res, "awg-forge-support.zip"); })}><ButtonContent busy={busyAction === "support"}>{m.maintenance.downloadSupport}</ButtonContent></Button>
    <div className="metric-grid">
      <Metric title={m.maintenance.tunnelHosts} value={[...tunnelHosts].join(", ") || "-"} />
      <Metric title={m.maintenance.applyConfig} value={state.apply_enabled ? m.common.enabled : m.common.manual} />
      <Metric title={m.maintenance.database} value={databaseLabel(state.database)} />
      <Metric title={m.maintenance.tls} value={tlsModeLabel(state.tls, m)} />
      <Metric title={m.maintenance.publishedUDP} value={ports} />
      <Metric title={m.maintenance.version} value={build?.version || "dev"} />
    </div>
    <section className="tls-summary" aria-label={m.maintenance.tlsStatus}>
      <h3>{m.maintenance.tlsStatus}</h3>
      <div>
          {state.tls.error && <p>{m.maintenance.tlsInvalid}: {state.tls.error}</p>}
          {state.tls.mode === "manual" && <>
            <p>{m.maintenance.tlsSubject}: {state.tls.subject || "-"}</p>
            <p>{m.maintenance.tlsIssuer}: {state.tls.issuer || "-"} · {m.maintenance.tlsValidUntil}: {formatTLSDate(state.tls.not_after, locale)}</p>
          </>}
          {(state.tls.mode === "acme-domain" || state.tls.mode === "acme-ip") && <>
            <p>{state.tls.mode === "acme-ip" ? m.maintenance.tlsACMEIP : m.maintenance.tlsDomain}: {state.tls.mode === "acme-ip" ? state.tls.ip || "-" : state.tls.domain || "-"}</p>
            <p>{m.maintenance.tlsCertificateStatus}: {state.tls.state === "active" ? m.maintenance.tlsActive : state.tls.state === "failed" ? m.maintenance.tlsFailed : m.maintenance.tlsPending}</p>
            {state.tls.state === "active" && <p>{m.maintenance.tlsValidUntil}: {formatTLSDate(state.tls.not_after, locale)}</p>}
            {state.tls.warning && <p>{m.maintenance.tlsRenewalWarning}</p>}
            {state.tls.next_attempt && <p>{m.maintenance.tlsNextAttempt}: {formatTLSDate(state.tls.next_attempt, locale)}</p>}
          </>}
          <p>{m.maintenance.trustedProxyHeaders}: {state.tls.trusted_proxy_headers ? m.maintenance.trustedProxyEnabled(state.tls.trusted_proxy_cidrs) : m.maintenance.trustedProxyDisabled}</p>
      </div>
    </section>
    <pre className="command-block">{`docker exec awg-forge awg-forge doctor
docker exec awg-forge awg-forge firewall check
docker compose logs -f`}</pre>
  </div>;
}

function databaseLabel(database: AppState["database"]): string {
  if (!database?.mode) return "off";
  return database.enabled ? database.mode : "off";
}

function tlsModeLabel(tls: AppState["tls"], m: Messages): string {
  if (tls.error) return m.maintenance.tlsInvalid;
  if (tls.mode === "manual") return m.maintenance.tlsManual;
  if (tls.mode === "reverse-proxy") return m.maintenance.tlsReverseProxy;
  if (tls.mode === "acme-domain") return m.maintenance.tlsACMEDomain;
  if (tls.mode === "acme-ip") return m.maintenance.tlsACMEIP;
  return m.maintenance.tlsOff;
}

function formatTLSDate(value: string | undefined, locale: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat(locale === "ru" ? "ru-RU" : "en-US", { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function WarpPanel({ state, action, busyAction }: { state: AppState; action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const { m, locale } = useI18n();
  const copy = maintenanceCopy(locale);
  return <div className="stack">
    <div className="metric-grid">
      <Metric title={m.warp.registration} value={state.warp.registered ? m.common.automatic : state.warp.configured ? m.common.manual : m.common.none} />
      <Metric title={m.warp.interface} value={state.warp.interface_name || "warp0"} />
      <Metric title={m.warp.endpoint} value={state.warp.endpoint || "-"} />
      <Metric title={m.dashboard.tunnels} value={m.warp.tunnelsViaWARP(state.warp.enabled_tunnel_count || 0)} />
    </div>
    <p className="note">{copy.warpHelp}</p>
    <div className="actions"><Button className="button primary" disabled={Boolean(busyAction)} type="button" onClick={() => action("warp-register", state.warp.configured ? m.warp.reregistered : m.warp.registered, async () => { await api.registerWarp(); })}><ButtonContent busy={busyAction === "warp-register"}>{state.warp.configured ? m.warp.reregister : m.warp.register}</ButtonContent></Button><Button className="button" disabled={!state.warp.configured || Boolean(busyAction)} type="button" onClick={() => action("warp-restart", m.warp.restarted, async () => { await api.restartWarp(); })}><ButtonContent busy={busyAction === "warp-restart"}>{m.warp.restart}</ButtonContent></Button><Button className="button danger" disabled={!state.warp.configured || Number(state.warp.enabled_tunnel_count || 0) > 0 || Boolean(busyAction)} type="button" onClick={() => confirm(m.warp.deleteConfirm) && action("warp-delete", m.warp.deleted, async () => { await api.deleteWarp(); })}><ButtonContent busy={busyAction === "warp-delete"}>{m.warp.delete}</ButtonContent></Button></div>
    {Number(state.warp.enabled_tunnel_count || 0) > 0 && <p className="note">{copy.warpDeleteBlocked}</p>}
    <details><summary>{m.warp.manualImport}</summary><WarpImport action={action} busyAction={busyAction} /></details>
  </div>;
}

function WarpImport({ action, busyAction }: { action: (key: string, label: string, fn: () => Promise<void>) => Promise<void>; busyAction: string }) {
  const { m } = useI18n();
  const [value, setValue] = useState("");
  return <form className="form single" onSubmit={(event) => { event.preventDefault(); void action("warp-import", m.warp.imported, async () => { await api.importWarp(value); }); }}><label>{m.warp.wireGuardConfig}<textarea aria-label={m.warp.wireGuardConfig} value={value} onInput={(event) => setValue((event.currentTarget as HTMLTextAreaElement).value)} placeholder="[Interface]&#10;PrivateKey = ..." /></label><Button className="button primary" disabled={Boolean(busyAction)} type="submit"><ButtonContent busy={busyAction === "warp-import"}>{m.warp.importConfig}</ButtonContent></Button></form>;
}

function BackupPanel({ notify }: { notify: (message: string) => void }) {
  const { m } = useI18n();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <form className="form single" onSubmit={async (event) => {
    event.preventDefault(); setBusy(true); setError("");
    try { const res = await api.backup(password); await downloadResponse(res, "awg-forge-backup.afbackup"); notify(m.backup.downloadStarted); }
    catch (err) { setError(backupErrorMessage(err, m)); }
    finally { setBusy(false); }
  }}>
    <PasswordInput visibilityToggleButtonProps={{ "aria-label": m.ui.togglePassword }} className="form-control" label={m.backup.password} aria-label={m.backup.password} autoComplete="new-password" value={password} disabled={busy} onChange={(event) => setPassword(event.currentTarget.value)} />
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="form-actions"><Button className="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{m.backup.create}</ButtonContent></Button></div>
  </form>;
}

function BackupRestorePanel({ report, setReport, notify }: { report: RestoreReport | null; setReport: (report: RestoreReport | null) => void; notify: (message: string) => void }) {
  const { m } = useI18n();
  return <div className="stack maintenance-sections">
    <section className="stack maintenance-section"><div><h3>{m.maintenance.backup}</h3><p className="note">{m.maintenance.backupText}</p></div><BackupPanel notify={notify} /></section>
    <section className="stack maintenance-section"><div><h3>{m.maintenance.restore}</h3><p className="note">{m.maintenance.restoreText}</p></div><RestorePanel report={report} setReport={setReport} notify={notify} /></section>
  </div>;
}

function RestorePanel({ report, setReport, notify }: { report: RestoreReport | null; setReport: (report: RestoreReport | null) => void; notify: (message: string) => void }) {
  const { m } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <div className="stack"><form className="form single" onSubmit={async (event) => {
    event.preventDefault(); setError("");
    if (!file) { setError(m.backup.fileRequired); return; }
    setBusy(true); setReport(null);
    try { setReport((await api.restoreVerify(file, password)).report); notify(m.backup.verified); }
    catch (err) { setError(backupErrorMessage(err, m)); }
    finally { setBusy(false); }
  }}>
    <FileInput className="form-control" label={m.backup.file} aria-label={m.backup.file} placeholder=".afbackup" leftSection={<Icon name="file" />} accept=".afbackup,application/octet-stream" value={file} onChange={(value) => { setFile(value); setError(""); setReport(null); }} disabled={busy} clearable clearButtonProps={{ "aria-label": m.backup.removeFile }} />
    <PasswordInput visibilityToggleButtonProps={{ "aria-label": m.ui.togglePassword }} className="form-control" label={m.backup.password} aria-label={m.backup.password} autoComplete="off" value={password} disabled={busy} onChange={(event) => setPassword(event.currentTarget.value)} />
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="form-actions"><Button className="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{m.backup.verify}</ButtonContent></Button></div>
  </form>{report && <div className="metric-grid"><Metric title={m.backup.format} value={report.format} /><Metric title={m.backup.schema} value={String(report.schema_version)} /><Metric title={m.dashboard.tunnels} value={String(report.tunnels.length)} /><Metric title={m.tunnel.clients} value={String(report.client_count)} /></div>}</div>;
}

function ResultList({ results }: { results: ResultListItem[] | null }) {
  const { m } = useI18n();
  if (!results) return <p>{m.maintenance.noResults}</p>;
  const groups = groupResults(results);
  if (groups.length > 1 || groups[0]?.category) {
    return <div className="result-groups">{groups.map((group) => <section className="result-group" key={group.category || "results"}><h3>{doctorCategoryLabel(group.category, m)}</h3><ResultRows results={group.results} /></section>)}</div>;
  }
  return <ResultRows results={results} />;
}

function ResultRows({ results }: { results: ResultListItem[] }) {
  return <div className="list">{results.map((item) => <div className="row" key={`${item.area}-${item.message}`}><div><strong>{item.area}</strong><p>{item.message}</p></div><Badge tone={toneFromLevel(item.level)}>{item.level}</Badge></div>)}</div>;
}

function groupResults(results: ResultListItem[]): Array<{ category: string; results: ResultListItem[] }> {
  const byCategory = new Map<string, ResultListItem[]>();
  const unknownCategories: string[] = [];
  for (const item of results) {
    const category = item.category || "";
    if (!byCategory.has(category) && !doctorCategoryOrder.includes(category)) {
      unknownCategories.push(category);
    }
    byCategory.set(category, [...(byCategory.get(category) || []), item]);
  }
  return [...doctorCategoryOrder, ...unknownCategories]
    .flatMap((category) => {
      const group = byCategory.get(category);
      return group?.length ? [{ category, results: group }] : [];
    });
}

function doctorCategoryLabel(category: string, m: Messages): string {
  return m.maintenance.doctorCategories[category as keyof typeof m.maintenance.doctorCategories] || category || m.maintenance.noResults;
}

function sortNewestFirst(events: AuditEvent[]): AuditEvent[] {
  return [...events].sort((left, right) => new Date(right.time).getTime() - new Date(left.time).getTime());
}
