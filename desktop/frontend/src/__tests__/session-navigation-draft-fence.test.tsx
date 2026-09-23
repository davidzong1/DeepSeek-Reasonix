import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";

import { useSessionNavigationCommands, type SessionNavigationCommandsInput } from "../app-runtime/useSessionNavigationCommands";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  IS_REACT_ACT_ENVIRONMENT: true,
});

let intent = 0;
let commands!: ReturnType<typeof useSessionNavigationCommands>;
const creationStarted = deferred();
const finishCreation = deferred();
let blockCreation = false;
const created: string[] = [];
const legacyCalls: string[] = [];
const retired = (name: string) => async () => { legacyCalls.push(name); throw new Error("retired draft database is unreadable"); };
installDesktopHostStub({
  ...makeSessionUIMock(async (_scope,_root,id) => {
    created.push(id);
    if (blockCreation) { creationStarted.resolve(); await finishCreation.promise; }
  }),
  ListSessionDraftSummaries: retired("list"), OpenSessionDraftForTarget: retired("open"),
  DismissSessionDraft: retired("dismiss"), SetSessionDraftRestoreTarget: retired("restore"),
});
const enqueued: Array<{ request: unknown; intent: number }> = [];

function Probe() {
  commands = useSessionNavigationCommands({
    activeTab: { id: "fixture", scope: "project", workspaceRoot: "/workspace" },
    showToast: () => {},
    closeTransientOverlays: () => {},
    clearImDetail: () => {},
    prepareBlankWorkspace: () => {},
    navigation: {
      enqueueNavigation: async () => {},
      enqueueNavigationWithIntent: async (request, navigationIntent) => {
        enqueued.push({ request, intent: navigationIntent });
      },
      openRemoteProject: async () => ({ status: "cancelled", reason: "superseded" }),
    },
    noteNavigationIntent: () => ++intent,
    beginNavigationSurface: () => {},
    settleNavigationSurface: () => {},
    isNavigationIntentCurrent: (candidate) => candidate === intent,
    markProjectChanged: () => {},
    refreshTabMetas: async () => {},
    refreshHistoryView: () => {},
    enterConversation: () => {},
    pickWorkspace: async () => "",
    switchWorkspace: async () => {},
    ports: {
      openTaskSessionForTab: async () => ({ ok: false }),
      listSessionsForTab: async () => [],
    },
  } as SessionNavigationCommandsInput);
  return null;
}

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => { root.render(<Probe />); });
  await act(async () => {
    await commands.openBlankSession("global", "/ignored/global/root");
    await commands.openBlankSession("project", "/workspace");
    await commands.handleNewTab();
  });
  assert.deepEqual(legacyCalls, [], "manual new never touches retired input, even if its database is unreadable");
  assert.equal(new Set(created).size, 3, "each click has its own formal identity");
  assert.equal(enqueued.length,3);
  enqueued.length=0;
  blockCreation = true;

  let stale!: Promise<void>;
  act(() => {
    stale = commands.openBlankSession("project", "/workspace");
  });
  await act(async () => { await creationStarted.promise; });
  await act(async () => { await commands.openCanonicalSession({ hostId: "local", sessionId: "session-b" }); });
  finishCreation.resolve();
  await act(async () => { await stale; });
  assert.equal(enqueued.length, 1);
  assert.equal(enqueued[0]?.intent, 5, "late formal creation cannot override newer navigation");
  assert.deepEqual(enqueued[0]?.request, {
    kind: "canonical-session",
    ref: { hostId: "local", sessionId: "session-b" },
  });
  assert.deepEqual(legacyCalls, [], "navigation never waits for or writes old drafts");

  await act(async () => { root.unmount(); });
  console.log("session navigation: retired drafts are untouched; late formal creation preserves the newest selection");
} finally {
  dom.window.close();
}
