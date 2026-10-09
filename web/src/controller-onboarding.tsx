import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import { downloadResponse } from "./utils";

// Only public arguments are copyable. There are no invented release URLs or
// floating image fallbacks for an unpublished build. Shell quoting still applies
// to names containing metacharacters, quotes and non-ASCII text.
export function publicJoinArguments(invitation: api.NodeInvitation, name: string, mode: "fresh" | "existing"): string {
  if (!/^https:\/\/(?:[a-z0-9.-]+|\[[a-f0-9:]+\]):[0-9]+\/?$/.test(invitation.controller_url) || !/^sha256:[a-f0-9]{64}$/.test(invitation.ca_pin) || !/^[a-f0-9]{8}-(?:[a-f0-9]{4}-){3}[a-f0-9]{12}$/.test(invitation.invitation_id) || !name || Array.from(name).length > 128 || /^[\s]*-/.test(name) || Array.from(name).some((char) => { const code = char.codePointAt(0)!; return code < 32 || (code >= 127 && code <= 159); })) throw new Error("Invalid public connection parameters");
  const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;
  return `--mode ${quote(mode)} --controller-url ${quote(invitation.controller_url)} --ca-pin ${quote(invitation.ca_pin)} --invitation-id ${quote(invitation.invitation_id)} --name ${quote(name)}`;
}

export function ControllerOnboarding({ recent, notify, m }: { recent: boolean; notify: (message: string) => void; m: Messages }) {
  const text = m.onboarding;
  const [endpoint, setEndpoint] = useState<api.ControlEndpoint | null>(null);
  const [controllerID, setControllerID] = useState("");
  const [bind, setBind] = useState("127.0.0.1");
  const [advertised, setAdvertised] = useState("127.0.0.1");
  const [port, setPort] = useState("9443");
  const [password, setPassword] = useState("");
  const [receipt, setReceipt] = useState("");
  const [retained, setRetained] = useState(false);
  const [consent, setConsent] = useState(false);
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<"fresh" | "existing">("fresh");
  const [name, setName] = useState("node");
  const [invitation, setInvitation] = useState<api.NodeInvitation | null>(null);
  const [status, setStatus] = useState("waiting");
  const [review, setReview] = useState<Awaited<ReturnType<typeof api.enrollmentReview>> | null>(null);
  const [matched, setMatched] = useState(false);
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const active = useRef(new AbortController());
  const account = useRef("");
  const alive = useRef(true);

  function reset() {
    generation.current++;
    active.current.abort(); active.current = new AbortController();
    setInvitation(null); setReview(null); setMatched(false); setOpen(false); setStatus("waiting");
    setPassword(""); setReceipt(""); setRetained(false); setConsent(false); setBusy(false);
  }
  function sessionIdentity(session: Awaited<ReturnType<typeof api.authSession>>) { return `${session.username || ""}\0${session.expires_at || ""}`; }
  async function checkIdentity(signal: AbortSignal) {
    const session = await api.authSession(signal);
    if (session.mode !== "controller" || !session.recent_auth || sessionIdentity(session) !== account.current) throw new Error(text.auth);
  }
  useEffect(() => {
    if (!recent) { reset(); return; }
    alive.current = true;
    const signal = active.current.signal;
    void Promise.all([api.controlStatus(signal), api.authSession(signal), api.installerInfo(signal)]).then(([control, session]) => {
      if (!alive.current || signal.aborted) return;
      setEndpoint(control.control); setControllerID(control.controller_id); account.current = sessionIdentity(session);
    }).catch((err) => { if (!signal.aborted) notify(err instanceof Error ? err.message : m.common.requestFailed); });
    return () => { alive.current = false; generation.current++; active.current.abort(); };
  }, [recent]);

  async function perform(action: (signal: AbortSignal) => Promise<void>) {
    const current = generation.current;
    const signal = active.current.signal;
    setBusy(true);
    try {
      // Reauthentication rotates sessions. A fresh identity starts a new flow,
      // retaining neither the former invitation nor a former backup receipt.
      const session = await api.authSession(signal);
      if (session.mode !== "controller" || !session.recent_auth) throw new Error(text.auth);
      if (account.current && account.current !== sessionIdentity(session)) { reset(); account.current = sessionIdentity(session); return; }
      account.current = sessionIdentity(session);
      await action(signal);
    } catch (err) {
      if (!signal.aborted && current === generation.current) { reset(); notify(err instanceof Error ? err.message : m.common.requestFailed); }
    } finally { if (alive.current && current === generation.current) setBusy(false); }
  }

  useEffect(() => {
    if (!invitation || !endpoint || !open) return;
    const signal = active.current.signal;
    const current = generation.current;
    const deadline = Math.min(Date.parse(invitation.expires_at), Date.now() + 10 * 60_000);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stopped = false;
    const valid = () => !stopped && !signal.aborted && current === generation.current;
    function finish(value: string) { if (valid()) { stopped = true; setStatus(value); setInvitation((old) => old ? { ...old, secret: "" } : null); } }
    async function poll() {
      if (!valid()) return;
      if (!Number.isFinite(deadline) || Date.now() >= deadline) { finish("expired"); return; }
      try {
        await checkIdentity(signal);
        const control = await api.controlStatus(signal);
        if (control.controller_id !== controllerID || !control.control?.enabled || control.control.ca_pin !== endpoint?.ca_pin || control.control.advertised !== endpoint?.advertised || control.control.port !== endpoint?.port) throw new Error(text.stopped);
        const result = await api.onboardingStatus(invitation!.invitation_id, signal);
        if (!valid()) return;
        if (result.controller_id !== controllerID || result.invitation_id !== invitation!.invitation_id) throw new Error(text.stopped);
        if (result.status === "rejected" || result.status === "expired") { finish(result.status); return; }
        if (result.status === "approved" && result.connected && result.node_id && result.enrollment_id) { finish("connected"); return; }
        setStatus(result.status);
        if (result.status === "pending") {
          const next = await api.enrollmentReview(invitation!.invitation_id, signal);
          if (!valid()) return;
          setReview((old) => {
            if (old?.enrollment_id !== next.enrollment_id || old?.verification_code !== next.verification_code) setMatched(false);
            return next;
          });
        }
        if (valid()) timer = setTimeout(() => void poll(), 1500);
      } catch (err) {
        if (valid()) { reset(); notify(err instanceof Error ? err.message : m.common.requestFailed); }
      }
    }
    const expiryTimer = setTimeout(() => { finish("expired"); active.current.abort(); }, Math.max(0, deadline - Date.now()));
    void poll();
    return () => { stopped = true; clearTimeout(expiryTimer); if (timer) clearTimeout(timer); };
  }, [invitation?.invitation_id, endpoint?.enabled, open]);

  const external = endpoint ? !(endpoint.bind_ip === "127.0.0.1" || endpoint.bind_ip === "::1") || !(endpoint.advertised === "127.0.0.1" || endpoint.advertised === "::1") : false;
  let argumentsText = "";
  try { if (invitation) argumentsText = publicJoinArguments(invitation, name, mode); } catch { /* Invalid material is never copyable. */ }
  return <section class="stack maintenance-section" aria-label={text.title}>
    <h3>{text.title}</h3>
    {!recent && <p class="note">{text.auth}</p>}
    {endpoint ? <><p>{text.endpoint}: <code>{`https://${endpoint.advertised.includes(":") ? `[${endpoint.advertised}]` : endpoint.advertised}:${endpoint.port}`}</code></p><p class="note">{text.immutable}</p></> : <form class="form single" onSubmit={(event) => { event.preventDefault(); void perform(async (signal) => { const prepared = await api.controlPrepare({ bind_ip: bind, advertised, port: Number(port) }, signal); if (!signal.aborted) { setEndpoint(prepared); setReceipt(""); setConsent(false); setRetained(false); } }); }}>
      <label>{text.bind}<input value={bind} onInput={(event) => setBind(event.currentTarget.value)} required /></label>
      <label>{text.advertised}<input value={advertised} onInput={(event) => setAdvertised(event.currentTarget.value)} required /></label>
      <label>{text.port}<input type="number" min={1} max={65535} value={port} onInput={(event) => setPort(event.currentTarget.value)} required /></label>
      <button type="submit" class="button" disabled={!recent || busy}>{text.prepare}</button>
    </form>}
    {endpoint && !endpoint.enabled && <>
      <form class="form single" onSubmit={(event) => { event.preventDefault(); void perform(async (signal) => {
        setReceipt(""); setRetained(false); setConsent(false);
        const res = await api.controlBackup(password, signal); setPassword("");
        await checkIdentity(signal);
        const nextReceipt = res.headers.get("X-Control-Enable-Receipt");
        if (!nextReceipt) throw new Error(m.common.requestFailed);
        if (signal.aborted) return;
        await downloadResponse(res, "awg-forge-control.afbackup");
        if (!signal.aborted) setReceipt(nextReceipt);
      }); }}>
        <label>{text.backupPassword}<input type="password" autocomplete="new-password" value={password} onInput={(event) => setPassword(event.currentTarget.value)} required minLength={12} /></label>
        <button type="submit" class="button" disabled={!recent || busy}>{text.backup}</button>
      </form>
      {receipt && <>
        <label class="check-label"><input type="checkbox" checked={retained} onChange={(event) => setRetained(event.currentTarget.checked)} />{text.retained}</label>
        {external && <label class="check-label"><input type="checkbox" checked={consent} onChange={(event) => setConsent(event.currentTarget.checked)} />{text.consent}</label>}
        <button type="button" class="button primary" disabled={!recent || busy || !retained || (external && !consent)} onClick={() => void perform(async (signal) => { const currentReceipt = receipt; setReceipt(""); setRetained(false); setConsent(false); await api.controlEnable(currentReceipt, external, signal); const control = await api.controlStatus(signal); if (!signal.aborted) { setEndpoint(control.control); setControllerID(control.controller_id); } })}>{text.enable}</button>
      </>}
    </>}
    {endpoint?.enabled && !open && <button type="button" class="button primary" disabled={!recent || busy} onClick={() => void perform(async (signal) => { const control = await api.controlStatus(signal); if (!signal.aborted && control.controller_id === controllerID && control.control?.enabled) setOpen(true); })}>{text.add}</button>}
    {open && <>
      {!invitation && <form class="form single" onSubmit={(event) => { event.preventDefault(); void perform(async (signal) => { const result = await api.nodeInvitation(signal); await checkIdentity(signal); publicJoinArguments(result, name, mode); if (!signal.aborted) { setStatus("waiting"); setInvitation(result); } }); }}>
        <label>{text.fresh}<select value={mode} onChange={(event) => setMode(event.currentTarget.value as "fresh" | "existing")}><option value="fresh">{text.fresh}</option><option value="existing">{text.existing}</option></select></label>
        <label>{text.name}<input value={name} maxLength={128} onInput={(event) => setName(event.currentTarget.value)} required /></label>
        <p class="note">{text.maintenance}</p>
        <button type="submit" class="button primary" disabled={!recent || busy}>{text.invite}</button>
      </form>}
      {invitation && <>
        <p class="note">{text.unpublished}</p>
        <label>{text.parameters}<code class="controller-secret">{argumentsText}</code></label>
        <button type="button" class="button" disabled={!argumentsText} onClick={() => { void navigator.clipboard.writeText(argumentsText).catch(() => notify(m.common.requestFailed)); }}>{text.copy}</button>
        {invitation.secret && <><label>{text.secret}<input type="password" readonly autocomplete="off" value={invitation.secret} /></label><p class="note">{text.secretNote}</p></>}
        {review && status === "pending" && <>
          <p>{text.name}: <strong>{review.requested_name}</strong></p>
          <p>{text.comparison}: <code>{review.verification_code}</code></p><p class="note">{text.compareNote}</p>
          <label class="check-label"><input type="checkbox" checked={matched} onChange={(event) => setMatched(event.currentTarget.checked)} />{text.matched}</label>
          <button type="button" class="button primary" disabled={!matched || busy} onClick={() => void perform(async (signal) => { await api.enrollmentDecide(review.enrollment_id, review.verification_code, true, signal); if (!signal.aborted) { setStatus("approved"); setInvitation((old) => old ? { ...old, secret: "" } : null); } })}>{text.approve}</button>
          <button type="button" class="button danger" disabled={busy} onClick={() => void perform(async (signal) => { await api.enrollmentDecide(review.enrollment_id, review.verification_code, false, signal); if (!signal.aborted) { setStatus("rejected"); active.current.abort(); setInvitation((old) => old ? { ...old, secret: "" } : null); } })}>{text.reject}</button>
        </>}
        <p role="status">{status === "connected" ? text.connected : status === "approved" ? text.presence : status === "expired" ? text.expired : status === "rejected" ? text.rejected : text.waiting}</p>
      </>}
      <button type="button" class="button" onClick={reset}>{text.cancel}</button>
    </>}
  </section>;
}
