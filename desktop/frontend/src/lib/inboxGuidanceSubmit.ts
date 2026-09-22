import type { AppBindings } from "./bridge";
import type { StructuredInvocationSubmit } from "./invocationDisplay";
import { resolveActiveTurnId } from "./inboxSubmit";
import type { PendingFollowup } from "./pendingFollowup";

type InboxEnqueueBindings = Pick<AppBindings, "EnqueueInboxFollowup" | "EnqueueInboxFollowupWithInvocations" | "EnqueueInboxSteer" | "EnqueueInboxSteerForTurn" | "EnqueueForAttachmentTarget">;

export async function enqueueComposerGuidance(binding: AppBindings, request: PendingFollowup, queueOnly: boolean, turnId?: string) {
  const { target, structured, tabId, display, submit, key } = request;
  // Structured invocations and image submissions require their own turn.
  if (queueOnly || structured) {
    return target && binding.EnqueueInboxFollowupForTarget && !structured?.attachments?.length
      ? binding.EnqueueInboxFollowupForTarget(target, display, submit, structured?.invocations ?? [], key)
      : enqueueInboxGuidance(binding, tabId, display, submit, structured, { idempotency: key });
  }
  if (target && binding.InboxQueueForTarget && !structured) {
    const activeTurnId = await resolveActiveTurnId(binding, tabId, turnId);
    if (!activeTurnId) throw new Error("reasonix_error:inbox_not_submitted");
    const result = await binding.InboxQueueForTarget(target, { kind: "enqueue_steer", text: submit, display, turnId: activeTurnId, idempotencyKey: key });
    if (result.reason === "unsupported") throw new Error("reasonix_error:inbox_not_submitted — update the service to guide the current turn");
    return result.receipt;
  }
  return enqueueInboxGuidanceForActiveTurn(binding, tabId, display, submit, structured, turnId, key);
}

export async function enqueueInboxGuidanceForActiveTurn(
  binding: InboxEnqueueBindings & Pick<AppBindings, "ListTabs">,
  tabId: string,
  display: string,
  submit: string,
  structured?: StructuredInvocationSubmit,
  knownTurnId?: string,
  idempotency?: string,
) {
  const turnId = !structured && typeof binding.EnqueueInboxSteerForTurn === "function"
    ? await resolveActiveTurnId(binding, tabId, knownTurnId)
    : knownTurnId;
  return enqueueInboxGuidance(binding, tabId, display, submit, structured, { steer: true, turnId, idempotency });
}

export function enqueueInboxGuidance(
  binding: InboxEnqueueBindings,
  tabId: string,
  display: string,
  submit: string,
  structured?: StructuredInvocationSubmit,
  opts?: { steer?: boolean; turnId?: string; idempotency?: string },
) {
	if (structured) {
		if (structured.attachments?.length) {
			if (!structured.attachmentTarget || !binding.EnqueueForAttachmentTarget) {
				return Promise.reject(new Error("unsupported: attachments-v2"));
			}
			return binding.EnqueueForAttachmentTarget(
				structured.attachmentTarget,
				structured.attachmentSubmissionId || opts?.idempotency || `image-${crypto.randomUUID()}`,
				structured.input.trim(),
				structured.display.trim() || display,
				structured.invocations,
				structured.attachments,
			);
		}
    return binding.EnqueueInboxFollowupWithInvocations(
      tabId,
      structured.display.trim() || display,
      structured.input.trim(),
      structured.invocations,
      opts?.idempotency ?? "",
    );
  }
  if (opts?.steer && typeof binding.EnqueueInboxSteer === "function") {
    if (typeof binding.EnqueueInboxSteerForTurn === "function") {
      if (!opts.turnId) return Promise.reject(new Error("active turn id is unavailable; refresh and try again"));
      return binding.EnqueueInboxSteerForTurn(tabId, opts.turnId, display, submit || display, opts.idempotency ?? "");
    }
    return binding.EnqueueInboxSteer(tabId, display, submit || display, opts.idempotency ?? "");
  }
  return binding.EnqueueInboxFollowup(tabId, display, submit || display, opts?.idempotency ?? "");
}
