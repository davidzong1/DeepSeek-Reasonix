import { useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { useT } from "../lib/i18n";
import { app } from "../lib/bridge";
import type { SessionTakeoverView, TabMeta } from "../lib/types";
import type { HistoricalSourceUpdateView, SessionPreparationView } from "../generated/desktopContract.generated";
import { historicalPreparationSnapshot, reconcileHistoricalPreparation, subscribeHistoricalPreparation, type DesktopNavigationIntent } from "../app-runtime/desktopNavigationOwner";
import { useManagementT } from "../lib/managementLocale";

/**
 * SessionTakeoverDialog confirms taking a lease-blocked session over from the
 * resident serve on this machine. The remote tab keeps watching through the
 * frame mirror and drops to read-only; when it reclaims, this window demotes
 * itself the same way.
 */
export function SessionTakeoverDialog({ tabId, onClose }: { tabId: string; onClose: () => void }) {
  const t = useT();
  const titleId = useId();
  const messageId = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const [view, setView] = useState<SessionTakeoverView | null>(null);
  const [queryError, setQueryError] = useState("");
  const [actionError, setActionError] = useState("");
  const [busyMode, setBusyMode] = useState<"wait" | "interrupt" | null>(null);

  useLayoutEffect(() => {
    restoreFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    cancelRef.current?.focus();
    return () => {
      if (restoreFocusRef.current?.isConnected) restoreFocusRef.current.focus();
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    app.QuerySessionTakeover(tabId)
      .then((result) => {
        if (!cancelled) setView(result);
      })
      .catch((error) => {
        if (!cancelled) setQueryError(error instanceof Error ? error.message : String(error));
      });
    return () => {
      cancelled = true;
    };
  }, [tabId]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        if (!busyMode) onClose();
      }
    };
    document.addEventListener("keydown", onKeyDown, { capture: true });
    return () => document.removeEventListener("keydown", onKeyDown, { capture: true });
  }, [busyMode, onClose]);

  const take = (mode: "wait" | "interrupt") => {
    if (busyMode) return;
    setBusyMode(mode);
    setActionError("");
    app.TakeoverSession(tabId, mode)
      .then(onClose)
      .catch((error) => {
        setActionError(error instanceof Error ? error.message : String(error));
        setBusyMode(null);
      });
  };

  const busy = busyMode !== null;
  let body: React.ReactNode;
  if (queryError) {
    body = <span className="reasonix-confirm-dialog__message-error">{t("takeover.unavailable", { reason: queryError })}</span>;
  } else if (!view) {
    body = <span>{t("takeover.querying")}</span>;
  } else if (!view.available) {
    body = <span>{t("takeover.unavailable", { reason: view.reason || t("takeover.noHolder") })}</span>;
  } else {
    body = (
      <>
        <span>{t("takeover.descRemote")}</span>
        <span className="session-takeover-dialog__state">
          {view.running ? t("takeover.running") : t("takeover.idle")}
        </span>
      </>
    );
  }
  const canTake = !queryError && view?.available === true;

  return createPortal(
    <div
      data-app-overlay=""
      className="modal-backdrop reasonix-confirm-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !busy) onClose();
      }}
    >
      <div className="modal reasonix-confirm-dialog session-takeover-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId} aria-describedby={messageId}>
        <div className="modal__title reasonix-confirm-dialog__title" id={titleId}>{t("takeover.title")}</div>
        <div className="reasonix-confirm-dialog__message" id={messageId}>
          {body}
          {actionError ? <span className="reasonix-confirm-dialog__message-error">{actionError}</span> : null}
        </div>
        <div className="modal__actions reasonix-confirm-dialog__actions">
          <button ref={cancelRef} className="btn btn--small" type="button" disabled={busy} onClick={onClose}>
            {t("takeover.cancel")}
          </button>
          {canTake ? (
            <button className="btn btn--small btn--primary" type="button" disabled={busy} onClick={() => take("wait")}>
              {busyMode === "wait" ? t("takeover.busy") : view?.running ? t("takeover.takeWait") : t("takeover.takeIdle")}
            </button>
          ) : null}
          {canTake && view?.running ? (
            <button className="btn btn--small btn--danger" type="button" disabled={busy} onClick={() => take("interrupt")}>
              {busyMode === "interrupt" ? t("takeover.busy") : t("takeover.takeInterrupt")}
            </button>
          ) : null}
        </div>
      </div>
    </div>,
    document.body,
  );
}

const terminalPreparation = new Set(["ready", "blocked", "failed", "cancelled"]);
export type HistoricalSessionBannerProps = {
  tab?: TabMeta;
  navigate(intent: DesktopNavigationIntent): Promise<void>;
  captureNavigation?(): () => boolean;
};
export function HistoricalSessionBanners({ tab, navigate, captureNavigation }: HistoricalSessionBannerProps) {
  const t = useT();
  const m = useManagementT();
  const activeRef = tab?.session ?? (tab?.sessionId ? { hostId: "local", sessionId: tab.sessionId } : undefined);
  const preparation = useSyncExternalStore(subscribeHistoricalPreparation, historicalPreparationSnapshot);
  const [update, setUpdate] = useState<HistoricalSourceUpdateView | null>(null);
  const [busy, setBusy] = useState(false);
  const [updateError, setUpdateError] = useState("");
  const updateOperation = useRef(0);
  const [cancellingOperationId, setCancellingOperationId] = useState("");
  const cancellingOperationRef = useRef("");
  const activeHostId = activeRef?.hostId ?? "";
  const activeSessionId = activeRef?.sessionId ?? "";
  const activeKey = activeSessionId ? `${activeHostId}:${activeSessionId}` : "";
  const activeKeyRef = useRef(activeKey);
  activeKeyRef.current = activeKey;
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);

  useEffect(() => {
    let current = true;
    updateOperation.current++;
    setUpdate(null);
    setUpdateError("");
    setBusy(false);
    if (!activeSessionId || activeHostId !== "local" || !app.CheckHistoricalSourceUpdate) return () => { current = false; };
    const ref = { hostId: activeHostId, sessionId: activeSessionId };
    const run = async () => {
      let next = await app.CheckHistoricalSourceUpdate!({ ref });
      for (let attempt = 0; current && next.status === "checking" && attempt < 60; attempt++) {
        await new Promise(resolve => setTimeout(resolve, 500));
        if (current) next = await app.CheckHistoricalSourceUpdate!({ ref });
      }
      if (!current || next.status !== "available" || !next.version || !next.source) return;
      try {
        if (localStorage.getItem(`historical-source-update:${next.sourceKey}`) === next.version) return;
      } catch { /* private storage can be unavailable */ }
      setUpdate(next);
    };
    void run().catch(() => {});
    return () => { current = false; };
  }, [activeHostId, activeSessionId]);

  const dismissUpdate = () => {
    if (update?.version) {
      try { localStorage.setItem(`historical-source-update:${update.sourceKey}`, update.version); } catch { /* best effort */ }
    }
    setUpdate(null);
  };
  const importUpdate = async () => {
    if (busy || !update?.source || !update.version || !app.PrepareHistoricalSourceVersion || !app.GetSessionPreparation) return;
    const expectedActive = activeKey;
    const operation = ++updateOperation.current;
    const navigationCurrent = captureNavigation?.() ?? (() => activeKeyRef.current === expectedActive);
    const current = () => mounted.current && operation === updateOperation.current && activeKeyRef.current === expectedActive && navigationCurrent();
    setBusy(true);
    setUpdateError("");
    try {
      let view: SessionPreparationView = await app.PrepareHistoricalSourceVersion(update.source, update.version);
      while (current() && !terminalPreparation.has(view.status)) {
        await new Promise(resolve => setTimeout(resolve, 300));
        if (!current()) return;
        view = await app.GetSessionPreparation(view.operationId);
      }
      if (!current()) return;
      if (view.status === "ready" && view.target) {
        await navigate({ kind: "canonical-session", ref: view.target });
      } else {
        setUpdateError(m(view.errorCode === "source_busy" ? "historicalSourceBusy" : "historicalImportFailed"));
      }
    } catch {
      if (current()) setUpdateError(m("historicalImportFailed"));
    }
    finally { if (mounted.current && operation === updateOperation.current) setBusy(false); }
  };
  const cancelPreparation = async () => {
    if (!preparation || !app.CancelSessionPreparation || cancellingOperationRef.current === preparation.operationId) return;
    const operationId = preparation.operationId;
    cancellingOperationRef.current = operationId;
    setCancellingOperationId(operationId);
    try {
      const view = await app.CancelSessionPreparation(operationId);
      if (mounted.current) reconcileHistoricalPreparation(preparation, view);
    } catch { /* The preparation poll remains the authority after a failed cancellation request. */ }
    finally {
      if (cancellingOperationRef.current === operationId) cancellingOperationRef.current = "";
      if (mounted.current) setCancellingOperationId(current => current === operationId ? "" : current);
    }
  };

  if (preparation) {
    const waiting = preparation.status === "queued" || preparation.status === "preparing";
    return <div className={`banner ${waiting ? "banner--warning" : "banner--error"} banner--actionable`} role="status">
      <span className="banner__msg">{m("historicalImporting")}: {preparation.session.title || preparation.session.topicId || m("historicalTitle")}</span>
      <span className="banner__hint">{m(preparation.status === "queued" ? "historicalQueued" : preparation.status === "preparing" ? "historicalImporting" : "historicalImportFailed")}</span>
      <span className="banner__spacer" />
      {waiting && <button type="button" className="btn btn--small" disabled={cancellingOperationId === preparation.operationId} onClick={() => void cancelPreparation()}>{t("common.cancel")}</button>}
      {!waiting && preparation.retryable && <button type="button" className="btn btn--small" onClick={() => void navigate({ kind: "resume-session", session: preparation.session })}>{t("common.retry")}</button>}
    </div>;
  }
  if (tab?.historicalSource) return <div className="banner banner--warning banner--actionable" role="status">
    <span className="banner__msg">{tab.topicTitle || m("historicalTitle")} · {m("historicalAvailable")}</span>
    <span className="banner__hint">{m("historicalImportDescription")}</span>
    <span className="banner__spacer" />
    <button id="reasonix-prepare-restored-session" type="button" className="btn btn--small" onClick={() => void navigate({ kind: "resume-session", session: {
      source: tab.historicalSource, path: tab.historicalSource!.path, scope: tab.scope, workspaceRoot: tab.workspaceRoot,
      topicId: tab.topicId, title: tab.topicTitle, preview: "", turns: 0, turnsState: "unknown", createdAt: 0, lastActivityAt: 0, modTime: 0, current: true, open: true,
    } })}>{m("historicalImportOpen")}</button>
  </div>;
  if (!update) return null;
  return <div className={`banner ${updateError ? "banner--error" : "banner--warning"} banner--actionable`} role={updateError ? "alert" : "status"}>
    <span className="banner__msg">{m("historicalSourceUpdated")}</span>
    {(updateError || busy) && <span className="banner__hint">{updateError || m("historicalImporting")}</span>}
    <span className="banner__spacer" />
    <button type="button" className="btn btn--small" disabled={busy} onClick={() => void importUpdate()}>{m("historicalImportOpen")} · {m("branch")}</button>
    <button type="button" className="btn btn--small" disabled={busy} onClick={dismissUpdate}>{t("updater.dismiss")}</button>
  </div>;
}

export function SessionRuntimeOverlays({ takeoverTabId, onCloseTakeover, historical }: {
  takeoverTabId: string | null;
  onCloseTakeover(): void;
  historical?: HistoricalSessionBannerProps;
}) {
  return <>
    {takeoverTabId ? <SessionTakeoverDialog tabId={takeoverTabId} onClose={onCloseTakeover} /> : null}
    {historical ? <HistoricalSessionBanners {...historical} /> : null}
  </>;
}
