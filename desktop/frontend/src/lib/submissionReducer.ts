import type { Action, State } from "./useController";
import { sessionIdentityStableKey } from "./sessionIdentity";
import { beginLocalSubmission, canonicalUserConfirmations, settleLocalSubmissions, updateLocalSubmission } from "./localSubmissionState";

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
  const updates = new Map(a.projection.items.map(item => [item.id, item]));
  const removed = new Set(a.projection.removeIds);
  const items = s.items.filter(item => !removed.has(item.id)).map(item => {
    const update = updates.get(item.id);
    if (item.kind === "tool" && update?.kind === "tool" && update.resultMissing && item.status === "running") {
      return { ...update, status: "running" as const, execution: item.execution, startedAt: item.startedAt };
    }
    if (item.kind === "assistant" && update?.kind === "assistant" && item.turnFinal && !update.turnFinal) {
      return { ...update, turnFinal: true, turnDurationMs: item.turnDurationMs, turnUsage: item.turnUsage,
        samplingCount: item.samplingCount, toolCount: item.toolCount };
    }
    return update ?? item;
  });
  const present = new Set(items.map(item => item.id));
  const positions = new Map(a.projection.items.map((item, index) => [item.id, index]));
  for (const item of updates.values()) {
    if (present.has(item.id)) continue;
    const following = items.findIndex(candidate => (positions.get(candidate.id) ?? -1) > positions.get(item.id)!);
    const output = item.kind === "user" && item.turnId
      ? items.findIndex(candidate => candidate.kind !== "user" && candidate.turnId === item.turnId) : -1;
    const at = following >= 0 && output >= 0 ? Math.min(following, output) : Math.max(following, output);
    items.splice(at < 0 ? items.length : at, 0, item);
    present.add(item.id);
  }
  return settleLocalSubmissions({ ...s, items, historyHasOlder: a.projection.hasOlder, historyHasNewer: a.projection.hasNewer }, items,
    [...a.confirmedUsers, ...canonicalUserConfirmations(a.projection.items)]);
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
