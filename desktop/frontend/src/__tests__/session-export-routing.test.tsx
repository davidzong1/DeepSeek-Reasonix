import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionExportCommands, type SessionExportFormat } from "../app-runtime/useSessionExportCommands";
import { t } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<!doctype html><div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Element: dom.window.Element,
  IS_REACT_ACT_ENVIRONMENT: true,
});

const hostExports: Array<{ tabId: string; path: string; title: string }> = [];
const rendererExports: Array<{ path: string; content: string }> = [];
const stub = installDesktopHostStub({
  PickExportFile: async (name: string) => `/tmp/${name}`,
  SaveSessionMarkdownForTab: async (tabId: string, path: string, title: string) => {
    hostExports.push({ tabId, path, title });
  },
  SaveExportFile: async (path: string, content: string) => {
    rendererExports.push({ path, content });
  },
});

let exportSession!: (format: SessionExportFormat) => Promise<void> | undefined;
function Probe({ remote }: { remote: boolean }) {
  exportSession = useSessionExportCommands({
    tabId: "tab-export",
    remote,
    sessionTitle: "Bounded session",
    items: [{ kind: "user", id: "u1", text: "resident projection" }],
    live: undefined,
    hasContent: true,
    t,
    showToast: () => {},
  }).exportSession;
  return null;
}

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => root.render(<Probe remote={false} />));
  await act(async () => { await exportSession("markdown"); });
  assert.deepEqual(hostExports, [{ tabId: "tab-export", path: "/tmp/Bounded session.md", title: "Bounded session" }]);
  assert.equal(rendererExports.length, 0, "local Markdown never materializes the bounded renderer projection");

  await act(async () => root.render(<Probe remote />));
  await act(async () => { await exportSession("markdown"); });
  assert.equal(rendererExports.length, 1, "remote compatibility export uses the renderer projection");
  assert.match(rendererExports[0]?.content ?? "", /resident projection/);
  console.log("session export routing: local host streaming and remote fallback passed");
} finally {
  await act(async () => root.unmount());
  stub.uninstall();
  dom.window.close();
}
