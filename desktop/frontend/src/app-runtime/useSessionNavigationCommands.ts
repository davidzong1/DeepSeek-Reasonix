import { useCommittedCommand } from "../lib/useCommittedCommand";
import { asArray } from "../lib/array";
import { resolveTaskMonitorSession } from "../lib/taskMonitorNavigation";
import { taskSessionIDFromPath, type SidebarImConnection } from "./sidebarImProjection";
import type { useDesktopNavigation } from "./useDesktopNavigation";
import type { WorkspaceNavigationPorts } from "./navigationOwner";
import type { ControlResult, SessionMeta, TabMeta } from "../lib/types";
import type { TopicShortcutEntry } from "../lib/topicShortcuts";
import type { Dispatch, SetStateAction } from "react";
import type { SessionRef } from "../lib/sessionRef";

const loadNavigationOwner = () => import("./navigationOwner");

export type SessionNavigationCommandsInput = {
  activeTab: TabMeta | undefined;
  showToast: (message: string, level: "error") => void;
  closeTransientOverlays: () => void;
  clearImDetail: () => void;
  prepareBlankWorkspace: (workspaceRoot?: string) => void;
  navigation: Pick<ReturnType<typeof useDesktopNavigation>, "enqueueNavigation" | "enqueueNavigationWithIntent" | "openRemoteProject">;
  noteNavigationIntent: () => number;
  beginNavigationSurface: (seq: number) => void;
  settleNavigationSurface: (seq: number) => void;
  isNavigationIntentCurrent: (seq: number) => boolean;
  markProjectChanged: Dispatch<SetStateAction<number>>;
  refreshTabMetas: (apply?: () => boolean, options?: { afterMutation?: boolean }) => Promise<unknown>;
  refreshHistoryView: () => void;
  enterConversation: () => void;
  pickWorkspace: WorkspaceNavigationPorts["pickWorkspace"];
  switchWorkspace: WorkspaceNavigationPorts["switchWorkspace"];
  draft: {
    target?: { scope: string; workspaceRoot: string };
    open(scope: string, workspaceRoot: string): Promise<void>;
    dismiss(): Promise<void> | void;
  };
  ports: {
    openTaskSessionForTab(tabId: string, taskId: string): Promise<ControlResult>;
    listSessionsForTab(tabId: string): Promise<SessionMeta[]>;
  };
};

/**
 * Owns the session-level navigation commands: local draft opening,
 * topic/resume/sidebar-IM enqueues, new-tab routing (remote hosts reopen
 * remotely), recovery refresh pairs, folder switching through the lazy
 * navigation owner and the task-monitor session lookup with its
 * navigation-intent fence. Formal-session navigation coalesces through the
 * shared navigation epoch from useDesktopNavigation; local drafts use their
 * own target/revision fencing.
 */
export function useSessionNavigationCommands(input: SessionNavigationCommandsInput) {
  const { activeTab, showToast, navigation, ports } = input;

  const blankSessionTarget = useCommittedCommand(() => {
    if (input.draft.target) return input.draft.target;
    const workspaceRoot = activeTab?.workspaceRoot || "";
    const scope = activeTab?.scope === "project" && workspaceRoot ? "project" : "global";
    return { scope, workspaceRoot };
  });

  const openBlankSession = useCommittedCommand((scope: string, workspaceRoot: string): Promise<void> => {
    const targetRoot = scope === "project" ? workspaceRoot : "";
    // UI preferences use the actual directory; global navigation uses an empty wire root.
    input.prepareBlankWorkspace(workspaceRoot);
    input.enterConversation();
    return input.draft.open(scope, targetRoot);
  });

  const handleNewTab = useCommittedCommand(async () => {
    input.closeTransientOverlays();
    input.clearImDetail();
    if (input.draft.target) {
      const target = input.draft.target;
      await openBlankSession(target.scope, target.workspaceRoot);
      return;
    }
    if (activeTab?.remote) {
      const navigationIntentSeq = input.noteNavigationIntent();
      await input.draft.dismiss();
      if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
      input.prepareBlankWorkspace();
      const outcome = await navigation.openRemoteProject(activeTab.remote, { newSession: true });
      if (outcome.status === "failed") showToast(outcome.error instanceof Error ? outcome.error.message : String(outcome.error), "error");
      return;
    }
    const target = blankSessionTarget();
    await openBlankSession(target.scope, target.workspaceRoot);
  });

  const handleOpenTopic = useCommittedCommand(async (scope: string, workspaceRoot: string, topicId: string, sessionPath?: string): Promise<void> => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
    input.closeTransientOverlays();
    input.clearImDetail();
    if (sessionPath?.startsWith("session-id:")) {
      return navigation.enqueueNavigationWithIntent({ kind: "canonical-session", ref: { hostId: "local", sessionId: sessionPath.slice("session-id:".length) } }, navigationIntentSeq);
    }
    return navigation.enqueueNavigationWithIntent({ kind: "topic", scope, workspaceRoot, topicId, sessionPath }, navigationIntentSeq);
  });

  const openSidebarImConnectionSession = useCommittedCommand(async (connection: SidebarImConnection): Promise<void> => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
    input.clearImDetail();
    return navigation.enqueueNavigationWithIntent({ kind: "sidebar-im", connection }, navigationIntentSeq);
  });

  const onResumeSession = useCommittedCommand(async (session: SessionMeta): Promise<void> => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
    return navigation.enqueueNavigationWithIntent({ kind: "resume-session", session }, navigationIntentSeq);
  });
  const openCanonicalSession = useCommittedCommand(async (ref: SessionRef): Promise<void> => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
    input.closeTransientOverlays();
    input.clearImDetail();
    return navigation.enqueueNavigationWithIntent({ kind: "canonical-session", ref }, navigationIntentSeq);
  });

  const onRecoveryCreated = useCommittedCommand(() => {
    input.markProjectChanged((value) => value + 1);
    void input.refreshTabMetas(undefined, { afterMutation: true });
  });
  const onRecoveryLineageChanged = useCommittedCommand(() => {
    input.markProjectChanged((value) => value + 1);
    input.refreshHistoryView();
  });

  const openTaskMonitorSession = useCommittedCommand(async (tabID: string, taskID: string): Promise<boolean> => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return false;
    // Claim the navigation epoch before the first bridge await. If the user
    // switches tabs while the task/session lookup is pending, its completion is
    // stale and must not enqueue a newer navigation request.
    input.beginNavigationSurface(navigationIntentSeq);
    let session: SessionMeta | null;
    try {
      session = await resolveTaskMonitorSession({
        tabID,
        taskID,
        intentSeq: navigationIntentSeq,
        isIntentCurrent: input.isNavigationIntentCurrent,
        openTaskSessionForTab: (sourceTabID, sourceTaskID) => ports.openTaskSessionForTab(sourceTabID, sourceTaskID),
        listSessionsForTab: async (sourceTabID) => asArray(await ports.listSessionsForTab(sourceTabID)),
        sessionIDFromPath: taskSessionIDFromPath,
      });
    } catch (error) {
      input.settleNavigationSurface(navigationIntentSeq);
      throw error;
    }
    if (!session) {
      input.settleNavigationSurface(navigationIntentSeq);
      return false;
    }
    await navigation.enqueueNavigationWithIntent({ kind: "resume-session", session }, navigationIntentSeq);
    return input.isNavigationIntentCurrent(navigationIntentSeq);
  });

  const refreshTabsAfterMutation = useCommittedCommand((latest: () => boolean) => (
    input.refreshTabMetas(latest, { afterMutation: true })
  ));
  const switchFolder = useCommittedCommand(async (path?: string) => {
    const navigationIntentSeq = input.noteNavigationIntent();
    await input.draft.dismiss();
    if (!input.isNavigationIntentCurrent(navigationIntentSeq)) return;
    input.enterConversation();
    return loadNavigationOwner().then(({ navigateWorkspace }) => navigateWorkspace(path, {
      claimIntent: input.noteNavigationIntent,
      beginSurface: input.beginNavigationSurface,
      isIntentCurrent: input.isNavigationIntentCurrent,
      pickWorkspace: input.pickWorkspace,
      switchWorkspace: input.switchWorkspace,
      markProjectChanged: input.markProjectChanged,
      refreshTabsAfterMutation,
      maskTarget: input.settleNavigationSurface,
    }));
  });

  const handleNavigateTopic = useCommittedCommand((entry: TopicShortcutEntry) => {
    void handleOpenTopic(entry.scope, entry.workspaceRoot, entry.topicId, entry.sessionPath);
  });

  return {
    openCanonicalSession,
    openBlankSession,
    handleNewTab,
    handleOpenTopic,
    openSidebarImConnectionSession,
    onResumeSession,
    onRecoveryCreated,
    onRecoveryLineageChanged,
    openTaskMonitorSession,
    refreshTabsAfterMutation,
    switchFolder,
    handleNavigateTopic,
  };
}
