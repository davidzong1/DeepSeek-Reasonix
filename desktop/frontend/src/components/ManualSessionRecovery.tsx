import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import { manualCreationPresentation } from "../lib/manualCreationPresentation";
import { beginManualCreation, manualCreationFailures, manualCreationSnapshot, subscribeManualCreationFailures } from "../lib/manualCreationRequests";

/** Observation only: recovery belongs to the host and never selects a page. */
export function ManualSessionRecovery() {
  const t = useT();
  const [items, setItems] = useState<ManualSessionCreationView[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const inFlight = useRef(new Set<string>());
  const observation = useRef(0);
  useSyncExternalStore(subscribeManualCreationFailures, manualCreationSnapshot);
  const failed = manualCreationFailures();
  useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    const refresh = async () => {
      const sequence = ++observation.current;
      try {
        const next = await app.ListManualSessionCreations();
        if (!disposed && sequence === observation.current) { setItems(Array.isArray(next) ? next : []); setError(""); }
      } catch (e) {
        if (!disposed && sequence === observation.current) setError(String(e));
      } finally {
        if (!disposed) timer = setTimeout(() => void refresh(), 1500);
      }
    };
    void refresh();
    return () => { disposed = true; clearTimeout(timer); };
  }, []);

  const run = async (id: string, action: () => Promise<unknown>) => {
    if (inFlight.current.has(id)) return;
    inFlight.current.add(id);
    setBusy(new Set(inFlight.current));
    try { await action(); setError(""); }
    catch (e) { setError(String(e)); }
    finally { inFlight.current.delete(id); setBusy(new Set(inFlight.current)); }
  };
  const retry = async (id: string) => {
    const updated = await app.RetryManualSessionCreation(id);
    ++observation.current; // A previously issued read must not replace this reply.
    setItems(current => updated.phase === "ready" ? current.filter(item => item.operationId !== id)
      : [...current.filter(item => item.operationId !== id), updated]);
  };
  if (!items.length && !error && !failed.length) return null;
  return <div className="management-notice" role="status">
    {error && <div role="alert">{t("creation.requestError")} {error}</div>}
    {failed.map(({ request, error: failure }) => <div key={request.operationId}>
      {request.workspaceRoot || request.scope} — {failure}
      <button className="btn btn--small" disabled={busy.has(request.operationId)} onClick={() => void run(request.operationId, async () => {
        const result = await beginManualCreation(request); await retry(result.operationId);
      })}>{t("common.retry")}</button>
    </div>)}
    {items.map(item => {
      const state = manualCreationPresentation(item);
      return <div key={item.operationId}>
        {item.workspaceRoot || item.scope} — {t(state.key)}
        {item.phase === "failed" && !item.progress && item.error ? `: ${item.error}` : ""}
        {state.retryable && <button className="btn btn--small" disabled={busy.has(item.operationId)}
          onClick={() => void run(item.operationId, () => retry(item.operationId))}>{t("common.retry")}</button>}
      </div>;
    })}
    <button className="btn btn--small" disabled={busy.has("export")}
      onClick={() => void run("export", () => app.ExportManualCreationDiagnostics())}>{t("creation.export")}</button>
  </div>;
}
