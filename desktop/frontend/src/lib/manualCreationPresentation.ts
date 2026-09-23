import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import type { DictKey } from "./i18n";

export function manualCreationPresentation(item: ManualSessionCreationView): { key: DictKey; retryable: boolean } {
  const status = item.progress?.status;
  // A failed record can already have an explicitly requested retry in flight.
  if (status === "blocked") return { key: "creation.blocked", retryable: true };
  if (status === "waiting_lock") return { key: "creation.waitingLock", retryable: true };
  if (status === "retrying_storage") return { key: item.progress?.stage === "persisting_result" ? "creation.saving" : "creation.storageRetry", retryable: false };
  if (status === "stopping") return { key: "creation.stopping", retryable: false };
  if (status === "running") return { key: item.progress?.slow ? "creation.slow" : "creation.running", retryable: false };
  if (status === "queued") return { key: "creation.queued", retryable: false };
  if (item.phase === "failed") return { key: "creation.failed", retryable: true };
  return { key: "creation.pending", retryable: true };
}
