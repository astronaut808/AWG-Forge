import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import type { NodeProjection, NodeView, RefreshRequest, RefreshStatus } from "./types";

type Props = { node: NodeView; controllerID: string; view: NodeProjection | null; updated: () => void; unauthorized: () => void; m: Messages };
const terminal = (s: RefreshStatus["status"]) => s !== "queued" && s !== "leased";

// The parent mounts a separate instance for each account/node/epoch/boot context.
// No operation IDs or results are written to browser storage.
export function NodeRefresh({ node, controllerID, view, updated, unauthorized, m }: Props) {
  const [operation, setOperation] = useState<RefreshStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const intent = useRef<RefreshRequest | null>(null);
  const createController = useRef<AbortController | null>(null);
  const alive = useRef(true);
  const callbacks = useRef({ updated, unauthorized }); callbacks.current = { updated, unauthorized };
  useEffect(() => () => { alive.current = false; createController.current?.abort(); }, []);

  const belongs = (next: RefreshStatus) => next.controller_id === controllerID && next.node_id === node.node_id && next.state_epoch === node.state_epoch && next.binding_epoch === node.binding_epoch && next.operation_id === intent.current?.operation_id;
  useEffect(() => {
    if (!operation || terminal(operation.status)) return;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    let controller: AbortController | null = null;
    const poll = async () => {
      controller = new AbortController();
      const deadline = setTimeout(() => controller?.abort(), 10_000);
      try {
        const next = await api.nodeRefreshStatus(node.node_id, operation.operation_id, controller.signal);
        if (disposed) return;
        if (!belongs(next)) { setError(m.fleet.refreshRejected); return; }
        setOperation(next); setError("");
        if (terminal(next.status)) { intent.current = null; callbacks.current.updated(); return; }
      } catch (err) {
        if (disposed) return;
        setError(m.fleet.refreshUnknown);
        if (err instanceof api.APIError && err.status === 401) { callbacks.current.unauthorized(); return; }
      } finally { clearTimeout(deadline); }
      if (!disposed) timer = setTimeout(poll, 1500);
    };
    void poll();
    return () => { disposed = true; clearTimeout(timer); controller?.abort(); };
  }, [operation?.operation_id, operation?.status]);

  async function create() {
    if (!view || busy || operation && !terminal(operation.status)) return;
    // After uncertain admission, retry exactly the same intent instead of
    // creating another operation. A definitive conflict requires a new snapshot.
    intent.current ||= { operation_id: api.refreshUUID(), idempotency_key: api.refreshUUID(), expected_state_epoch: view.state_epoch, expected_binding_epoch: node.binding_epoch, expected_desired_generation: view.desired_generation };
    const controller = new AbortController(); createController.current = controller;
    const deadline = setTimeout(() => controller.abort(), 10_000);
    setBusy(true); setError("");
    try {
      const next = await api.createNodeRefresh(node.node_id, intent.current, controller.signal);
      if (!alive.current) return;
      if (!belongs(next)) { setError(m.fleet.refreshRejected); return; }
      setOperation(next);
      if (terminal(next.status)) { intent.current = null; callbacks.current.updated(); }
    } catch (err) {
      if (!alive.current) return;
      const code = err instanceof api.APIError ? err.code : undefined;
      setError(code === "refresh_unsupported" ? m.fleet.refreshUnsupported : code === "operation_limit" ? m.fleet.refreshLimit : code === "operation_fenced" ? m.fleet.refreshConflict : m.fleet.refreshUnknown);
      if (err instanceof api.APIError && err.status < 500 && err.status !== 429) { intent.current = null; callbacks.current.updated(); }
      if (err instanceof api.APIError && err.status === 401) callbacks.current.unauthorized();
    } finally { clearTimeout(deadline); if (alive.current) setBusy(false); }
  }

  const supported = node.capabilities.includes("snapshot.refresh.v1");
  const pending = Boolean(operation && !terminal(operation.status));
  const statusText = operation ? {
    queued: m.fleet.refreshQueued, leased: m.fleet.refreshLeased, succeeded: m.fleet.refreshSucceeded,
    conflict: m.fleet.refreshConflict, expired: m.fleet.refreshExpired, failed: m.fleet.refreshFailed, rejected: m.fleet.refreshRejected,
  }[operation.status] : "";
  return <div class="fleet-refresh">
    <div class="actions"><button class="button" type="button" onClick={() => void create()} disabled={busy || pending || !supported || node.status !== "online" || !view?.available || view.stale}>
      {busy ? m.fleet.refreshing : error || operation && terminal(operation.status) && operation.status !== "succeeded" ? m.fleet.retryRefresh : m.fleet.refresh}
    </button></div>
    {!supported && <p class="muted">{m.fleet.refreshUnsupported}</p>}
    {(error || statusText) && <p role="status" class={`notice ${error || operation?.status !== "succeeded" ? "warn" : ""}`}>{error || statusText}</p>}
    {operation && pending && <p class="muted"><time dateTime={operation.expires_at}>{new Date(operation.expires_at).toLocaleString(document.documentElement.lang)}</time></p>}
  </div>;
}
