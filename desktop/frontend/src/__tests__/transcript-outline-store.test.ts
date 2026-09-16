import assert from "node:assert/strict";
import { OutlineUnsupported, TranscriptOutlineStore, type OutlineRead } from "../lib/transcriptOutlineStore";
import type { TranscriptOutlineEntry, TranscriptOutlinePage, TranscriptOutlineRequest } from "../lib/transcriptProtocol";

function entry(index: number, extra: Partial<TranscriptOutlineEntry> = {}): TranscriptOutlineEntry {
  return { id: `m:${index}`, messageId: `${index}`, turn: index, order: index, prompt: `prompt ${index}`, answer: `answer ${index}`, ...extra };
}

function page(snapshotId: string, entries: TranscriptOutlineEntry[], nextOffset: number, done: boolean): TranscriptOutlinePage {
  return { protocolVersion: 1, snapshotId, entries, nextOffset, done, total: entries.length, stale: false };
}

/** Answers from a scripted page table and records every request. */
function scripted(snapshotId: string, pages: Map<number, TranscriptOutlinePage>) {
  const requests: TranscriptOutlineRequest[] = [];
  const read: OutlineRead = async (_tabId, request) => {
    requests.push(request);
    const found = pages.get(request.offset ?? 0);
    if (!found) throw new Error(`unexpected outline offset ${request.offset}`);
    return found;
  };
  return { read, requests, snapshotId };
}

async function main() {
  {
    // Complete multi-page outline, assembled in offset order regardless of how
    // the host ordered the pages.
    const store = new TranscriptOutlineStore();
    store.register("tab", scripted("s1", new Map([
      [0, page("s1", [entry(1), entry(2)], 2, false)],
      [2, page("s1", [entry(3)], 3, true)],
    ])).read);
    await store.sync("tab", "s1");
    const view = store.getView("tab");
    assert.equal(view.mode, "ready", "multi-page outline is ready");
    assert.deepEqual(view.entries.map(item => item.id), ["m:1", "m:2", "m:3"], "every page is assembled in order");
  }

  {
    // Re-reading the same snapshot must not issue another request.
    const plan = scripted("s1", new Map([[0, page("s1", [entry(1)], 1, true)]]));
    const store = new TranscriptOutlineStore();
    store.register("tab", plan.read);
    await store.sync("tab", "s1");
    await store.sync("tab", "s1");
    assert.equal(plan.requests.length, 1, "an unchanged snapshot reuses the index");
    await store.sync("tab", "s2").catch(() => undefined);
    assert.equal(plan.requests.length, 2, "a replaced snapshot re-reads");
  }

  {
    // Duplicate identity would make the rail ambiguous.
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => page("s1", [entry(1), entry(1, { prompt: "duplicate" })], 2, true));
    await store.sync("tab", "s1");
    assert.deepEqual(store.getView("tab").entries.map(item => item.id), ["m:1"], "duplicate entries collapse to one mark");
  }

  {
    // A recycled cut must be reported, never silently answered from the newest
    // revision: the caller has to re-resolve the target against a fresh one.
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => ({ ...page("s1", [], 0, true), stale: true }));
    await store.sync("tab", "s1");
    const view = store.getView("tab");
    assert.equal(view.mode, "error", "a recycled cut is an error, not an empty outline");
    assert.equal(view.entries.length, 0);
  }

  {
    // A cursor that does not advance would page forever.
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => page("s1", [entry(1)], 0, false));
    await store.sync("tab", "s1");
    assert.equal(store.getView("tab").mode, "error", "a stalled cursor ends the read");
  }

  {
    // Unimplemented capability is compatibility, not failure.
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => { throw new OutlineUnsupported("missing"); });
    await store.sync("tab", "s1");
    const view = store.getView("tab");
    assert.equal(view.mode, "legacy", "an absent capability falls back to loaded turns");
    assert.equal(view.error, undefined, "an absent capability is not shown as a retryable error");
  }

  {
    // A real failure keeps its message so the rail can offer a retry.
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => { throw new Error("network down"); });
    await store.sync("tab", "s1");
    assert.equal(store.getView("tab").mode, "error");
    assert.equal(store.getView("tab").error, "network down");
  }

  {
    // Releasing a tab fences a read still in flight: its result must not
    // resurrect the outline of a session that is gone.
    let release!: () => void;
    const barrier = new Promise<void>(resolve => { release = resolve; });
    const store = new TranscriptOutlineStore();
    store.register("tab", async () => {
      await barrier;
      return page("s1", [entry(1)], 1, true);
    });
    const pending = store.sync("tab", "s1");
    store.release("tab");
    release();
    await pending;
    assert.equal(store.getView("tab").mode, "legacy", "a released tab keeps no outline from a late response");
  }

  {
    // Replacing a cut hides and fences the old outline without unbinding its
    // owner. A transient snapshot refresh failure must leave a second retry
    // able to call the same refresher and rebuild the index.
    const store = new TranscriptOutlineStore();
    let snapshotId = "s1";
    let refreshes = 0;
    store.register("tab", async (_tabId, request) => page(request.snapshotId, [entry(1)], 1, true), async () => {
      refreshes += 1;
      if (refreshes === 1) throw new Error("network down");
      snapshotId = "s2";
      await store.load("tab", snapshotId);
    });
    await store.sync("tab", snapshotId);
    store.invalidate("tab");
    assert.equal(store.getView("tab").mode, "legacy", "the replaced cut is hidden during refresh");
    await assert.rejects(store.refresh("tab"), /network down/);
    await store.refresh("tab");
    assert.equal(refreshes, 2, "the failed refresh did not discard the owner binding");
    assert.equal(store.getView("tab").snapshotId, "s2");
    assert.deepEqual(store.getView("tab").entries.map(item => item.id), ["m:1"]);
  }

  {
    // A hostile or buggy host that never finishes must not drive an unbounded
    // request-and-append loop. Each page advances the cursor by one and claims
    // there is more, so only the page cap can stop it.
    let requests = 0;
    const store = new TranscriptOutlineStore();
    store.register("tab", async (_tabId, request) => {
      requests += 1;
      const offset = request.offset ?? 0;
      return page("s1", [entry(offset)], offset + 1, false);
    });
    await store.sync("tab", "s1");
    const view = store.getView("tab");
    assert.ok(requests <= 64, `endless host issued ${requests} requests`);
    assert.equal(view.truncated, true, "an endless host is cut off");
    assert.equal(view.mode, "ready", "the turns that did arrive still navigate");
  }

  {
    // Running out of budget keeps what was indexed and says it is partial: a
    // huge conversation must still navigate instead of losing its rail.
    const store = new TranscriptOutlineStore();
    store.register("tab", async (_tabId, request) => {
      const offset = request.offset ?? 0;
      const entries = Array.from({ length: 1000 }, (_, index) => entry(offset + index));
      return page("s1", entries, offset + entries.length, false);
    });
    await store.sync("tab", "s1");
    const view = store.getView("tab");
    assert.equal(view.mode, "ready", "an oversized outline still navigates");
    assert.equal(view.truncated, true, "the view reports that it is partial");
    assert.ok(view.entries.length > 0 && view.entries.length <= 20_000, `kept ${view.entries.length} entries`);
  }

  {
    // An unregistered tab never claims the capability.
    const store = new TranscriptOutlineStore();
    await store.sync("unbound", "s1");
    assert.equal(store.getView("unbound").mode, "legacy", "a tab this host never loaded stays legacy");
  }

  console.log("transcript outline store: paging, dedup, stale, stall, compatibility and release fencing passed");
}

await main();
