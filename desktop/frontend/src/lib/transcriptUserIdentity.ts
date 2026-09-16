import type { Item } from "./useController";

/** Last-line render protection for mixed optimistic and canonical snapshots. */
export function uniqueUserItems(items: readonly Item[]): Item[] {
  const messages = new Map<string, number>();
  const submissions = new Map<string, number>();
  const output: Item[] = [];
  for (const item of items) {
    if (item.kind !== "user") { output.push(item); continue; }
    let index = item.messageId ? messages.get(item.messageId) : undefined;
    if (index === undefined && item.submissionId) {
      const candidate = submissions.get(item.submissionId);
      const prior = candidate === undefined ? undefined : output[candidate];
      if (prior?.kind === "user" && (!prior.messageId || !item.messageId || prior.messageId === item.messageId)) index = candidate;
    }
    if (index !== undefined) {
      const prior = output[index];
      if (prior.kind === "user" && item.messageId) output[index] = { ...prior, ...item, id: prior.id };
    } else { index = output.length; output.push(item); }
    if (item.messageId) messages.set(item.messageId, index);
    if (item.submissionId) submissions.set(item.submissionId, index);
  }
  return output;
}
