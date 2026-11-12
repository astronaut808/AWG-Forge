import { Input, Switch, Textarea, TextInput } from "@mantine/core";
import { Icon } from "./icons";
import { Button, SelectField } from "./ui";
import * as api from "./api";
import { useI18n } from "./context";
import { actionErrorMessage } from "./errors";
import { expirationFromForm, field, mtuValue, trafficLimitFromForm, trafficLimitInitial } from "./form-values";
import { navigate } from "./navigation";
import { ButtonContent, Form, PanelTitle } from "./shared";
import type { AppState, Client, Profile, Tunnel } from "./types";
import type { PortSelectionMode, RunAction, TrafficLimitPeriod } from "./ui-types";
import { classNames, dateOnly, dateTimeLocalValue, expirationValue, isExperimentalProfile, profileTitle } from "./utils";
import { useEffect, useRef, useState } from "react";

const clientNamePattern = "[A-Za-z0-9](?:[A-Za-z0-9 ._\\-]{0,62}[A-Za-z0-9])?";

export function ServerNameForm({ state, save }: { state: AppState; save: (name: string) => Promise<void> }) {
  const { m } = useI18n();
  return <Form title={m.serverName.title} subtitle={m.serverName.subtitle} submit={m.common.save} onSubmit={(form) => save(field(form, "name"))}>
    <TextInput className="form-control full" label={m.serverName.name} name="name" defaultValue={state.server_name || ""} pattern="[A-Za-z0-9]*" maxLength={32} description={m.serverName.hint} autoComplete="off" data-autofocus />
  </Form>;
}

export function DeleteTunnelConfirmation({ tunnel, close, runAction }: { tunnel: Tunnel; close: () => void; runAction: RunAction }) {
  const { m } = useI18n();
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const confirmationRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    confirmationRef.current?.focus();
  }, []);
  return <PanelTitle title={m.actions.deleteTunnelTitle} subtitle={tunnel.name}>
    <form className="form single" onSubmit={async (event) => {
      event.preventDefault();
      if (confirmation !== tunnel.name) return;
      setBusy(true);
      setError("");
      try {
        await runAction(m.actions.tunnelDeleted, () => api.deleteTunnel(tunnel.id, confirmation), { errorMode: "inline" });
      } catch (err) {
        setError(actionErrorMessage(err, m));
      } finally {
        setBusy(false);
      }
    }}>
      <p className="note">{m.actions.deleteTunnelConnectedWarning}</p>
      <TextInput className="form-control" label={m.actions.deleteTunnelTypeName(tunnel.name)} ref={confirmationRef} aria-label={m.actions.deleteTunnelNameLabel} value={confirmation} onInput={(event) => setConfirmation((event.currentTarget as HTMLInputElement).value)} />
      {error && <p className="form-error" role="alert" aria-live="assertive">{error}</p>}
      <div className="form-actions"><Button className="button" type="button" onClick={close}>{m.common.close}</Button><Button className="button danger" disabled={busy || confirmation !== tunnel.name} type="submit"><ButtonContent busy={busy}>{m.common.delete}</ButtonContent></Button></div>
    </form>
  </PanelTitle>;
}

export function CreateTunnelForm({ state, profile, runAction, onCreateStarted }: { state: AppState; profile: Profile; runAction: RunAction; onCreateStarted: () => (tunnelID: string) => boolean }) {
  const { m } = useI18n();
  const profiles = state.profiles.length ? state.profiles : [profile];
  const [profileID, setProfileID] = useState(profile.id);
  const [portMode, setPortMode] = useState<PortSelectionMode>("automatic");
  const [suggestedPort, setSuggestedPort] = useState(0);
  const [suggestedRange, setSuggestedRange] = useState("");
  const [suggestionError, setSuggestionError] = useState("");
  const [suggestionRequest, setSuggestionRequest] = useState(0);
  const selected = profiles.find((item) => item.id === profileID) || profiles[0] || profile;

  useEffect(() => {
    let active = true;
    setSuggestedPort(0);
    setSuggestionError("");
    api.tunnelSuggestion(selected.id).then(({ suggestion }) => {
      if (!active) return;
      setSuggestedPort(suggestion.port);
      setSuggestedRange(suggestion.udp_port_range);
    }).catch((error: unknown) => {
      if (!active) return;
      setSuggestionError(error instanceof Error && error.message ? error.message : m.forms.portSuggestionFailed);
    });
    return () => {
      active = false;
    };
  }, [selected.id, suggestionRequest, m.forms.portSuggestionFailed]);

  return <Form title={m.forms.createTunnelTitle} subtitle={m.forms.createTunnelSubtitle} submit={m.common.createTunnel} onSubmit={(form) => {
    const port = portMode === "automatic" ? suggestedPort : Number(field(form, "port"));
    const onCreated = onCreateStarted();
    return runAction(m.forms.tunnelCreated, async () => {
      if (!port) throw new Error(m.forms.portSuggestionFailed);
      const result = await api.createTunnel({ profile: field(form, "profile"), name: field(form, "name"), port, automatic_port: portMode === "automatic", subnet: field(form, "subnet"), egress_mode: field(form, "egress_mode") });
      if (onCreated(result.tunnel.id)) navigate("tunnels");
    }, { errorMode: "inline" });
  }}>
    <SelectField label={m.forms.protocol} aria-label={m.forms.protocol} description={m.ui.selectProtocolHelp} name="profile" value={selected.id} onChange={(value) => setProfileID(value!)} data={profiles.map((item) => ({ value: item.id, label: `${profileTitle(item.id)}${isExperimentalProfile(item.id) ? ` · ${m.common.experimental}` : ""}` }))} />
    <TextInput className="form-control" label={m.forms.nameInterface} key={`${selected.id}-name`} aria-label={m.forms.nameInterface} name="name" defaultValue={selected.suggested_name || "awg0"} />
    <SelectField label={m.forms.portSelection} aria-label={m.forms.portSelection} value={portMode} onChange={(value) => setPortMode(value! as PortSelectionMode)} data={[{ value: "automatic", label: m.forms.automaticPort }, { value: "manual", label: m.forms.manualPort }]} />
    <Input.Wrapper className="form-control port-field" id="create-tunnel-port" label={m.forms.listenPort}>
      <div className={classNames("port-input-wrap", portMode === "manual" && "manual")}>
      {portMode === "automatic" ? <>
        <TextInput className="form-control" id="create-tunnel-port" aria-label={m.forms.listenPort} aria-describedby="create-tunnel-port-hint" value={suggestedPort || ""} readOnly />
        <Button className="button port-refresh" type="button" title={m.forms.refreshPort} aria-label={m.forms.refreshPort} onClick={() => setSuggestionRequest((value) => value + 1)}><Icon name="refresh" /></Button>
      </> : <TextInput className="form-control" id="create-tunnel-port" key={`${selected.id}-manual-port`} aria-label={m.forms.listenPort} aria-describedby="create-tunnel-port-hint" name="port" type="number" min={1} max={65535} required defaultValue={selected.suggested_port || 51820} />}
      </div>
      <small id="create-tunnel-port-hint" className={`form-note port-mode-note${suggestionError && portMode === "automatic" ? " field-error" : ""}`} aria-live="polite">{portMode === "automatic" ? (suggestionError || (suggestedRange ? m.forms.automaticPortRange(suggestedRange) : "")) : m.forms.manualPortHint}</small>
    </Input.Wrapper>
    <TextInput className="form-control" label={m.forms.ipv4Subnet} key={`${selected.id}-subnet`} aria-label={m.forms.ipv4Subnet} name="subnet" defaultValue={selected.suggested_subnet || "10.8.0.0/24"} />
    <SelectField label={m.forms.egress} aria-label={m.forms.egress} name="egress_mode" defaultValue="wan" data={[{ value: "wan", label: m.forms.serverWAN }, { value: "warp", label: m.forms.cloudflareWARP }]} />
    {!state.warp?.configured && <small className="form-note full">{m.forms.warpAutoRegister}</small>}
  </Form>;
}

export function TunnelSettingsForm({ state, tunnel, runAction }: { state: AppState; tunnel: Tunnel; runAction: RunAction }) {
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
  }), { errorMode: "inline" })}>
    <fieldset className="connection-group full"><legend>{m.ui.addressing}</legend><div className="form-grid">
      <TextInput className="form-control" label={m.forms.nameInterface} aria-label={m.forms.nameInterface} name="name" defaultValue={tunnel.name} />
      <TextInput className="form-control" label={m.forms.serverHost} aria-label={m.forms.serverHost} description={m.ui.inheritEndpoint} name="server_host" defaultValue={tunnel.server_host || ""} placeholder={state.server_host} />
      <TextInput className="form-control" label={m.forms.listenPort} aria-label={m.forms.listenPort} name="port" inputMode="numeric" defaultValue={tunnel.listen_port} />
      <TextInput className="form-control" label={m.forms.ipv4Subnet} aria-label={m.forms.ipv4Subnet} name="subnet" defaultValue={tunnel.subnet} />
    </div></fieldset>
    <fieldset className="connection-group full"><legend>{m.ui.routing}</legend><div className="form-grid">
      <SelectField label={m.forms.egress} aria-label={m.forms.egress} name="egress_mode" value={egressMode} onChange={(value) => setEgressMode(value!)} data={[{ value: "wan", label: m.forms.serverWAN }, { value: "warp", label: m.forms.cloudflareWARP }]} />
      <TextInput className="form-control" label={m.tunnel.dns} aria-label={m.tunnel.dns} name="dns" defaultValue={tunnel.dns} />
      <TextInput className="form-control full" label={m.forms.allowedIPs} aria-label={m.forms.allowedIPs} name="allowed_ips" defaultValue={tunnel.allowed_ips} />
      {!state.warp?.configured && egressMode === "warp" && <small className="form-note full">{m.forms.warpAutoRegister}</small>}
    </div></fieldset>
    <fieldset className="connection-group full"><legend>{m.ui.clientConnection}</legend><div className="form-grid">
      <TextInput className="form-control" label={m.forms.persistentKeepalive} aria-label={m.forms.persistentKeepalive} description={m.ui.keepaliveHelp} name="keepalive" inputMode="numeric" defaultValue={tunnel.keepalive} />
      <div className="stack mtu-fields"><MTUField value={tunnel.mtu || 0} /></div>
    </div></fieldset>
    <Switch className="toggle-row full" label={m.common.enabled} aria-label={m.common.enabled} description={m.forms.renderTunnelEnabled} name="enabled" defaultChecked={tunnel.enabled} />
    <p className="form-note full">{m.ui.configChangeHelp}</p>
  </Form>;
}

export function ProtocolForm({ tunnel, runAction, onRegenerate }: { tunnel: Tunnel; runAction: RunAction; onRegenerate: () => void }) {
  const { m, locale } = useI18n();
  const groups = [
    { title: m.ui.protocolJunk, kind: "numeric", columns: "cols-3", matches: (key: string) => key.startsWith("J") },
    { title: m.ui.protocolPadding, kind: "numeric", columns: "cols-2", matches: (key: string) => /^S\d/.test(key) || key === "ContentPaddingAddition" },
    { title: m.ui.protocolHeaders, kind: "numeric", columns: "cols-2", matches: (key: string) => /^H\d/.test(key) },
    { title: m.ui.protocolSignatures, kind: "signatures", columns: "cols-1", matches: (key: string) => /^I\d/.test(key) },
    { title: m.ui.protocolTiming, kind: "advanced", columns: "cols-2", matches: (key: string) => /^(Rekey|Reject|KeepaliveTimeout|MaxHandshake|PersistentKeepalive)/.test(key) },
  ];
  const junkOrder = ["Jc", "Jmin", "Jmax"];
  const grouped = groups.map((group) => ({ ...group, params: tunnel.params.filter((param) => group.matches(param.key)).sort((a, b) => group.title === m.ui.protocolJunk ? junkOrder.indexOf(a.key) - junkOrder.indexOf(b.key) : 0) }));
  const other = tunnel.params.filter((param) => !groups.some((group) => group.matches(param.key)));
  const sections = [...grouped, { title: m.ui.protocolOptions, kind: "advanced", columns: "cols-2", params: other }].filter((group) => group.params.length);
  const renderSection = (group: typeof sections[number]) => <section className={classNames("protocol-group", group.title === m.ui.protocolHeaders && "protocol-headers")} key={group.title}>
    <h3 className="form-section-title">{group.title}</h3>
    <div className={classNames("protocol-fields", group.columns)}>
      {group.params.map((item) => item.key === "RandomTrailers" || item.key === "DisableCookies"
        ? <SelectField key={item.key} label={item.key} name={item.key} defaultValue={item.value || "off"} data={[{ value: "off", label: "off" }, { value: "on", label: "on" }]} />
        : /^I\d/.test(item.key)
          ? <Textarea key={item.key} label={item.key} name={item.key} defaultValue={item.value || ""} autosize minRows={2} maxRows={5} />
          : <TextInput key={item.key} className={item.key === "ContentPaddingAddition" ? "full" : undefined} label={item.key} name={item.key} defaultValue={item.value || ""} />)}
    </div>
  </section>;
  return <>
    <Form title={m.forms.protocolTitle} subtitle={`${tunnel.name} · ${profileTitle(tunnel.profile)}`} submit={m.forms.saveProtocol} onSubmit={(form) => {
      const params: Record<string, string> = {};
      for (const item of tunnel.params) params[item.key] = field(form, item.key).trim();
      return runAction(m.forms.protocolSaved, () => api.updateProtocol(tunnel.id, tunnel.profile, params), { errorMode: "inline" });
    }}>
      {tunnel.profile === "awg_3" && <p className="form-note full">{m.forms.awg3Warning}</p>}
      <p className="form-note full">{m.ui.protocolHelp} <a href={`https://github.com/astronaut808/awg-forge/blob/master/docs/${locale}/protocol-matrix.md`} target="_blank" rel="noreferrer">{m.ui.protocolDocs}</a></p>
      <div className={classNames("protocol-layout full", !sections.some((group) => group.kind === "signatures") && "protocol-basic")}>
        <div className="protocol-column">{sections.filter((group) => group.kind === "numeric").map(renderSection)}</div>
        {sections.filter((group) => group.kind === "signatures").map(renderSection)}
      </div>
      {sections.some((group) => group.kind === "advanced") && <div className="protocol-layout protocol-advanced full">{sections.filter((group) => group.kind === "advanced").map(renderSection)}</div>}
      <p className="form-note full">{m.ui.configChangeHelp}</p>
      <section className="regenerate-section full"><div><h3>{m.forms.regenerate}</h3><p>{m.ui.regenerateDetail}</p></div>
        <Button className="button" onClick={onRegenerate}><Icon name="refresh" />{m.forms.regenerate}</Button>
      </section>
    </Form>
  </>;
}

export function CreateClientForm({ tunnels, initialTunnelID, trafficLimitsEnabled, runAction, onCreated }: { tunnels: Tunnel[]; initialTunnelID?: string; trafficLimitsEnabled: boolean; runAction: RunAction; onCreated: (tunnelID: string) => void }) {
  const { m } = useI18n();
  const [tunnelID, setTunnelID] = useState(initialTunnelID || "");
  const selected = tunnels.find((tunnel) => tunnel.id === tunnelID);
  return <Form title={m.forms.createClientTitle} subtitle="" submit={m.common.createClient} onSubmit={(form) => runAction(m.forms.clientCreatedOpenConfig, async () => {
    const target = tunnels.find((tunnel) => tunnel.id === field(form, "tunnel_id"));
    if (!target) throw new Error(m.ui.chooseTunnel);
    const trafficLimit = trafficLimitsEnabled ? trafficLimitFromForm(form, m.forms.trafficLimitInvalid) : { bytes: null, period: "lifetime" as TrafficLimitPeriod };
    await api.createClient(target.id, field(form, "name"), expirationFromForm(form), trafficLimit.bytes, trafficLimit.period);
    onCreated(target.id);
  }, { errorMode: "inline" })}>
    <div className="stack full">
      <SelectField label={m.ui.clientTunnel} aria-label={m.ui.clientTunnel} name="tunnel_id" required value={selected?.id || null} searchable nothingFoundMessage={m.ui.noMatches} placeholder={m.ui.chooseTunnel} onChange={(value) => setTunnelID(value!)} data={tunnels.map((tunnel) => ({ value: tunnel.id, label: tunnel.name }))} />
      {selected && <p className="client-tunnel-context">{profileTitle(selected.profile)} · {selected.subnet}</p>}
      <TextInput className="form-control" label={m.forms.clientName} aria-label={m.forms.clientName} name="name" placeholder={m.ui.clientNamePlaceholder} description={m.ui.clientNameShortHint} title={m.ui.clientNameHint} required minLength={1} maxLength={64} pattern={clientNamePattern} />
      <details className="field-help"><summary>{m.ui.nameRules}</summary><p>{m.ui.clientNameHint}</p></details>
      <ExpirationField />
      {trafficLimitsEnabled && <TrafficLimitField />}
    </div>
  </Form>;
}

export function ClientSettingsForm({ client, runAction }: { client: Client; runAction: RunAction }) {
  const { m } = useI18n();
  return <Form title={m.forms.clientSettingsTitle} subtitle={`${client.name} · ${client.address}`} submit={m.common.save} onSubmit={(form) => runAction(m.forms.clientSaved, async () => {
    await api.updateClient(client.id, { name: field(form, "name"), notes: field(form, "notes"), expires_at: expirationFromForm(form, client.expires_at) });
    if (client.traffic?.enabled) {
      const trafficLimit = trafficLimitFromForm(form, m.forms.trafficLimitInvalid);
      await api.updateClientTrafficLimit(client.id, trafficLimit.bytes, trafficLimit.period);
    }
  }, { errorMode: "inline" })}>
    <TextInput className="form-control" label={m.forms.clientName} aria-label={m.forms.clientName} name="name" description={m.ui.clientNameHint} required maxLength={64} pattern={clientNamePattern} defaultValue={client.name} />
    <ExpirationField current={client.expires_at} keepCurrent />
    {client.traffic?.enabled && <TrafficLimitField limitBytes={client.traffic.limit_bytes} limitPeriod={client.traffic.limit_period} exceeded={client.traffic.exceeded} />}
    <Textarea className="form-control full" label={m.forms.notes} aria-label={m.forms.notes} name="notes" maxLength={1000} defaultValue={client.notes || ""} />
  </Form>;
}

function TrafficLimitField({ limitBytes, limitPeriod = "lifetime", exceeded = false }: { limitBytes?: number | null; limitPeriod?: "" | TrafficLimitPeriod; exceeded?: boolean }) {
  const { m } = useI18n();
  const initial = trafficLimitInitial(limitBytes);
  const [mode, setMode] = useState(limitBytes ? "limit" : "unlimited");
  const [period, setPeriod] = useState<TrafficLimitPeriod>(limitPeriod || "lifetime");
  return <div className="traffic-limit-field full">
    <span className="field-title">{m.forms.trafficLimit}</span>
    <div className={classNames("traffic-limit-row", mode === "unlimited" && "compact")}>
      <SelectField className="traffic-limit-mode" aria-label={m.forms.trafficLimit} name="traffic_limit_mode" value={mode} onChange={(value) => setMode(value!)} data={[{ value: "unlimited", label: m.forms.trafficLimitUnlimited }, { value: "limit", label: m.forms.trafficLimitLimited }]} />
      {mode === "limit" && <>
        <TextInput className="traffic-limit-value" aria-label={m.forms.trafficLimitValue} name="traffic_limit_value" type="number" min="0.001" step="0.001" inputMode="decimal" defaultValue={initial.value} />
        <SelectField className="traffic-limit-unit" aria-label={m.forms.trafficLimitUnit} name="traffic_limit_unit" defaultValue={initial.unit} data={["mib", "gib", "tib"].map((value) => ({ value, label: value === "mib" ? "MiB" : value === "gib" ? "GiB" : "TiB" }))} />
        <SelectField className="traffic-limit-period" aria-label={m.forms.trafficLimitPeriod} name="traffic_limit_period" value={period} onChange={(value) => setPeriod(value! as TrafficLimitPeriod)} data={[{ value: "lifetime", label: m.forms.trafficLimitLifetime }, { value: "rolling_30d", label: m.forms.trafficLimitRolling30Days }]} />
      </>}
    </div>
    <span className="help">{exceeded ? period === "rolling_30d" ? m.forms.trafficLimitRolling30DaysExceededHelp : m.forms.trafficLimitExceededHelp : period === "rolling_30d" && mode === "limit" ? m.forms.trafficLimitRolling30DaysHelp : m.forms.trafficLimitHelp}</span>
  </div>;
}

function MTUField({ value }: { value: number }) {
  const { m } = useI18n();
  const normalized = Number(value || 0);
  const preset = normalized === 0 || normalized === 1280 || normalized === 1420 ? String(normalized) : "custom";
  const [mode, setMode] = useState(preset);
  return <>
    <SelectField label="MTU" aria-label="MTU" description={m.ui.mtuHelp} name="mtu_mode" value={mode} onChange={(value) => setMode(value!)} data={[{ value: "0", label: m.forms.auto }, { value: "1280", label: "1280" }, { value: "1420", label: "1420" }, { value: "custom", label: m.common.custom }]} />
    {mode === "custom" && <TextInput className="form-control" label={m.forms.customMTU} aria-label={m.forms.customMTU} name="mtu_custom" inputMode="numeric" defaultValue={normalized || 1280} />}
  </>;
}

function ExpirationField({ current, keepCurrent = false }: { current?: string; keepCurrent?: boolean }) {
  const { m, locale } = useI18n();
  const [mode, setMode] = useState(keepCurrent ? "keep" : "");
  return <>
    <SelectField label={m.forms.expiration} aria-label={m.forms.expiration} name="expires_mode" value={mode} onChange={(value) => setMode(value!)} data={[...(keepCurrent ? [{ value: "keep", label: current ? m.forms.keepCurrentValue(dateOnly(current, locale)) : m.forms.keepCurrent }] : []), { value: "", label: m.forms.neverExpires }, { value: "1h", label: m.forms.oneHour }, { value: "1d", label: m.forms.oneDay }, { value: "7d", label: m.forms.sevenDays }, { value: "30d", label: m.forms.thirtyDays }, { value: "custom", label: m.forms.customDate }]} />
    {mode === "custom" && <TextInput className="form-control" label={m.forms.customExpiration} aria-label={m.forms.customExpiration} name="expires_custom" type="datetime-local" min={dateTimeLocalValue()} defaultValue={dateTimeLocalValue(current || expirationValue("1d"))} />}
  </>;
}
