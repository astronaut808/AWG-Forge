import { Textarea, TextInput } from "@mantine/core";
import { useEffect, useId, useRef, useState } from "react";
import * as api from "./api";
import { useI18n } from "./context";
import { field } from "./form-values";
import { Icon } from "./icons";
import { Badge, ConfirmAction, Dialog, Form } from "./shared";
import type { Relay, RelayHealth, RelaySettings, Tunnel } from "./types";
import { Button } from "./ui";
import type { RunAction } from "./ui-types";

type RelayDialog = { kind: "add" } | { kind: "edit" | "delete"; relay: Relay } | { kind: "install"; id: string; created?: Relay };

export function TunnelRelays({ tunnel, serverHost, runAction }: { tunnel: Tunnel; serverHost: string; runAction: RunAction }) {
  const { m } = useI18n();
  const [dialog, setDialog] = useState<RelayDialog | null>(null);
  const titleID = useId();
  const section = useRef<HTMLElement>(null);
  const deletedRelay = useRef(false);
  const relays = tunnel.relays ?? [];
  const healthKey = JSON.stringify([tunnel.id, tunnel.enabled, tunnel.revision, tunnel.listen_port, relays.map(({ id, host, port, target_host, target_ipv4 }) => [id, host, port, target_host, target_ipv4])]);
  const [health, setHealth] = useState<{ key: string; values: Record<string, RelayHealth> }>();
  const hasRelays = relays.length > 0;
  useEffect(() => {
    if (!hasRelays) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      try {
        const result = await api.relayHealth(tunnel.id, controller.signal);
        if (!controller.signal.aborted) setHealth({ key: healthKey, values: result.health });
      } catch {
        if (!controller.signal.aborted) setHealth(undefined);
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(refresh, 15_000);
      }
    };
    void refresh();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [hasRelays, healthKey, tunnel.id]);
  const selectedID = dialog && dialog.kind !== "add" ? (dialog.kind === "install" ? dialog.id : dialog.relay.id) : null;
  const activeRelay = relays.find(({ id }) => id === selectedID);
  const created = dialog?.kind === "install" ? dialog.created : undefined;
  const addButton = () => section.current?.querySelector<HTMLButtonElement>(".relay-add") ?? undefined;

  useEffect(() => {
    if (!created || !activeRelay) return;
    setDialog((current) => current?.kind === "install" && current.created === created ? { kind: "install", id: current.id } : current);
  }, [created, activeRelay]);

  const close = () => {
    setDialog(null);
    if (deletedRelay.current) {
      deletedRelay.current = false;
      requestAnimationFrame(() => {
        if (document.activeElement === document.body) addButton()?.focus();
      });
    }
  };

  const installationRelay = activeRelay ?? created;
  return <section className="relay-section" aria-labelledby={titleID} ref={section}>
    <div className="clients-toolbar"><h3 id={titleID}>{m.relays.title}</h3><Button className="button primary relay-add" onClick={() => setDialog({ kind: "add" })}><Icon name="plus" />{m.relays.add}</Button></div>
    {relays.length ? <ul className="relay-list">
      {relays.map((relay) => <RelayRow key={relay.id} relay={relay} health={health?.key === healthKey ? health.values[relay.id] : undefined} listenPort={tunnel.listen_port} assigned={tunnel.clients.some((client) => client.relay_id === relay.id)} open={(kind) => setDialog(kind === "install" ? { kind, id: relay.id } : { kind, relay })} />)}
    </ul> : <p className="empty compact">{m.relays.empty}</p>}
    {(dialog?.kind === "add" || dialog?.kind === "edit") && <RelayEditor tunnel={tunnel} serverHost={serverHost} relay={dialog.kind === "edit" ? dialog.relay : undefined}
      runAction={runAction} close={close} onSaved={(newRelay) => setDialog((current) => {
        if (current !== dialog || !newRelay) return current === dialog ? null : current;
        return { kind: "install", id: newRelay.id, created: newRelay };
      })} />}
    {dialog?.kind === "install" && installationRelay && <RelayInstallation tunnelID={tunnel.id} relay={installationRelay} listenPort={tunnel.listen_port}
      returnFocusTo={created ? addButton() : undefined} close={close} />}
    {dialog?.kind === "delete" && <ConfirmAction
      title={m.relays.deleteTitle} description={m.relays.deleteConfirm(dialog.relay.name)} submit={m.common.delete} onClose={close} danger
      onConfirm={async () => {
        try {
          await runAction(m.relays.deleted, async () => {
            await api.deleteRelay(tunnel.id, dialog.relay.id);
            deletedRelay.current = true;
          }, { close: false, errorMode: "inline" });
        } catch (err) {
          if (err instanceof api.APIError && err.code === "relay_delete_failed") throw new Error(m.relays.deleteFailed);
          throw err;
        }
      }}
    />}
  </section>;
}

function RelayRow({ relay, health, listenPort, assigned, open }: { relay: Relay; health?: RelayHealth; listenPort: number; assigned: boolean; open: (kind: "edit" | "install" | "delete") => void }) {
  const { m } = useI18n();
  const assignmentID = useId();
  const status = health?.status ?? "pending";
  const tone = status === "reachable" ? "ok" : status === "unreachable" ? "bad" : status === "stale" ? "warn" : "";
  return <li className="relay-row">
    <h4>{relay.name}</h4>
    <dl className="relay-addresses">
      <div><dt>{m.relays.endpoint}</dt><dd className="mono">{relay.host}:{relay.port}</dd></div>
      <div><dt>{m.relays.destination}</dt><dd className="mono">{relay.target_host ?? relay.target_ipv4}:{listenPort}</dd></div>
      <div className="relay-health"><dt>{m.relays.healthLabel}</dt><dd><Badge tone={tone} title={m.relays.healthHint}>{m.relays.health[status]}</Badge>
        {health?.succeeded_at && <time className="note" dateTime={health.succeeded_at}>{m.relays.lastSuccess(new Date(health.succeeded_at).toLocaleString())}</time>}
      </dd></div>
    </dl>
    <div className="actions relay-actions">
      <Button className="button" onClick={() => open("install")}>{m.relays.installation}</Button>
      <Button className="button" onClick={() => open("edit")}>{m.common.edit}</Button>
      <Button className="button danger" disabled={assigned} aria-describedby={assigned ? assignmentID : undefined} onClick={() => open("delete")}>{m.common.delete}</Button>
    </div>
    {assigned && <p className="note relay-assignment" id={assignmentID}>{m.relays.assigned}</p>}
  </li>;
}

function ipv4Octets(value: string): number[] | null {
  const parts = value.split(".");
  if (parts.length !== 4 || parts.some((part) => !/^(0|[1-9][0-9]{0,2})$/.test(part) || Number(part) > 255)) return null;
  return parts.map(Number);
}

function usableIPv4([first, second]: number[]): boolean {
  return first > 0 && first < 224 && first !== 127 && !(first === 169 && second === 254);
}

function validAddress(value: string): boolean {
  const ip = ipv4Octets(value);
  if (ip) return usableIPv4(ip);
  const host = value.replace(/\.$/, "");
  const label = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;
  return host.length <= 253 && /[A-Za-z]/.test(host) && host.split(".").every((part) => label.test(part));
}

function RelayEditor({ tunnel, serverHost, relay, runAction, close, onSaved }: {
  tunnel: Tunnel; serverHost: string; relay?: Relay; runAction: RunAction; close: () => void; onSaved: (relay?: Relay) => void;
}) {
  const { m } = useI18n();
  const busy = useRef(false);
  const endpoint = tunnel.server_host?.trim() || serverHost?.trim() || "";
  const [target, setTarget] = useState(relay?.target_host ?? relay?.target_ipv4 ?? (validAddress(endpoint) ? endpoint : ""));
  const title = relay ? m.relays.editTitle : m.relays.add;
  const closeWhenIdle = () => { if (!busy.current) close(); };
  return <Dialog title={title} onClose={closeWhenIdle}>
    <Form title={title} subtitle={relay ? m.relays.editHelp : ""} submit={m.common.save} onSubmit={async (form) => {
      if (busy.current) return;
      const host = field(form, "host").trim();
      const destination = field(form, "destination").trim();
      const body: RelaySettings = {
        name: field(form, "name").trim() || host.slice(0, 64), host,
        port: Number(field(form, "port")),
        ...(ipv4Octets(destination) ? { target_ipv4: destination } : { target_host: destination }),
      };
      if (!body.name || new TextEncoder().encode(body.name).length > 64 || /\p{Cc}/u.test(body.name)) throw new Error(m.relays.invalidName);
      if (!validAddress(host)) throw new Error(m.relays.invalidHost);
      if (!validAddress(destination)) throw new Error(m.relays.invalidTarget);
      busy.current = true;
      try {
        let createdRelay: Relay | undefined;
        await runAction(m.relays.saved, async () => {
          if (relay) await api.updateRelay(tunnel.id, relay, body);
          else createdRelay = (await api.createRelay(tunnel.id, body)).relay;
        }, { close: false, errorMode: "inline" });
        onSaved(createdRelay);
      } catch (err) {
        if (err instanceof api.APIError && err.code === "relay_changed") throw new Error(m.relays.editConflict);
        if (err instanceof api.APIError && err.code === "relay_save_failed") throw new Error(m.relays.saveFailed);
        throw err;
      } finally {
        busy.current = false;
      }
    }}>
      <TextInput className="form-control" label={m.relays.host} name="host" defaultValue={relay?.host} maxLength={253} autoCapitalize="off" spellCheck={false} required data-autofocus />
      <TextInput className="form-control" label={m.relays.port} description={m.relays.portHint} name="port" type="number" min={1} max={65535} defaultValue={relay?.port ?? tunnel.listen_port} required />
      <TextInput className="form-control full" label={m.relays.target}
        description={m.relays.targetHint(tunnel.listen_port)}
        name="destination" value={target} onChange={(event) => setTarget(event.currentTarget.value)} maxLength={254} autoCapitalize="off" spellCheck={false} required />
      <TextInput className="form-control full" label={m.relays.name} name="name" defaultValue={relay?.name} placeholder={m.relays.namePlaceholder} maxLength={64} />
    </Form>
  </Dialog>;
}

function RelayInstallation({ tunnelID, relay, listenPort, returnFocusTo, close }: { tunnelID: string; relay: Relay; listenPort: number; returnFocusTo?: HTMLElement; close: () => void }) {
  const { m } = useI18n();
  return <Dialog title={m.relays.installation} onClose={close} returnFocusTo={returnFocusTo}>
    <div className="stack relay-installation">
      <p className="note relay-installation-name">{relay.name}</p>
      <p>{m.relays.installHelp}</p>
      <InstallCommand key={`${relay.id}:${relay.port}:${relay.target_host ?? relay.target_ipv4}:${listenPort}`} tunnelID={tunnelID} relayID={relay.id} />
      <p>{m.relays.removeHelp}</p>
      <CommandField label={m.relays.removeCommand} command={`sudo bash ./awg-forge-install.sh relay remove --id ${relay.id}`} />
      <div className="form-actions"><Button className="button" onClick={close}>{m.common.close}</Button></div>
    </div>
  </Dialog>;
}

type CommandResult = { kind: "loading" } | { kind: "ready"; command: string } | { kind: "error"; unavailable: boolean };

function InstallCommand({ tunnelID, relayID }: { tunnelID: string; relayID: string }) {
  const { m } = useI18n();
  const [attempt, setAttempt] = useState(0);
  const [result, setResult] = useState<CommandResult>({ kind: "loading" });
  useEffect(() => {
    let active = true;
    api.relayInstallCommand(tunnelID, relayID).then(({ command }) => {
      if (active) setResult({ kind: "ready", command });
    }).catch((err: unknown) => {
      if (active) setResult({ kind: "error", unavailable: err instanceof api.APIError && err.code === "relay_install_unavailable" });
    });
    return () => { active = false; };
  }, [tunnelID, relayID, attempt]);

  if (result.kind === "loading") return <p className="note" role="status">{m.common.loading}</p>;
  if (result.kind === "ready") return <CommandField label={m.relays.installCommand} command={result.command} />;
  return <div className="relay-command-error">
    <p className="form-error" role="alert">{result.unavailable ? m.relays.installError : m.relays.installRetryError}</p>
    {!result.unavailable && <Button className="button" onClick={() => {
      setResult({ kind: "loading" });
      setAttempt((current) => current + 1);
    }}>{m.common.retry}</Button>}
  </div>;
}

function CommandField({ label, command }: { label: string; command: string }) {
  const { m } = useI18n();
  const commandInput = useRef<HTMLTextAreaElement>(null);
  const [copyStatus, setCopyStatus] = useState<"copied" | "failed" | null>(null);

  async function copy() {
    const input = commandInput.current;
    setCopyStatus(null);
    try {
      await navigator.clipboard.writeText(command);
      if (input?.isConnected) setCopyStatus("copied");
    } catch {
      if (!input?.isConnected) return;
      input.focus();
      input.select();
      setCopyStatus("failed");
    }
  }

  return <div className="relay-command">
    <Textarea ref={commandInput} className="form-control" classNames={{ input: "relay-command-input mono" }} label={label} value={command} readOnly autosize minRows={2} />
    <div className="actions"><Button className="button" onClick={() => void copy()}>{m.relays.copy}</Button>
      <span className="note" role="status">{copyStatus === "copied" ? m.relays.copied : copyStatus === "failed" ? m.relays.copyFailed : ""}</span></div>
  </div>;
}
