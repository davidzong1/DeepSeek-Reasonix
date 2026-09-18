import { useCallback, useMemo } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import type { QuestionAnswer, ToolApprovalMode, WireApproval, WireAsk, WireMCPInteraction } from "../lib/types";
import { executeSessionPrompt, type PromptPorts, type PromptRequest, type SessionPromptKind } from "./sessionPromptExecutor";
import { interactionInstanceKey, type InteractionKind, type InteractionTarget } from "../lib/interactionTarget";
import type { SessionRef } from "../lib/sessionRef";
import type { MCPInteractionAction, RecoveryAction } from "./sessionActionOwner";
import type { SessionResource, useSessionOperations } from "./useSessionOperations";

type Input = {
  target: SessionResource;
  sessionGeneration?: number;
  session?: SessionRef | null;
  approval?: WireApproval;
  question?: WireAsk;
  mcpInteraction?: WireMCPInteraction;
  remote: boolean;
  goal: string;
  toolApprovalMode: ToolApprovalMode;
  ports: PromptPorts;
  operations: ReturnType<typeof useSessionOperations>;
  reportError: (error: unknown) => void;
};

export function useSessionPromptCommands(input: Input) {
  const { target: sessionTarget, session, sessionGeneration, operations, ports } = input;
  const makeTarget = useCallback((prompt: WireApproval | WireAsk | WireMCPInteraction | undefined, promptKind: SessionPromptKind): InteractionTarget | undefined => {
    if (!prompt?.id || !sessionTarget.tabId || !session?.sessionId || !sessionGeneration) return undefined;
    let kind: InteractionKind;
    if (promptKind === "mcpInteraction") kind = "mcp";
    else if (promptKind === "ask") kind = "ask";
    else {
      const approval = prompt as WireApproval;
      kind = approval.kind === "recovery" || approval.recovery
        ? "recovery" : approval.tool === "exit_plan_mode" ? "plan" : "approval";
    }
    const base = {
      ...sessionTarget,
      hostId: session.hostId,
      sessionId: session.sessionId,
      sessionGeneration,
      promptId: prompt.id,
      turnId: prompt.turnId,
      runtimeEpoch: prompt.runtimeEpoch,
      kind,
      requestGeneration: "generation" in prompt ? prompt.generation : undefined,
      permissionRevision: "permissionRevision" in prompt ? prompt.permissionRevision : undefined,
    };
    return { ...base, instanceKey: interactionInstanceKey(base) };
  }, [sessionTarget, session, sessionGeneration]);
  const approvalTarget = makeTarget(input.approval, "approval");
  const questionTarget = makeTarget(input.question, "ask");
  const mcpTarget = makeTarget(input.mcpInteraction, "mcpInteraction");
  const run = useCallback(async (target: InteractionTarget | undefined, promptKind: SessionPromptKind, request: PromptRequest) => {
    if (!target) return;
    const result = await operations(
      { tabId: target.tabId, sessionKey: target.sessionKey },
      `prompt:${promptKind}`,
      { target, promptKind, request, ports },
      executeSessionPrompt,
    );
    if (result.status === "failed") throw result.error;
  }, [operations, ports]);
  const plan = useCallback((action: "start_execution" | "revise_plan" | "exit_plan", revision?: string) => run(approvalTarget, "approval", {
    kind: "plan", action, leavePlanMode: action !== "revise_plan", remote: input.remote,
    goal: input.goal, toolApprovalMode: input.toolApprovalMode, revision,
  }), [approvalTarget, input.remote, input.goal, input.toolApprovalMode, run]);
  const report = useCommittedCommand(input.reportError);
  const handleApprovalAnswer = useCallback((allow: boolean, session: boolean, persist: boolean) => (
    input.approval?.tool === "exit_plan_mode"
      ? plan(allow ? "start_execution" : "revise_plan")
      : run(approvalTarget, "approval", { kind: "approval", allow, session, persist })
  ), [approvalTarget, input.approval?.tool, plan, run]);
  const handleRecoveryAnswer = useCallback((action: RecoveryAction, feedback = "") => {
    void run(approvalTarget, "approval", { kind: "recovery", action, feedback }).catch(report);
  }, [approvalTarget, report, run]);
  const handleRevisePlan = useCallback((revision: string) => { void plan("revise_plan", revision).catch(report); }, [plan, report]);
  const handleExitPlan = useCallback(() => plan("exit_plan"), [plan]);
  const handleQuestionAnswer = useCallback((_id: string, answers: QuestionAnswer[]) => run(questionTarget, "ask", { kind: "question", answers }), [questionTarget, run]);
  const handleQuestionDismiss = useCallback(() => run(questionTarget, "ask", { kind: "question", answers: [] }), [questionTarget, run]);
  const handleMCPAnswer = useCallback((_id: string, action: MCPInteractionAction, content?: Record<string, unknown>) => {
    void run(mcpTarget, "mcpInteraction", { kind: "mcp", action, content }).catch(report);
  }, [mcpTarget, report, run]);
  return useMemo(() => ({
    approvalTarget, questionTarget, mcpTarget,
    handleApprovalAnswer, handleRecoveryAnswer, handleRevisePlan, handleExitPlan,
    handleQuestionAnswer, handleQuestionDismiss, handleMCPAnswer,
  }), [approvalTarget, questionTarget, mcpTarget, handleApprovalAnswer, handleRecoveryAnswer, handleRevisePlan, handleExitPlan, handleQuestionAnswer, handleQuestionDismiss, handleMCPAnswer]);
}
