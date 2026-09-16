import { registerHooks } from "node:module";
registerHooks({ resolve(specifier, context, nextResolve) { return specifier.endsWith(".svg") ? nextResolve("./asset-stub-for-tests.ts", { ...context, parentURL: import.meta.url }) : nextResolve(specifier, context); } });
import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import type { SessionMeta } from "../lib/types";
import { installDesktopHostStub } from "./desktopHostStub";
const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { TrashPage } = await import("../components/TrashPage");
type Row = { id: string; ref: { hostId: string; sessionId: string }; title: string; workspaceId: string; workspaceTitle: string; archivedAt: number; health: string; canPreview: boolean; canRestore: boolean; canPurge: boolean };
const row = (id: string): Row => ({ id, ref: { hostId: "local", sessionId: id }, title: id, workspaceId: "global", workspaceTitle: "Global", archivedAt: 0, health: "ready", canPreview: true, canRestore: true, canPurge: true });
let rows = [row("a"), row("b")];
let latePreview!: () => void;
let listFails = false;
const calls: string[] = [], requests: string[] = [];
installDesktopHostStub({
  ListTrashEntries: async () => { if (listFails) throw new Error("read failure"); return { items: rows, generation: 7 }; },
  ListRecoveryEntries: async () => ({ items: [{ id: "protected", title: "Protected history", canRestore: true, canPreview: true }], generation: 7 }),
  ReadSessionHistory: (ref: {sessionId:string}) => ref.sessionId === "a" ? new Promise(resolve => { latePreview = () => resolve({ messages: [{ messageId: "old", role: "user", content: "stale preview" }] }); }) : Promise.resolve({ messages: [{ messageId: "b", role: "user", content: "B history" }] }),
  ApplySessionLifecycle: async (request: { operationId:string; action:string; targets:{ref:{sessionId:string}}[] }) => {
    requests.push(request.operationId);
    const items = request.targets.map(target => {
      const id = target.ref.sessionId;
      // Completed children are not executed again when the whole command retries.
      if (!rows.some(row => row.id === id)) return { target, ref: target.ref, committed: true, workspaceId: "global" };
      calls.push(`${request.action}:${id}`);
      const committed = request.action === "restore" || id !== "b";
      if (committed) rows = rows.filter(row => row.id !== id);
      if (request.action === "restore") listFails = true;
      return { target, ref: target.ref, committed, errorCode: committed ? "" : "operation_failed", workspaceId: "global" };
    });
    return { operationId: request.operationId, generation: 8, committed: items.every(item => item.committed), items };
  },
});
const root = createRoot(document.getElementById("root")!);
const render = (active = true) => <LocaleProvider><TrashPage active={active} onBack={() => {}} onOpenSession={async () => {}} list={async ():Promise<SessionMeta[]> => { throw new Error("legacy list must not be queried"); }} purge={async () => { throw new Error("legacy purge must not be called"); }} restore={async () => {}} /></LocaleProvider>;
const button = (text: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(node => node.textContent?.trim() === text)!;
await act(async () => root.render(render()));
assert.equal(document.querySelector('[aria-pressed="true"]'), null, "no archived/deleted sub-tabs");
assert.equal(document.querySelectorAll('.archived-sessions__row').length, 2);
await act(async () => (document.querySelectorAll('.archived-sessions__open')[0] as HTMLButtonElement).click());
await act(async () => (document.querySelectorAll('.archived-sessions__open')[1] as HTMLButtonElement).click());
await act(async () => latePreview());
assert.ok(document.querySelector('.archived-sessions__preview')?.textContent?.includes("B history"));
assert.ok(!document.body.textContent?.includes("stale preview"));
await act(async () => (document.querySelector('.history-clear') as HTMLButtonElement).click());
assert.ok(document.querySelector('[role="dialog"]')?.textContent?.includes("these 2 archived conversations"));
assert.equal(document.activeElement?.textContent, "Cancel");
await act(async () => button("Permanently delete").click());
assert.deepEqual(calls, ["purge:a", "purge:b"]);
assert.equal(document.querySelectorAll('.archived-sessions__row').length, 1);
await act(async () => button("Retry failed items").click());
assert.deepEqual(calls, ["purge:a", "purge:b", "purge:b"]);
assert.equal(requests[0], requests[1], "retry keeps the durable operation ID");
await act(async () => button("Restore").click());
assert.ok(document.body.textContent?.includes("Operation completed. Refresh failed"));
assert.equal(document.querySelectorAll('.archived-sessions__row').length, 0);
await act(async () => root.render(render(false)));
assert.equal(document.querySelector('[role="dialog"]'), null);
await act(async () => root.unmount());
dom.window.close();
console.log("PASS unified trash, stale preview, confirmation, failed-only retry and committed refresh failure");
