import type { AppBindings } from "./bridge";
import type { SessionRef } from "./sessionRef";
import type { WorkspaceSnapshot } from "../generated/desktopContract.generated";
import type { HistoryPage } from "./types";

export interface CanonicalProjectNodeFields {
  session?: SessionRef;
  canArchive?: boolean;
}

export interface SessionLifecycleBindings {
  ListHistoricalSessions?(): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  GetHistoricalImportStatus?(): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  ImportHistoricalSession?(id: string): Promise<import("../generated/desktopContract.generated").SessionRestoreResult>;
  PrepareSession?(selector: import("../generated/desktopContract.generated").SessionSelector): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  GetSessionPreparation?(operationId: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  CancelSessionPreparation?(operationId: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  CheckHistoricalSourceUpdate?(selector: import("../generated/desktopContract.generated").SessionSelector): Promise<import("../generated/desktopContract.generated").HistoricalSourceUpdateView>;
  PrepareHistoricalSourceVersion?(source: import("../generated/desktopContract.generated").SessionSourceRef, version: string): Promise<import("../generated/desktopContract.generated").SessionPreparationView>;
  StartHistoricalImport?(ids: string[]): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
  ControlHistoricalImport?(action: string): Promise<import("../generated/desktopContract.generated").HistoricalImportStatus>;
	ApplySessionLifecycle(request: import("../generated/desktopContract.generated").SessionLifecycleRequest): Promise<import("../generated/desktopContract.generated").SessionLifecycleResult>;
	ListTrashEntries(query: string, cursor: string, limit: number): Promise<import("../generated/desktopContract.generated").TrashEntryPage>;
  ListRecoveryEntries(query: string, cursor: string, limit: number): Promise<import("../generated/desktopContract.generated").RecoveryEntryPage>;
  PreviewRecoveryEntry(id: string): Promise<HistoryPage>;
  RestoreRecoveryEntry(id: string, operationId: string): Promise<import("../generated/desktopContract.generated").SessionRestoreResult>;
  GetSessionUpgradeStatus(): Promise<import("../generated/desktopContract.generated").SessionUpgradeStatus>;
  PurgeCanonicalSession(ref: SessionRef): Promise<void>;
}

export function makeMockSessionLifecycleBindings(mockWorkspaceSnapshot: () => WorkspaceSnapshot, mockArchivedSessionIDs: Set<string>, mockPurgedSessionIDs: Set<string>, notifyMockProjectTreeChanged: () => void): SessionLifecycleBindings {
  const mockLifecycleResults = new Map<string, { request: string; result: import("../generated/desktopContract.generated").SessionLifecycleResult }>();
  return {
    async PurgeCanonicalSession(ref: SessionRef) {
      if (!mockArchivedSessionIDs.has(ref.sessionId) && !mockPurgedSessionIDs.has(ref.sessionId)) throw new Error("Only archived sessions can be permanently deleted");
      mockPurgedSessionIDs.add(ref.sessionId); mockArchivedSessionIDs.delete(ref.sessionId); notifyMockProjectTreeChanged();
    },
    async ListTrashEntries(this: AppBindings, query, cursor, limit) {
      const items: import("../generated/desktopContract.generated").TrashEntry[] = [];
      for (const workspace of mockWorkspaceSnapshot().workspaces) {
        const page = await this.ListWorkspaceSessions(workspace.id, query, "", 1000, true);
        items.push(...page.sessions.filter(row => row.archived).map(row => ({ id: row.ref.sessionId, ref: row.ref, title: row.title,
          workspaceId: workspace.id, workspaceTitle: workspace.title, archivedAt: 0, health: "ready", canPreview: true, canRestore: true, canPurge: true })));
      }
      const offset = Number(cursor || 0), end = offset + limit;
      return { items: items.slice(offset, end), generation: 1, nextCursor: end < items.length ? String(end) : "" };
    },
    async ApplySessionLifecycle(this: AppBindings, request) {
      const previous = mockLifecycleResults.get(request.operationId), encoded = JSON.stringify(request);
      if (previous) { if (previous.request !== encoded) throw new Error("Lifecycle request conflict"); return previous.result; }
      const result: import("../generated/desktopContract.generated").SessionLifecycleResult = { operationId: request.operationId, generation: 1, committed: true, items: [] };
      for (const target of request.targets) {
        if (!target.ref) throw new Error("Unknown mock recovery entry");
        if (request.action === "archive") await this.ArchiveCanonicalSession(target.ref);
        else if (request.action === "restore") await this.RestoreCanonicalSession(target.ref);
        else await this.PurgeCanonicalSession(target.ref);
        const workspace = mockWorkspaceSnapshot().workspaces.find(row => row.sessionIds.includes(target.ref!.sessionId));
        result.items.push({ target, ref: target.ref, workspaceId: workspace?.id || "global", committed: true, retryable: false });
      }
      mockLifecycleResults.set(request.operationId, { request: encoded, result });
      return result;
    },
    async ListRecoveryEntries() { return { items: [], generation: 1 }; },
    async PreviewRecoveryEntry() { return { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false }; },
    async RestoreRecoveryEntry() { throw new Error("recovery entry is unavailable"); },
    async GetSessionUpgradeStatus() { return { sources: 0, sessions: 0, operations: 0, discovered: 0, migrated: 0, pending: 0, failed: 0, conflicts: 0, pendingOperations: 0 }; },
  };
}
