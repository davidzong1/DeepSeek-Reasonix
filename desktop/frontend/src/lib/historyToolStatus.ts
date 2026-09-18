import type { HistoryMessage, HistoryToolCall } from "./types";
import type { ToolStatus } from "./useController";

/** Missing resident payload is not evidence that execution stopped. */
export function historyToolStatus(result: HistoryMessage | undefined, call?: HistoryToolCall, error?: string): ToolStatus {
  const evidence = result?.execution?.state ?? call?.resultObservation?.state;
  if (evidence === "cancelled" || evidence === "not_started") return "stopped";
  if (evidence === "failed" || evidence === "error") return "error";
  if (error) return "error";
  if (evidence === "completed" || evidence === "user_confirmed") return "done";
  if (evidence === "running" || evidence === "started" || evidence === "pending" || call?.pending) return "running";
  return result ? "done" : "unknown";
}
