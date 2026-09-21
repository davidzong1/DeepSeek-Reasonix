import type { Action, State } from "./useController";
import { sessionIdentityStableKey } from "./sessionIdentity";
import { beginLocalSubmission, canonicalUserConfirmations, settleLocalSubmissions, updateLocalSubmission } from "./localSubmissionState";
import { recordFrontendDiagnostic } from "./frontendDiagnosticBridge";

export function submissionBindingCurrent(current: State | undefined, expected: State): boolean {
  return current?.sessionGen === expected.sessionGen && sessionIdentityStableKey(current?.meta) === sessionIdentityStableKey(expected.meta);
}

export function resetTurnTiming(now = Date.now()): Pick<State, "turnStartAt" | "turnDoneAt" | "turnWaitAccumMs" | "promptWaitStartedAt" | "turnTokens" | "turnTotalTokens" | "turnUsage" | "turnOutputTokens" | "turnOutputChars" | "turnOutputCharsAtUsage" | "turnOutputEstimated" | "turnModelActiveAt" | "turnModelActiveMs" | "turnCost" | "turnRateBand" | "turnArgChars" | "pendingRequestModelMs"> {
  return {
    turnStartAt: now,
    turnDoneAt: 0,
    turnWaitAccumMs: 0,
    promptWaitStartedAt: undefined,
    turnTokens: 0,
    turnTotalTokens: 0,
    turnUsage: undefined,
    turnOutputTokens: 0,
    turnOutputChars: 0,
    turnOutputCharsAtUsage: 0,
    turnOutputEstimated: false,
    turnModelActiveAt: undefined,
    turnModelActiveMs: 0, pendingRequestModelMs: undefined,
    turnCost: 0,
    turnRateBand: undefined,
    turnArgChars: 0,
  };
}

export function confirmPendingUser(s: State, submissionId: string | undefined): State {
  if (!submissionId) return s;
  const next = s.pendingSubmissionId === submissionId ? { ...s, pendingUser: undefined, pendingSubmissionId: undefined } : s;
  if (s.localSubmissions[submissionId]?.status === "failed") return next;
  return updateLocalSubmission(next, submissionId, {
    status: "accepted",
  });
}


export function installTranscriptRecords(s: State, a: Extract<Action, { type: "transcript_records" }>): State {
  const ids = new Set<string>();
  for (const item of a.projection.items) {
    if (!ids.has(item.id)) { ids.add(item.id); continue; }
    recordFrontendDiagnostic("transcript", "projection.duplicate-item-id", {
      itemId: item.id, projectedItems: a.projection.items.length,
    });
    return s;
  }
  const projected = a.projection.items;
  const projectedIds = new Set(projected.map(item => item.id));
  const removed = new Set(a.projection.removeIds);
  const current = new Map<string, (typeof s.items)[number]>();
  for (const item of s.items) if (!current.has(item.id)) current.set(item.id, item);
  const items = projected.map(update => {
    const item = current.get(update.id);
    if (!item) return update;
    let merged = update;
    if (item.kind === "tool" && update.kind === "tool") {
      const runtime = {
        parentId: update.parentId ?? item.parentId,
        profile: update.profile ?? item.profile,
        subagentProgress: update.subagentProgress ?? item.subagentProgress,
        subagentOutcome: update.subagentOutcome ?? item.subagentOutcome,
        verifying: update.verifying ?? item.verifying,
        argChars: update.argChars ?? item.argChars,
      };
      if (update.resultEvidence === "missing" || update.resultMissing) {
        merged = { ...update, ...runtime, status: item.status, output: item.output, error: item.error,
          execution: item.execution, presentedFiles: item.presentedFiles, durationMs: item.durationMs,
          startedAt: item.startedAt, truncated: item.truncated };
      } else if (update.resultEvidence === "observation") {
        merged = { ...update, ...runtime, output: item.output, error: item.error,
          execution: update.execution ?? item.execution, presentedFiles: update.presentedFiles ?? item.presentedFiles,
          durationMs: update.durationMs ?? item.durationMs, startedAt: item.startedAt, truncated: item.truncated };
      } else merged = { ...update, ...runtime };
    } else if (item.kind === "assistant" && update.kind === "assistant" && item.turnFinal && !update.turnFinal) {
      merged = { ...update, turnFinal: true, turnDurationMs: item.turnDurationMs, turnUsage: item.turnUsage,
        samplingCount: item.samplingCount, toolCount: item.toolCount };
    }
    return JSON.stringify(item) === JSON.stringify(merged) ? item : merged;
  });
  // Preserve local/live rows at their nearest persisted anchors. The previous
  // projection set distinguishes reclaimed formal rows from genuinely local
  // rows, so a page removal cannot resurrect a stale transcript item.
  const previousProjected = new Set(s.transcriptProjectedIds);
  if (previousProjected.size === 0) {
    for (const item of s.items) if (projectedIds.has(item.id)) previousProjected.add(item.id);
  }
  const localRows: Array<{ item: (typeof s.items)[number]; left?: string; right?: string }> = [];
  for (let index = 0; index < s.items.length; index += 1) {
    const item = s.items[index];
    if (previousProjected.has(item.id) || projectedIds.has(item.id) || removed.has(item.id)) continue;
    let left: string | undefined;
    let right: string | undefined;
    for (let cursor = index - 1; cursor >= 0; cursor -= 1) {
      if (previousProjected.has(s.items[cursor].id)) { left = s.items[cursor].id; break; }
    }
    for (let cursor = index + 1; cursor < s.items.length; cursor += 1) {
      if (previousProjected.has(s.items[cursor].id)) { right = s.items[cursor].id; break; }
    }
    localRows.push({ item, left, right });
  }
  // A durable user record can replace a local submission between two
  // projections. Live process/assistant rows from that turn were previously
  // anchored to the formal row before the local echo because the echo itself
  // is owned outside `items`. Move that anchor to the durable user record so
  // the already-mounted turn stays in user -> process -> assistant order.
  const turnAnchors = new Map<string, string>();
  for (const item of projected) {
    if (item.kind === "user" && item.turnId) turnAnchors.set(item.turnId, item.id);
  }
  for (const local of Object.values(s.localSubmissions)) {
    const id = local.messageId ? `m:${local.messageId}` : undefined;
    if (local.turnId && id && projectedIds.has(id)) turnAnchors.set(local.turnId, id);
  }
  const tails = new Map<string, string>();
  for (const row of localRows) {
    const anchor = (row.item.turnId && turnAnchors.get(row.item.turnId)) || row.left;
    const left = anchor && (tails.get(anchor) ?? anchor);
    const leftIndex = left ? items.findIndex(item => item.id === left) : -1;
    if (leftIndex >= 0) {
      items.splice(leftIndex + 1, 0, row.item);
      if (anchor) tails.set(anchor, row.item.id);
      continue;
    }
    const rightIndex = row.right ? items.findIndex(item => item.id === row.right) : -1;
    if (rightIndex >= 0) items.splice(rightIndex, 0, row.item);
    else items.push(row.item);
  }
  const mutation = a.projection.mutation ?? "patch";
  const projectionIds = [...projectedIds];
  const structural = items.length !== s.items.length || items.some((item, index) => item !== s.items[index])
    || projectionIds.length !== s.transcriptProjectedIds.length
    || projectionIds.some((id, index) => id !== s.transcriptProjectedIds[index]);
  const digest = a.projection.digest || s.historyDigest;
  const revision = a.projection.revisionKnown ? a.projection.revision : s.historyRevision;
  const metadata = s.historyPrefixCount !== projected.length || s.historyStartTurn !== a.projection.startTurn
    || s.historyEndTurn !== a.projection.endTurn || s.historyTotalTurns !== a.projection.totalTurns
    || s.historyHasOlder !== a.projection.hasOlder || s.historyHasNewer !== a.projection.hasNewer
    || s.historyOlderLoading || s.historyNewerLoading || s.historyOlderError !== undefined
    || s.historyNewerError !== undefined || s.historyRevision !== revision || s.historyDigest !== digest;
  const confirmations = [...a.confirmedUsers, ...canonicalUserConfirmations(a.projection.items)];
  const settled = settleLocalSubmissions(s, items, confirmations);
  if (!structural && !metadata && settled === s) return s;
  return { ...settled, items, transcriptProjectedIds: projectionIds,
    historyPrefixCount: projected.length,
    historyStartTurn: a.projection.startTurn, historyEndTurn: a.projection.endTurn,
    historyTotalTurns: a.projection.totalTurns,
    historyHasOlder: a.projection.hasOlder, historyHasNewer: a.projection.hasNewer,
    historyOlderLoading: false, historyOlderError: undefined,
    historyNewerLoading: false, historyNewerError: undefined,
    historyRevision: revision, historyDigest: digest,
    historyLayoutRevision: s.historyLayoutRevision + (structural ? 1 : 0),
    historyMutation: structural
      ? { seq: s.historyMutation.seq + 1, kind: mutation === "patch" ? "patch" : mutation }
      : s.historyMutation };
}

export function startLocalSubmission(s: State, a: Extract<Action, { type: "user" }>, clock: number): State {
  const seq = a.seq !== undefined ? a.seq : s.seq;
  const userItemId = `u${seq}`;
  const next = {
    ...s,
    completionSummary: undefined,
    seq: seq + 1,
    items: s.items.map(item => item.kind==="notice" && item.action==="recover_context" ? {...item,action:undefined} : item),
    running: true,
    pendingPrompt: false,
    cancelRequested: false,
    cancellable: true,
    ...resetTurnTiming(),
    turnLifecycleObservedAt: clock,
    // New turn epoch: forget the previous prompt anchor so a genuinely new
    // prompt re-anchors freshly instead of inheriting a stale id/time.
    promptArrivedAt: undefined,
    promptArrivedId: undefined,
    pendingUser: a.text,
    pendingSubmissionId: a.submissionId,
    activeTurnId: s.turnActive ? s.activeTurnId : undefined,
    currentAssistant: undefined,
    assistantSegmentOrdinal: 0,
    live: undefined,
    streamAttemptJournal: undefined,
    streamInterruptNoticeShown: undefined,
    deliveryRecoveryActive: Boolean(a.deliveryRecovery),
    discardTurn: false,
  };
  return beginLocalSubmission(next, {
    submissionId: a.submissionId,
    localId: userItemId,
    text: a.text,
    submitText: a.submitText,
    createdAt: Date.now(),
    sequence: seq,
    anchorItemId: s.historyHasNewer ? undefined : s.items[s.items.length - 1]?.id,
    placement: s.historyHasNewer ? "latest" : s.items.length ? "after" : "start",
  });
}
