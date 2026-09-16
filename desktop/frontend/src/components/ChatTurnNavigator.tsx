import { useCallback, useMemo, useSyncExternalStore } from "react";
import type { ChatSource } from "../lib/chatViewSource";
import type { ChatScrollController } from "../lib/chatScrollController";
import type { ChatMountedOrder } from "../lib/chatMountedOrder";
import { findLoadedTurn, indexLoadedTurns, type LoadedTurnNode } from "../lib/chatTurnRail";
import { getTranscriptOutlineStore, type TranscriptOutlineView } from "../lib/transcriptOutlineStore";
import type { TurnJumpReason } from "../lib/chatTurnJump";
import type { TranscriptOutlineEntry } from "../lib/transcriptProtocol";
import { useT } from "../lib/i18n";
import { TurnNavigator, type TurnRailAnchor, type TurnRailItem } from "./harness-chat/TurnNavigator";
import css from "./harness-chat/TurnNavigator.styles";
import "./harness-chat/TurnNavigator.css";

function Preview({ source, item }: { source: ChatSource; item: TurnRailItem }) {
  const subscribe = useCallback((notify: () => void) => {
    const user = item.anchor.kind === "loaded" ? source.subscribeNode(item.anchor.key, notify) : undefined;
    const answer = item.answerKey ? source.subscribeNode(item.answerKey, notify) : undefined;
    return () => { user?.(); answer?.(); };
  }, [source, item]);
  const snapshot = useCallback(() => {
    // An unloaded turn has no node to read, so its outline preview is used as
    // it arrived. Prefer the loaded body when there is one: it carries the
    // running turn's text that the snapshot could not have seen yet.
    const user = item.anchor.kind === "loaded" ? source.getNodeSnapshot(item.anchor.key) : undefined;
    const answer = item.answerKey ? source.getNodeSnapshot(item.answerKey) : undefined;
    const prompt = user?.kind === "user" ? user.item.text.slice(0, 300) : item.prompt;
    const response = answer?.kind === "assistant" && answer.item.text.trim() ? answer.item.text.slice(0, 500) : item.response;
    return JSON.stringify([prompt, response]);
  }, [source, item]);
  const [prompt, response] = JSON.parse(useSyncExternalStore(subscribe, snapshot, snapshot)) as string[];
  return <><div className={css.previewPrompt}>{prompt || item.ordinal}</div><div className={css.previewResponse}>{response}</div></>;
}

export default function ChatTurnNavigator({ source, scroll, mounts, tabId, onNavigate, onRetryJump, onCancelJump, busyTurn, failedTurn, failedReason, knownTurns = 0 }: {
  source: ChatSource;
  scroll: ChatScrollController;
  mounts: ChatMountedOrder;
  tabId?: string;
  /** The single entry point for every click: loaded and unloaded alike. */
  onNavigate?: (target: { anchor: TurnRailAnchor; entry: TranscriptOutlineEntry }) => void;
  onRetryJump?: () => void;
  onCancelJump?: () => void;
  busyTurn?: string | null;
  failedTurn?: string | null;
  /** Why the last jump failed, so the retry can say what it is recovering. */
  failedReason?: TurnJumpReason;
  /** Turns the session is already known to hold, independent of the outline. */
  knownTurns?: number;
}) {
  const t = useT();
  const store = useMemo(() => getTranscriptOutlineStore(), []);
  // An unknown tab id resolves to the store's frozen legacy view, so the
  // snapshot stays referentially stable when no tab is bound yet.
  const subscribeOutline = useCallback((notify: () => void) => (tabId ? store.subscribe(tabId, notify) : () => {}), [store, tabId]);
  const readOutline = useCallback((): TranscriptOutlineView => store.getView(tabId ?? ""), [store, tabId]);
  const outline = useSyncExternalStore(subscribeOutline, readOutline, readOutline);
  const order = useSyncExternalStore(mounts.subscribe, mounts.getSnapshot, mounts.getSnapshot);
  const position = useSyncExternalStore(scroll.subscribe, scroll.getSnapshot, scroll.getSnapshot);

  // A new array identity whenever the outline changes so the merge re-runs.
  const outlineEntries = outline.entries;

  const items = useMemo(() => {
    const turns: TurnRailItem[] = [];
    const byTurn = new Map<string, TurnRailItem>();
    const identity = new Map<string, LoadedTurnNode>();
    for (const key of order) {
      const node = source.getNodeSnapshot(key);
      if (node?.kind === "user") {
        const item: TurnRailItem = {
          turn: key, ordinal: turns.length + 1, prompt: "", response: "",
          anchor: { kind: "loaded", key },
        };
        turns.push(item);
        byTurn.set(key, item);
        identity.set(key, { id: node.item.id, messageId: node.item.messageId });
      } else if (node?.kind === "assistant" && turns.length) turns[turns.length - 1].answerKey = key;
    }
    if (outline.mode !== "ready") return turns;

    // The outline is the complete conversation; loaded turns only enrich it.
    // Ordering and numbering come from the outline so loading an earlier page
    // never renumbers the rail. One pass builds the identity index the merge
    // then resolves from in constant time.
    const loaded = indexLoadedTurns(order, (key) => identity.get(key));
    const merged: TurnRailItem[] = [];
    // Mark identities already emitted, and the mounted nodes they consumed, so
    // a turn is never listed twice under two different identities.
    const emitted = new Set<string>();
    const claimed = new Set<string>();
    for (const entry of outlineEntries) {
      const key = findLoadedTurn(loaded, entry);
      // The mark keeps the outline's record id as its identity for its whole
      // life, so finishing a load never remounts it or moves its position.
      if (emitted.has(entry.id)) continue;
      emitted.add(entry.id);
      if (key !== undefined) claimed.add(key);
      const mounted = key ? byTurn.get(key) : undefined;
      merged.push({
        turn: entry.id,
        ordinal: entry.turn > 0 ? entry.turn : merged.length + 1,
        prompt: mounted?.prompt || entry.prompt,
        response: mounted?.response || entry.answer || "",
        answerKey: mounted?.answerKey,
        anchor: key ? { kind: "loaded", key } : { kind: "unloaded", recordId: entry.id, messageId: entry.messageId },
        unloaded: key === undefined,
      });
    }
    // A question submitted while the outline was being read is not in it yet.
    // Keep it rather than dropping a turn the reader can already see.
    for (const item of turns) {
      if (claimed.has(item.turn) || emitted.has(item.turn)) continue;
      emitted.add(item.turn);
      merged.push({ ...item, ordinal: merged.length + 1, unloaded: false });
    }
    return merged;
  }, [order, source, outline.mode, outlineEntries]);

  // Every click goes through the caller's single transaction entry point so a
  // newer selection supersedes a pending jump instead of racing it.
  const navigate = useCallback((item: TurnRailItem) => {
    onNavigate?.({
      anchor: item.anchor,
      entry: {
        id: item.anchor.kind === "unloaded" ? item.anchor.recordId : item.turn,
        messageId: item.anchor.kind === "unloaded" ? item.anchor.messageId : undefined,
        turn: item.ordinal, order: 0, prompt: item.prompt, answer: item.response,
      },
    });
  }, [onNavigate]);
  const reloadOutline = useCallback(() => { if (tabId) void store.retry(tabId); }, [store, tabId]);
  const preview = useCallback((item: TurnRailItem) => <Preview key={item.turn} source={source} item={item} />, [source]);
  // Only a session already known to hold more than one turn keeps the rail's
  // area while the outline is still loading; a fresh conversation shows nothing.
  // A failure is different: whatever markers are already known stay, and the
  // retry entry is offered whether or not any are.
  const failed = outline.mode === "error";
  const loading = knownTurns > 1 && items.length < 2 && outline.mode === "loading";
  // A failed jump is retried against its own target, not by reading more
  // history; a jump that is still paging offers its own cancel.
  const jumpFailed = failedTurn !== null && failedTurn !== undefined;
  // A budget running out and a recycled cut are different situations, so the
  // retry says which one it is recovering from.
  const jumpReasonKey = !jumpFailed ? undefined
    : failedReason === "pageBudgetExhausted" ? "chat.turnNavigation.reasonBudget"
    : failedReason === "snapshotExpired" ? "chat.turnNavigation.reasonExpired"
    : "chat.turnNavigation.reasonUnavailable";
  return <TurnNavigator items={items} activeTurn={position.activeKey || null} busyTurn={busyTurn ?? null}
    onNavigate={navigate} renderPreview={preview} t={t}
    loading={loading} failed={failed} onRetry={failed ? reloadOutline : jumpFailed ? onRetryJump : undefined}
    jumpFailed={jumpFailed} jumpReasonKey={jumpReasonKey} truncated={outline.truncated}
    onCancelJump={busyTurn ? onCancelJump : undefined} />;
}
