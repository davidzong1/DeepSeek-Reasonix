import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { ManualSessionRecovery } from "../components/ManualSessionRecovery";
import { LocaleProvider } from "../lib/i18n";
import { manualCreationPresentation } from "../lib/manualCreationPresentation";
import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

const item = { operationId: "recovery-test", scope: "global", phase: "starting" } as ManualSessionCreationView;
const progress = (status: string, slow = false) => ({ status, stage: "building_runtime", stageStartedAt: 1, elapsedMs: 0, slow });
assert.equal(manualCreationPresentation(item).key, "creation.pending");
assert.equal(manualCreationPresentation({ ...item, progress: progress("future-status") }).key, "creation.pending");
assert.equal(manualCreationPresentation({ ...item, progress: progress("running", true) }).key, "creation.slow");
assert.equal(manualCreationPresentation({ ...item, phase: "failed", progress: progress("queued") }).retryable, false);
assert.equal(manualCreationPresentation({ ...item, progress: progress("retrying_storage") }).retryable, false);
assert.equal(manualCreationPresentation({ ...item, progress: progress("retrying_storage") }).key, "creation.storageRetry");
assert.equal(manualCreationPresentation({ ...item, progress: { ...progress("retrying_storage"), stage: "persisting_result" } }).key, "creation.saving");

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
let retryCount = 0;
let exports = 0;
let finish!: (view: ManualSessionCreationView) => void;
const reply = new Promise<ManualSessionCreationView>(resolve => { finish = resolve; });
const stub = installDesktopHostStub({
  ListManualSessionCreations: async () => [{ ...item, progress: progress("waiting_lock") }],
  RetryManualSessionCreation: async () => { retryCount++; return reply; },
  ExportManualCreationDiagnostics: async () => { exports++; return "report.json"; },
});
const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery /></LocaleProvider>));
  assert.match(document.body.textContent!, /Another process/);
  const retry = document.querySelector("button")!;
  act(() => { retry.click(); retry.click(); });
  assert.equal(retryCount, 1, "duplicate retry is suppressed while the RPC is pending");
  assert.equal(retry.disabled, true);
  await act(async () => finish({ ...item, progress: progress("running") }));
  assert.match(document.body.textContent!, /Initializing session/);
  assert.equal(document.querySelectorAll("button").length, 1, "running work only exposes diagnostics");
  await act(async () => document.querySelector("button")!.click());
  assert.equal(exports, 1);
  console.log("PASS recovery states, retry coalescing, reply feedback and diagnostic export");
} finally {
  await act(async () => root.unmount());
  stub.uninstall();
  dom.window.close();
}
