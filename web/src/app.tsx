import { PasswordInput, TextInput } from "@mantine/core";
import { AppProviders } from "./theme";
import { Button } from "./ui";
import * as api from "./api";
import { useI18n } from "./context";
import { actionErrorMessage, errorMessage } from "./errors";
import { ClientConfigPanel } from "./export-panel";
import { defaultCreateProfile } from "./form-values";
import { ClientSettingsForm, CreateClientForm, CreateTunnelForm, DeleteTunnelConfirmation, ProtocolForm, TunnelSettingsForm } from "./forms";
import { initialLocale, localeStorageKey, messages } from "./i18n";
import type { Locale } from "./i18n";
import { Icon } from "./icons";
import { MaintenanceCenter } from "./maintenance";
import { navigate } from "./navigation";
import { ConfirmAction, Dialog, Empty, Toast } from "./shared";
import type { AppState, Tunnel } from "./types";
import type { LoadResult, Modal, RunAction, RunActionOptions } from "./ui-types";
import { profileTitle } from "./utils";
import { AllClients, Brand, ClientRow, FooterLinks, Shell, TunnelCard, TunnelFirstDashboard } from "./workspace";
import { useCallback, useEffect, useRef, useState } from "react";

const themeKey = "awg-forge.theme";
const dashboardFilterKey = "awg-forge.dashboard-filter";

export function App() {
  const [state, setState] = useState<AppState | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [confirmation, setConfirmation] = useState<{ title: string; description: string; submit: string; action: () => Promise<void>; danger?: boolean } | null>(null);
  const [modal, setModal] = useState<Modal | null>(null);
  const [toast, setToast] = useState("");
  const [theme, setTheme] = useState(initialTheme);
  const [locale, setLocale] = useState<Locale>(initialLocale);
  const [clientTunnelFilter, setClientTunnelFilter] = useState("all");
  const [dashboardFilter, setDashboardFilterValue] = useState(localStorage.getItem(dashboardFilterKey) || "all");
  const [requestedTunnelID, setRequestedTunnelID] = useState<string | null>(null);
  const selectionVersion = useRef(0);
  const liveUpdatesEnabled = state !== null;
  const m = messages[locale];
  const messagesRef = useRef(m);
  const authenticatedRef = useRef(false);
  messagesRef.current = m;

  const load = useCallback(async (options: { quiet?: boolean } = {}): Promise<LoadResult> => {
    try {
      const next = await api.state();
      authenticatedRef.current = true;
      setState(next);
      setAuthChecked(true);
      return "ok";
    } catch (err) {
      setAuthChecked(true);
      if (err instanceof api.APIError && err.status === 401) {
        const wasAuthenticated = authenticatedRef.current;
        authenticatedRef.current = false;
        setModal(null);
        setConfirmation(null);
        setState(null);
        if (wasAuthenticated) notify(messagesRef.current.common.sessionExpired);
        return "unauthorized";
      }
      if (!options.quiet) notify(errorMessage(err, messagesRef.current.common.requestFailed));
      return "failed";
    }
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem(themeKey, theme);
  }, [theme]);

  useEffect(() => {
    document.documentElement.lang = locale;
    localStorage.setItem(localeStorageKey, locale);
  }, [locale]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!liveUpdatesEnabled) return undefined;
    let fallback: ReturnType<typeof globalThis.setInterval> | null = null;
    const events = new EventSource("/api/events");
    events.addEventListener("state", (event) => {
      try {
        setState(JSON.parse((event as MessageEvent).data) as AppState);
      } catch {
        notify(messagesRef.current.events.liveUpdateFailed);
      }
    });
    events.onerror = () => {
      events.close();
      void load({ quiet: true });
      fallback = globalThis.setInterval(() => void load({ quiet: true }), 5000);
    };
    const authCheck = globalThis.setInterval(() => void load({ quiet: true }), 60000);
    return () => {
      events.close();
      if (fallback) globalThis.clearInterval(fallback);
      globalThis.clearInterval(authCheck);
    };
  }, [liveUpdatesEnabled, load]);

  const profiles = state?.profiles || [];
  const active = defaultCreateProfile(profiles);
  const allTunnels = state?.tunnels || [];

  function setDashboardFilter(filter: string) {
    localStorage.setItem(dashboardFilterKey, filter);
    setDashboardFilterValue(filter);
  }

  function beginTunnelCreate() {
    const version = ++selectionVersion.current;
    setRequestedTunnelID(null);
    return (tunnelID: string) => {
      if (version !== selectionVersion.current) return false;
      localStorage.setItem("awg-forge.selected-tunnel", tunnelID);
      setRequestedTunnelID(tunnelID);
      return true;
    };
  }

  function selectTunnelExplicitly() {
    selectionVersion.current += 1;
    setRequestedTunnelID(null);
  }

  function notify(message: string) {
    setToast(message);
    globalThis.setTimeout(() => setToast((current) => (current === message ? "" : current)), 3600);
  }

  async function runAction(label: string, fn: () => Promise<unknown>, options: RunActionOptions = { reload: true }) {
    try {
      await fn();
      if (options.close !== false) setModal(null);
      if (options.reload !== false) await load({ quiet: true });
      notify(label);
    } catch (err) {
      if (options.errorMode === "inline") throw err;
      const message = actionErrorMessage(err, m);
      if (message.includes("apply failed")) {
        setModal(null);
        await load({ quiet: true });
      }
      notify(message);
    }
  }

  const i18n = { locale, setLocale, m };
  const shellProps = { theme, setTheme, locale, setLocale, resetTunnelFilter: () => setDashboardFilter("all"), notify, reload: async () => { await load({ quiet: true }); } };

  if (!authChecked) return <AppProviders i18n={i18n} theme={theme}><Splash /></AppProviders>;
  if (!state) {
    return (
      <AppProviders i18n={i18n} theme={theme}>
        <Login onLogin={() => load()} {...shellProps} />
        <Toast message={toast} />
      </AppProviders>
    );
  }
  if (!active) return <AppProviders i18n={i18n} theme={theme}><Shell state={state} {...shellProps} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}><Empty title={m.dashboard.noProfiles} text={m.dashboard.noProfilesText} /></Shell></AppProviders>;

  const requestRegeneration = (tunnel: Tunnel) => setConfirmation({
    title: m.forms.regenerate,
    description: `${tunnel.name} · ${profileTitle(tunnel.profile)}. ${m.forms.regenerateConfirm} ${m.ui.regenerateDetail} ${tunnel.profile === "awg_3" ? m.ui.awg3RegenerateDetail : ""}`,
    submit: m.forms.regenerate,
    action: () => runAction(m.forms.protocolRegenerated, () => api.regenerateProtocol(tunnel.id, tunnel.profile), { errorMode: "inline" }),
  });

  const renderTunnel = (tunnel: Tunnel) => (
    <TunnelCard
      key={tunnel.id}
      tunnel={tunnel}
      onCreateClient={() => setModal({ kind: "create-client", tunnelID: tunnel.id })}
      onSettings={() => setModal({ kind: "settings", tunnel })}
      onProtocol={() => setModal({ kind: "protocol", tunnel })}
      renderSettings={() => <TunnelSettingsForm key={`${tunnel.id}:${tunnel.revision}`} state={state} tunnel={tunnel} runAction={runAction} />}
      renderProtocol={() => <ProtocolForm key={`${tunnel.id}:${tunnel.revision}`} tunnel={tunnel} runAction={runAction} onRegenerate={() => requestRegeneration(tunnel)} />}
      onRegenerate={() => requestRegeneration(tunnel)}
      onRestart={() => runAction(m.actions.tunnelRestarted, () => api.restartTunnel(tunnel.id))}
      onDelete={() => {
        if (tunnel.clients.some((client) => client.ever_connected)) {
          setModal({ kind: "delete-tunnel", tunnel });
          return;
        }
        setConfirmation({ title: m.actions.deleteTunnelTitle, description: m.actions.deleteTunnelConfirm(tunnel.name), submit: m.common.delete, danger: true, action: () => runAction(m.actions.tunnelDeleted, () => api.deleteTunnel(tunnel.id), { errorMode: "inline" }) });
      }}
      onClientConfig={(client) => setModal({ kind: "client-config", client })}
      onClientSettings={(client) => setModal({ kind: "client-settings", tunnel, client })}
      onClientToggle={(client) => {
        if (!client.enabled && client.traffic?.exceeded) {
          notify(m.forms.trafficLimitEnableBlocked);
          return;
        }
        void runAction(client.enabled ? m.actions.clientDisabled : m.actions.clientEnabled, () => api.setClientEnabled(client.id, !client.enabled));
      }}
      onClientDelete={(client) => setConfirmation({ title: m.common.delete, description: m.actions.deleteClientConfirm(client.name, tunnel.name, client.address), submit: m.common.delete, danger: true, action: () => runAction(m.actions.clientDeleted, () => api.deleteClient(client.id), { errorMode: "inline" }) })}
    />
  );

  const overlays = <>
    {modal && <Dialog title={modalTitle(modal, m)} wide={modal.kind === "protocol" || modal.kind === "client-config"} onClose={() => setModal(null)}><ModalContent onTunnelCreateStarted={beginTunnelCreate} onClientCreated={(tunnelID) => {
      if (modal.kind === "create-client" && modal.origin === "global") {
        if (clientTunnelFilter !== "all") setClientTunnelFilter(tunnelID);
        navigate("clients");
      }
    }} onRegenerate={requestRegeneration} modal={modal} state={state} notify={notify} close={() => setModal(null)} reload={async () => { await load({ quiet: true }); }} runAction={runAction} /><Toast message={toast} /></Dialog>}
    {confirmation && <ConfirmAction title={confirmation.title} description={confirmation.description} submit={confirmation.submit} danger={confirmation.danger} onClose={() => setConfirmation(null)} onConfirm={confirmation.action} />}
    {!modal && <Toast message={toast} />}
  </>;
  const renderClient = (client: import("./types").Client, tunnel: Tunnel) => <ClientRow key={client.id} client={client} tunnelName={tunnel.name}
    onConfig={() => setModal({ kind: "client-config", client })}
    onSettings={() => setModal({ kind: "client-settings", client, tunnel })}
    onToggle={() => { if (!client.enabled && client.traffic?.exceeded) { notify(m.forms.trafficLimitEnableBlocked); return; } void runAction(client.enabled ? m.actions.clientDisabled : m.actions.clientEnabled, () => api.setClientEnabled(client.id, !client.enabled)); }}
    onDelete={() => setConfirmation({ title: m.common.delete, description: m.actions.deleteClientConfirm(client.name, tunnel.name, client.address), submit: m.common.delete, danger: true, action: () => runAction(m.actions.clientDeleted, () => api.deleteClient(client.id), { errorMode: "inline" }) })}
  />;
  const onCreateClient = (tunnel?: Tunnel) => setModal({ kind: "create-client", tunnelID: tunnel?.id, origin: "global" });
  const renderClients = () => <AllClients onCreateTunnel={() => setModal({ kind: "create-tunnel", profile: active })} state={state} tunnelFilter={clientTunnelFilter} setTunnelFilter={setClientTunnelFilter} renderClient={renderClient} onCreateClient={onCreateClient} />;
  return (
    <AppProviders i18n={i18n} theme={theme}>
    <Shell onCreateTunnel={() => setModal({ kind: "create-tunnel", profile: active })} onCreateClient={onCreateClient} onExplicitTunnelSelected={selectTunnelExplicitly} renderClient={renderClient} overlays={overlays} renderClients={renderClients} state={state} {...shellProps} logout={() => doLogout(setState)} openMaintenance={() => setModal({ kind: "maintenance" })}>
      <TunnelFirstDashboard
        profiles={profiles}
        tunnels={allTunnels}
        filter={dashboardFilter}
        setFilter={setDashboardFilter}
        requestedTunnelID={requestedTunnelID}
        onRequestedTunnelOpened={() => setRequestedTunnelID(null)}
        onExplicitTunnelSelected={selectTunnelExplicitly}
        onCreateTunnel={(profile) => setModal({ kind: "create-tunnel", profile: profile || active })}
        renderTunnel={renderTunnel}
      />
    </Shell>
    </AppProviders>
  );
}

function Login({ onLogin, theme, setTheme, locale, setLocale }: { onLogin: () => Promise<LoadResult>; notify: (message: string) => void; theme: string; setTheme: (theme: string) => void; locale: Locale; setLocale: (locale: Locale) => void }) {
  const { m } = useI18n();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [secureCookieRejected, setSecureCookieRejected] = useState(false);
  const documentationURL = `https://github.com/astronaut808/awg-forge/blob/master/docs/${locale}/configuration.md`;
  return (
    <main className="login-shell">
      <div className="login-stack">
        <section className="panel login-card">
          <Brand />
          <div className="login-heading"><h1>{m.ui.loginTitle}</h1><p>{m.ui.loginSubtitle}</p></div>
          <form
            className="form single"
            onSubmit={async (event) => {
              event.preventDefault();
              setBusy(true);
              setSecureCookieRejected(false);
              setError("");
              try {
                await api.login(username, password);
                if (await onLogin() === "unauthorized") setSecureCookieRejected(true);
              } catch (err) {
                setError(err instanceof api.APIError && err.status === 401 ? m.ui.invalidCredentials : errorMessage(err, m.common.requestFailed));
              } finally {
                setBusy(false);
              }
            }}
          >
            <TextInput className="form-control" label={m.ui.username} aria-label={m.ui.username} name="username" autoComplete="username" autoCapitalize="none" spellCheck={false} required value={username} onChange={(event) => setUsername(event.currentTarget.value)} />
            <PasswordInput visibilityToggleButtonProps={{ "aria-label": m.ui.togglePassword }} className="form-control" label={m.login.password} aria-label={m.login.password} required autoComplete="current-password" value={password} onChange={(event) => setPassword(event.currentTarget.value)} />
            <Button className="button primary wide" disabled={busy} type="submit">{busy ? m.login.loggingIn : m.login.logIn}</Button>
            {error && <p className="form-error" role="alert">{error}</p>}
            {secureCookieRejected && (
              <div className="notice login-notice" role="alert">
                <p>{m.login.secureCookieRejected}</p>
                <p>{m.login.secureCookieHTTPS}</p>
                <p>{m.login.secureCookieHTTP} <code>SESSION_COOKIE_SECURE=false</code></p>
                <a href={documentationURL} target="_blank" rel="noreferrer">{m.common.docs}</a>
              </div>
            )}
          </form>
        </section>
        <FooterLinks
          extra={(
            <>
              <Button className="footer-link icon" type="button" title={m.aria.toggleTheme} aria-label={m.aria.toggleTheme} onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
                <Icon name={theme === "dark" ? "sun" : "moon"} />
              </Button>
              <Button className="footer-link" type="button" title={m.aria.toggleLanguage} aria-label={m.aria.toggleLanguage} onClick={() => setLocale(locale === "en" ? "ru" : "en")}>
                {locale === "en" ? "RU" : "EN"}
              </Button>
            </>
          )}
        />
      </div>
    </main>
  );
}

function ModalContent({ modal, state, notify, close, reload, runAction, onRegenerate, onTunnelCreateStarted, onClientCreated }: {
  modal: Modal;
  onRegenerate: (tunnel: Tunnel) => void;
  onTunnelCreateStarted: () => (tunnelID: string) => boolean;
  onClientCreated: (tunnelID: string) => void;
  state: AppState;
  notify: (message: string) => void;
  close: () => void;
  reload: () => Promise<void>;
  runAction: RunAction;
}) {
  if (modal.kind === "create-tunnel") return <CreateTunnelForm state={state} profile={modal.profile} runAction={runAction} onCreateStarted={onTunnelCreateStarted} />;
  if (modal.kind === "settings") return <TunnelSettingsForm state={state} tunnel={modal.tunnel} runAction={runAction} />;
  if (modal.kind === "protocol") return <ProtocolForm tunnel={modal.tunnel} runAction={runAction} onRegenerate={() => onRegenerate(modal.tunnel)} />;
  if (modal.kind === "create-client") return <CreateClientForm tunnels={state.tunnels} initialTunnelID={modal.tunnelID} onCreated={onClientCreated} trafficLimitsEnabled={state.database.enabled} runAction={runAction} />;
  if (modal.kind === "client-settings") return <ClientSettingsForm client={modal.client} runAction={runAction} />;
  if (modal.kind === "client-config") {
    return <ClientConfigPanel key={modal.client.id} client={modal.client} notify={notify} />;
  }
  if (modal.kind === "delete-tunnel") return <DeleteTunnelConfirmation tunnel={modal.tunnel} close={close} runAction={runAction} />;
  return <MaintenanceCenter state={state} notify={notify} close={close} reload={reload} />;
}

function Splash() {
  const { m } = useI18n();
  return <main className="login-shell"><section className="panel login-card"><Brand /><p>{m.login.loading}</p></section></main>;
}

function initialTheme(): string {
  const stored = localStorage.getItem(themeKey);
  if (stored === "light" || stored === "dark") return stored;
  return globalThis.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

async function doLogout(setState: (state: AppState | null) => void) {
  try {
    await api.logout();
  } finally {
    setState(null);
  }
}

function modalTitle(modal: Modal, m: (typeof messages)["en"]): string {
  switch (modal.kind) {
    case "create-tunnel": return m.forms.createTunnelTitle;
    case "settings": return m.forms.tunnelSettingsTitle;
    case "protocol": return m.forms.protocolTitle;
    case "create-client": return m.forms.createClientTitle;
    case "client-settings": return m.forms.clientSettingsTitle;
    case "client-config": return m.clientConfig.title;
    case "delete-tunnel": return m.actions.deleteTunnelTitle;
    case "maintenance": return m.common.maintenance;
  }
}
