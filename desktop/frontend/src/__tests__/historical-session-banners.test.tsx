import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";
import type { SessionPreparationView } from "../generated/desktopContract.generated";

const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { HistoricalSessionBanners } = await import("../components/SessionTakeoverDialog");
const { seedActiveTabMetaList } = await import("../lib/tabMetaRefresh");
const { setHistoricalPreparation, historicalPreparationSnapshot, reconcileHistoricalPreparation } = await import("../app-runtime/desktopNavigationOwner");
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
let cancellation: ReturnType<typeof deferred<SessionPreparationView>> | undefined;
let branch: ReturnType<typeof deferred<SessionPreparationView>> | undefined;
let branchError = false;
let polledPreparation: SessionPreparationView | undefined;
let navigationEpoch = 0;
let cancelled = 0;
const opened: string[] = [];
const source = { hostId: "local", sourceKey: "legacy", path: "/fixture/legacy.jsonl" };
const host = installDesktopHostStub({
  CheckHistoricalSourceUpdate: async () => ({ sourceKey: "legacy", status: "available", version: "v2", source, retryable: false }),
  PrepareHistoricalSourceVersion: async () => {
    if (branchError) throw new Error("import request failed");
    return branch ? branch.promise : ({ operationId: "version-v2", sourceKey: "legacy", status: "ready", revision: 2,
      target: { hostId: "local", sessionId: "branch-v2" }, retryable: false });
  },
  GetSessionPreparation: async () => {
    if (polledPreparation) return polledPreparation;
    throw new Error("terminal preparation must not poll");
  },
  CancelSessionPreparation: async (operationId: string) => { cancelled++; return cancellation ? cancellation.promise : { operationId, sourceKey: "legacy", status: "cancelled", revision: 3, retryable: true }; },
});
const root = createRoot(document.getElementById("root")!);
const baseProps = {
  tab: { id: "base", scope: "global", workspaceRoot: "", workspaceName: "Global", topicId: "base", topicTitle: "Base",
    label: "Base", ready: true, running: false, sessionId: "base" },
  navigate: async (intent: { kind: string; ref?: { sessionId: string } }) => { if (intent.ref) opened.push(intent.ref.sessionId); },
  captureNavigation: () => { const epoch = navigationEpoch; return () => epoch === navigationEpoch; },
};
setHistoricalPreparation({
  operationId: "prepare-legacy", status: "queued", retryable: false,
  session: { scope: "global", title: "Legacy title", topicId: "legacy", source },
});
await act(async () => root.render(<LocaleProvider><HistoricalSessionBanners {...baseProps} /></LocaleProvider>));
assert.ok(document.body.textContent?.includes("Importing: Legacy title"));
await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Cancel")!.click());
assert.equal(cancelled, 1);

await act(async () => setHistoricalPreparation(null));
await act(async () => root.render(<LocaleProvider><HistoricalSessionBanners {...baseProps} /></LocaleProvider>));
await act(async () => {});
assert.ok(document.body.textContent?.includes("Historical source changed. Import as a separate branch."));
assert.ok(!document.body.textContent?.includes("Not imported"), "an imported conversation must not be labelled unimported");
await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Import and open · Branch")!.click());
assert.deepEqual(opened, ["branch-v2"]);

const importBranchButton = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Import and open · Branch")!;
branch = deferred<SessionPreparationView>();
await act(async () => importBranchButton().click());
assert.equal(importBranchButton().disabled, true);
assert.ok(document.body.textContent?.includes("Importing"));
polledPreparation = { operationId: "version-v2", sourceKey: "legacy", status: "failed", revision: 4, errorCode: "import_failed", retryable: true };
await act(async () => branch!.resolve({ ...polledPreparation!, status: "preparing", revision: 3 }));
await act(async () => { await new Promise(resolve => setTimeout(resolve, 350)); });
assert.ok(document.querySelector('[role="alert"]')?.textContent?.includes("Import failed"), "polled failures must be visible");
assert.equal(importBranchButton().disabled, false, "failed imports remain retryable");
assert.deepEqual(opened, ["branch-v2"], "failed preparation must not navigate");

branch = deferred<SessionPreparationView>();
await act(async () => importBranchButton().click());
assert.equal(document.querySelector('[role="alert"]'), null, "retry clears the previous error");
await act(async () => branch!.resolve({ ...polledPreparation!, status: "blocked", errorCode: "source_busy" }));
assert.ok(document.querySelector('[role="alert"]')?.textContent?.includes("In use by another instance"));

branchError = true;
await act(async () => importBranchButton().click());
assert.ok(document.querySelector('[role="alert"]')?.textContent?.includes("Import failed"), "RPC errors must not disappear");
branchError = false;
branch = undefined;
await act(async () => importBranchButton().click());
assert.equal(document.querySelector('[role="alert"]'), null);
assert.deepEqual(opened, ["branch-v2", "branch-v2"], "retry can open the imported branch");

const pendingA = { operationId: "prepare-a", status: "preparing", retryable: false, revision: 4,
  session: { scope: "global", title: "A", source } };
await act(async () => setHistoricalPreparation(pendingA));
cancellation = deferred<SessionPreparationView>();
await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Cancel")!.click());
const pendingCancel = [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Cancel")!;
assert.equal(pendingCancel.disabled, true, "one operation accepts only one in-flight cancellation");
await act(async () => pendingCancel.click());
assert.equal(cancelled, 2, "a disabled cancellation control does not submit twice");
await act(async () => setHistoricalPreparation({ ...pendingA, operationId: "prepare-b" }));
await act(async () => cancellation!.resolve({ operationId: "prepare-a", sourceKey: "legacy", status: "cancelled", revision: 5, retryable: true }));
assert.equal(historicalPreparationSnapshot()?.operationId, "prepare-b", "late cancellation cannot replace a newer selection");
await act(async () => setHistoricalPreparation(pendingA));
await act(async () => reconcileHistoricalPreparation(pendingA, { operationId: "prepare-a", sourceKey: "legacy", status: "cancelled", revision: 3, retryable: true }));
assert.equal(historicalPreparationSnapshot()?.status, "preparing", "older revisions cannot regress the task");
await act(async () => reconcileHistoricalPreparation(pendingA, { operationId: "prepare-a", sourceKey: "legacy", status: "ready", revision: 6, retryable: false }));
assert.equal(historicalPreparationSnapshot()?.status, "preparing", "ready cancellation leaves activation to the navigation owner");
await act(async () => setHistoricalPreparation(null));
branch = deferred<SessionPreparationView>();
await act(async () => [...document.querySelectorAll<HTMLButtonElement>("button")].find(button => button.textContent === "Import and open · Branch")!.click());
navigationEpoch++;
await act(async () => branch!.resolve({ operationId: "version-v2", sourceKey: "legacy", status: "ready", revision: 7,
  target: { hostId: "local", sessionId: "stale-branch" }, retryable: false }));
assert.deepEqual(opened, ["branch-v2", "branch-v2"], "pending navigation invalidates a branch open even while the old tab is retained");
branch = deferred<SessionPreparationView>();
await act(async () => importBranchButton().click());
navigationEpoch++;
await act(async () => branch!.resolve({ ...polledPreparation!, status: "failed" }));
assert.equal(document.querySelector('[role="alert"]'), null, "stale failures cannot replace the newer navigation state");
const pendingIntents: unknown[] = [];
await act(async () => root.render(<LocaleProvider><HistoricalSessionBanners
  tab={{ ...baseProps.tab, sessionId: undefined, ready: false, historicalSource: source }}
  navigate={async intent => { pendingIntents.push(intent); }}
/></LocaleProvider>));
assert.deepEqual(pendingIntents, [], "restoring a legacy tab never starts preparation automatically");
await act(async () => document.getElementById("reasonix-prepare-restored-session")!.click());
assert.equal(pendingIntents.length, 1);
assert.equal((pendingIntents[0] as { kind: string }).kind, "resume-session", "explicit preparation uses the shared navigation owner");
const [canonicalTab] = seedActiveTabMetaList([{ ...baseProps.tab, historicalSource: source }], baseProps.tab);
assert.equal(canonicalTab.historicalSource, undefined, "canonical metadata omission clears the old preparation state");
await act(async () => root.render(<LocaleProvider><HistoricalSessionBanners {...baseProps} tab={canonicalTab} /></LocaleProvider>));
assert.equal(document.getElementById("reasonix-prepare-restored-session"), null, "successful activation removes the preparation action");
await act(async () => root.unmount());
host.uninstall();
dom.window.close();
console.log("PASS preparation banner, cancellation and source update branch import");
