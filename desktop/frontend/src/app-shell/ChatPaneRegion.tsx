import { lazy, Suspense, type ReactNode } from "react";
import { Transcript, type TranscriptProps } from "../components/Transcript";
import { SessionRecoveryBanner, SessionRecoveryPlaceholder } from "../components/SessionRecoveryBanner";
import { NoticePreviewPanel, noticePreviewMockEnabled } from "./NoticePreviewPanel";
import type { SidebarImConnection } from "../app-runtime/sidebarImProjection";
import type { TabMeta } from "../lib/types";
import type { State } from "../lib/useController";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import type { Translator } from "../lib/i18n";
import type { ForkBlockReason } from "../lib/forkTargets";
import type { SessionAvailability } from "../lib/sessionAvailability";
import { orderedLocalSubmissions } from "../lib/localSubmissionState";
import { Plug, RotateCcw, Trash2 } from "lucide-react";
import { draftSubmissionLocksEditing, type SessionDraftSurface } from "../app-runtime/useSessionDraftSurface";

const RemoteSessionSurface = lazy(() => import("../components/RemoteSessionSurface").then((module) => ({ default: module.RemoteSessionSurface })));
const SidebarImConnectionDetail = lazy(() => import("./SidebarImConnectionDetail").then((module) => ({ default: module.SidebarImConnectionDetail })));

export type ChatPaneTranscriptInput = {
  state: State;
  items: TranscriptProps["items"];
  tabId: TranscriptProps["tabId"];
  geometrySessionKey: TranscriptProps["geometrySessionKey"];
  footerHeight: TranscriptProps["footerHeight"];
  invocationMetadata: TranscriptProps["invocationMetadata"];
  surfaceCommitToken: TranscriptProps["surfaceCommitToken"];
  liveStore: TranscriptProps["liveStore"];
  transcriptHydrating: boolean;
  navigationDataReady: boolean;
  readOnly: boolean;
  controllerReady: boolean;
  hydratePlaceholderActive: boolean;
  clearContextPending: boolean;
  emptyHero?: boolean;
  availability: SessionAvailability;
  rewind: {
    stateActive: boolean;
    committing: boolean;
  };
};

export type ChatPaneRegionProps = {
  transitioning: boolean;
  t: Translator;
  imDetail: {
    connection: SidebarImConnection;
    onClose: () => void;
    onOpenSettings: () => void;
    onManageAllowlist: (connectionId: string) => void;
    onOpenSession: (connection: SidebarImConnection) => void;
  } | null;
  remote: { tab: TabMeta; session: RemoteSessionApi } | undefined;
  draft?: {
    surface: SessionDraftSurface;
    onUseSaved(): void;
    onKeepLocal(): void;
    onRetrySave(): void;
    onDismissTaskError?(): void;
    onDiscard(): void;
    onResume(): void;
    onOpenSession(): void;
    onCheckSubmission(): void;
    onSetMCPEnabled(server: SessionDraftSurface["servers"][number], enabled: boolean): void;
  };
  /** Floating dock launcher card, mounted over the transcript's right edge. */
  launcher?: ReactNode;
  transcript: ChatPaneTranscriptInput;
  onRetryHistory: () => Promise<unknown>;
  commands: {
    onPrompt: TranscriptProps["onPrompt"];
    onFork: TranscriptProps["onFork"];
    onLoadOlderHistory: TranscriptProps["onLoadOlderHistory"];
    onLoadNewerHistory: TranscriptProps["onLoadNewerHistory"];
    onSurfacePaintReady: TranscriptProps["onSurfacePaintReady"];
  };
};

/**
 * The chat-pane main surface: IM/bot detail, notice preview mock, remote
 * session surface or the local transcript with its navigation-transition
 * wrapper and history-load error. Pure prop-driven; all ownership stays in
 * the caller's owners.
 */
export function ChatPaneRegion(props: ChatPaneRegionProps) {
  const { transitioning, t, transcript, commands } = props;
  const { state, rewind } = transcript;
  // A fork entry reads persisted turn records, so it never waits for the session
  // to stop running, and a read-only source still forks: the child is written
  // from the source, never into it. It does wait for the surface it belongs to:
  // while the transcript hydrates or the source identity is switching, the
  // records on screen are not yet the ones a cut would address.
  const forkBlocked: ForkBlockReason | null = state.forkCreating ? "creating"
    : !transcript.controllerReady || transcript.transcriptHydrating || transcript.hydratePlaceholderActive || transitioning
      ? "loading"
      : null;
  const noticePreview = noticePreviewMockEnabled();
  if (props.draft && !props.imDetail && !noticePreview) {
    const draft = props.draft.surface;
    const operationUnknown = draft.operation?.phase === "dispatch_unknown";
    const operationError = draft.operation && ["terminal_failed", "runtime_failed", "resume_required", "dispatch_unknown", "dispatching_shell"].includes(draft.operation.phase)
      ? draft.operation.error
      : "";
    const workspaceName = draft.draft.scope === "project"
      ? draft.draft.workspaceRoot.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || draft.draft.workspaceRoot
      : t("draft.globalWorkspace");
    return <main className="main main--draft">
      <section className="session-draft-surface" aria-label={t("draft.surfaceLabel")}>
        <div className="session-draft-surface__heading">
          <div>
            <span className="session-draft-surface__eyebrow">{t("draft.badge")}</span>
            <h1>{workspaceName}</h1>
            {draft.draft.workspaceRoot ? <p>{draft.draft.workspaceRoot}</p> : null}
          </div>
          <button className="icon-btn" type="button" title={t("draft.discard")} aria-label={t("draft.discard")} onClick={props.draft.onDiscard}>
            <Trash2 size={16} />
          </button>
        </div>
        <div className={`session-draft-surface__status session-draft-surface__status--${draft.saveState}`} role="status">
        {draft.operation?.phase === "accepted" ? t("draft.openSession") : operationUnknown ? t("draft.resultUnknown")
          : draft.preparingSubmission || draftSubmissionLocksEditing(draft.operation)
            ? t("draft.starting")
            : draft.saveState === "saving" ? t("draft.saving")
              : draft.saveState === "saved" ? t("draft.saved")
                : draft.saveState === "error" ? t("draft.saveFailed")
                  : draft.saveState === "conflict" ? t("draft.conflict") : t("draft.unsaved")}
        </div>
        {draft.saveState === "conflict" ? <div className="session-draft-surface__conflict" role="alert">
          <span>{t("draft.conflictDetail")}</span>
          <button type="button" onClick={props.draft.onUseSaved}><RotateCcw size={14} />{t("draft.useSaved")}</button>
          <button type="button" onClick={props.draft.onKeepLocal}>{t("draft.keepLocal")}</button>
        </div> : null}
        {draft.servers.length > 0 ? <details className="session-draft-surface__mcp">
          <summary><Plug size={14} />{t("draft.mcpTitle")}<span>{draft.servers.filter((server) => server.enabled && !draft.settings.disabledMcp[server.name]).length}/{draft.servers.length}</span></summary>
          <div className="session-draft-surface__mcp-list">
            {draft.servers.map((server) => {
              const available = server.enabled;
              const selected = available && !draft.settings.disabledMcp[server.name];
              return <label key={server.name}>
                <input type="checkbox" checked={selected} disabled={!available || draft.preparingSubmission || draftSubmissionLocksEditing(draft.operation)} onChange={(event) => props.draft!.onSetMCPEnabled(server, event.currentTarget.checked)} />
                <span>{server.name}</span>
                <small>{available ? t(selected ? "draft.mcpEnabled" : "draft.mcpDisabled") : t("draft.mcpUnavailable")}</small>
              </label>;
            })}
          </div>
        </details> : null}
        {draft.error ? <p className="session-draft-surface__error">{draft.error} <button type="button" onClick={props.draft.onRetrySave}>{t("draft.retrySave")}</button></p> : null}
        {operationError ? <p className="session-draft-surface__error">{operationError}</p> : null}
        {draft.taskError ? <p className="session-draft-surface__error">{draft.taskError} <button type="button" onClick={props.draft.onDismissTaskError}>{t("common.close")}</button></p> : null}
        {draft.operation?.canResume ? <button type="button" onClick={props.draft.onResume}>{t("draft.resume")}</button> : null}
        {operationUnknown ? <button type="button" onClick={props.draft.onCheckSubmission}>{t("draft.checkSubmission")}</button> : null}
        {draft.operation?.phase === "accepted" ? <button type="button" onClick={props.draft.onOpenSession}>{t("draft.openSession")}</button> : null}
      </section>
    </main>;
  }
  if (props.remote && !(props.imDetail && !transitioning) && !noticePreview) {
    return <Suspense fallback={null}><RemoteSessionSurface tab={props.remote.tab} session={props.remote.session}
      surfaceCommitToken={transcript.surfaceCommitToken} onSurfacePaintReady={commands.onSurfacePaintReady} /></Suspense>;
  }
  const localSubmissions = orderedLocalSubmissions(state);
  const recoveringEmpty = !transitioning && transcript.availability.kind !== "ready" && transcript.items.length === 0 && localSubmissions.length === 0
    && !state.live?.text && !state.live?.reasoning;
  return (
    <>
    {!transitioning && !props.imDetail && !noticePreview && <SessionRecoveryBanner key={transcript.tabId}
      availability={transcript.availability} onRetry={props.onRetryHistory} />}
    <main className="main">
      {props.imDetail && !transitioning ? (
        <SidebarImConnectionDetail
          connection={props.imDetail.connection}
          onClose={props.imDetail.onClose}
          onOpenSettings={props.imDetail.onOpenSettings}
          onManageAllowlist={() => props.imDetail!.onManageAllowlist(props.imDetail!.connection.connectionId)}
          onOpenSession={() => props.imDetail!.onOpenSession(props.imDetail!.connection)}
        />
      ) : noticePreview ? (
        <NoticePreviewPanel />
      ) : (
        <>
          <div className="transcript-navigation-surface" aria-busy={transitioning}>
            {props.launcher}
            <div
              className="transcript-navigation-content"
              aria-hidden={transitioning || undefined}
              ref={(node) => {
                if (!node) return;
                (node as HTMLElement & { inert?: boolean }).inert = transitioning;
              }}
            >
              {recoveringEmpty ? <SessionRecoveryPlaceholder availability={transcript.availability} /> : <Transcript
                items={transcript.items}
                localSubmissions={localSubmissions}
                localSubmissionSendRevision={state.localSubmissionSendRevision}
                visibleSubmissionHandoffs={state.visibleSubmissionHandoffs}
                live={transitioning ? undefined : state.live}
                liveStore={transcript.liveStore}
                tabId={transcript.tabId}
                geometrySessionKey={transcript.geometrySessionKey}
                footerHeight={transcript.footerHeight}
                onPrompt={commands.onPrompt}
                onFork={commands.onFork}
                forkTargets={state.forkTargets}
                forkBlocked={forkBlocked}
                running={state.running || rewind.committing}
                turnStartAt={state.turnStartAt}
                hydrating={transcript.transcriptHydrating || (transitioning && !transcript.navigationDataReady)}
                hasOlderHistory={!transitioning && state.historyHasOlder && !rewind.stateActive}
                hasNewerHistory={!transitioning && state.historyHasNewer && !rewind.stateActive}
                historyStartTurn={state.historyStartTurn}
                historyEndTurn={state.historyEndTurn}
                totalTurns={state.historyTotalTurns}
                loadingOlderHistory={state.historyOlderLoading}
                olderHistoryError={state.historyOlderError}
                loadingNewerHistory={state.historyNewerLoading}
                newerHistoryError={state.historyNewerError}
                onLoadOlderHistory={commands.onLoadOlderHistory}
                onLoadNewerHistory={commands.onLoadNewerHistory}
                invocationMetadata={transcript.invocationMetadata}
                surfaceCommitToken={transcript.surfaceCommitToken}
                onSurfacePaintReady={commands.onSurfacePaintReady}
              />}
            </div>
            {transitioning ? (
              <div className="transcript-navigation-overlay" role="status" aria-live="polite">
                <span className="transcript-navigation-overlay__spinner" aria-hidden="true" />
                <span>{t("common.loading")}</span>
              </div>
            ) : null}
          </div>
        </>
      )}
    </main>
    </>
  );
}
