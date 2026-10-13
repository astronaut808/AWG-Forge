import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import type { ClientConfigExport, ClientConfigExportRequest, NodeProjection, NodeView } from "./types";

type Props = { node: NodeView; controllerID: string; view: NodeProjection | null; updated: () => void; unauthorized: () => void; m: Messages };
type Intent = { nodeName: string; tunnelName: string; clientName: string; request: ClientConfigExportRequest };

function safeFilename(...values: string[]): string {
  return `awg-forge-${values.map((value) => value.replace(/[^A-Za-z0-9._-]/g, "_").slice(0, 64) || "client").join("-")}.conf`;
}

function validExport(value: unknown, request: ClientConfigExportRequest, nodeID: string): value is ClientConfigExport {
  if (!value || typeof value !== "object") return false;
  const item = value as Record<string, unknown>;
  const expiresAt = typeof item.expires_at === "string" ? Date.parse(item.expires_at) : Number.NaN;
  return Object.keys(item).length === 5 && ["export_id", "node_id", "tunnel_id", "client_id", "expires_at"].every((key) => key in item) && typeof item.export_id === "string" && /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(item.export_id) && item.node_id === nodeID && item.tunnel_id === request.tunnel_id && item.client_id === request.client_id && Number.isFinite(expiresAt) && expiresAt > Date.now() && expiresAt <= Date.now() + 61_000;
}

function safeDownloadResponse(response: Response): boolean {
  const type = response.headers.get("Content-Type")?.split(";", 1)[0].trim().toLowerCase();
  return type === "text/plain" && /(?:^|,)\s*no-store\s*(?:,|$)/i.test(response.headers.get("Cache-Control") || "") && /^attachment(?:;|$)/i.test(response.headers.get("Content-Disposition") || "");
}

async function boundedConfigBlob(response: Response): Promise<Blob> {
  const limit = 16 * 1024;
  const length = Number(response.headers.get("Content-Length"));
  if (Number.isFinite(length) && (length < 0 || length > limit)) throw new Error("export too large");
  const reader = response.body?.getReader();
  if (!reader) throw new Error("export body unavailable");
  const chunks: BlobPart[] = [];
  let size = 0;
  try {
    for (;;) {
      const next = await reader.read();
      if (next.done) break;
      size += next.value.byteLength;
      if (size > limit) throw new Error("export too large");
      const copy = new Uint8Array(next.value.byteLength);
      copy.set(next.value);
      chunks.push(copy);
    }
  } finally {
    if (size > limit) await reader.cancel();
    reader.releaseLock();
  }
  return new Blob(chunks, { type: "text/plain" });
}

// The export ID is held only in this mounted component. A context change cancels
// the request and best-effort cancels the server-side one-use export.
export function RemoteClientConfigExport({ node, controllerID, view, updated, unauthorized, m }: Props) {
  const [target, setTarget] = useState("");
  const [confirmation, setConfirmation] = useState<Intent | null>(null);
  const [phase, setPhase] = useState<"preparing" | "downloading" | null>(null);
  const [message, setMessage] = useState("");
  const active = useRef<AbortController | null>(null);
  const exportID = useRef("");
  const activeIntent = useRef<Intent | null>(null);
  const alive = useRef(true);
  const callbacks = useRef({ updated, unauthorized }); callbacks.current = { updated, unauthorized };
  const choices = view?.desired?.tunnels.flatMap((tunnel) => tunnel.clients.map((client) => ({ key: `${tunnel.id}/${client.id}`, tunnel, client }))) || [];
  const selected = choices.find((choice) => choice.key === target) || choices[0];
  const supported = node.capabilities.includes("client.config.export.v1");
  const enabled = supported && node.status === "online" && Boolean(view?.available) && !view?.stale;
  const context = useRef({ node, controllerID, view, enabled });
  context.current = { node, controllerID, view, enabled };

  const cancel = () => {
    const controller = active.current;
    active.current = null;
    controller?.abort();
    const id = exportID.current;
    exportID.current = "";
    activeIntent.current = null;
    if (id) void api.cancelClientConfigExport(node.node_id, id);
  };
  useEffect(() => () => { alive.current = false; cancel(); }, []);
  useEffect(() => { if (selected && selected.key !== target) setTarget(selected.key); }, [selected?.key]);

  function contextMatches(intent: Intent, current = context.current): boolean {
    const tunnel = current.view?.desired?.tunnels.find((item) => item.id === intent.request.tunnel_id);
    const client = tunnel?.clients.find((item) => item.id === intent.request.client_id);
    return current.enabled && current.view?.controller_id === current.controllerID && current.view.node.node_id === current.node.node_id && current.view.state_epoch === current.node.state_epoch && current.view.node.state_epoch === current.node.state_epoch && current.view.node.binding_epoch === current.node.binding_epoch && current.view.state_epoch === intent.request.expected_state_epoch && current.node.binding_epoch === intent.request.expected_binding_epoch && current.view.desired_generation === intent.request.expected_desired_generation && tunnel?.revision === intent.request.expected_tunnel_revision && Boolean(client);
  }

  useEffect(() => {
    if (!activeIntent.current || contextMatches(activeIntent.current)) return;
    cancel(); setPhase(null); setMessage(m.fleet.remoteExportConflict); callbacks.current.updated();
  }, [node.node_id, node.state_epoch, node.binding_epoch, node.status, controllerID, supported, enabled, view?.state_epoch, view?.node.state_epoch, view?.node.binding_epoch, view?.desired_generation, view?.available, view?.stale, view?.desired]);

  function begin() {
    if (!enabled || !selected || phase) return;
    setMessage("");
    setConfirmation({
      nodeName: node.name,
      tunnelName: selected.tunnel.name,
      clientName: selected.client.name,
      request: {
        expected_state_epoch: view!.state_epoch,
        expected_binding_epoch: node.binding_epoch,
        expected_desired_generation: view!.desired_generation,
        tunnel_id: selected.tunnel.id,
        client_id: selected.client.id,
        expected_tunnel_revision: selected.tunnel.revision,
      },
    });
  }

  async function submit() {
    const intent = confirmation;
    if (!intent || phase) return;
    if (!contextMatches(intent)) { setConfirmation(null); setMessage(m.fleet.remoteExportConflict); callbacks.current.updated(); return; }
    const controller = new AbortController();
    active.current = controller;
    activeIntent.current = intent;
    const deadline = setTimeout(() => controller.abort(), 45_000);
    setConfirmation(null); setPhase("preparing"); setMessage("");
    let ownExportID = "";
    try {
      const prepared = await api.prepareClientConfigExport(node.node_id, intent.request, controller.signal);
      if (!alive.current || active.current !== controller || controller.signal.aborted) return;
      if (!contextMatches(intent)) { setMessage(m.fleet.remoteExportConflict); return; }
      if (!validExport(prepared, intent.request, node.node_id)) { setMessage(m.fleet.remoteExportMetadata); return; }
      ownExportID = prepared.export_id;
      exportID.current = ownExportID;
      setPhase("downloading");
      const response = await api.downloadClientConfigExport(node.node_id, ownExportID, controller.signal);
      if (!alive.current || active.current !== controller || controller.signal.aborted) return;
      if (!contextMatches(intent)) { setMessage(m.fleet.remoteExportConflict); return; }
      // This is a consuming request. Any malformed or interrupted response is
      // intentionally not retried because the server may already have consumed it.
      if (!safeDownloadResponse(response)) { setMessage(m.fleet.remoteExportMetadata); return; }
      const blob = await boundedConfigBlob(response);
      if (!alive.current || active.current !== controller || controller.signal.aborted) return;
      if (!contextMatches(intent)) { setMessage(m.fleet.remoteExportConflict); return; }
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url; link.download = safeFilename(node.node_id, intent.request.tunnel_id, intent.request.client_id); link.rel = "noopener";
      document.body.appendChild(link); link.click(); link.remove();
      globalThis.setTimeout(() => URL.revokeObjectURL(url), 0);
      if (exportID.current === ownExportID) exportID.current = "";
      activeIntent.current = null;
      setMessage(m.fleet.remoteExportReady);
    } catch (error) {
      if (!alive.current || active.current !== controller) return;
      const code = error instanceof api.APIError ? error.code : "";
      setMessage(code === "recent_auth_required" ? m.fleet.remoteExportRecentAuth : m.fleet.remoteExportUnknown);
      if (error instanceof api.APIError && error.status === 401) callbacks.current.unauthorized();
    } finally {
      clearTimeout(deadline);
      if (alive.current && active.current === controller) {
        if (ownExportID && exportID.current === ownExportID) cancel(); else activeIntent.current = null;
        setPhase(null);
      }
    }
  }

  if (!choices.length && !confirmation && !phase && !message) return null;
  const disabled = !enabled || !selected || Boolean(phase);
  return <section class="stack" aria-label={m.fleet.remoteConfigExport}>
    <label>{m.fleet.remoteExportClient}<select aria-label={m.fleet.remoteExportClient} value={selected?.key || ""} disabled={Boolean(phase)} onChange={(event) => { setTarget(event.currentTarget.value); setConfirmation(null); setMessage(""); }}>
      {choices.map((choice) => <option key={choice.key} value={choice.key}>{choice.tunnel.name} / {choice.client.name}</option>)}
    </select></label>
    {!supported && <p class="muted">{m.fleet.remoteExportUnsupported}</p>}
    {!enabled && supported && <p class="muted">{m.fleet.remoteExportUnsafe}</p>}
    {confirmation ? <div class="notice warn"><p>{confirmation.nodeName} · {confirmation.tunnelName} · {confirmation.clientName}</p><p>{m.fleet.remoteExportImpact}</p><div class="actions"><button class="button" type="button" onClick={() => void submit()}>{m.fleet.remoteExportConfirm}</button><button class="button" type="button" onClick={() => setConfirmation(null)}>{m.fleet.clientCancel}</button></div></div>
      : <div class="actions"><button class="button" type="button" disabled={disabled} onClick={begin}>{m.fleet.remoteExport}</button>{phase && <button class="button" type="button" onClick={() => { cancel(); setPhase(null); setMessage(m.fleet.remoteExportUnknown); }}>{m.fleet.remoteExportCancel}</button>}</div>}
    {phase && <p role="status" class="notice warn">{phase === "preparing" ? m.fleet.remoteExportPreparing : m.fleet.remoteExportDownloading}</p>}
    {message && <p role="status" class="notice">{message}</p>}
  </section>;
}
