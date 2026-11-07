import { Tabs, Textarea } from "@mantine/core";
import { Button } from "./ui";
import * as api from "./api";
import { useI18n } from "./context";
import { errorMessage } from "./errors";
import { Dialog, PanelTitle } from "./shared";
import type { Client } from "./types";
import type { ExpandedQR, QRImportMode } from "./ui-types";
import { downloadConfig } from "./utils";
import { useEffect, useRef, useState } from "react";

function qrImageKey(clientID: string, mode: QRImportMode, chunk: number): string {
  return `${clientID}:${mode}:${mode === "amneziavpn" ? chunk : 0}`;
}

export function ClientConfigPanel({ client, notify }: { client: Client; notify: (message: string) => void }) {
  const { m } = useI18n();
  const notifyRef = useRef(notify);
  const qrOpenerRef = useRef<HTMLButtonElement | null>(null);
  const qrImageURLsRef = useRef<Record<string, string>>({});
  const [importKey, setImportKey] = useState("");
  const [importWarning, setImportWarning] = useState("");
  const [vpnQRChunks, setVPNQRChunks] = useState(1);
  const [vpnQRChunk, setVPNQRChunk] = useState(0);
  const [qrMode, setQRMode] = useState<QRImportMode>("amneziavpn");
  const [qrImageURLs, setQRImageURLs] = useState<Record<string, string>>({});
  const [loadingQRKeys, setLoadingQRKeys] = useState<Record<string, boolean>>({});
  const [failedQRKeys, setFailedQRKeys] = useState<Record<string, boolean>>({});
  const [expandedQR, setExpandedQR] = useState<ExpandedQR | null>(null);
  const [busy, setBusy] = useState(false);
  const activeQRMode: QRImportMode = qrMode;
  const hasVPNQRSeries = vpnQRChunks > 1;
  const vpnQRDownloadName = hasVPNQRSeries ? `${client.name}-amneziavpn-${vpnQRChunk + 1}-of-${vpnQRChunks}.png` : `${client.name}-amneziavpn.png`;
  const activeQRKey = qrImageKey(client.id, activeQRMode, vpnQRChunk);
  const activeQRRequestURL = activeQRMode === "amneziavpn"
    ? api.clientAmneziaVPNQRCodeURL(client.id, vpnQRChunk)
    : api.clientQRCodeURL(client.id);
  const activeQRURL = qrImageURLs[activeQRKey] || "";
  const activeQRLoading = Boolean(loadingQRKeys[activeQRKey]);
  const activeQRFailed = Boolean(failedQRKeys[activeQRKey]);
  const activeQRTitle = activeQRMode === "amneziavpn" ? m.clientConfig.amneziaVPNQR : m.clientConfig.amneziaWGQR;
  const activeQRDescription = activeQRMode === "amneziavpn"
    ? m.clientConfig.amneziaVPNDescription
    : m.clientConfig.amneziaWGDescription;
  const activeQRAlt = activeQRMode === "amneziavpn" ? m.clientConfig.amneziaVPNAlt(client.name) : m.clientConfig.amneziaWGAlt(client.name);
  const activeQRDownloadName = activeQRMode === "amneziavpn" ? vpnQRDownloadName : `${client.name}-amneziawg.png`;
  const expandedQRKey = expandedQR ? qrImageKey(client.id, expandedQR.mode, expandedQR.chunk) : "";
  const expandedQRURL = expandedQRKey ? qrImageURLs[expandedQRKey] || "" : "";
  const expandedQRFailed = Boolean(failedQRKeys[expandedQRKey]);
  const expandedQRTitle = expandedQR?.mode === "amneziavpn" ? m.clientConfig.amneziaVPNQR : m.clientConfig.amneziaWGQR;
  const expandedQRDownloadName = expandedQR?.mode === "amneziavpn"
    ? (hasVPNQRSeries ? `${client.name}-amneziavpn-${(expandedQR?.chunk || 0) + 1}-of-${vpnQRChunks}.png` : `${client.name}-amneziavpn.png`)
    : `${client.name}-amneziawg.png`;
  const messagesRef = useRef(m);
  messagesRef.current = m;

  useEffect(() => {
    notifyRef.current = notify;
  }, [notify]);

  useEffect(() => {
    return () => {
      for (const url of Object.values(qrImageURLsRef.current)) URL.revokeObjectURL(url);
      qrImageURLsRef.current = {};
    };
  }, [client.id]);

  useEffect(() => {
    let cancelled = false;
    setVPNQRChunks(1);
    setVPNQRChunk(0);
    api.clientAmneziaVPNQRSeries(client.id)
      .then((res) => {
        if (cancelled) return;
        setVPNQRChunks(Math.max(1, res.chunks));
      })
      .catch((err) => notifyRef.current(errorMessage(err, messagesRef.current.common.requestFailed)));
    return () => {
      cancelled = true;
    };
  }, [client.id]);

  useEffect(() => {
    if (qrImageURLs[activeQRKey] || failedQRKeys[activeQRKey]) return;

    let cancelled = false;
    const controller = new AbortController();
    setLoadingQRKeys((current) => ({ ...current, [activeQRKey]: true }));
    api.clientQRCodePNG(activeQRRequestURL, controller.signal)
      .then((image) => {
        const url = URL.createObjectURL(image);
        if (cancelled) {
          URL.revokeObjectURL(url);
          return;
        }
        qrImageURLsRef.current[activeQRKey] = url;
        setQRImageURLs((current) => ({ ...current, [activeQRKey]: url }));
      })
      .catch((err) => {
        if (cancelled || (err instanceof DOMException && err.name === "AbortError")) return;
        setFailedQRKeys((current) => ({ ...current, [activeQRKey]: true }));
      })
      .finally(() => {
        if (!cancelled) setLoadingQRKeys((current) => ({ ...current, [activeQRKey]: false }));
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [activeQRKey, activeQRRequestURL, failedQRKeys, qrImageURLs]);

  function selectVPNQRChunk(chunk: number) {
    setVPNQRChunk(chunk);
    setExpandedQR((current) => current?.mode === "amneziavpn" ? { ...current, chunk } : current);
  }

  function retryQR(key: string) {
    setFailedQRKeys((current) => {
      const next = { ...current };
      delete next[key];
      return next;
    });
  }

  async function loadImportKey(): Promise<string> {
    if (importKey) return importKey;
    if (busy) return "";
    setBusy(true);
    try {
      const res = await api.clientImportKey(client.id);
      setImportKey(res.import_key);
      setImportWarning(res.warning);
      return res.import_key;
    } catch (err) {
      notify(errorMessage(err, m.common.requestFailed));
      return "";
    } finally {
      setBusy(false);
    }
  }

  async function copyImportKey() {
    const key = importKey || await loadImportKey();
    if (!key) return;
    try {
      await navigator.clipboard.writeText(key);
      notify(m.clientConfig.vpnLinkCopied);
    } catch {
      notify(m.clientConfig.clipboardUnavailable);
    }
  }

  return <PanelTitle hideTitle title={m.clientConfig.title} subtitle={`${client.name} · ${client.address}`}>
    <div className="config-options">
      <section className="config-option qr-config-option">
        <div>
          <h3>{activeQRTitle}</h3>
          <p>{activeQRDescription}</p>
        </div>
        <Tabs value={qrMode} onChange={(value) => setQRMode(value as QRImportMode)} keepMounted={false}>
          <Tabs.List aria-label={m.clientConfig.qrImportMode}>
            <Tabs.Tab value="amneziavpn">AmneziaVPN</Tabs.Tab>
            <Tabs.Tab value="amneziawg">AmneziaWG</Tabs.Tab>
          </Tabs.List>
        </Tabs>
        <div className="qr-panel">
          <Button className="qr-image-button" type="button" hidden={!activeQRURL} disabled={!activeQRURL} onClick={(event) => { qrOpenerRef.current = event.currentTarget; setExpandedQR({ mode: activeQRMode, chunk: vpnQRChunk }); }} aria-label={m.clientConfig.openLarger(activeQRTitle)}>
            {activeQRURL && <img className="qr-image" src={activeQRURL} alt={activeQRAlt} />}
          </Button>
          {!activeQRURL && <div className="qr-image-loading" role={activeQRFailed ? "alert" : "status"}>
            <span>{activeQRFailed ? m.common.requestFailed : m.common.loading}</span>
            {activeQRFailed && <Button className="button" type="button" onClick={() => retryQR(activeQRKey)}>{m.common.retry}</Button>}
          </div>}
          {activeQRMode === "amneziavpn" && hasVPNQRSeries && <div className="qr-series">
            <Button className="button" type="button" disabled={vpnQRChunk === 0} onClick={() => selectVPNQRChunk(Math.max(0, vpnQRChunk - 1))}>{m.clientConfig.previous}</Button>
            <span>{m.clientConfig.qrCounter(vpnQRChunk + 1, vpnQRChunks)}</span>
            <Button className="button" type="button" disabled={vpnQRChunk + 1 >= vpnQRChunks} onClick={() => selectVPNQRChunk(Math.min(vpnQRChunks - 1, vpnQRChunk + 1))}>{m.clientConfig.next}</Button>
          </div>}
        </div>
        <div className="action-row">
          {activeQRURL
            ? <a className="button" href={activeQRURL} download={activeQRDownloadName}>{m.clientConfig.downloadQR} {activeQRMode === "amneziavpn" && hasVPNQRSeries ? `${vpnQRChunk + 1}` : ""}</a>
            : <Button className="button" type="button" disabled>{activeQRLoading ? m.common.loading : m.clientConfig.downloadQR}</Button>}
        </div>
      </section>
      <section className="config-option">
        <div>
          <h3>{m.clientConfig.importOptions}</h3>
          <p>{m.clientConfig.importOptionsText}</p>
        </div>
        <div className="config-actions">
          <Button className="button primary" type="button" onClick={() => downloadConfig(client.id)}>{m.clientConfig.downloadConf}</Button>
          <Button className="button" disabled={busy} type="button" onClick={copyImportKey}>{m.clientConfig.copyVpnKey}</Button>
        </div>
        {importKey && <Textarea className="mono import-key" aria-label={m.clientConfig.vpnImportLink} readOnly value={importKey} />}
        {importWarning && <p className="note">{importWarning}</p>}
      </section>
    </div>
    <p className="note">{m.clientConfig.secretWarning}</p>
    {expandedQR && <Dialog onClose={() => setExpandedQR(null)} title={expandedQRTitle} closeLabel={m.clientConfig.closePreview} returnFocusTo={qrOpenerRef.current}>
      <div className="stack">
        <p className="note">{expandedQR.mode === "amneziavpn" ? (hasVPNQRSeries ? m.clientConfig.qrCounter(expandedQR.chunk + 1, vpnQRChunks) : m.clientConfig.amneziaVPNImportQR) : m.clientConfig.rawConfQR}</p>
        {expandedQRURL ? <img className="qr-lightbox-image" src={expandedQRURL} alt={m.clientConfig.expandedAlt(expandedQRTitle, client.name)} /> : <div className="qr-image-loading" role={expandedQRFailed ? "alert" : "status"}><span>{expandedQRFailed ? m.common.requestFailed : m.common.loading}</span>{expandedQRFailed && <Button className="button" onClick={() => retryQR(expandedQRKey)}>{m.common.retry}</Button>}</div>}
        {expandedQR.mode === "amneziavpn" && hasVPNQRSeries && <div className="qr-series">
          <Button className="button" type="button" disabled={expandedQR.chunk === 0} onClick={() => selectVPNQRChunk(Math.max(0, expandedQR.chunk - 1))}>{m.clientConfig.previous}</Button>
          <span>{m.clientConfig.qrCounter(expandedQR.chunk + 1, vpnQRChunks)}</span>
          <Button className="button" type="button" disabled={expandedQR.chunk + 1 >= vpnQRChunks} onClick={() => selectVPNQRChunk(Math.min(vpnQRChunks - 1, expandedQR.chunk + 1))}>{m.clientConfig.next}</Button>
        </div>}
        {expandedQRURL
          ? <a className="button" href={expandedQRURL} download={expandedQRDownloadName}>{m.clientConfig.downloadQR}</a>
          : <Button className="button" type="button" disabled>{expandedQRFailed ? m.clientConfig.downloadQR : m.common.loading}</Button>}
      </div>
    </Dialog>}
  </PanelTitle>;
}
