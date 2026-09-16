import { compactArchivedToolItems } from "./archivedToolItems";
import { duplicateLiveItemIds } from "./hydrateHistoryApply";
import { historyRevisionIsOlder } from "./sessionTranscriptMode";
import type { HistoryMutation, Item } from "./useController";

type WindowFields = {
  items: Item[];
  historyPrefixCount: number;
  hydrateHistoryLoaded?: boolean;
  hydratePlaceholderItems?: Item[];
  historyStartTurn: number;
  historyEndTurn: number;
  historyTotalTurns: number;
  historyHasOlder: boolean;
  historyHasNewer: boolean;
  historyOlderLoading: boolean;
  historyOlderError?: string;
  historyNewerLoading: boolean;
  historyNewerError?: string;
  historyRevision?: number;
  historyDigest?: string;
  historyLayoutRevision: number;
  historyMutation: HistoryMutation;
  pendingSubmissionId?: string;
};

export type HistoryWindowMutationAction = {
  type: "history_replace" | "history_rebase" | "history_prepend" | "history_append";
  items: Item[];
  removeIds?: string[];
  startTurn: number;
  endTurn?: number;
  totalTurns: number;
  hasOlder: boolean;
  hasNewer?: boolean;
  revision?: number;
  digest?: string;
};

function preserveMountedUserIds(items: Item[], existing: Item[]): Item[] {
  const mounted = new Map<string, string>();
  for (const item of existing) if (item.kind === "user" && item.messageId) mounted.set(item.messageId, item.id);
  if (mounted.size === 0) return items;
  return items.map((item) => {
    if (item.kind !== "user") return item;
    const messageId = item.messageId ?? (item.id.startsWith("m:") ? item.id.slice(2) : undefined);
    const id = messageId ? mounted.get(messageId) : undefined;
    return id ? { ...item, id, messageId } : item;
  });
}

function common<S extends WindowFields>(state: S, action: HistoryWindowMutationAction, items: Item[], prefix: number, kind: "replace" | "prepend" | "append"): S {
  return {
    ...state,
    items: compactArchivedToolItems(items),
    historyPrefixCount: prefix,
    hydrateHistoryLoaded: true,
    hydratePlaceholderItems: undefined,
    historyStartTurn: action.startTurn,
    historyEndTurn: action.endTurn ?? (kind === "prepend" ? state.historyEndTurn : action.totalTurns),
    historyTotalTurns: action.totalTurns,
    historyHasOlder: action.hasOlder,
    historyHasNewer: Boolean(action.hasNewer),
    historyOlderLoading: false,
    historyOlderError: undefined,
    historyNewerLoading: false,
    historyNewerError: undefined,
    historyRevision: action.revision,
    historyDigest: action.digest,
    historyMutation: { seq: state.historyMutation.seq + 1, kind },
  };
}

export function reduceHistoryWindowState<S extends WindowFields>(state: S, action: HistoryWindowMutationAction): S {
  if (historyRevisionIsOlder(state.historyRevision, action.revision)) return state;
  if (action.type === "history_replace") {
    return { ...common(state, action, action.items, action.items.length, "replace"), pendingSubmissionId: undefined };
  }
  if (action.type === "history_rebase") {
    const liveTail = state.items.slice(Math.min(state.historyPrefixCount, state.items.length));
    const duplicates = new Set(duplicateLiveItemIds(action.items, liveTail));
    const items = [...preserveMountedUserIds(action.items, state.items), ...liveTail.filter((item) => !duplicates.has(item.id))];
    return { ...common(state, action, items, action.items.length, "replace"), historyLayoutRevision: state.historyLayoutRevision + 1 };
  }
  if (action.type === "history_prepend") {
    const remove = action.removeIds?.length ? new Set(action.removeIds) : undefined;
    const rest = remove ? state.items.filter((item) => !remove.has(item.id)) : state.items;
    const prefix = state.items.slice(0, Math.min(state.historyPrefixCount, state.items.length));
    const retainedPrefix = remove ? prefix.filter((item) => !remove.has(item.id)) : prefix;
    const items = [...preserveMountedUserIds(action.items, state.items), ...rest];
    return common(state, action, items, action.items.length + retainedPrefix.length, "prepend");
  }
  return {
    ...common(state, action, preserveMountedUserIds(action.items, state.items), action.items.length, "append"),
    historyLayoutRevision: state.historyLayoutRevision + 1,
  };
}
