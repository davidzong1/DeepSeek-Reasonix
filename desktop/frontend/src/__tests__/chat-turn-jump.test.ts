import assert from "node:assert/strict";
import { ChatMountedOrder } from "../lib/chatMountedOrder";
import { ChatTurnJump } from "../lib/chatTurnJump";
import type { ChatScrollController } from "../lib/chatScrollController";
import { alignOutlineEntries, findLoadedTurn, indexLoadedTurns, recordIdOf } from "../lib/chatTurnRail";
import type { TranscriptOutlineEntry } from "../lib/transcriptProtocol";

// Node has no animation frame; the mount-settle path is driven by the mounted
// store in these tests, so a recorded no-op keeps it deterministic.
const frames: FrameRequestCallback[] = [];
(globalThis as unknown as { requestAnimationFrame: (cb: FrameRequestCallback) => number }).requestAnimationFrame =
  (callback) => frames.push(callback);
(globalThis as unknown as { cancelAnimationFrame: (id: number) => void }).cancelAnimationFrame = () => {};

function flushFrames(): void {
  const pending = frames.splice(0);
  for (const callback of pending) callback(0);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

/** Records the writes a jump asks the shared gateway to make. */
function fakeScroll() {
  const jumps: string[] = [];
  const readers = new Set<() => void>();
  let stopped = false;
  const scroll = {
    stopFollowing: () => { stopped = true; },
    jump: (key: string) => { jumps.push(key); },
    subscribeReaderIntent: (listener: () => void) => { readers.add(listener); return () => { readers.delete(listener); }; },
    subscribe: () => () => {},
    getSnapshot: () => ({ following: false, activeKey: "" }),
  };
  return {
    scroll: scroll as unknown as ChatScrollController,
    jumps,
    stopped: () => stopped,
    // Reader intent is what a wheel, touch, key press, or return-to-bottom
    // reports through the controller's dedicated channel.
    readerIntent: () => { for (const listener of [...readers]) listener(); },
    readerCount: () => readers.size,
  };
}

function jumpFor(mounts: ChatMountedOrder, options: {
  mounted: Set<string>;
  pages?: string[][];
  hasOlder?: () => boolean;
  current?: () => boolean;
  snapshotId?: () => string;
  refresh?: (entry: TranscriptOutlineEntry) => Promise<TranscriptOutlineEntry | undefined>;
  drainMs?: number;
}) {
  const fake = fakeScroll();
  let pageIndex = 0;
  const loads: number[] = [];
  const jump = new ChatTurnJump({
    mounts,
    scroll: fake.scroll,
    loadOlder: async () => {
      loads.push(pageIndex);
      const revealed = options.pages?.[pageIndex] ?? [];
      pageIndex += 1;
      for (const key of revealed) options.mounted.add(key);
      // One page also advances the progressive mount.
      mounts.publish([...options.mounted]);
      return revealed.length > 0 ? "loaded" as const : "empty" as const;
    },
    hasOlder: options.hasOlder ?? (() => true),
    resolveKey: (entry) => (options.mounted.has(entry.id) ? entry.id : undefined),
    currentSnapshotId: options.snapshotId ?? (() => "cut"),
    refreshSnapshot: options.refresh ?? (async (entry) => entry),
    isCurrent: options.current ?? (() => true),
    drainMs: options.drainMs,
  });
  return { jump, fake, loads, mounted: options.mounted };
}

function target(id: string): TranscriptOutlineEntry {
  return { id, messageId: id.replace("m:", ""), turn: 1, order: 0, prompt: "p", answer: "a" };
}

async function main() {
  {
    // An already-mounted target must not page at all.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(["m:1"]) });
    await state.jump.jump(target("m:1"));
    assert.deepEqual(state.fake.jumps, ["m:1"], "a loaded target scrolls immediately");
    assert.deepEqual(state.loads, [], "a loaded target does not page history");
    assert.equal(state.jump.getSnapshot().status, "idle", "the jump completes");
    assert.ok(state.fake.stopped(), "a jump leaves tail following before it writes");
  }

  {
    // An unloaded target pages until its node is really mounted, then scrolls.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(["m:9"]), pages: [["m:5"], ["m:3"], ["m:1"]] });
    await state.jump.jump(target("m:1"));
    assert.deepEqual(state.loads, [0, 1, 2], "history is paged one batch at a time");
    assert.deepEqual(state.fake.jumps, ["m:1"], "the write happens only after the node mounts");
    assert.equal(state.jump.getSnapshot().status, "idle");
  }

  {
    // Exhausting history without reaching the target is a specific failure.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(["m:9"]), pages: [["m:9"], ["m:8"]] });
    await state.jump.jump(target("m:404"));
    assert.deepEqual(state.fake.jumps, [], "an unreachable target never moves the viewport");
    assert.equal(state.jump.getSnapshot().status, "failed");
    assert.equal(state.jump.getSnapshot().reason, "turnUnavailable");
  }

  {
    // No older history at all fails instead of looping. The wait for a
    // progressively mounting last page is still honoured first.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), hasOlder: () => false, drainMs: 0 });
    await state.jump.jump(target("m:404"));
    assert.deepEqual(state.loads, [], "a missing target with no history is not retried");
    assert.equal(state.jump.getSnapshot().status, "failed");
    assert.equal(state.jump.getSnapshot().reason, "turnUnavailable");
  }

  {
    // Exhausted history is not the same as an unreachable turn: the last page
    // mounts progressively, so the target can still appear afterwards.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(["m:9"]), pages: [["m:5"]], hasOlder: () => false });
    const pending = state.jump.jump(target("m:1"));
    let settled = false;
    void pending.then(() => { settled = true; });
    // The reveal lands several frames after the page that carried it.
    for (let i = 0; i < 50 && !settled; i++) {
      await Promise.resolve();
      state.mounted.add("m:1");
      flushFrames();
    }
    await pending;
    assert.deepEqual(state.fake.jumps, ["m:1"], "a target that mounts after the last page is still reached");
    assert.equal(state.jump.getSnapshot().status, "idle");
  }

  {
    // Budget exhaustion is reported as such, not as a missing turn.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), pages: Array.from({ length: 500 }, (_, index) => [`m:${index}`]) });
    await state.jump.jump(target("m:nowhere"));
    assert.equal(state.jump.getSnapshot().status, "failed");
    assert.equal(state.jump.getSnapshot().reason, "pageBudgetExhausted", "a page budget is not a missing turn");
  }

  {
    // A recycled snapshot ends the jump instead of silently replacing the body.
    const mounts = new ChatMountedOrder();
    const fake = fakeScroll();
    const jump = new ChatTurnJump({
      mounts, scroll: fake.scroll,
      loadOlder: async () => "stale" as const,
      hasOlder: () => true,
      resolveKey: () => undefined,
      currentSnapshotId: () => "cut",
      refreshSnapshot: async (entry) => entry,
      isCurrent: () => true,
    });
    await jump.jump(target("m:1"));
    assert.equal(jump.getSnapshot().status, "failed");
    assert.equal(jump.getSnapshot().reason, "snapshotExpired", "a recycled cut is its own outcome");
    assert.deepEqual(fake.jumps, [], "a recycled cut never moves the viewport");
  }

  {
    // Retrying a recycled cut must install a fresh snapshot first: re-running
    // the same jump against the same dead cut just fails again.
    const mounts = new ChatMountedOrder();
    let snapshot = "cut";
    let refreshes = 0;
    let stale = true;
    const fake = fakeScroll();
    let mounted: string | undefined;
    const jump = new ChatTurnJump({
      mounts, scroll: fake.scroll,
      loadOlder: async () => {
        if (stale) return "stale" as const;
        // The retry's page is productive and advances the mount, as a real one
        // does; the recycled cut never got this far.
        mounted = "m:1";
        mounts.publish(["m:1"]);
        return "loaded" as const;
      },
      hasOlder: () => true,
      resolveKey: () => mounted,
      currentSnapshotId: () => snapshot,
      refreshSnapshot: async (entry) => { refreshes += 1; stale = false; snapshot = "cut:2"; return entry; },
      isCurrent: () => true,
      drainMs: 0,
    });
    await jump.jump(target("m:1"));
    assert.equal(jump.getSnapshot().reason, "snapshotExpired", "the recycled cut is reported");
    await jump.retry();
    assert.equal(refreshes, 1, "the retry installs a fresh snapshot");
    assert.deepEqual(fake.jumps, ["m:1"], "the retry reaches the target on the new cut");
    assert.equal(jump.getSnapshot().status, "idle", "a successful retry ends the transaction");
  }

  {
    // A newer click during the refresh abandons the retry, like any other
    // pending transaction.
    const mounts = new ChatMountedOrder();
    const refreshed = deferred<TranscriptOutlineEntry | undefined>();
    let refreshStarted = false;
    const fake = fakeScroll();
    const jump = new ChatTurnJump({
      mounts, scroll: fake.scroll,
      loadOlder: async () => "stale" as const,
      hasOlder: () => true,
      resolveKey: () => undefined,
      currentSnapshotId: () => "cut",
      refreshSnapshot: async () => { refreshStarted = true; return refreshed.promise; },
      isCurrent: () => true,
    });
    const entry = target("m:1");
    await jump.jump(entry);
    const pending = jump.retry();
    assert.equal(refreshStarted, true, "the retry is waiting inside snapshot refresh");
    jump.jumpTo("u9");
    refreshed.resolve(entry);
    await pending;
    assert.deepEqual(fake.jumps, ["u9"], "a click during the refresh wins");
    assert.equal(jump.getSnapshot().status, "idle");
  }

  {
    // A transient refresh failure keeps the original target retryable. The
    // next retry must call the refresher again and can then reach the turn.
    const mounts = new ChatMountedOrder();
    const fake = fakeScroll();
    let refreshes = 0;
    let stale = true;
    let mounted: string | undefined;
    const jump = new ChatTurnJump({
      mounts, scroll: fake.scroll,
      loadOlder: async () => {
        if (stale) return "stale" as const;
        mounted = "m:1";
        mounts.publish(["m:1"]);
        return "loaded" as const;
      },
      hasOlder: () => true,
      resolveKey: () => mounted,
      currentSnapshotId: () => stale ? "cut" : "cut:2",
      refreshSnapshot: async (entry) => {
        refreshes += 1;
        if (refreshes === 1) throw new Error("network down");
        stale = false;
        return entry;
      },
      isCurrent: () => true,
    });
    await jump.jump(target("m:1"));
    await jump.retry();
    assert.equal(jump.getSnapshot().status, "failed", "a failed refresh returns to a retryable state");
    assert.equal(jump.getSnapshot().reason, "snapshotExpired");
    assert.ok(jump.getSnapshot().retry, "the failed refresh retains its target");
    await jump.retry();
    assert.equal(refreshes, 2, "the second retry invokes snapshot refresh again");
    assert.deepEqual(fake.jumps, ["m:1"], "the second retry can reach the refreshed target");
  }

  {
    // A replaced snapshot invalidates the locators this jump was resolved
    // against, so it stops rather than continuing against the new body.
    const mounts = new ChatMountedOrder();
    let snapshot = "cut";
    const state = jumpFor(mounts, { mounted: new Set(), pages: [["m:5"], ["m:1"]], snapshotId: () => snapshot });
    const pending = state.jump.jump(target("m:1"));
    await Promise.resolve();
    snapshot = "cut:2";
    await pending;
    assert.deepEqual(state.fake.jumps, [], "a replaced snapshot cancels the pending jump");
    assert.equal(state.loads.length, 1, "no page is requested against the replaced snapshot");
    // Stopping is not enough: the transaction must also release the state it
    // still owns, or the mark pulses forever and the reader stays subscribed.
    assert.equal(state.jump.getSnapshot().status, "idle", "a replaced snapshot ends the loading state");
    assert.equal(state.fake.readerCount(), 0, "a replaced snapshot releases the reader subscription");
  }

  {
    // Clicking an already-loaded turn must supersede a pending jump rather than
    // race it: both go through the same transaction.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(["u4"]), pages: [["m:5"], ["m:1"]] });
    const pending = state.jump.jump(target("m:1"));
    await Promise.resolve();
    state.jump.jumpTo("u4");
    await pending;
    assert.deepEqual(state.fake.jumps, ["u4"], "the newest click wins and the pending jump is abandoned");
    assert.equal(state.jump.getSnapshot().status, "idle");
  }

  {
    // A page that adds nothing stops the loop rather than spinning the network.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), pages: [[]] });
    await state.jump.jump(target("m:404"));
    assert.equal(state.loads.length, 1, "an unproductive page ends the jump");
    assert.equal(state.jump.getSnapshot().status, "failed");
  }

  {
    // Reader intent preempts a pending jump and no later page takes the viewport.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), pages: [["m:5"], ["m:1"]] });
    const pending = state.jump.jump(target("m:1"));
    await Promise.resolve();
    state.fake.readerIntent();
    await pending;
    assert.deepEqual(state.fake.jumps, [], "a preempted jump never scrolls");
    assert.equal(state.loads.length, 1, "no page is requested after the reader takes over");
    assert.equal(state.jump.getSnapshot().status, "idle", "preemption clears the busy state");
    assert.equal(state.fake.readerCount(), 0, "the reader subscription is released");
  }

  {
    // A newer target supersedes the pending one; only the newest may scroll.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), pages: [["m:5"], ["m:2"], ["m:7"]] });
    const first = state.jump.jump(target("m:1"));
    await Promise.resolve();
    const second = state.jump.jump(target("m:7"));
    await Promise.all([first, second]);
    assert.deepEqual(state.fake.jumps, ["m:7"], "only the newest target takes scroll control");
    assert.equal(state.jump.getSnapshot().status, "idle");
  }

  {
    // A replaced session leaves no late callback able to move the viewport.
    const mounts = new ChatMountedOrder();
    let current = true;
    const state = jumpFor(mounts, { mounted: new Set(), pages: [["m:5"], ["m:1"]], current: () => current });
    const pending = state.jump.jump(target("m:1"));
    await Promise.resolve();
    current = false;
    await pending;
    assert.deepEqual(state.fake.jumps, [], "a replaced session cannot take scroll control back");
    assert.equal(state.loads.length, 1, "no further page is requested for a replaced session");
  }

  {
    // Cancelling explicitly ends the pending transaction.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set(), pages: [["m:5"], ["m:1"]] });
    const pending = state.jump.jump(target("m:1"));
    await Promise.resolve();
    state.jump.cancel();
    await pending;
    assert.deepEqual(state.fake.jumps, [], "a cancelled jump never scrolls");
    assert.equal(state.jump.getSnapshot().status, "idle");
  }

  {
    // The bounded frame budget resolves the settle wait even when nothing
    // publishes, so a jump cannot hang on an idle mount.
    const mounts = new ChatMountedOrder();
    const state = jumpFor(mounts, { mounted: new Set() });
    let pages = 0;
    const jump = new ChatTurnJump({
      mounts,
      scroll: state.fake.scroll,
      loadOlder: async () => { pages += 1; return "loaded" as const; },
      hasOlder: () => pages < 3,
      resolveKey: () => undefined,
      currentSnapshotId: () => "cut",
      refreshSnapshot: async (entry) => entry,
      isCurrent: () => true,
    });
    let settled = false;
    const pending = jump.jump(target("m:1")).then(() => { settled = true; });
    // Drive microtasks and frames together: the settle wait must expire on its
    // own frame budget even though nothing ever publishes a mount.
    for (let i = 0; i < 20_000 && !settled; i++) {
      await Promise.resolve();
      flushFrames();
    }
    await pending;
    assert.ok(settled, "the mount wait is bounded and the jump terminates");
    assert.equal(pages, 3, "paging stops as soon as history is exhausted");
    assert.equal(jump.getSnapshot().status, "failed", "an unreachable target ends as a failure, not a hang");
  }

  {
    // Identity resolution. The node's own identity decides, never the shape of
    // its anchor key: a question written in this app session keeps its
    // optimistic `u<seq>` id after the authoritative message arrives and only
    // gains a messageId, so a key-shaped match would miss the turns the reader
    // just wrote — the exact regression this covers.
    const settled: TranscriptOutlineEntry = { id: "m:abc", messageId: "abc", turn: 1, order: 0, prompt: "", answer: "" };
    const nodes = new Map<string, { id: string; messageId?: string }>([
      ["u7", { id: "u7", messageId: "abc" }],
      ["m:other", { id: "m:other" }],
    ]);
    const index = (order: string[]) => indexLoadedTurns(order, (key) => nodes.get(key));
    assert.equal(findLoadedTurn(index(["u7"]), settled), "u7", "an optimistically submitted question is found by its message ID");
    assert.equal(findLoadedTurn(index([]), settled), undefined, "an unmounted question has no key");
    assert.equal(findLoadedTurn(index(["m:other"]), settled), undefined, "an unrelated node is not claimed");

    const uncommitted: TranscriptOutlineEntry = { id: "m:tmp", turn: 2, order: 2, prompt: "", answer: "" };
    nodes.set("m:tmp", { id: "m:tmp" });
    assert.equal(findLoadedTurn(index(["m:tmp"]), uncommitted), "m:tmp", "an uncommitted question resolves by record ID");

    // History that carries no message id is keyed `record:<recordId>` by the
    // transcript, while the outline carries the bare record id. Comparing the
    // two item keys directly never matches, so the conversion is part of the
    // contract rather than an accident of the fixture.
    nodes.set("record:m:xyz", { id: "record:m:xyz" });
    const historical: TranscriptOutlineEntry = { id: "m:xyz", turn: 3, order: 4, prompt: "", answer: "" };
    assert.equal(findLoadedTurn(index(["record:m:xyz"]), historical), "record:m:xyz",
      "a history record without a message ID resolves through its record key");
    assert.equal(recordIdOf({ id: "record:m:xyz" }), "m:xyz", "the item key converts back to the outline identity");
    assert.equal(recordIdOf({ id: "u7", messageId: "abc" }), "m:abc", "a settled question converts through its message ID");

    // A message ID match wins even when a record-ID-only match appears earlier
    // in the mounted order, so settlement cannot move a mark to a stale node.
    nodes.set("m:abc:legacy", { id: "m:abc" });
    assert.equal(findLoadedTurn(index(["m:abc:legacy", "u7"]), settled), "u7",
      "a message ID match outranks an earlier record ID match");

    // Indexing once and looking up per entry is what keeps a long conversation
    // linear instead of quadratic.
    const wide = Array.from({ length: 4000 }, (_, i) => `u${i}`);
    for (let i = 0; i < 4000; i++) nodes.set(`u${i}`, { id: `u${i}`, messageId: `${i}` });
    const wideIndex = indexLoadedTurns(wide, (key) => nodes.get(key));
    const started = Date.now();
    for (let i = 0; i < 4000; i++) {
      findLoadedTurn(wideIndex, { id: `m:${i}`, messageId: `${i}`, turn: i, order: i, prompt: "", answer: "" });
    }
    const elapsed = Date.now() - started;
    assert.ok(elapsed < 500, `4000 lookups over 4000 nodes took ${elapsed}ms; the merge must not rescan per entry`);

    const aligned = alignOutlineEntries([
      { id: "m:b", turn: 2, order: 2, prompt: "", answer: "" },
      { id: "m:a", turn: 1, order: 0, prompt: "", answer: "" },
      { id: "m:b", turn: 2, order: 2, prompt: "duplicate", answer: "" },
    ]);
    assert.deepEqual(aligned.map(item => item.id), ["m:a", "m:b"], "entries are ordered by snapshot position and de-duplicated");
  }

  console.log("chat turn jump: mount-confirmed paging, preemption, supersession, replacement, cancel and rail identity passed");
}

await main();
