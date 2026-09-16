import { Archive, ArrowLeft, MessageSquare, RotateCcw, Search, Trash2 } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { app, onProjectTreeChanged } from "../lib/bridge";
import { getLocale, useT } from "../lib/i18n";
import type { HistoryMessage, SessionMeta } from "../lib/types";
import type { SessionRef } from "../lib/sessionRef";
import { useConfirmDialog } from "./ConfirmDialog";
import "./ArchivedSessionsList.css";

type TrashRow = { key: string; ref: SessionRef; title: string; workspace: string; updatedAt: number; canRestore: boolean; canPreview: boolean; health: string };
export function ArchivedSessionsList({ active, onOpenSession }: {
  active: boolean; onOpenSession: (ref: SessionRef) => Promise<void>;
  legacyList?: () => Promise<SessionMeta[]>; legacyRestore?: (path: string) => Promise<void>; legacyPurge?: (path: string) => Promise<void>;
}) {
  const t = useT();
  const [rows, setRows] = useState<TrashRow[]>([]);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [selected, setSelected] = useState<TrashRow>();
  const [preview, setPreview] = useState<HistoryMessage[]>([]);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [previewCursor, setPreviewCursor] = useState("");
  const [failedRows, setFailedRows] = useState<TrashRow[]>([]);
  const [lastKind, setLastKind] = useState<"restore" | "purge">("purge");
  const registryGeneration = useRef(0);
  const surfaceGeneration = useRef(0);
  const pendingRequest = useRef<import("../generated/desktopContract.generated").SessionLifecycleRequest | undefined>(undefined);
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
        next.push(...page.items.map(row => ({ key: row.id, ref: row.ref, title: row.title,
          workspace: row.workspaceTitle, updatedAt: row.archivedAt, canRestore: row.canRestore,
          canPreview: row.canPreview, health: row.health })));
        const after = page.nextCursor ?? "";
        if (after && after === cursor) throw new Error(t("history.failedLoadHistory"));
        cursor = after;
      } while (cursor);
      registryGeneration.current = snapshotGeneration ?? 0;
      if (seq !== generation.current) return;
      next.sort((a, b) => b.updatedAt - a.updatedAt || a.key.localeCompare(b.key));
      setRows(next); setError("");
    } catch (err) {
      if (seq === generation.current) setError(err instanceof Error ? err.message : String(err));
      throw err;
    } finally { if (seq === generation.current) setLoading(false); }
  }, [t]);
  useEffect(() => {
    if (!active) return;
    void reload().catch(() => {});
    const unsubscribe = onProjectTreeChanged(() => { if (!mutating.current) void reload().catch(() => {}); });
    return () => { generation.current++; previewGeneration.current++; surfaceGeneration.current++; unsubscribe(); };
  }, [active, reload]);
  useEffect(() => { if (!active) dismiss(); }, [active, dismiss]);
  const select = async (row: TrashRow, cursor = "") => {
    const seq = ++previewGeneration.current;
    setSelected(row); setPreview([]); setPreviewLoading(true); setPreviewError(""); setPreviewCursor("");
    try {
      const page = await app.ReadSessionHistory(row.ref, cursor, 32);
      if (seq !== previewGeneration.current) return;
      setPreview(page.messages); setPreviewCursor("nextCursor" in page ? String(page.nextCursor || "") : "");
    } catch (err) { if (seq === previewGeneration.current) setPreviewError(String(err)); }
    finally { if (seq === previewGeneration.current) setPreviewLoading(false); }
  };
  const closePreview = () => { ++previewGeneration.current; setSelected(undefined); setPreview([]); };
  const mutate = async (targets: TrashRow[], kind: "restore" | "purge", retry = false) => {
    if (mutating.current) return;
    const surface = surfaceGeneration.current;
    if (!retry) pendingRequest.current = undefined;
    mutating.current = true; setBusy(true); ++generation.current; setError(""); setNotice(""); setLastKind(kind); setFailedRows([]);
    let succeeded = 0;
    const failures: TrashRow[] = [];
    try {
      const request = pendingRequest.current ?? { operationId: crypto.randomUUID(), action: kind,
        targets: targets.map(row => ({ ref: row.ref })), expectedGeneration: registryGeneration.current };
      pendingRequest.current = request;
      const result = await app.ApplySessionLifecycle(request);
      if (surface !== surfaceGeneration.current) return;
      for (const item of result.items) {
        const row = targets.find(row => row.ref.sessionId === item.target.ref?.sessionId);
        if (!row) continue;
        if (!item.committed) { failures.push(row); continue; }
        succeeded++;
        setRows(current => current.filter(other => other.key !== row.key));
        if (selected?.key === row.key) closePreview();
      }
      if (result.committed) pendingRequest.current = undefined;
      if (result.items.some(item => !item.committed && item.retryable === false)) pendingRequest.current = undefined;
      setFailedRows(failures);
      setNotice(t(kind === "restore" ? "history.restoreComplete" : "history.purgeComplete", { n: succeeded }));
      try { await reload(); } catch { setNotice(t("history.operationRefreshFailed")); }
      if (failures.length) setError(t("history.trashPartialFailure", { n: failures.length }));
      if (surface === surfaceGeneration.current && kind === "restore" && succeeded === 1 && targets.length === 1 && targets[0].ref) {
        try { await onOpenSession(targets[0].ref); } catch { setNotice(t("history.restoredRefreshFailed")); }
      }
    } catch (err) {
      const message = String(err);
      if (message.includes("workspace mutation conflicts with persisted state")) {
        pendingRequest.current = undefined;
        setFailedRows([]);
        await reload().catch(() => {});
      } else setFailedRows(targets);
      setError(message);
    } finally { mutating.current = false; setBusy(false); }
  };
  const purge = async (targets: TrashRow[]) => {
    if (!targets.length || mutating.current) return;
    if (await confirm({ title: t(targets.length > 1 ? "history.emptyTrashConfirm" : "history.purgeConfirm"),
      message: targets.length > 1 ? t("history.emptyTrashExplanation", { n: targets.length }) : t("history.purgeExplanation", { name: targets[0].title }),
      confirmLabel: t("history.permanentlyDelete"), cancelLabel: t("common.cancel"), tone: "danger" })) await mutate(targets, "purge");
  };
  const filtered = rows.filter(row => `${row.title}\n${row.workspace}`.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase()));
  return <div className="archived-sessions" aria-busy={busy}>
    <div className="archived-sessions__toolbar">
      <label className="archived-sessions__search"><Search size={16} aria-hidden="true" /><input aria-label={t("history.searchPlaceholder")} placeholder={t("history.searchPlaceholder")} value={query} onChange={event => setQuery(event.target.value)} /></label>
      <button className="btn btn--small" disabled={busy || loading} onClick={() => void reload().catch(() => {})}><RotateCcw size={14} />{t("history.refreshTrash")}</button>
      <button className="btn btn--small btn--danger history-clear" disabled={busy || loading || !!error || !rows.length} onClick={() => void purge([...rows])}><Trash2 size={14} />{t("history.clearTrash")}</button>
    </div>
    {notice && <div className="management-notice" role="status">{notice}</div>}
    {error && <div className="management-notice" role="alert">{error}<button className="btn btn--small" disabled={busy} onClick={() => failedRows.length ? void mutate(failedRows, lastKind, true) : void reload().catch(() => {})}>{t(failedRows.length ? "history.retryFailed" : "common.retry")}</button></div>}
    <div className="archived-sessions__layout" data-detail={!!selected}>
      <div className="archived-sessions__list">
        {loading && <p role="status">{t("common.loading")}</p>}
        {!loading && !error && !filtered.length && <div className="archived-sessions__empty"><Archive size={30} /><h3>{t(query ? "history.noTrashMatches" : "history.noArchivedSessions")}</h3><p>{t(query ? "history.tryOtherSearch" : "history.emptyTrashHint")}</p></div>}
        {filtered.map(row => <div className="archived-sessions__row" key={row.key} data-selected={selected?.key === row.key || undefined}>
          <button className="archived-sessions__open" disabled={busy || !row.canPreview} onClick={() => void select(row)} title={row.title}><MessageSquare size={17} /><span><strong>{row.title}</strong><small>{row.workspace}{row.health === "purge_pending" && <> · {t("history.purgePending")}</>}{row.updatedAt > 0 && <> · {new Date(row.updatedAt).toLocaleDateString(getLocale())}</>}</small></span></button>
          <button className="btn btn--small" disabled={busy || !row.canRestore} aria-label={t("history.restoreSession")} onClick={() => void mutate([row], "restore")}><RotateCcw size={14} />{t("history.restore")}</button>
          <button className="btn btn--small archived-sessions__delete" disabled={busy} aria-label={`${t("history.permanentlyDelete")} ${row.title}`} onClick={() => void purge([row])}><Trash2 size={14} /></button>
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
