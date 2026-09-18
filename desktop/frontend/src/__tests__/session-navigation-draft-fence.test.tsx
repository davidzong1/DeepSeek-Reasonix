import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

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
const firstDismiss = deferred();
const secondDismiss = deferred();
const dismissals = [firstDismiss, secondDismiss];
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
    draft: {
      open: async () => {},
      dismiss: () => dismissals.shift()!.promise,
    },
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

  let stale!: Promise<void>;
  let latest!: Promise<void>;
  act(() => {
    stale = commands.handleOpenTopic("project", "/workspace", "topic-a");
    latest = commands.openCanonicalSession({ hostId: "local", sessionId: "session-b" });
  });

  firstDismiss.resolve();
  await act(async () => { await stale; });
  assert.deepEqual(enqueued, [], "navigation superseded during draft cleanup cannot enqueue afterward");

  secondDismiss.resolve();
  await act(async () => { await latest; });
  assert.equal(enqueued.length, 1);
  assert.equal(enqueued[0]?.intent, 2, "the winning request keeps the intent captured before its first await");
  assert.deepEqual(enqueued[0]?.request, {
    kind: "canonical-session",
    ref: { hostId: "local", sessionId: "session-b" },
  });

  await act(async () => { root.unmount(); });
  console.log("session navigation draft fence: stale cleanup completion cannot override the latest target");
} finally {
  dom.window.close();
}
