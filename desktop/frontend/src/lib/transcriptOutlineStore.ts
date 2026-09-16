import { app } from "./bridge";
import type { TranscriptOutlineEntry, TranscriptOutlinePage, TranscriptOutlineRequest } from "./transcriptProtocol";

/** Reads one outline page. */
export type OutlineRead = (tabId: string, request: TranscriptOutlineRequest) => Promise<TranscriptOutlinePage>;

/**
 * The host does not implement the outline protocol. This is a compatibility
 * answer rather than a failure: it must not be presented as a retryable error,
 * and it must not be mistaken for "this conversation has no turns".
 */
export class OutlineUnsupported extends Error {}

export type TranscriptOutlineMode =
  /** No outline protocol: the rail shows loaded turns only and claims nothing. */
  | "legacy"
  /** Bound to a snapshot, pages still arriving. The rail keeps its area. */
  | "loading"
  | "ready"
  /** A real read failure. Known markers stay and a retry is offered. */
  | "error";

export interface TranscriptOutlineView {
  readonly mode: TranscriptOutlineMode;
  readonly snapshotId: string;
  readonly entries: readonly TranscriptOutlineEntry[];
  readonly error?: string;
  /** True when a read budget stopped the index short of the whole session. */
  readonly truncated?: boolean;
}

const EMPTY_ENTRIES: readonly TranscriptOutlineEntry[] = [];
const LEGACY: TranscriptOutlineView = Object.freeze({ mode: "legacy", snapshotId: "", entries: EMPTY_ENTRIES });

// A hostile or buggy host drives this loop, so it is bounded like every other
// server-driven read in this codebase (MAX_REPLAY_PAGES, MAX_JUMP_PAGES).
// 64 pages of 1000 entries is far beyond any real conversation.
const MAX_OUTLINE_PAGES = 64;
const MAX_OUTLINE_ENTRIES = 20_000;

/**
 * A budget running out keeps the turns already indexed and marks the view
 * truncated, so a huge conversation still navigates. Discarding the whole
 * index would trade a bounded cost for losing navigation entirely.
 */
const TRUNCATED = "outline is incomplete";

/**
 * The complete turn index of the installed snapshot, shared by the local
 * controller and remote sessions. Paging the body changes what is mounted, not
 * what exists, so this answers from one snapshot regardless of how much history
 * the reader has loaded.
 *
 * Reads are fenced by tab generation and by snapshot identity: a response from a
 * replaced session, a replaced snapshot, or a released tab can neither publish
 * nor clear state.
 */
export class TranscriptOutlineStore {
  private readonly views = new Map<string, TranscriptOutlineView>();
  private readonly listeners = new Map<string, Set<() => void>>();
  private readonly generations = new Map<string, number>();
  private readonly pending = new Map<string, Promise<void>>();
  private readonly readers = new Map<string, OutlineRead>();
  private readonly refreshers = new Map<string, () => Promise<void>>();

  /**
   * Bind a tab to the host that owns it. Local controllers and remote sessions
   * share this index, so the owning hook registers the reader for its own tabs
   * and a tab it never loaded stays in the legacy mode.
   */
  register(tabId: string, read: OutlineRead, refresh?: () => Promise<void>): void {
    this.readers.set(tabId, read);
    if (refresh) this.refreshers.set(tabId, refresh);
  }

  /**
   * Have the owning session install a fresh snapshot, whatever the current view
   * says. A navigation jump that hit a recycled cut needs this even when the
   * outline itself still reads as ready, so it is not gated on the view's mode.
   */
  async refresh(tabId: string): Promise<void> {
    const refresh = this.refreshers.get(tabId);
    if (!refresh) throw new Error("transcript snapshot refresh is unavailable");
    await refresh();
    // The cut notification starts outline synchronization without blocking the
    // controller commit. A reader retry, however, must wait until the fresh
    // identity is usable before it resolves its target again.
    const pending = this.pending.get(tabId);
    if (pending) await pending;
    const view = this.views.get(tabId);
    if (view?.mode === "error") throw new Error(view.error || "transcript outline refresh failed");
  }

  /**
   * A user-initiated retry after a recycled cut. A stale id cannot be read
   * again, so the owning session installs a fresh snapshot first and this index
   * re-aligns with it through the ordinary cut notification. The body is only
   * replaced by that explicit request, never as an automatic reaction.
   */
  retry(tabId: string): Promise<void> {
    const view = this.views.get(tabId);
    if (view?.error && this.refreshers.has(tabId)) return this.refresh(tabId);
    if (!view?.snapshotId) return Promise.resolve();
    return this.load(tabId, view.snapshotId);
  }

  subscribe(tabId: string, listener: () => void): () => void {
    let listeners = this.listeners.get(tabId);
    if (!listeners) { listeners = new Set(); this.listeners.set(tabId, listeners); }
    listeners.add(listener);
    return () => { listeners.delete(listener); if (!listeners.size) this.listeners.delete(tabId); };
  }

  getView(tabId: string): TranscriptOutlineView {
    return this.views.get(tabId) ?? LEGACY;
  }

  /** Resolve an entry again after refreshing its snapshot. Message identity
   * wins because an optimistic mounted key can differ from the durable record
   * id learned later in the same app session. */
  resolve(tabId: string, target: TranscriptOutlineEntry): TranscriptOutlineEntry | undefined {
    const entries = this.views.get(tabId)?.entries ?? EMPTY_ENTRIES;
    if (target.messageId) {
      const byMessage = entries.find(entry => entry.messageId === target.messageId);
      if (byMessage) return byMessage;
    }
    return entries.find(entry => entry.id === target.id);
  }

  /** Fence and hide a cut being replaced while preserving the owning host
   * binding. A failed refresh can therefore be retried instead of degrading
   * permanently to the legacy rail. */
  invalidate(tabId: string): void {
    this.generations.set(tabId, (this.generations.get(tabId) ?? 0) + 1);
    this.views.delete(tabId);
    this.pending.delete(tabId);
    this.publish(tabId);
  }

  /** Drop a tab's index and fence every read still in flight for it. */
  release(tabId: string): void {
    this.invalidate(tabId);
    this.readers.delete(tabId);
    this.refreshers.delete(tabId);
  }

  /**
   * Align the index with the installed snapshot. An unchanged snapshot reuses
   * the current index instead of re-reading it, and an absent host or an absent
   * snapshot stays in the legacy mode.
   */
  sync(tabId: string, snapshotId: string | undefined): Promise<void> {
    if (!snapshotId || !this.readers.has(tabId)) return Promise.resolve();
    const view = this.views.get(tabId);
    if (view && view.snapshotId === snapshotId && view.mode !== "error") return this.pending.get(tabId) ?? Promise.resolve();
    return this.load(tabId, snapshotId);
  }

  /** Retry after a failure, or after the capability was reported absent. */
  load(tabId: string, snapshotId: string): Promise<void> {
    const read = this.readers.get(tabId);
    if (!read) return Promise.resolve();
    const generation = (this.generations.get(tabId) ?? 0) + 1;
    this.generations.set(tabId, generation);
    this.set(tabId, { mode: "loading", snapshotId, entries: EMPTY_ENTRIES });
    const run = this.readAll(tabId, snapshotId, generation, read).finally(() => {
      if (this.pending.get(tabId) === run) this.pending.delete(tabId);
    });
    this.pending.set(tabId, run);
    return run;
  }

  private async readAll(tabId: string, snapshotId: string, generation: number, read: OutlineRead): Promise<void> {
    const current = () => this.generations.get(tabId) === generation;
    const entries: TranscriptOutlineEntry[] = [];
    const seen = new Set<string>();
    let truncated = false;
    try {
      let offset = 0;
      for (let pages = 0; ; pages++) {
        if (pages >= MAX_OUTLINE_PAGES) { truncated = true; break; }
        const page = await read(tabId, { snapshotId, offset });
        if (!current()) return;
        if (page.stale) {
          // The cut was recycled. Reporting it lets the caller install a fresh
          // snapshot and resolve the target again; silently continuing would
          // answer positions against a different revision.
          this.set(tabId, { mode: "error", snapshotId, entries: EMPTY_ENTRIES, error: "outline snapshot expired" });
          return;
        }
        if (page.snapshotId !== snapshotId) {
          this.set(tabId, { mode: "error", snapshotId, entries: EMPTY_ENTRIES, error: "outline snapshot changed" });
          return;
        }
        for (const entry of page.entries) {
          // Identity is unique per turn; a duplicate would make the rail
          // ambiguous and break keyed reconciliation.
          if (seen.has(entry.id)) continue;
          seen.add(entry.id);
          entries.push(entry);
          if (entries.length >= MAX_OUTLINE_ENTRIES) { truncated = true; break; }
        }
        if (truncated || page.done) break;
        if (!Number.isSafeInteger(page.nextOffset) || page.nextOffset <= offset) {
          this.set(tabId, { mode: "error", snapshotId, entries: EMPTY_ENTRIES, error: "outline cursor stalled" });
          return;
        }
        offset = page.nextOffset;
      }
      if (!current()) return;
      entries.sort((left, right) => left.order - right.order);
      this.set(tabId, { mode: "ready", snapshotId, entries, error: truncated ? TRUNCATED : undefined, truncated });
    } catch (error) {
      if (!current()) return;
      if (error instanceof OutlineUnsupported) {
        // Keep any markers already known, and never report this as a network
        // failure that the reader could retry.
        this.set(tabId, { mode: "legacy", snapshotId: "", entries: EMPTY_ENTRIES });
        return;
      }
      this.set(tabId, { mode: "error", snapshotId, entries: EMPTY_ENTRIES, error: message(error) });
    }
  }

  private set(tabId: string, view: TranscriptOutlineView): void {
    this.views.set(tabId, view);
    this.publish(tabId);
  }

  private publish(tabId: string): void {
    for (const listener of [...(this.listeners.get(tabId) ?? [])]) listener();
  }
}

function message(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

// Bridge-backed singleton, matching the transcript store: the owning session
// hook registers its own reader, and a tab it never loaded stays legacy.
let singleton: TranscriptOutlineStore | undefined;

export function getTranscriptOutlineStore(): TranscriptOutlineStore {
  singleton ??= new TranscriptOutlineStore();
  return singleton;
}

/** Local tabs read the controller binding; absent means the host is older. */
export function localOutlineRead(tabId: string, request: TranscriptOutlineRequest): Promise<TranscriptOutlinePage> {
  const read = app.TranscriptOutlineForTab;
  if (typeof read !== "function") return Promise.reject(new OutlineUnsupported("transcript outline is unavailable"));
  return read(tabId, request).catch((error: unknown) => { throw unavailable(error); });
}

/**
 * Remote tabs read the negotiated Serve route. The Go client already refuses
 * the request when the capability was not advertised, so an unavailable
 * projection is translated here rather than retried.
 */
export function remoteOutlineRead(tabId: string, request: TranscriptOutlineRequest): Promise<TranscriptOutlinePage> {
  const read = app.RemoteTranscriptOutlineForTab;
  if (typeof read !== "function") return Promise.reject(new OutlineUnsupported("remote transcript outline is unavailable"));
  return read(tabId, request).catch((error: unknown) => { throw unavailable(error); });
}

/**
 * The host answering "this controller has no outline projection" is a
 * compatibility answer, not a failure: both transports must degrade to the
 * loaded-turn rail rather than offer a retry that can never succeed.
 */
function unavailable(error: unknown): unknown {
  return message(error).toLowerCase().includes("transcript projection is unavailable")
    ? new OutlineUnsupported("transcript outline is unavailable")
    : error;
}
