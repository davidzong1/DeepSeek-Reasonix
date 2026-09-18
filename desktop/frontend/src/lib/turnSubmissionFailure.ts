import type { AppBindings } from "./bridge";
import { asArray } from "./array";
import { removeEmptyAssistantItems } from "./assistantItems";
import type { Item, State } from "./useController";
import { removeLocalSubmission, updateLocalSubmission } from "./localSubmissionState";

export function reduceSubmitFailure(
  state: State,
  submissionId: string,
  error: string,
  conservative: boolean,
  observedAt: number,
): State {
  if (!state.localSubmissions[submissionId] || state.localSubmissions[submissionId].settled) return state;
  const ownsRequest = state.pendingSubmissionId === submissionId;
  const ownsTurn = Boolean(state.activeTurnId && state.localSubmissions[submissionId].turnId === state.activeTurnId);
  if (!ownsRequest && (state.pendingSubmissionId || !ownsTurn)) {
    return updateLocalSubmission(state, submissionId, { status: "failed" });
  }
  const next = updateLocalSubmission({
    ...state,
    pendingUser: undefined,
    pendingSubmissionId: undefined,
    deliveryRecoveryActive: false,
    cancelRequested: false,
    seq: state.seq + 1,
    items: [...(state.transcriptProtocol === 2 ? state.items : removeEmptyAssistantItems(state.items)), { kind: "notice", id: `n${state.seq}`, local: true, level: "warn", text: error } as Item],
  }, submissionId, { status: "failed" });
  return {
    ...next,
    running: conservative,
    turnActive: conservative,
    pendingPrompt: conservative && Boolean(state.approval || state.ask || state.mcpInteraction),
    cancellable: conservative,
    ...(conservative ? {} : {
      activeTurnId: undefined,
      currentAssistant: undefined,
      assistantSegmentOrdinal: 0,
      live: undefined,
      streamAttemptJournal: undefined,
      turnLifecycleObservedAt: observedAt,
    }),
  };
}

export function reduceManagementConfirmation(state: State, submissionId: string, observedAt: number): State {
  if (state.pendingSubmissionId !== submissionId) return removeLocalSubmission(state, submissionId);
  return removeLocalSubmission({
    ...state,
    pendingUser: undefined,
    pendingSubmissionId: undefined,
    running: false,
    turnActive: false,
    pendingPrompt: false,
    cancelRequested: false,
    cancellable: false,
    activeTurnId: undefined,
    currentAssistant: undefined,
    assistantSegmentOrdinal: 0,
    live: undefined,
    streamAttemptJournal: undefined,
    deliveryRecoveryActive: false,
    turnLifecycleObservedAt: observedAt,
  }, submissionId);
}

export async function findTabAfterSubmitFailure(
  binding: Pick<AppBindings, "ListTabs">,
  tabId: string,
  delays: readonly number[],
  clock: () => number,
) {
  for (const delay of delays) {
    if (delay) await new Promise((resolve) => setTimeout(resolve, delay));
    try {
      // Fence at read start, so a delayed response cannot override a turn or
      // prompt observed while it was in flight. Preserve a sub-tick advance
      // for synchronous bridges called in the initiating event's clock tick.
      const snapshotAt = clock() + 0.001;
      const tab = asArray(await binding.ListTabs()).find((candidate) => candidate.id === tabId);
      return [tab, snapshotAt] as const;
    } catch {
      // The caller's stale-turn watchdog remains the long-tail backstop.
    }
  }
  return undefined;
}
