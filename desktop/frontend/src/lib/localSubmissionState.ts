export type LocalSubmissionStatus = "sending" | "accepted" | "unknown" | "failed";

export interface LocalSubmission {
  submissionId: string;
  localId: string;
  text: string;
  submitText?: string;
  createdAt: number;
  sequence: number;
  anchorItemId?: string;
  placement?: "start" | "after" | "latest";
  status: LocalSubmissionStatus;
  messageId?: string;
  turnId?: string;
  checkpointTurn?: number;
  /** The first terminal event consumed this submission's checkpoint authority. */
  settled?: boolean;
}

export interface LocalSubmissionFields {
  localSubmissions: Record<string, LocalSubmission>;
  localSubmissionOrder: string[];
  localSubmissionSendRevision: number;
  visibleSubmissionHandoffs: Record<string, { submissionId: string }>;
}

export type CanonicalUserConfirmation = { messageId: string; submissionId?: string; turnId?: string };
type CanonicalUserIdentity = {
  kind: string;
  messageId?: string;
  submissionId?: string;
  turnId?: string;
};

export function canonicalUserConfirmations(items: readonly CanonicalUserIdentity[]): CanonicalUserConfirmation[] {
  return items.flatMap(item => item.kind === "user" && item.messageId ? [{ messageId: item.messageId, submissionId: item.submissionId, turnId: item.turnId }] : []);
}

/** Identity matching is shared by state settlement and defensive presentation. */
export function matchLocalSubmissions(submissions: readonly LocalSubmission[], confirmations: readonly CanonicalUserConfirmation[]) {
  const consumed = new Set<string>();
  const matches: Array<{ submissionId: string; messageId: string }> = [];
  for (const confirmation of confirmations) {
    const available = (local: LocalSubmission) => !consumed.has(local.submissionId);
    const local = submissions.find(local => available(local) && local.messageId === confirmation.messageId)
      ?? submissions.find(local => available(local) && !local.messageId && Boolean(confirmation.submissionId) && local.submissionId === confirmation.submissionId);
    if (!local) continue;
    consumed.add(local.submissionId);
    matches.push({ submissionId: local.submissionId, messageId: confirmation.messageId });
  }
  return matches;
}

export function pruneSubmissionHandoffs<T extends LocalSubmissionFields>(state: T, items: readonly CanonicalUserIdentity[]): T {
  const visible = new Set(canonicalUserConfirmations(items).map(item => item.messageId));
  const entries = Object.entries(state.visibleSubmissionHandoffs).filter(([id]) => visible.has(id));
  return entries.length === Object.keys(state.visibleSubmissionHandoffs).length ? state
    : { ...state, visibleSubmissionHandoffs: Object.fromEntries(entries) };
}

export function isUnknownSubmissionError(error: unknown): boolean {
  return /timeout|timed out|network|connection|socket|channel.*closed|fetch failed|failed to fetch|\beof\b/i.test(error instanceof Error ? error.message : String(error));
}

export function orderedLocalSubmissions(state: LocalSubmissionFields): LocalSubmission[] {
  return state.localSubmissionOrder.flatMap((submissionId) => {
    const submission = state.localSubmissions[submissionId];
    return submission ? [submission] : [];
  });
}

export function beginLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submission: Omit<LocalSubmission, "status">,
): T {
  return {
    ...state,
    localSubmissions: { ...state.localSubmissions, [submission.submissionId]: { ...submission, status: "sending" } },
    localSubmissionOrder: state.localSubmissionOrder.includes(submission.submissionId)
      ? state.localSubmissionOrder
      : [...state.localSubmissionOrder, submission.submissionId],
    localSubmissionSendRevision: state.localSubmissionSendRevision + 1,
  };
}

export function updateLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submissionId: string | undefined,
  patch: Partial<LocalSubmission>,
): T {
  if (!submissionId) return state;
  const current = state.localSubmissions[submissionId];
  if (!current) return state;
  if (current.messageId && patch.messageId && current.messageId !== patch.messageId) return state;
  return {
    ...state,
    localSubmissions: { ...state.localSubmissions, [submissionId]: { ...current, ...patch, submissionId } },
  };
}

export function removeLocalSubmission<T extends LocalSubmissionFields>(state: T, submissionId: string | undefined): T {
  if (!submissionId || !state.localSubmissions[submissionId]) return state;
  const localSubmissions = { ...state.localSubmissions };
  delete localSubmissions[submissionId];
  return {
    ...state,
    localSubmissions,
    localSubmissionOrder: state.localSubmissionOrder.filter((candidate) => candidate !== submissionId),
  };
}

/** Retire each local echo once its durable user message is installed. */
export function settleLocalSubmissions<T extends LocalSubmissionFields>(
  state: T,
  items: readonly CanonicalUserIdentity[],
  confirmations: readonly CanonicalUserConfirmation[] = canonicalUserConfirmations(items),
): T {
  state = pruneSubmissionHandoffs(state, items);
  if (state.localSubmissionOrder.length === 0) return state;
  const remaining = { ...state.localSubmissions };
  const matches = matchLocalSubmissions(orderedLocalSubmissions(state), confirmations);
  const consumed = new Set(matches.map(match => match.submissionId));
  const visible = new Set(canonicalUserConfirmations(items).map(item => item.messageId));
  const handoffs = { ...state.visibleSubmissionHandoffs };
  for (const match of matches) {
    delete remaining[match.submissionId];
    if (visible.has(match.messageId) && !handoffs[match.messageId]) handoffs[match.messageId] = { submissionId: match.submissionId };
  }
  if (consumed.size === 0) return state;
  return {
    ...state,
    localSubmissions: remaining,
    visibleSubmissionHandoffs: handoffs,
    localSubmissionOrder: state.localSubmissionOrder.filter((submissionId) => !consumed.has(submissionId)),
  };
}

export function checkpointLocalSubmission<T extends LocalSubmissionFields>(
  state: T,
  submissionId: string | undefined,
  checkpointTurn: number | undefined,
): T {
  if (!submissionId) return state;
  const current = state.localSubmissions[submissionId];
  if (!current || current.settled) return state;
  const validTurn = Number.isInteger(checkpointTurn) && checkpointTurn! >= 0;
  return updateLocalSubmission(state, submissionId, {
    checkpointTurn: validTurn ? checkpointTurn : current.checkpointTurn,
    settled: true,
  });
}
