import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import type { ClientCRUDOperationStatus, ClientCreateRequest, ClientDeleteRequest, NodeProjection, NodeView } from "./types";

type Props = { node: NodeView; controllerID: string; view: NodeProjection | null; updated: () => void; unauthorized: () => void; m: Messages };
type Intent = { action: "create"; request: ClientCreateRequest; nodeName: string; tunnelName: string; clientName: string } | { action: "delete"; request: ClientDeleteRequest; nodeName: string; tunnelName: string; clientName: string };
const pending = (status: ClientCRUDOperationStatus["status"]) => status === "queued" || status === "leased" || status === "reconciling";
const clientName = /^[A-Za-z0-9](?:[A-Za-z0-9_. -]{0,62}[A-Za-z0-9])?$/;

function clientID(): string {
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(8));
  return [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function RemoteClientCRUD({ node, controllerID, view, updated, unauthorized, m }: Props) {
  const [tunnelID, setTunnelID] = useState("");
  const [name, setName] = useState("");
  const [client, setClient] = useState("");
  const [confirmation, setConfirmation] = useState<Intent | null>(null);
  const [operation, setOperation] = useState<ClientCRUDOperationStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const intent = useRef<Intent | null>(null);
  const alive = useRef(true);
  const active = useRef<AbortController | null>(null);
  const callbacks = useRef({ updated, unauthorized }); callbacks.current = { updated, unauthorized };
  const tunnels = view?.desired?.tunnels || [];
  const selectedTunnel = tunnels.find((tunnel) => tunnel.id === tunnelID) || tunnels[0];
  const clients = selectedTunnel?.clients || [];
  const selectedClient = clients.find((item) => item.id === client) || clients[0];
  const createSupported = node.capabilities.includes("client.create.v1");
  const deleteSupported = node.capabilities.includes("client.delete.v1");
  const enabled = node.status === "online" && Boolean(view?.available) && !view?.stale;

  useEffect(() => () => { alive.current = false; active.current?.abort(); }, []);
  useEffect(() => { if (selectedTunnel && selectedTunnel.id !== tunnelID) setTunnelID(selectedTunnel.id); }, [selectedTunnel?.id]);
  useEffect(() => { if (selectedClient && selectedClient.id !== client) setClient(selectedClient.id); }, [selectedClient?.id]);

  function matches(status: ClientCRUDOperationStatus): boolean {
    const current = intent.current;
    if (!current || status.controller_id !== controllerID || status.node_id !== node.node_id || status.state_epoch !== node.state_epoch || status.binding_epoch !== node.binding_epoch || status.operation_id !== current.request.operation_id || status.expected_desired_generation !== current.request.expected_desired_generation || status.kind !== `client.${current.action}`) return false;
    if (status.status !== "succeeded") return true;
    if (status.code !== (current.action === "create" ? "client_created_committed" : "client_deleted_committed")) return false;
    const variants = status as ClientCRUDOperationStatus & { client_result?: unknown };
    if (variants.client_result || current.action === "create" && status.delete_result || current.action === "delete" && status.create_result) return false;
    if (current.action === "create") {
      const result = status.create_result;
      return Boolean(result && result.committed_desired_generation === current.request.expected_desired_generation + 1 && result.tunnel_id === current.request.tunnel_id && result.client_id === current.request.client_id && result.name === current.request.name);
    }
    const result = status.delete_result;
    return Boolean(result && result.committed_desired_generation === current.request.expected_desired_generation + 1 && result.tunnel_id === current.request.tunnel_id && result.client_id === current.request.client_id && result.deleted === true);
  }

  function committedProjection(): boolean {
    if (!operation || operation.status !== "succeeded" || !intent.current || !view?.desired || !view.available || view.stale || view.controller_id !== controllerID || view.node.node_id !== node.node_id || view.state_epoch !== node.state_epoch || view.node.state_epoch !== node.state_epoch || view.node.binding_epoch !== node.binding_epoch) return false;
    const generation = intent.current.request.expected_desired_generation + 1;
    if (view.desired_generation < generation) return false;
    // A later projection can legitimately contain a later local change. It must
    // not be treated as evidence that this committed result should be undone.
    if (view.desired_generation > generation) return true;
    const present = view.desired.tunnels.find((tunnel) => tunnel.id === intent.current?.request.tunnel_id)?.clients.find((item) => item.id === intent.current?.request.client_id);
    return intent.current.action === "create" ? Boolean(present && present.name === intent.current.request.name) : !present;
  }

  useEffect(() => {
    if (!committedProjection()) return;
    intent.current = null; setOperation(null); setError("");
  }, [operation?.status, view?.available, view?.stale, view?.desired_generation, view?.snapshot_sequence]);

  useEffect(() => {
    if (!operation || !pending(operation.status)) return;
    let disposed = false; let timer: ReturnType<typeof setTimeout>; let request: AbortController | null = null;
    const poll = async () => {
      request = new AbortController(); const deadline = setTimeout(() => request?.abort(), 10_000);
      try {
        const status = await api.remoteClientOperationStatus(node.node_id, operation.operation_id, request.signal);
        if (disposed) return;
        if (!matches(status)) {
          // A stale or malformed receipt proves neither failure nor success.
          // Keep polling the same immutable operation instead of stranding it.
          setError(m.fleet.clientUnknown);
        } else {
          setOperation(status); setError("");
          if (status.status === "succeeded") callbacks.current.updated();
          if (!pending(status.status)) { if (status.status !== "succeeded") { intent.current = null; callbacks.current.updated(); } return; }
        }
      } catch (err) {
        if (disposed) return;
        setError(m.fleet.clientUnknown);
        if (err instanceof api.APIError && err.status === 401) callbacks.current.unauthorized();
      } finally { clearTimeout(deadline); }
      if (!disposed) timer = setTimeout(poll, 1500);
    };
    void poll(); return () => { disposed = true; clearTimeout(timer); request?.abort(); };
  }, [operation?.operation_id, operation?.status]);

  function beginCreate() {
    if (!enabled || !selectedTunnel || !clientName.test(name.trim()) || intent.current) return;
    setConfirmation({ action: "create", nodeName: node.name, tunnelName: selectedTunnel.name, clientName: name.trim(), request: { operation_id: api.refreshUUID(), idempotency_key: api.refreshUUID(), expected_state_epoch: view!.state_epoch, expected_binding_epoch: node.binding_epoch, expected_desired_generation: view!.desired_generation, tunnel_id: selectedTunnel.id, client_id: clientID(), name: name.trim() } });
  }
  function beginDelete() {
    if (!enabled || !selectedTunnel || !selectedClient || intent.current) return;
    setConfirmation({ action: "delete", nodeName: node.name, tunnelName: selectedTunnel.name, clientName: selectedClient.name, request: { operation_id: api.refreshUUID(), idempotency_key: api.refreshUUID(), expected_state_epoch: view!.state_epoch, expected_binding_epoch: node.binding_epoch, expected_desired_generation: view!.desired_generation, tunnel_id: selectedTunnel.id, client_id: selectedClient.id, confirmed: true } });
  }
  async function submit() {
    const current = intent.current || confirmation;
    if (!current || busy || !view) return;
    if (!intent.current) {
      const tunnel = view.desired?.tunnels.find((item) => item.id === current.request.tunnel_id);
      const present = tunnel?.clients.find((item) => item.id === current.request.client_id);
      const capable = current.action === "create" ? createSupported : deleteSupported;
      const context = enabled && capable && view.controller_id === controllerID && view.node.node_id === node.node_id && view.state_epoch === node.state_epoch && view.node.state_epoch === node.state_epoch && view.node.binding_epoch === node.binding_epoch;
      const mutationMatches = current.action === "create" ? !present && clientName.test(current.request.name) : Boolean(present && present.name === current.clientName);
      if (!context || view.desired_generation !== current.request.expected_desired_generation || view.state_epoch !== current.request.expected_state_epoch || node.binding_epoch !== current.request.expected_binding_epoch || !tunnel || !mutationMatches) { setConfirmation(null); setError(m.fleet.refreshConflict); callbacks.current.updated(); return; }
      intent.current = current;
    }
    active.current = new AbortController(); const deadline = setTimeout(() => active.current?.abort(), 10_000);
    setBusy(true); setConfirmation(null); setError("");
    try {
      const status = current.action === "create"
        ? await api.createRemoteClient(node.node_id, current.request, active.current.signal)
        : await api.deleteRemoteClient(node.node_id, current.request, active.current.signal);
      if (!alive.current) return;
      if (!matches(status)) { setError(m.fleet.clientUnknown); return; }
      setOperation(status);
      if (status.status === "succeeded") callbacks.current.updated();
      if (!pending(status.status) && status.status !== "succeeded") { intent.current = null; callbacks.current.updated(); }
    } catch (err) {
      if (!alive.current) return;
      const code = err instanceof api.APIError ? err.code : "";
      setError(code === "refresh_unsupported" ? m.fleet.clientUnsupported : code === "operation_limit" ? m.fleet.refreshLimit : code === "resource_conflict" || code === "address_unavailable" || code === "client_unavailable" || code === "tunnel_unavailable" ? m.fleet.clientFailed : m.fleet.clientUnknown);
      if (err instanceof api.APIError && err.status < 500 && err.status !== 429) { intent.current = null; callbacks.current.updated(); }
      if (err instanceof api.APIError && err.status === 401) callbacks.current.unauthorized();
    } finally { clearTimeout(deadline); if (alive.current) setBusy(false); }
  }

  const inProgress = Boolean(operation && pending(operation.status));
  const waiting = Boolean(operation?.status === "succeeded" && !committedProjection());
  const blocked = busy || inProgress || waiting || !enabled || Boolean(intent.current);
  const retry = Boolean(intent.current && !operation && error);
  const statusText = operation ? { queued: m.fleet.clientQueued, leased: m.fleet.clientApplying, reconciling: m.fleet.clientReconciling, succeeded: m.fleet.clientCommitted, conflict: m.fleet.refreshConflict, failed: m.fleet.clientFailed, expired: m.fleet.clientExpired, rejected: m.fleet.clientRejected }[operation.status] : "";
  if (!tunnels.length && !confirmation && !operation && !error) return null;
  const shown = confirmation || intent.current;
  return <section class="stack" aria-label={m.fleet.remoteClientManagement}>
    <h2>{m.fleet.remoteClientManagement}</h2>
    <label>{m.fleet.remoteTunnel}<select aria-label={m.fleet.remoteTunnel} value={selectedTunnel?.id || ""} disabled={blocked} onChange={(event) => { setTunnelID(event.currentTarget.value); setConfirmation(null); }}>
      {tunnels.map((tunnel) => <option value={tunnel.id} key={tunnel.id}>{tunnel.name}</option>)}
    </select></label>
    <label>{m.fleet.remoteClientName}<input aria-label={m.fleet.remoteClientName} value={name} maxLength={64} disabled={blocked || !createSupported} onInput={(event) => setName(event.currentTarget.value)} /></label>
    <button class="button" type="button" disabled={blocked || !createSupported || !clientName.test(name.trim())} onClick={beginCreate}>{m.fleet.remoteCreate}</button>
    <label>{m.fleet.remoteExistingClient}<select aria-label={m.fleet.remoteExistingClient} value={selectedClient?.id || ""} disabled={blocked || !deleteSupported} onChange={(event) => { setClient(event.currentTarget.value); setConfirmation(null); }}>
      {clients.map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
    </select></label>
    <button class="button" type="button" disabled={blocked || !deleteSupported || !selectedClient} onClick={beginDelete}>{m.fleet.remoteDelete}</button>
    {(!createSupported || !deleteSupported) && <p class="muted">{m.fleet.clientUnsupported}</p>}
    {shown && <div class="notice warn"><p>{shown.nodeName} · {shown.tunnelName} · {shown.clientName} · {shown.action === "create" ? m.fleet.remoteCreate : m.fleet.remoteDelete}</p><div class="actions"><button class="button" type="button" disabled={intent.current ? !retry : busy || inProgress || waiting || !enabled} onClick={() => void submit()}>{intent.current ? m.fleet.clientRetry : m.fleet.clientConfirm}</button><button class="button" type="button" disabled={Boolean(intent.current)} onClick={() => setConfirmation(null)}>{m.fleet.clientCancel}</button></div></div>}
    {(error || statusText) && <p role="status" class={`notice ${error || operation?.status !== "succeeded" ? "warn" : ""}`}>{error || statusText}</p>}
  </section>;
}
