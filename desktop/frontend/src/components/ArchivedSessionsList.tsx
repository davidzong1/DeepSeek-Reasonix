import { Archive, ArrowLeft, MessageSquare, RotateCcw, Search, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { app, onLegacyEmptySessionCleanupChanged, onProjectTreeChanged } from "../lib/bridge";
import { getLocale, useT } from "../lib/i18n";
import type { HistoryMessage, SessionMeta } from "../lib/types";
import type { SessionRef } from "../lib/sessionRef";
import type { LegacyEmptySessionCleanupStatus, SessionLifecycleRequest } from "../generated/desktopContract.generated";
import { useConfirmDialog } from "./ConfirmDialog";
import "./ArchivedSessionsList.css";

type TrashRow = { key: string; ref?: SessionRef; recoveryEntryId?: string; cleanupKind?: string; workspaceId: string; title: string; workspace: string; updatedAt: number; canRestore: boolean; canPreview: boolean; canPurge: boolean; health: string };
export function ArchivedSessionsList({ active, onOpenSession }: {
  active: boolean; onOpenSession: (ref: SessionRef) => Promise<void>;
  legacyList?: () => Promise<SessionMeta[]>; legacyRestore?: (path: string) => Promise<void>; legacyPurge?: (path: string) => Promise<void>;
}) {
  const t = useT();
  const [rows, setRows] = useState<TrashRow[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<TrashRow>();
  const [preview, setPreview] = useState<HistoryMessage[]>([]);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [previewCursor, setPreviewCursor] = useState("");
  const [pendingRequest, setPendingRequest] = useState<SessionLifecycleRequest | null>(null);
  const [cleanupStatus, setCleanupStatus] = useState<LegacyEmptySessionCleanupStatus>();
  const [cleanupRetrying, setCleanupRetrying] = useState(false);
  const registryGeneration = useRef(0);
  const surfaceGeneration = useRef(0);
  const selectedKey = useRef("");
  const generation = useRef(0), previewGeneration = useRef(0), mutating = useRef(false);
  const { confirm, dialog, dismiss } = useConfirmDialog();
  const reload = useCallback(async () => {
    const seq = ++generation.current;
    setLoading(true);
    try {
      const next: TrashRow[] = [];
      let cursor = "", snapshotGeneration: number | undefined;
      do {
        const page = await app.ListTrashEntries("", cursor, 200);
        if (seq !== generation.current) return;
        if (snapshotGeneration !== undefined && snapshotGeneration !== page.generation) throw new Error(t("history.failedLoadHistory"));
        snapshotGeneration = page.generation;
        next.push(...page.items.map(row => ({ key: row.id, ref: row.ref ?? undefined, recoveryEntryId: row.recoveryEntryId, cleanupKind: row.cleanupKind, workspaceId: row.workspaceId, title: row.title,
          workspace: row.workspaceTitle, updatedAt: row.archivedAt, canRestore: row.canRestore,
          canPreview: row.canPreview, canPurge: row.canPurge, health: row.health })));
        const after = page.nextCursor ?? "";
        if (after && after === cursor) throw new Error(t("history.failedLoadHistory"));
        cursor = after;
      } while (cursor);
      registryGeneration.current = snapshotGeneration ?? 0;
      if (seq !== generation.current) return;
      next.sort((a, b) => b.updatedAt - a.updatedAt || a.key.localeCompare(b.key));
      const selectedRow = next.find(row => row.key === selectedKey.current);
      if (selectedKey.current && !selectedRow?.canPreview) {
        selectedKey.current = ""; ++previewGeneration.current;
        setSelected(undefined); setPreview([]); setPreviewCursor(""); setPreviewLoading(false); setPreviewError("");
      } else if (selectedRow) setSelected(selectedRow);
      setRows(next); setLoadError("");
      try {
        const cleanup = await app.GetLegacyEmptySessionCleanupStatus();
        if (seq === generation.current) setCleanupStatus(cleanup);
      } catch {
        // Trash remains usable when a future or damaged cleanup sidecar makes
        // only the optional upgrade status unavailable.
        if (seq === generation.current) setCleanupStatus(undefined);
      }
    } catch (err) {
      if (seq === generation.current) setLoadError(err instanceof Error ? err.message : String(err));
      throw err;
    } finally { if (seq === generation.current) setLoading(false); }
  }, [t]);
  useEffect(() => {
    if (!active) return;
    void reload().catch(() => {});
    const unsubscribe = onProjectTreeChanged(() => { if (!mutating.current) void reload().catch(() => {}); });
    const unsubscribeCleanup = onLegacyEmptySessionCleanupChanged(setCleanupStatus);
    return () => { generation.current++; previewGeneration.current++; surfaceGeneration.current++; unsubscribe(); unsubscribeCleanup(); };
  }, [active, reload]);
  useEffect(() => { if (!active) dismiss(); }, [active, dismiss]);
  const select = async (row: TrashRow, cursor = "") => {
    if (!row.ref) return;
    const seq = ++previewGeneration.current;
    selectedKey.current = row.key;
    setSelected(row); setPreview([]); setPreviewLoading(true); setPreviewError(""); setPreviewCursor("");
    try {
      const page = await app.ReadSessionHistory(row.ref, cursor, 32);
      if (seq !== previewGeneration.current) return;
      setPreview(page.messages); setPreviewCursor("nextCursor" in page ? String(page.nextCursor || "") : "");
    } catch (err) { if (seq === previewGeneration.current) setPreviewError(String(err)); }
    finally { if (seq === previewGeneration.current) setPreviewLoading(false); }
  };
  const closePreview = () => { selectedKey.current = ""; ++previewGeneration.current; setSelected(undefined); setPreview([]); setPreviewCursor(""); setPreviewLoading(false); setPreviewError(""); };
  const requestFor = (targets: TrashRow[], action: "restore" | "purge"): SessionLifecycleRequest => ({
    operationId: crypto.randomUUID(), action, expectedGeneration: registryGeneration.current,
    targets: targets.map(row => row.ref
      ? ({ ref: { ...row.ref } })
      : ({ workspaceId: row.workspaceId, recoveryEntryId: row.recoveryEntryId ?? "" })),
  });
  const mutate = async (request: SessionLifecycleRequest) => {
    if (mutating.current) return;
    const surface = surfaceGeneration.current;
    const kind = request.action;
    mutating.current = true; setBusy(true); ++generation.current; setError(""); setNotice(""); setPendingRequest(request);
    let succeeded = 0;
    let retryable = false, failed = 0, conflicts = 0;
    try {
      const result = await app.ApplySessionLifecycle(request);
      if (surface !== surfaceGeneration.current) return;
      for (const item of result.items) {
        if (!item.committed) {
          failed++;
          retryable ||= item.retryable;
          if (item.errorCode === "state_conflict") conflicts++;
          continue;
        }
        succeeded++;
        const id = item.target.ref?.sessionId ?? item.target.recoveryEntryId?.replace(/^legacy-cleanup:/, "");
        setRows(current => current.filter(row => row.ref?.sessionId !== id && row.key !== id));
        if (selectedKey.current === id) closePreview();
      }
      if (!retryable) setPendingRequest(null);
      setNotice(t(kind === "restore" ? "history.restoreComplete" : "history.purgeComplete", { n: succeeded }));
      try { await reload(); } catch { setNotice(t("history.operationRefreshFailed")); }
      if (conflicts) setError(t("projectTree.sessionError.targetChanged"));
      else if (failed) setError(t("history.trashPartialFailure", { n: failed }));
      if (surface === surfaceGeneration.current && kind === "restore" && succeeded === 1 && request.targets.length === 1 && request.targets[0].ref) {
        try { await onOpenSession(request.targets[0].ref); } catch { setNotice(t("history.restoredRefreshFailed")); }
      }
    } catch (err) {
      if (surface !== surfaceGeneration.current) return;
      const message = String(err);
      if (message.includes("workspace mutation conflicts with persisted state")) {
        setPendingRequest(null);
        await reload().catch(() => {});
        setError(t("projectTree.sessionError.targetChanged"));
      } else {
        setError(message);
      }
    } finally { mutating.current = false; setBusy(false); }
  };
  const purge = async (targets: TrashRow[]) => {
    targets = targets.filter(row => row.canPurge);
    if (!targets.length || mutating.current) return;
    const request = requestFor(targets, "purge"), surface = surfaceGeneration.current;
    if (await confirm({ title: t(targets.length > 1 ? "history.emptyTrashConfirm" : "history.purgeConfirm"),
      message: targets.length > 1 ? t("history.emptyTrashExplanation", { n: targets.length }) : t("history.purgeExplanation", { name: targets[0].title }),
      confirmLabel: t("history.permanentlyDelete"), cancelLabel: t("common.cancel"), tone: "danger" }) && surface === surfaceGeneration.current) await mutate(request);
  };
  const refresh = () => { if (!pendingRequest) setError(""); void reload().catch(() => {}); };
  const retryCleanup = async () => {
    if (cleanupRetrying) return;
    setCleanupRetrying(true); setError("");
    try {
      setCleanupStatus(await app.RetryLegacyEmptySessionCleanup());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setCleanupRetrying(false);
    }
  };
  const filtered = rows.filter(row => `${row.title}\n${row.workspace}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <div className="archived-sessions" aria-busy={busy}>
    <div className="archived-sessions__toolbar">
      <label className="archived-sessions__search"><Search size={16} aria-hidden="true" /><input aria-label={t("history.searchPlaceholder")} placeholder={t("history.searchPlaceholder")} value={query} onChange={event => setQuery(event.target.value)} /></label>
      <button className="btn btn--small" disabled={busy || loading} onClick={refresh}><RotateCcw size={14} />{t("history.refreshTrash")}</button>
      <button className="btn btn--small btn--danger history-clear" disabled={busy || loading || !!error || !!loadError || !rows.some(row => row.canPurge)} onClick={() => void purge([...rows])}><Trash2 size={14} />{t("history.clearTrash")}</button>
    </div>
    {notice && <div className="management-notice" role="status">{notice}</div>}
    {!!cleanupStatus?.pending && <div className="management-notice" role="status">
      {t("history.legacyCleanupPending", { n: cleanupStatus.pending })}
      <button className="btn btn--small" disabled={cleanupRetrying} onClick={() => void retryCleanup()}>{t("history.recheckLegacyCleanup")}</button>
    </div>}
    {(error || pendingRequest) && <div className="management-notice" role="alert">{error}<button className="btn btn--small" disabled={busy} onClick={() => pendingRequest ? void mutate(pendingRequest) : refresh()}>{t(pendingRequest ? "history.retryFailed" : "common.retry")}</button></div>}
    {loadError && <div className="management-notice" role="alert">{loadError}<button className="btn btn--small" disabled={busy} onClick={refresh}>{t("common.retry")}</button></div>}
    <div className="archived-sessions__layout" data-detail={!!selected}>
      <div className="archived-sessions__list">
        {loading && <p role="status">{t("common.loading")}</p>}
        {!loading && !error && !filtered.length && <div className="archived-sessions__empty"><Archive size={30} /><h3>{t(query ? "history.noTrashMatches" : "history.noArchivedSessions")}</h3><p>{t(query ? "history.tryOtherSearch" : "history.emptyTrashHint")}</p></div>}
        {filtered.map(row => <div className="archived-sessions__row" key={row.key} data-selected={selected?.key === row.key || undefined}>
          <button className="archived-sessions__open" disabled={busy || !row.canPreview} onClick={() => void select(row)} title={row.title}><MessageSquare size={17} /><span><strong>{row.title}</strong><small>{row.workspace}{row.cleanupKind === "topic_placeholder" && <> · {t("history.legacyPlaceholder")}</>}{row.health === "purge_pending" && <> · {t("history.purgePending")}</>}{row.updatedAt > 0 && <> · {new Date(row.updatedAt).toLocaleDateString(getLocale())}</>}</small></span></button>
          <button className="btn btn--small" disabled={busy || !row.canRestore} aria-label={t("history.restoreSession")} onClick={() => void mutate(requestFor([row], "restore"))}><RotateCcw size={14} />{t("history.restore")}</button>
          <button className="btn btn--small archived-sessions__delete" disabled={busy || !row.canPurge} aria-label={`${t("history.permanentlyDelete")} ${row.title}`} onClick={() => void purge([row])}><Trash2 size={14} /></button>
        </div>)}
      </div>
      {selected && <aside className="archived-sessions__preview" aria-label={t("history.previewRecovery")}>
        <header><button className="btn btn--small" onClick={closePreview}><ArrowLeft size={14} />{t("history.backToTrash")}</button><h3>{selected.title}</h3><p>{t("history.previewReadOnly")}</p></header>
        <div className="archived-sessions__messages">
          {previewLoading && <p role="status">{t("common.loading")}</p>}
          {previewError && <div role="alert">{previewError}<button className="btn btn--small" onClick={() => void select(selected)}>{t("common.retry")}</button></div>}
          {!previewLoading && !previewError && !preview.length && <p>{t("history.emptySession")}</p>}
          {preview.map((message, index) => <article key={message.messageId || message.recordId || index}><small>{message.role}</small><div>{message.content}</div></article>)}
          {previewCursor && <button className="btn btn--small" onClick={() => void select(selected, previewCursor)}>{t("projectTree.loadMore")}</button>}
        </div>
      </aside>}
    </div>
    {active && dialog}
  </div>;
}
