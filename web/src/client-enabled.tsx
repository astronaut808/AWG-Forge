import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import type { ClientEnabledRequest, ClientOperationStatus, NodeProjection, NodeView } from "./types";

type Props = { node: NodeView; controllerID: string; view: NodeProjection | null; updated: () => void; unauthorized: () => void; m: Messages };
const pending = (s: ClientOperationStatus["status"]) => s === "queued" || s === "leased" || s === "reconciling";

// Mounted under the account/node/epoch/boot context. A saved result proves a
// commit, while the projection independently reports the current client state.
export function ClientEnabled({ node, controllerID, view, updated, unauthorized, m }: Props) {
  const [target, setTarget] = useState("");
  const [confirmation, setConfirmation] = useState<{ nodeName: string; name: string; request: ClientEnabledRequest } | null>(null);
  const [busy, setBusy] = useState(false);
  const [operation, setOperation] = useState<ClientOperationStatus | null>(null);
  const [error, setError] = useState("");
  const intent = useRef<ClientEnabledRequest | null>(null);
  const alive = useRef(true);
  const active = useRef<AbortController | null>(null);
  const callbacks = useRef({ updated, unauthorized }); callbacks.current = { updated, unauthorized };
  useEffect(() => () => { alive.current = false; active.current?.abort(); }, []);
  const choices = view?.desired?.tunnels.flatMap((t) => t.clients.map((c) => ({ tunnel: t.id, client: c.id, enabled: c.enabled, name: `${t.name} / ${c.name}`, key: `${t.id}/${c.id}` }))) || [];
  const selected = choices.find((c) => c.key === target) || choices[0];
  const supported = node.capabilities.includes("client.enabled.set.v1");
  const matches = (s: ClientOperationStatus) => s.kind === "client.enabled.set" && s.controller_id === controllerID && s.node_id === node.node_id && s.state_epoch === node.state_epoch && s.binding_epoch === node.binding_epoch && s.operation_id === intent.current?.operation_id && s.expected_desired_generation === intent.current.expected_desired_generation && (s.status !== "succeeded" || Boolean(s.client_result && s.client_result.tunnel_id === intent.current.tunnel_id && s.client_result.client_id === intent.current.client_id && s.client_result.enabled === intent.current.enabled && s.client_result.committed_desired_generation === intent.current.expected_desired_generation + 1));
  const finish = (s: ClientOperationStatus) => {
    setOperation(s); setError("");
    if (!pending(s.status)) { intent.current = null; callbacks.current.updated(); }
  };
  useEffect(() => {
    if (!operation || !pending(operation.status)) return;
    let disposed = false; let timer: ReturnType<typeof setTimeout>; let request: AbortController | null = null;
    const poll = async () => {
      request = new AbortController(); const timeout = setTimeout(() => request?.abort(), 10_000);
      try {
        const s = await api.clientOperationStatus(node.node_id, operation.operation_id, request.signal);
        if (disposed) return;
        if (!matches(s)) { setError(m.fleet.clientUnknown); return; }
        finish(s);
        if (!pending(s.status)) return;
      } catch (e) {
        if (disposed) return; setError(m.fleet.clientUnknown);
        if (e instanceof api.APIError && e.status === 401) { callbacks.current.unauthorized(); return; }
      } finally { clearTimeout(timeout); }
      if (!disposed) timer = setTimeout(poll, 1500);
    };
    void poll(); return () => { disposed = true; clearTimeout(timer); request?.abort(); };
  }, [operation?.operation_id, operation?.status]);

  const inProgress = Boolean(operation && pending(operation.status));
  const waitingProjection = operation?.status === "succeeded" && (!view || view.desired_generation < (operation.client_result?.committed_desired_generation || Infinity));
  const disabled = busy || inProgress || waitingProjection || !supported || node.status !== "online" || !view?.available || view.stale || !selected;
  function confirm() {
    if (disabled || !view || !selected) return;
    setConfirmation({ nodeName: node.name, name: selected.name, request: { operation_id: api.refreshUUID(), idempotency_key: api.refreshUUID(), expected_state_epoch: view.state_epoch, expected_binding_epoch: node.binding_epoch, expected_desired_generation: view.desired_generation, tunnel_id: selected.tunnel, client_id: selected.client, enabled: !selected.enabled } });
  }
  async function create() {
    if (disabled || !view || !selected) return;
    if (!intent.current) {
      if (!confirmation) return;
      const request = confirmation.request;
      if (view.desired_generation !== request.expected_desired_generation || !choices.some((c) => c.tunnel === request.tunnel_id && c.client === request.client_id && c.enabled !== request.enabled)) {
        setConfirmation(null); setError(m.fleet.refreshConflict); callbacks.current.updated(); return;
      }
      intent.current = request;
    }
    active.current = new AbortController(); const timeout = setTimeout(() => active.current?.abort(), 10_000);
    setBusy(true); setConfirmation(null); setError("");
    try {
      const s = await api.createClientEnabled(node.node_id, intent.current, active.current.signal);
      if (!alive.current) return;
      if (!matches(s)) { setError(m.fleet.clientUnknown); return; }
      if (s.status === "succeeded" && (!s.client_result || s.client_result.tunnel_id !== intent.current.tunnel_id || s.client_result.client_id !== intent.current.client_id || s.client_result.enabled !== intent.current.enabled || s.client_result.committed_desired_generation !== intent.current.expected_desired_generation + 1)) { setError(m.fleet.clientUnknown); return; }
      finish(s);
    } catch (e) {
      if (!alive.current) return;
      setError(e instanceof api.APIError && e.code === "refresh_unsupported" ? m.fleet.clientUnsupported : e instanceof api.APIError && e.code === "operation_limit" ? m.fleet.refreshLimit : m.fleet.clientUnknown);
      if (e instanceof api.APIError && e.status < 500 && e.status !== 429) { intent.current = null; callbacks.current.updated(); }
      if (e instanceof api.APIError && e.status === 401) callbacks.current.unauthorized();
    } finally { clearTimeout(timeout); if (alive.current) setBusy(false); }
  }
  if (!choices.length && !operation && !intent.current && !error && !confirmation) return null;
  const text = operation ? { queued: m.fleet.clientQueued, leased: m.fleet.clientApplying, reconciling: m.fleet.clientReconciling, succeeded: m.fleet.clientCommitted, conflict: m.fleet.refreshConflict, failed: m.fleet.clientFailed, expired: m.fleet.clientExpired, rejected: m.fleet.clientRejected }[operation.status] : "";
  return <section class="stack" aria-label={m.fleet.clientAccess}>
    <label>{m.fleet.clientAccess}<select aria-label={m.fleet.clientAccess} value={selected?.key || ""} disabled={busy || inProgress || Boolean(intent.current)} onChange={(e) => { setTarget(e.currentTarget.value); setConfirmation(null); }}>
      {choices.map((c) => <option value={c.key} key={c.key}>{c.name}</option>)}
    </select></label>
    {!supported && <p class="muted">{m.fleet.clientUnsupported}</p>}
    {confirmation ? <div class="notice warn"><p>{confirmation.nodeName} · {confirmation.name} · {confirmation.request.enabled ? m.fleet.clientEnable : m.fleet.clientDisable}</p><p>{m.fleet.clientImpact}</p><div class="actions"><button class="button" type="button" disabled={disabled} onClick={() => void create()}>{m.fleet.clientConfirm}</button><button class="button" type="button" onClick={() => setConfirmation(null)}>{m.fleet.clientCancel}</button></div></div>
      : <button class="button" type="button" disabled={disabled} onClick={() => intent.current ? void create() : confirm()}>{intent.current ? m.fleet.clientRetry : selected?.enabled ? m.fleet.clientDisable : m.fleet.clientEnable}</button>}
    {(error || text) && <p role="status" class={`notice ${error || operation?.status !== "succeeded" ? "warn" : ""}`}>{error || text}</p>}
  </section>;
}
