import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

import { useSessionDraftSurface } from "../app-runtime/useSessionDraftSurface";
import type { PersistentComposerDraft } from "../components/Composer";
import type { SessionDraftSubmissionRequest, SessionDraftView } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  IS_REACT_ACT_ENVIRONMENT: true,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0),
  cancelAnimationFrame: (id: number) => clearTimeout(id),
});
dom.window.requestAnimationFrame = globalThis.requestAnimationFrame;
dom.window.cancelAnimationFrame = globalThis.cancelAnimationFrame;

const settings = {
  model: "fixture/model",
  effort: "high",
  mode: "normal",
  collaborationMode: "normal",
  toolApprovalMode: "ask",
  disabledMcp: {},
  mcpOrder: [],
};
const makeDraft = (id: string, root: string): SessionDraftView => ({
  id,
  workspaceId: `workspace-${id}`,
  scope: "project",
  workspaceRoot: root,
  revision: 1,
  contentJson: "{}",
  settings: structuredClone(settings),
  status: "active",
  updatedAt: 1,
});
const drafts = new Map<string, SessionDraftView>([
  ["draft-a", makeDraft("draft-a", "/workspace/a")],
  ["draft-b", makeDraft("draft-b", "/workspace/b")],
  ["draft-c", makeDraft("draft-c", "/workspace/c")],
]);
const firstSaveStarted = deferred<void>();
const releaseFirstSave = deferred<void>();
const openC = deferred<SessionDraftView>();
const savePayloads: Array<{ id: string; text: string }> = [];
const submissions: SessionDraftSubmissionRequest[] = [];
let saveCount = 0;
let loseNextSaveAcknowledgement = false;
let submissionPolls = 0;

const stub = installDesktopHostStub({
  ListSessionDraftSummaries: async () => [],
  OpenSessionDraftForTarget: async (_scope: string, root: string) => {
    if (root.endsWith("/c")) return openC.promise;
    return structuredClone(root.endsWith("/a") ? drafts.get("draft-a") : drafts.get("draft-b"));
  },
  GetDraftContext: async (id: string) => ({ draft: structuredClone(drafts.get(id)!), commands: [], servers: [] }),
  GetSessionDraft: async (id: string) => structuredClone(drafts.get(id)!),
  SetSessionDraftRestoreTarget: async () => {},
  DismissSessionDraft: async () => {},
  RestoreSessionDraft: async () => null,
  ListTabs: async () => [{ id: "formal" }],
  AttachmentDataURLForTarget: async () => "",
  SaveSessionDraft: async (request: { draftId: string; revision: number; contentJson: string; settings: typeof settings }) => {
    saveCount++;
    const current = drafts.get(request.draftId)!;
    savePayloads.push({ id: request.draftId, text: JSON.parse(request.contentJson).text ?? "" });
    if (saveCount === 1) {
      firstSaveStarted.resolve();
      await releaseFirstSave.promise;
    }
    if (request.revision !== current.revision) {
      return { draft: structuredClone(current), conflict: true, outcome: "conflict" };
    }
    const next = { ...current, revision: current.revision + 1, contentJson: request.contentJson, settings: structuredClone(request.settings) };
    drafts.set(request.draftId, next);
    if (loseNextSaveAcknowledgement) {
      loseNextSaveAcknowledgement = false;
      throw new Error("save acknowledgement lost");
    }
    return { draft: structuredClone(next), conflict: false, outcome: "saved" };
  },
  BeginDraftSubmission: async (request: SessionDraftSubmissionRequest) => {
    submissions.push(structuredClone(request));
    return {
      operationId: "operation-goal",
      draftId: request.draftId,
      phase: "starting",
      submissionId: "submission-goal",
      session: { hostId: "local", sessionId: "session-goal" },
      updatedAt: 2,
    };
  },
  GetDraftSubmission: async () => {
    submissionPolls++;
    return {
      operationId: "operation-goal",
      draftId: "draft-a",
      phase: "accepted",
      submissionId: "submission-goal",
      session: { hostId: "local", sessionId: "session-goal" },
      updatedAt: 3,
    };
  },
});

let owner!: ReturnType<typeof useSessionDraftSurface>;
function Probe() {
  owner = useSessionDraftSurface({ onAccepted: () => {}, onChanged: () => {} });
  return null;
}

const content = (text: string): PersistentComposerDraft => ({
  text,
  invocations: [],
  attachments: [],
  workspaceRefs: [],
  pastedBlocks: [],
  openPastedLabels: [],
  sessionRefs: [],
  selectedTextRefs: [],
});

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => { root.render(<Probe />); });
  await act(async () => { await owner.open("project", "/workspace/a"); });
  act(() => owner.updateContent(content("version one")));
  let flush!: Promise<SessionDraftView | null>;
  act(() => { flush = owner.flush(); });
  await firstSaveStarted.promise;
  act(() => owner.updateContent(content("version two")));
  await act(async () => { await owner.open("project", "/workspace/b"); });
  assert.equal(owner.surface?.draft.id, "draft-b", "navigation is independent from A's active save");
  releaseFirstSave.resolve();
  await act(async () => { await flush; });
  assert.deepEqual(savePayloads.slice(0, 2), [
    { id: "draft-a", text: "version one" },
    { id: "draft-a", text: "version two" },
  ], "an edit made during save is persisted by the same draft's next CAS iteration");

  await act(async () => { await owner.open("project", "/workspace/a"); });
  assert.equal(owner.surface?.content.text, "version two", "returning to A keeps the newest confirmed content");

  loseNextSaveAcknowledgement = true;
  act(() => owner.updateContent(content("acknowledgement recovered")));
  await act(async () => { await owner.flush(); });
  assert.equal(owner.surface?.saveState, "saved", "read-back confirms a save whose acknowledgement was lost");
  assert.equal(JSON.parse(drafts.get("draft-a")!.contentJson).text, "acknowledgement recovered");

  const sourceDraftId = owner.surface!.draft.id;
  const sourceGeneration = owner.surface!.generation;
  const attachmentFinished = deferred<void>();
  const attachmentTask = attachmentFinished.promise.then(() => owner.updateContentFor(sourceDraftId, sourceGeneration, {
    ...content("acknowledgement recovered"),
    attachments: [{ path: ".reasonix/attachments/background.txt", name: "background.txt", mime: "text/plain" }],
  }));
  act(() => { owner.trackTask(sourceDraftId, sourceGeneration, attachmentTask); });
  await act(async () => { await owner.open("project", "/workspace/b"); });
  await act(async () => {
    attachmentFinished.resolve();
    await attachmentTask;
  });
  await act(async () => { await owner.flushDraft(sourceDraftId); });
  assert.equal(JSON.parse(drafts.get("draft-a")!.contentJson).attachments[0]?.path, ".reasonix/attachments/background.txt",
    "an attachment completed in the background is persisted to its captured draft");

  await act(async () => { await owner.open("project", "/workspace/a"); });
  act(() => owner.updateContent(content("local conflict value")));
  const firstRemote = drafts.get("draft-a")!;
  drafts.set("draft-a", { ...firstRemote, revision: firstRemote.revision + 1, contentJson: JSON.stringify(content("remote revision two")) });
  await act(async () => { await owner.flush(); });
  assert.equal(owner.surface?.saveState, "conflict");
  const secondRemote = drafts.get("draft-a")!;
  drafts.set("draft-a", { ...secondRemote, revision: secondRemote.revision + 1, contentJson: JSON.stringify(content("remote revision three")) });
  await act(async () => { await owner.keepLocalConflict(); });
  assert.equal(owner.surface?.saveState, "conflict", "keep-local uses CAS and cannot overwrite a newer third-party revision");
  assert.equal(JSON.parse(drafts.get("draft-a")!.contentJson).text, "remote revision three");
  act(() => owner.useSavedConflict());
  assert.equal(owner.surface?.content.text, "remote revision three", "the latest conflict snapshot remains available to resolve explicitly");

  let staleOpen!: Promise<void>;
  act(() => { staleOpen = owner.open("project", "/workspace/c"); });
  await act(async () => { await owner.dismiss(); });
  openC.resolve(structuredClone(drafts.get("draft-c")!));
  await act(async () => { await staleOpen; });
  assert.equal(owner.surface, null, "a pending draft open cannot reinstall after formal navigation dismisses it");

  await act(async () => { await owner.open("project", "/workspace/a"); });
  act(() => {
    owner.updateSettings({ collaborationMode: "goal", goal: "" });
    owner.updateContent(content("goal text"));
  });
  await act(async () => { await owner.submit("goal text", "task bytes"); });
  assert.equal(submissions.length, 1);
  assert.equal(submissions[0]?.draftId, "draft-a");
  assert.equal(submissions[0]?.goal, "goal text", "initial Goal has a non-empty objective");
  assert.equal(submissions[0]?.input, "/goal task bytes", "ordinary initial Goal preserves the established provider-visible prefix");
  assert.equal(submissions[0]?.settings.model, "fixture/model", "the operation carries its frozen model settings");
  assert.equal(submissionPolls, 1, "a recoverable operation phase keeps polling the original OperationID");

  await act(async () => { root.unmount(); });
  console.log("session draft store races: save iteration, navigation fencing and initial Goal passed");
} finally {
  stub.uninstall();
  dom.window.close();
}
