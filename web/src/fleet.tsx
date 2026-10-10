import type { ComponentChildren } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import * as api from "./api";
import type { Messages } from "./i18n";
import type { NodeInventory, NodeProjection, NodeView } from "./types";
import { formatBytes } from "./utils";

type FleetProps = {
  selected: string;
  select: (nodeID: string) => void;
  unauthorized: () => void;
  m: Messages;
  children: ComponentChildren;
};

// Inventory and remote views live only in memory. Each authentication context
// mounts a fresh workspace; selecting a node immediately unmounts its predecessor.
export function FleetWorkspace({ selected, select, unauthorized, m, children }: FleetProps) {
  const [inventory, setInventory] = useState<NodeInventory | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const controllerRef = useRef("");
  const selectRef = useRef(select);
  const unauthorizedRef = useRef(unauthorized);
  selectRef.current = select;
  unauthorizedRef.current = unauthorized;

  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    let pending: AbortController | null = null;
    const refresh = async () => {
      pending = new AbortController();
      const deadline = setTimeout(() => pending?.abort(), 10_000);
      try {
        const next = await api.controllerNodes(pending.signal);
        if (disposed) return;
        if (controllerRef.current && controllerRef.current !== next.controller_id) selectRef.current("");
        controllerRef.current = next.controller_id;
        setInventory(next);
        setUnavailable(false);
      } catch (err) {
        if (disposed) return;
        setUnavailable(true);
        if (err instanceof api.APIError && err.status === 401) unauthorizedRef.current();
      } finally {
        clearTimeout(deadline);
        if (!disposed) timer = setTimeout(refresh, 5000);
      }
    };
    void refresh();
    return () => { disposed = true; clearTimeout(timer); pending?.abort(); };
  }, []);

  const node = inventory?.nodes.find((item) => item.node_id === selected);
  const status = (value: NodeView["status"]) => m.fleet[value];
  return <>
    <section class="panel fleet-switcher">
      <label class="field fleet-select"><span>{m.fleet.server}</span>
        <select aria-label={m.fleet.server} value={selected} onChange={(event) => select(event.currentTarget.value)}>
          <option value="">{m.fleet.thisServer}</option>
          {inventory?.nodes.map((item) => <option key={item.node_id} value={item.node_id}>{item.name} · {item.node_id.slice(0, 8)} · {status(item.status)}</option>)}
        </select>
      </label>
      <p class="muted">{selected ? m.fleet.readOnly : m.fleet.localControls}</p>
      {unavailable && <p role="status" class="notice warn">{m.fleet.unavailable}</p>}
    </section>
    {!selected ? children : node && inventory && !unavailable
      ? <ReadOnlyNode key={`${inventory.controller_id}:${node.node_id}:${node.binding_epoch}:${node.state_epoch}:${node.boot_id}:${node.status}`} node={node} controllerID={inventory.controller_id} unauthorized={unauthorized} m={m} />
      : <section class="panel fleet-summary" role="status">{inventory ? m.fleet.unavailable : m.common.loading}</section>}
  </>;
}

function ReadOnlyNode({ node, controllerID, unauthorized, m }: { node: NodeView; controllerID: string; unauthorized: () => void; m: Messages }) {
  const [view, setView] = useState<NodeProjection | null>(null);
  const [failed, setFailed] = useState(false);
  const unauthorizedRef = useRef(unauthorized);
  unauthorizedRef.current = unauthorized;
  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    let pending: AbortController | null = null;
    const refresh = async () => {
      pending = new AbortController();
      const deadline = setTimeout(() => pending?.abort(), 10_000);
      try {
        const next = await api.controllerNode(node.node_id, pending.signal);
        if (disposed) return;
        // Metadata belongs to the inventory context that initiated this read.
        // An epoch changing during flight clears the view until the next inventory.
        if (next.controller_id !== controllerID || next.node.node_id !== node.node_id || next.node.binding_epoch !== node.binding_epoch || next.node.state_epoch !== node.state_epoch || next.node.boot_id !== node.boot_id) {
          setView(null); setFailed(true); return;
        }
        setView(next); setFailed(false);
      } catch (err) {
        if (disposed) return;
        setView(null); setFailed(true);
        if (err instanceof api.APIError && err.status === 401) unauthorizedRef.current();
      } finally {
        clearTimeout(deadline);
        if (!disposed) timer = setTimeout(refresh, 5000);
      }
    };
    void refresh();
    return () => { disposed = true; clearTimeout(timer); pending?.abort(); };
  }, [node.node_id, node.binding_epoch, node.state_epoch, node.boot_id, controllerID]);

  const data = view?.desired;
  const observations = view?.observations;
  const stale = !view || view.stale;
  const status = view?.node.status || node.status;
  const doctor = observations?.doctor;
  return <section class="fleet-view" aria-label={m.fleet.nodeView}>
    <header class="panel fleet-summary">
      <h1>{node.name}</h1>
      <p class="muted mono fleet-identity">{node.node_id}</p>
      <div class="fleet-facts">
        <span>{m.fleet.connection}: <strong>{m.fleet[status]}</strong></span>
        <span>{m.fleet.version}: <strong>{node.application_version || "—"}</strong></span>
        <span>{m.fleet.lastConfirmed}: <time>{displayTime(node.last_confirmed_at)}</time></span>
        <span>{m.fleet.snapshotTime}: <time>{displayTime(view?.observed_at)}</time></span>
        <span>{m.fleet.receivedTime}: <time>{displayTime(view?.received_at)}</time></span>
      </div>
      <p class={`notice ${stale ? "warn" : ""}`} role="status">{stale ? m.fleet.stale : m.fleet.fresh}</p>
      <p class="muted">{m.fleet.connectionNote}</p>
    </header>
    {failed ? <p class="panel fleet-summary" role="status">{m.fleet.unavailable}</p>
      : !view ? <p role="status">{m.common.loading}</p>
      : !view.available ? <p class="panel fleet-summary" role="status">{status === "revoked" ? m.fleet.revokedNote : status === "incompatible" ? m.fleet.incompatibleNote : m.fleet.noSnapshot}</p>
      : <>
        <section class="panel fleet-summary" aria-label={m.fleet.doctor}>
          <h2>{m.fleet.doctor}</h2>
          <p class="muted">{m.fleet.doctorScope}</p>
          <div class="fleet-facts">
            <span>TUN: {doctor?.tun_available ? m.fleet.available : m.fleet.unavailableShort}</span>
            <span>{m.fleet.forwarding}: {doctor?.forwarding ? m.common.enabled : m.common.disabled}</span>
            <span>{m.fleet.down}: {doctor?.tunnels_down ?? "—"}</span>
            <span>{m.fleet.unknown}: {doctor?.runtime_unknown ?? "—"}</span>
            <span>{m.fleet.applyFailed}: {doctor?.apply_failures ?? "—"}</span>
          </div>
          <p class="muted">{observations?.history_available ? m.fleet.historyEnabled : m.fleet.historyOff}</p>
        </section>
        {!data?.tunnels.length && <p class="panel fleet-summary">{m.fleet.empty}</p>}
        {data?.tunnels.map((tunnel) => {
          const observation = observations?.tunnels.find((item) => item.id === tunnel.id);
          return <article class="panel fleet-tunnel" key={tunnel.id}>
            <header class="fleet-summary"><h2>{tunnel.name}</h2>
              <div class="fleet-facts"><span>{tunnel.profile} · {tunnel.interface} · UDP {tunnel.listen_port}</span>
                <span>{tunnel.enabled ? m.common.enabled : m.common.disabled}</span>
                <span>{m.fleet.vpn}: {observation?.known ? observation.up ? m.fleet.up : m.fleet.downShort : m.fleet.unknownShort}{stale ? ` · ${m.common.stale}` : ""}</span>
                <span>{m.fleet.revision}: {tunnel.revision}</span></div>
            </header>
            {!tunnel.clients.length ? <p class="fleet-summary muted">{m.fleet.noClients}</p>
              : <div class="fleet-table-scroll" tabIndex={0} role="region" aria-label={m.fleet.clients}>
                <table class="fleet-table"><thead><tr><th>{m.fleet.client}</th><th>{m.fleet.address}</th><th>{m.fleet.vpn}</th><th>{m.fleet.handshake}</th><th>RX</th><th>TX</th></tr></thead>
                  <tbody>{tunnel.clients.map((client) => {
                    const runtime = observation?.clients.find((item) => item.id === client.id);
                    return <tr key={client.id}><td><strong>{client.name}</strong><small class="muted mono">{client.id}</small></td><td>{client.address}</td>
                      <td>{!client.enabled ? m.common.disabled : !observation?.known ? m.fleet.unknownShort : runtime?.present ? m.fleet.peerPresent : m.fleet.peerMissing}</td>
                      <td>{displayTime(runtime?.last_handshake)}</td><td>{observation?.known ? formatBytes(runtime?.rx_bytes || 0) : "—"}</td><td>{observation?.known ? formatBytes(runtime?.tx_bytes || 0) : "—"}</td></tr>;
                  })}</tbody></table>
              </div>}
          </article>;
        })}
      </>}
  </section>;
}

function displayTime(value?: string | null): string {
  return !value || value.startsWith("0001-") ? "—" : new Date(value).toLocaleString(document.documentElement.lang);
}
