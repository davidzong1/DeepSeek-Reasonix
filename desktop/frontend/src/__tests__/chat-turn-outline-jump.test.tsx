import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { TranscriptOutlineStore } from "../lib/transcriptOutlineStore";
import type { TranscriptOutlineEntry, TranscriptOutlinePage } from "../lib/transcriptProtocol";
import type { Item } from "../lib/useController";

const TAB = "outline-jump-tab";
const SNAPSHOT = "snapshot-1";
const TOTAL = 6;

function entry(turn: number): TranscriptOutlineEntry {
  return {
    id: `m:u${turn}`, messageId: `u${turn}`, turn, order: turn * 2 - 2,
    prompt: `outline prompt ${turn}`, answer: `outline answer ${turn}`,
  };
}

function outlinePage(): TranscriptOutlinePage {
  return {
    protocolVersion: 1, snapshotId: SNAPSHOT, stale: false,
    entries: Array.from({ length: TOTAL }, (_, index) => entry(index + 1)),
    nextOffset: TOTAL, done: true, total: TOTAL,
  };
}

/** The loaded body window: the newest turns, oldest first, as the transcript
 * loads them — an earlier page prepends above this.
 *
 * The user items carry the runtime shape of a question written in this app
 * session: the reducer keeps the optimistic `u<seq>` id after the authoritative
 * message settles and only attaches `messageId`, so matching the rail by the
 * shape of the anchor key would miss them. */
function turnsFrom(start: number): Item[] {
  const items: Item[] = [];
  for (let index = start; index <= TOTAL; index += 1) {
    items.push({ kind: "user", id: `u${index}`, messageId: `u${index}`, text: `loaded prompt ${index}`, historyTurn: index });
    items.push({ kind: "assistant", id: `a${index}`, text: `loaded answer ${index}`, reasoning: "", streaming: false });
  }
  return items;
}

const harness = await createTranscriptHarness({ deterministic: true });
let store: TranscriptOutlineStore | undefined;
try {
  await harness.loadModule("/src/components/ChatToolBody.tsx");
  // The component resolves the store through the harness's module graph, so the
  // test must take the same singleton instance rather than its own import.
  const outlineModule = await harness.loadModule<{
    getTranscriptOutlineStore: () => TranscriptOutlineStore;
  }>("/src/lib/transcriptOutlineStore.ts");
  store = outlineModule.getTranscriptOutlineStore();
  store.register(TAB, async () => outlinePage());
  await store.sync(TAB, SNAPSHOT);
  assert.equal(store.getView(TAB).mode, "ready", "the outline is bound to the snapshot");

  // Only the newest two turns are loaded. The rail must still show the whole
  // conversation, which is the reported defect.
  let windowStart = TOTAL - 1;
  let pages = 0;
  const pageCommits: Array<(loaded: boolean) => void> = [];
  const render = () => harness.render(turnsFrom(windowStart), {
    tabId: TAB, totalTurns: TOTAL, hasOlderHistory: windowStart > 1, historyStartTurn: windowStart - 1,
    onLoadOlderHistory: async () => {
      pages += 1;
      if (windowStart <= 1) return false;
      windowStart = Math.max(1, windowStart - 2);
      // The parent commit is driven outside the click's act() scope. Rendering
      // recursively from this callback creates overlapping act() calls and can
      // leave later props uncommitted, which previously disguised hasOlder.
      return new Promise<boolean>(resolve => { pageCommits.push(resolve); });
    },
  });
  await render();
  await harness.settle();

  const marks = () => Array.from(harness.container.querySelectorAll<HTMLElement>("[data-nav-turn]"));
  // The rail is a lazily imported chunk, so let it commit before asserting.
  await harness.waitFor(() => marks().length === TOTAL, "the rail to list the complete outline");
  assert.equal(marks().length, TOTAL, "the rail lists every turn, not only the loaded ones");
  assert.deepEqual(
    marks().map(mark => mark.dataset.navTurn),
    Array.from({ length: TOTAL }, (_, index) => `m:u${index + 1}`),
    "rail order follows the complete conversation",
  );
  const unloaded = marks().filter(mark => mark.dataset.navUnloaded === "true");
  assert.deepEqual(unloaded.map(mark => mark.dataset.navTurn), ["m:u1", "m:u2", "m:u3", "m:u4"],
    "turns without a mounted body are marked unloaded");
  assert.equal(harness.container.querySelector('[data-chat-anchor-key="u1"]'), null, "the oldest turn is not loaded yet");

  // A question written in this app session keeps its optimistic `u<seq>` anchor
  // key, which does not match its outline record id. It must still be one mark
  // — matched by identity — and not a duplicate "unloaded" entry beside a
  // loaded one.
  const keys = new Set(marks().map(mark => mark.dataset.navTurn));
  assert.equal(keys.size, TOTAL, "every turn appears exactly once despite mismatched anchor keys");
  assert.equal(marks().find(mark => mark.dataset.navTurn === "m:u6")?.dataset.navUnloaded, undefined,
    "a loaded turn with an optimistic anchor key is not marked unloaded");

  // Absolute turn numbering must survive loading an earlier page.
  const labelsBefore = marks().map(mark => mark.getAttribute("aria-label"));
  assert.match(labelsBefore[0]!, /1/, "the first mark is turn 1");

  // Clicking an unloaded turn pages history in until its node is mounted.
  const targetMark = marks().find(mark => mark.dataset.navTurn === "m:u1")!;
  act(() => { targetMark.click(); });
  for (let page = 0; page < 2; page += 1) {
    await harness.waitFor(() => pageCommits.length > 0, `history page ${page + 1} to be requested`);
    await render();
    const commit = pageCommits.shift()!;
    await act(async () => { commit(true); await Promise.resolve(); });
  }
  await harness.waitFor(
    () => harness.container.querySelector('[data-chat-anchor-key="u1"]') !== null,
    "the oldest turn's node to mount",
  );
  await harness.settle();
  assert.ok(pages >= 2, "the jump paged older history more than once");
  assert.equal(
    marks().find(mark => mark.dataset.navTurn === "m:u1")?.dataset.navUnloaded,
    undefined,
    "the target is no longer marked unloaded once mounted",
  );
  assert.deepEqual(
    marks().map(mark => mark.dataset.navTurn),
    Array.from({ length: TOTAL }, (_, index) => `m:u${index + 1}`),
    "the rail keeps its identity and order after the jump",
  );
  assert.deepEqual(marks().map(mark => mark.getAttribute("aria-label")), labelsBefore,
    "loading an earlier page never renumbers the rail");

  // The rail's busy state clears once the target is reached.
  await harness.waitFor(
    () => harness.container.querySelectorAll('[aria-busy="true"]').length === 0,
    "the busy state to clear",
  );

  // A jump that fails must offer its own retry. The outline stays perfectly
  // readable here, so an entry gated on the outline's own failure never appears
  // — which is exactly the case that used to render no button at all.
  const phantom: TranscriptOutlineEntry = {
    id: "m:u9", messageId: "u9", turn: TOTAL + 1, order: 99, prompt: "phantom prompt", answer: "",
  };
  let outlineSnapshot = "snapshot-2";
  let outlineRefreshes = 0;
  store.register(TAB, async (_tabId, request) => ({
    ...outlinePage(), snapshotId: request.snapshotId,
    entries: [...outlinePage().entries, phantom], total: TOTAL + 1, nextOffset: TOTAL + 1,
  }), async () => {
    outlineRefreshes += 1;
    outlineSnapshot = `snapshot-retry-${outlineRefreshes}`;
    await store!.load(TAB, outlineSnapshot);
  });
  await act(async () => { await store!.load(TAB, outlineSnapshot); });
  let jumpLoads = 0;
  await harness.render(turnsFrom(TOTAL - 1), {
    tabId: TAB, geometrySessionKey: "outline-jump-phantom", totalTurns: TOTAL + 1,
    hasOlderHistory: true, historyStartTurn: TOTAL - 2,
    onLoadOlderHistory: async () => {
      pages += 1;
      jumpLoads += 1;
      return jumpLoads === 1 ? "stale" : "empty";
    },
  });
  await harness.waitFor(() => marks().length === TOTAL + 1, "the rail to list the phantom turn");
  assert.ok(harness.container.querySelector(".chat-older"), "the remounted session committed hasOlderHistory=true");
  const phantomMark = marks().find(mark => mark.dataset.navTurn === "m:u9")!;
  await act(async () => { phantomMark.click(); });
  await harness.settle();
  await harness.waitFor(
    () => harness.container.querySelector('[data-nav-retry="jump"]') !== null,
    "the failed jump to offer its own retry",
  );
  const retryButton = harness.container.querySelector<HTMLButtonElement>('[data-nav-retry="jump"]')!;
  assert.ok(retryButton.getAttribute("title"), "the retry says why the jump failed");
  assert.equal(store.getView(TAB).mode, "ready", "the outline itself never failed");
  assert.equal(
    harness.container.querySelector('[data-nav-retry="outline"]'),
    null,
    "no outline-retry entry is offered when only the jump failed",
  );
  const pagesBeforeRetry = pages;
  await act(async () => { retryButton.click(); });
  await harness.waitFor(() => outlineRefreshes === 1, "the retry to refresh the snapshot", 400);
  await harness.waitFor(() => pages > pagesBeforeRetry, "the retry to re-run the jump", 400);

  console.log("chat turn outline jump: complete rail, unloaded marks, absolute numbering, mount-confirmed jump and failed-jump retry passed");
} finally {
  await harness.unmount();
  store?.release(TAB);
  await harness.close();
}
