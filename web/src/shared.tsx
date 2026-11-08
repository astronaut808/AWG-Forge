import { Button, PopupLayerContext } from "./ui";
import { useI18n } from "./context";
import { actionErrorMessage } from "./errors";
import { classNames } from "./utils";
import { createContext, useCallback, useContext, useRef, useState } from "react";
import { Modal } from "@mantine/core";
import type { ReactNode } from "react";

const DialogContext = createContext<(() => void) | null>(null);
const dialogRoots: HTMLElement[] = [];

function dialogTabStops(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>("button, a[href], input, select, textarea, [tabindex]")).filter((element) =>
    element.tabIndex >= 0 && !element.matches(":disabled") && !element.closest("[hidden], [inert]") &&
    element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden"
  );
}

export function Form({ title, subtitle, submit, secondary, onSubmit, children }: { title: string; subtitle: string; submit: string; secondary?: ReactNode; onSubmit: (form: HTMLFormElement) => Promise<void>; children: ReactNode }) {
  const { m } = useI18n();
  const close = useContext(DialogContext);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <PanelTitle title={title} subtitle={subtitle}><form className="form form-grid" onSubmit={async (event) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await onSubmit(event.currentTarget as HTMLFormElement);
    } catch (err) {
      setError(actionErrorMessage(err, m));
    } finally {
      setBusy(false);
    }
  }}>{children}{error && <p className="form-error full" role="alert" aria-live="assertive">{error}</p>}<div className="form-actions full">{secondary}{close && <Button className="button" type="button" onClick={close}>{m.common.close}</Button>}<Button className="button primary" disabled={busy} type="submit"><ButtonContent busy={busy}>{submit}</ButtonContent></Button></div></form></PanelTitle>;
}

export function Dialog({ title, onClose, children, wide = false, size, closeLabel, returnFocusTo }: { title: string; onClose: () => void; children: ReactNode; wide?: boolean; size?: string; closeLabel?: string; returnFocusTo?: HTMLElement | null }) {
  const { m } = useI18n();
  const [popupLayer, setPopupLayer] = useState<HTMLElement | null>(null);
  const previousFocus = useRef(returnFocusTo ?? document.activeElement);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const attachDialog = useCallback((root: HTMLDivElement | null) => {
    if (!root) return;
    setPopupLayer(root);
    dialogRoots.push(root);
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || dialogRoots.at(-1) !== root) return;
      if (event.key === "Escape") {
        if (event.target instanceof HTMLElement && event.target.getAttribute("data-mantine-stop-propagation") === "true") return;
        event.stopImmediatePropagation();
        onCloseRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const stops = dialogTabStops(root);
      const first = stops[0], last = stops[stops.length - 1];
      const active = document.activeElement;
      if (!first) {
        event.preventDefault();
        root.querySelector<HTMLElement>("[role=dialog]")?.focus();
      } else if (event.shiftKey ? active === first || !root.contains(active) : active === last || !root.contains(active)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    const initialFocus = root.querySelector<HTMLElement>("[data-autofocus]") ?? dialogTabStops(root)[0] ?? root.querySelector<HTMLElement>("[role=dialog]");
    initialFocus?.focus({ preventScroll: true });
    return () => {
      dialogRoots.splice(dialogRoots.indexOf(root), 1);
      document.removeEventListener("keydown", handleKeyDown);
      // Restore after React has removed the closing portal, without racing an autofocus timer.
      queueMicrotask(() => {
        if (root.isConnected) return;
        const target = previousFocus.current;
        const active = document.activeElement;
        if (target instanceof HTMLElement && target.isConnected && (!active || active === document.body || root.contains(active))) {
          target.focus({ preventScroll: true });
        }
      });
    };
  }, []);
  return <Modal ref={attachDialog} opened onClose={onClose} title={title} centered size={size || (wide ? "72rem" : "44rem")}
    closeButtonProps={{ "aria-label": closeLabel || m.common.close }} closeOnClickOutside={false} closeOnEscape={false} trapFocus={false} returnFocus={false} removeScrollProps={{ removeScrollBar: false }}
    classNames={{ content: classNames("dialog", wide && "dialog-wide"), header: "dialog-header", body: "dialog-body", close: "dialog-close" }}>
    <PopupLayerContext.Provider value={popupLayer}><DialogContext.Provider value={onClose}>{children}</DialogContext.Provider></PopupLayerContext.Provider>
  </Modal>;
}

export function ConfirmAction({ title, description, submit, onClose, onConfirm, danger = false }: {
  title: string; description: string; submit: string; onClose: () => void; onConfirm: () => Promise<void>; danger?: boolean;
}) {
  const { m } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <Dialog title={title} onClose={() => { if (!busy) onClose(); }}><div className="stack">
    <p>{description}</p>
    {error && <p className="form-error" role="alert">{error}</p>}
    <div className="form-actions full"><Button className="button" onClick={onClose} disabled={busy}>{m.common.close}</Button>
      <Button className={classNames("button", danger ? "danger" : "primary")} disabled={busy} onClick={async () => {
        setBusy(true); setError("");
        try { await onConfirm(); onClose(); } catch (err) { setError(actionErrorMessage(err, m)); } finally { setBusy(false); }
      }}><ButtonContent busy={busy}>{submit}</ButtonContent></Button>
    </div>
  </div></Dialog>;
}

export function PanelTitle({ title, subtitle, children, hideTitle = false }: { title: string; subtitle: string; children: ReactNode; hideTitle?: boolean }) {
  const inDialog = useContext(DialogContext);
  return <div className="stack">{((!inDialog && !hideTitle) || subtitle) && <div className="modal-head"><div>{!inDialog && !hideTitle && <h2>{title}</h2>}{subtitle && <p>{subtitle}</p>}</div></div>}{children}</div>;
}

export function Empty({ title, text, action }: { title: string; text: string; action?: ReactNode }) {
  return <div className="empty"><strong>{title}</strong><p>{text}</p>{action}</div>;
}

export function Metric({ title, value }: { title: string; value: string }) {
  return <div className="fact"><span>{title}</span><strong className="mono">{value}</strong></div>;
}

export function Badge({ tone, title, children }: { tone: string; title?: string; children: ReactNode }) {
  return <span className={`badge ${tone}`} title={title}>{children}</span>;
}

export function ButtonContent({ busy, children }: { busy: boolean; children: ReactNode }) {
  const { m } = useI18n();
  return <>{busy && <span className="spinner" aria-hidden="true" />}<span>{busy ? m.common.working : children}</span></>;
}

export function Toast({ message }: { message: string }) {
  return <output aria-live="polite" className={classNames("toast", message && "show")}>{message}</output>;
}
