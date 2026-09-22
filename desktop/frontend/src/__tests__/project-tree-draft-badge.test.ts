import assert from "node:assert/strict";
import { test } from "node:test";
import type { SessionDraftSummary } from "../generated/desktopContract.generated";
import { workspaceDraftBadge } from "../lib/projectTreeTopic";

function summary(patch: Partial<SessionDraftSummary>): SessionDraftSummary {
  return { id: "draft", workspaceId: "ws", scope: "global", workspaceRoot: "", revision: 1, hasContent: false, updatedAt: 1, ...patch };
}

test("legacy drafts remain recoverable even without message content", () => {
  assert.equal(workspaceDraftBadge([summary({ state: "saved" })], "global", "")?.id, "draft");
  assert.equal(workspaceDraftBadge([summary({})], "global", "")?.id, "draft");
  const project = summary({ scope: "project", workspaceRoot: "/repo/agent" });
  assert.equal(workspaceDraftBadge([project], "project", "/repo/agent")?.id, "draft");
  assert.equal(workspaceDraftBadge([project], "project", "/repo/other"), undefined);
  assert.equal(workspaceDraftBadge([], "global", ""), undefined);
});

test("unsent content badges only the owning workspace", () => {
  const drafts = [
    summary({ id: "g", hasContent: true }),
    summary({ id: "p", scope: "project", workspaceRoot: "/repo/agent", hasContent: true }),
  ];
  assert.equal(workspaceDraftBadge(drafts, "global", "")?.id, "g");
  assert.equal(workspaceDraftBadge(drafts, "project", "/repo/agent")?.id, "p");
  assert.equal(workspaceDraftBadge(drafts, "project", "/repo/other"), undefined);
});

test("an unsettled save keeps the badge while the content is still empty", () => {
  for (const state of ["dirty", "saving", "error", "conflict"]) {
    assert.equal(workspaceDraftBadge([summary({ state })], "global", "")?.state, state);
  }
});
