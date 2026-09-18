// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/project-tree-loading.test.tsx
import assert from "node:assert/strict";
import { mock } from "node:test";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import type { Root } from "react-dom/client";
import type { ReasonixDesktopHost } from "../lib/desktopHost";
import type { ProjectNode, ProjectTopicPage, ProjectTopicPageRequest, SessionGroup } from "../lib/types";

const dom = new JSDOM('<html><body><div id="root"></div></body></html>', { url: "http://localhost/", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement, Node: dom.window.Node, Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent, localStorage: dom.window.localStorage,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(Date.now()), 0),
  cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
window.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;

// Import the event system after installing the DOM, so real React input events
// exercise the component's search handler as they do in the renderer.
const { createRoot } = await import("react-dom/client");
const { ProjectTree } = await import("../components/ProjectTree");
const { LocaleProvider } = await import("../lib/i18n");
const { resetProjectTreeRuntimeWindowLimits } = await import("../lib/projectTreeWindow");

const roots = ["/review-a", "/review-b"];
const projects: ProjectNode[] = roots.map((root, i) => ({ key: `project-${i}`, kind: "project", label: i ? "B" : "A", root, children: [] }));
const topic = (id: string, root = roots[0]): ProjectNode => ({ key: id, topicId: id, kind: "topic", label: id, root, children: [] });
const listeners = new Map<string, Set<(...args: unknown[]) => void>>();
let revision = 1;
let rows: Record<string, ProjectNode[]> = {};
let groups: SessionGroup[] = [];
let calls: ProjectTopicPageRequest[] = [];
let intercept: ((req: ProjectTopicPageRequest) => Promise<ProjectTopicPage> | undefined) | undefined;
const catalog = () => ({ state: "ready", revision, indexed: 2, total: 2, repairPending: 0 });

function page(req: ProjectTopicPageRequest): ProjectTopicPage {
  const query = req.query?.toLowerCase() ?? "";
  const selected = req.groupId ? [] : (rows[req.workspaceRoot ?? ""] ?? []).filter(row => row.label.toLowerCase().includes(query));
  const start = Number(req.cursor || 0), limit = req.limit ?? 5;
  const items = selected.slice(start, start + limit);
  return { revision, items, complete: true, nextCursor: start + items.length < selected.length ? String(start + items.length) : undefined };
}
const bindings = {
  GetProjectTreeSnapshot: async () => ({ revision, projects, catalog: catalog() }),
  ListProjectTopics: async (req: ProjectTopicPageRequest) => { calls.push(req); return intercept?.(req) ?? page(req); },
  GetSessionCatalogStatus: async () => catalog(),
  GetSessionOrganization: async () => ({ groups, revision, order: [], manualOrderEnabled: false }),
  GetProjectTreeRuntimeSnapshot: async () => ({ revision: 0, topics: [] }),
  Platform: async () => "darwin",
  RemoteConnectionStatuses: async () => [],
};
window.reasonixDesktop = {
  kind: "electron", contract: { commands: Object.keys(bindings) }, platform: { os: "darwin" }, native: {},
  invoke: (method: string, args: unknown[]) => (bindings as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>)[method](...args),
  on: (name: string, cb: (...args: unknown[]) => void) => {
    const set = listeners.get(name) ?? new Set(); set.add(cb); listeners.set(name, set);
    return () => set.delete(cb);
  },
} as unknown as ReasonixDesktopHost;

const container = document.getElementById("root")!;
let root: Root;
const flush = async () => { await act(async () => { await new Promise<void>(resolve => setImmediate(resolve)); }); };
const advance = async (ms = 200) => { await act(async () => mock.timers.tick(ms)); await flush(); };
const labels = () => [...container.querySelectorAll(".project-tree__topic-label")].map(el => el.textContent);
const count = (workspaceRoot = roots[0]) => calls.filter(req => req.workspaceRoot === workspaceRoot).length;
async function click(selector: string, scope: ParentNode = container) {
  const target = scope.querySelector<HTMLElement>(selector);
  assert.ok(target, `missing control: ${selector}`);
  await act(async () => target.click()); await flush();
}
async function folder(label = "A") {
  const target = [...container.querySelectorAll<HTMLElement>(".project-tree__folder--project .project-tree__folder-main")]
    .find(el => el.textContent?.trim() === label);
  assert.ok(target, `missing project ${label}`);
  await act(async () => target.click()); await flush();
}
async function event(stale = false) {
  revision++;
  await act(async () => {
    for (const callback of listeners.get("project-tree:changed-v2") ?? []) callback({ revision: stale ? 0 : revision, roots: [roots[0]], reason: "changed" });
  });
  await flush();
}
async function search(value: string) {
  const input = container.querySelector<HTMLInputElement>(".project-tree__search input")!;
  assert.ok(input, "search input exists");
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  });
  await flush();
}
async function mount(withGroups = false) {
  revision = 1; calls = []; intercept = undefined;
  rows = Object.fromEntries(roots.map((path, i) => [path, Array.from({ length: 12 }, (_, n) => topic(`${i ? "B" : "A"}-${n}`, path))]));
  groups = withGroups ? [{ id: "feature", title: "Feature", topicIds: [] }] : [];
  resetProjectTreeRuntimeWindowLimits(); localStorage.clear();
  root = createRoot(container);
  await act(async () => root.render(<LocaleProvider><ProjectTree activeScope="project" activeWorkspaceRoot={roots[0]} onOpenTopic={() => {}} onAddProject={async () => {}} /></LocaleProvider>));
  await flush(); await advance();
}
async function unmount() { await act(async () => root.unmount()); }
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

mock.timers.enable({ apis: ["setTimeout"] });
try {
  await mount();
  assert.equal(count(), 1, "cold first page loads once");
  await folder("B");
  assert.equal(count(roots[1]), 1);
  await folder(); await folder(); await advance();
  assert.equal(count(), 1, "unchanged project reopen reuses its cache");
  assert.equal(count(roots[1]), 1, "sibling expand/collapse never reloads B");
  await click('[aria-label="Collapse all"]');
  await click('[aria-label="Restore previous groups"]'); await advance();
  assert.equal(calls.length, 2, "unchanged global restore reuses both caches");

  await folder();
  rows[roots[0]] = [topic("A-new"), ...rows[roots[0]].filter(row => row.topicId !== "A-0")];
  await event();
  assert.equal(count(), 1, "collapsed invalidation does not eagerly fetch");
  await folder(); await advance();
  assert.equal(count(), 2, "reopening an invalidated project fetches once");
  assert.ok(labels().includes("A-new"));
  assert.ok(!labels().includes("A-0"), "externally archived row is removed");
  assert.equal(count(roots[1]), 1, "A's invalidation leaves B's cache valid");

  await click('[aria-label="Collapse all"]');
  rows[roots[0]] = [topic("A-newer"), ...rows[roots[0]].filter(row => row.topicId !== "A-new")];
  await event();
  assert.equal(count(), 2);
  await click('[aria-label="Restore previous groups"]'); await advance();
  assert.equal(count(), 3, "global restore reloads only invalidated A");
  assert.equal(count(roots[1]), 1);
  assert.ok(labels().includes("A-newer")); assert.ok(!labels().includes("A-new"));
  await folder();
  rows[roots[0]] = [topic("A-reconciled"), ...rows[roots[0]]];
  await event(true);
  await folder(); await advance();
  assert.ok(labels().includes("A-reconciled"), "out-of-order events still invalidate cached lists while reconciling shells");
  assert.equal(count(), 4); assert.equal(count(roots[1]), 1);
  await unmount();
  console.log("  PASS  project/global reopen caches, deferred invalidation and sibling isolation");

  await mount(true);
  assert.deepEqual(calls.map(req => req.groupId || "ungrouped").sort(), ["feature", "ungrouped"], "group and parent initialization share one request per list");
  await advance();
  assert.equal(calls.length, 2, "no delayed duplicate first page");
  await folder(); await folder(); await advance();
  assert.equal(calls.length, 2, "reopening a grouped project reuses both lists");
  await unmount();
  console.log("  PASS  grouped cold start requests each list exactly once");

  for (const oldFinishesFirst of [true, false]) {
    await mount();
    const oldRequest = deferred<ProjectTopicPage>(), freshRequest = deferred<ProjectTopicPage>();
    let requestIndex = 0;
    intercept = () => (++requestIndex === 1 ? oldRequest.promise : freshRequest.promise);
    await event();
    const oldPage = page(calls.at(-1)!);
    rows[roots[0]] = [topic("A-fresh"), ...rows[roots[0]]];
    await event();
    const freshPage = page(calls.at(-1)!);
    assert.equal(count(), 3, "invalidation starts a new generation despite an older pending request");
    if (oldFinishesFirst) {
      await act(async () => oldRequest.resolve(oldPage)); await flush();
      assert.ok(container.querySelector(".project-tree__topic-window-status"), "old completion cannot clear the newer loading state");
      await act(async () => freshRequest.resolve(freshPage));
    } else {
      await act(async () => freshRequest.resolve(freshPage)); await flush();
      await act(async () => oldRequest.resolve(oldPage));
    }
    await flush();
    assert.ok(labels().includes("A-fresh"), "late stale response cannot overwrite fresh data");
    assert.equal(count(), 3);
    await unmount();
  }
  console.log("  PASS  old/new request completion order preserves the current generation");

  await mount();
  await folder();
  await search("A-");
  assert.equal(count(), 1, "typing is debounced");
  // An event is allowed to populate a visible search before debounce expires.
  rows[roots[0]] = [topic("A-search-new"), ...rows[roots[0]]];
  await event();
  assert.equal(count(), 2, "search visibility refreshes even a manually collapsed project");
  await advance();
  assert.equal(count(), 2, "debounced search reuses the event's initialized page");
  assert.ok(labels().includes("A-search-new"));
  const oldSearch = deferred<ProjectTopicPage>();
  intercept = req => req.query === "pending" ? oldSearch.promise : undefined;
  await search("pending"); await advance();
  await search("");
  await folder();
  await act(async () => oldSearch.resolve({ revision, items: [topic("stale-search")], complete: true })); await flush();
  assert.ok(labels().includes("A-search-new")); assert.ok(!labels().includes("stale-search"));
  assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 0, "abandoned query cannot strand the normal list in loading");
  await unmount();
  console.log("  PASS  search visibility, debounce deduplication and abandoned requests");

  await mount();
  const initialSort = calls.at(-1)?.sortMode ?? "created";
  const nextSort = initialSort === "created" ? "updated" : "created";
  const oldSort = deferred<ProjectTopicPage>();
  intercept = req => req.sortMode === initialSort ? oldSort.promise : undefined;
  await event();
  const oldSortPage = page(calls.at(-1)!);
  await click('.project-tree__header-menu-wrap button[aria-haspopup="menu"]');
  const nextSortLabel = nextSort === "created" ? "Created time" : "Updated time";
  const nextSortButton = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(button => button.textContent?.includes(nextSortLabel));
  assert.ok(nextSortButton);
  await act(async () => nextSortButton.click()); await flush();
  assert.equal(calls.at(-1)?.sortMode, nextSort, "sorting starts a new request while the old order is pending");
  assert.equal(calls.at(-1)?.cursor, "", "new sort discards the old order's pagination cursor");
  assert.equal(count(), 3);
  await act(async () => oldSort.resolve(oldSortPage)); await flush();
  assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 0);
  assert.equal(count(), 3);
  await unmount();
  console.log("  PASS  sort changes retire pending requests and their cursors");
} finally {
  mock.timers.reset();
  dom.window.close();
}
