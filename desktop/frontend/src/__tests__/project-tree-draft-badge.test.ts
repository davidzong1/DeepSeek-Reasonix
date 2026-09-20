import assert from "node:assert/strict";
import { test } from "node:test";
import type { SessionDraftSummary } from "../generated/desktopContract.generated";
import { workspaceDraftBadge } from "../lib/projectTreeTopic";

function summary(patch: Partial<SessionDraftSummary>): SessionDraftSummary {
  return { id: "draft", workspaceId: "ws", scope: "global", workspaceRoot: "", revision: 1, hasContent: false, updatedAt: 1, ...patch };
}

test("a clean empty draft does not badge its workspace", () => {
  assert.equal(workspaceDraftBadge([summary({ state: "saved" })], "global", ""), undefined);
  assert.equal(workspaceDraftBadge([summary({})], "global", ""), undefined);
  assert.equal(workspaceDraftBadge([summary({ scope: "project", workspaceRoot: "/repo/agent" })], "project", "/repo/agent"), undefined);
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
