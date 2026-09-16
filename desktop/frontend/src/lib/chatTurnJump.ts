import type { ChatMountedOrder } from "./chatMountedOrder";
import type { ChatScrollController } from "./chatScrollController";
import type { TranscriptOutlineEntry } from "./transcriptProtocol";

export type TurnJumpStatus = "idle" | "loading" | "failed";

/** Why a turn could not be reached. Kept distinct so the rail can say which. */
export type TurnJumpReason =
  /** History is exhausted (or the budget ran out) and the node never appeared. */
  | "turnUnavailable"
  /** The snapshot was recycled; the target has to be resolved against a fresh one. */
  | "snapshotExpired"
  /** The jump pulled as many pages as it is allowed to. */
  | "pageBudgetExhausted";

export interface TurnJumpState {
  /** Stable identity of the turn being located, or null when idle. */
  readonly turn: string | null;
  readonly status: TurnJumpStatus;
  readonly reason?: TurnJumpReason;
  /** The entry a failed jump can be retried against. */
  readonly retry?: TranscriptOutlineEntry;
}

const IDLE: TurnJumpState = Object.freeze({ turn: null, status: "idle" });

/** Frames to let the progressive mount advance before re-checking the target. */
const MOUNT_SETTLE_FRAMES = 120;
/** Wall-clock ceiling for the same wait; frames stop arriving when hidden. */
const MOUNT_SETTLE_MS = 2000;
/** How long to keep waiting for the target's node after history is exhausted. */
const DRAIN_MOUNT_MS = 5000;
/** Pages a single jump may pull before giving up. */
const MAX_JUMP_PAGES = 400;

export interface TurnJumpDeps {
  readonly mounts: ChatMountedOrder;
  readonly scroll: ChatScrollController;
  /** One older body page. `stale` means the snapshot it was paging is gone. */
  loadOlder: () => Promise<"loaded" | "empty" | "stale">;
  hasOlder: () => boolean;
  /** The DOM key of a turn once its node is mounted. */
  resolveKey: (entry: TranscriptOutlineEntry) => string | undefined;
  /** Identity of the snapshot this rail is describing; a change ends the jump. */
  currentSnapshotId: () => string;
  /** False once the session, tab, or snapshot this jump belongs to is gone. */
  isCurrent: () => boolean;
  /**
   * Ask the owning session to install a fresh snapshot. Only a recycled cut
   * needs it, and only a reader-initiated retry calls it, so navigation never
   * replaces the body on its own.
   */
  refreshSnapshot: (entry: TranscriptOutlineEntry) => Promise<TranscriptOutlineEntry | undefined>;
  /** Overrides the post-exhaustion wall-clock budget; tests shorten it. */
  drainMs?: number;
}

/**
 * Loads history until an unloaded turn's node is really mounted, then hands the
 * scroll write to the shared gateway. It never assumes "the data arrived" means
 * "the DOM exists": each page commits, the progressive mount advances, and the
 * target is re-resolved before the viewport moves.
 *
 * Every navigation click goes through this one entry point, so a newer target
 * always supersedes a pending one instead of racing it. Reader intent, an
 * explicit cancel, a newer target, or a session/snapshot replacement all end
 * the pending transaction; a page already in flight may finish, but it can
 * never take scroll control back.
 */
export class ChatTurnJump {
  private listeners = new Set<() => void>();
  private state: TurnJumpState = IDLE;
  /** Interaction id; a newer target or a cancel invalidates the pending loop. */
  private interaction = 0;
  private unsubscribeReader: (() => void) | undefined;

  constructor(private readonly deps: TurnJumpDeps) {}

  getSnapshot = (): TurnJumpState => this.state;
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  /** Reader intent observed on the transcript ends any pending jump. */
  private watchReader(): void {
    this.unsubscribeReader ??= this.deps.scroll.subscribeReaderIntent(() => { this.cancel(); });
  }

  private detach(): void {
    this.unsubscribeReader?.();
    this.unsubscribeReader = undefined;
  }

  cancel(): void {
    if (this.state.status === "idle") return;
    this.interaction++;
    this.detach();
    this.publish(IDLE);
  }

  dispose(): void {
    this.cancel();
    this.listeners.clear();
  }

  /**
   * Re-run the jump that last failed. A recycled cut cannot be retried against
   * itself, so the owning session installs a fresh snapshot first and the target
   * is then re-resolved from it by its stable identity — which is what makes
   * the retry able to succeed. The refresh is part of the transaction, so a
   * newer click or a cancel abandons it just like a pending page loop.
   */
  async retry(): Promise<void> {
    const entry = this.state.retry;
    const reason = this.state.reason;
    if (entry === undefined) return;
    const interaction = ++this.interaction;
    if (reason === "snapshotExpired") {
      this.detach();
      this.watchReader();
      this.publish({ turn: entry.id, status: "loading", retry: entry });
      let refreshed: TranscriptOutlineEntry | undefined;
      try {
        refreshed = await this.deps.refreshSnapshot(entry);
      } catch {
        if (this.interaction !== interaction || !this.deps.isCurrent()) {
          this.bail(interaction);
          return;
        }
        // Keep the same retryable failure. A transient refresh error must not
        // fall through into paging an absent cut or consume the retry target.
        this.fail(entry, interaction, "snapshotExpired");
        return;
      }
      if (this.interaction !== interaction || !this.deps.isCurrent()) {
        this.bail(interaction);
        return;
      }
      if (!refreshed) {
        this.fail(entry, interaction, "turnUnavailable");
        return;
      }
      await this.jump(refreshed);
      return;
    }
    await this.jump(entry);
  }

  /**
   * Scroll to a turn whose node is already mounted. It still goes through the
   * transaction so it supersedes a pending jump instead of racing it: the
   * reader's newest click must win, whatever is still paging behind it.
   */
  jumpTo(key: string): void {
    this.interaction++;
    this.detach();
    this.publish(IDLE);
    this.deps.scroll.stopFollowing();
    this.deps.scroll.jump(key);
  }

  async jump(entry: TranscriptOutlineEntry): Promise<void> {
    const interaction = ++this.interaction;
    const snapshotId = this.deps.currentSnapshotId();
    const current = () => this.interaction === interaction && this.deps.isCurrent() && this.deps.currentSnapshotId() === snapshotId;
    // Exit follow first: the reader asked for a specific turn, and a tail pin
    // would otherwise fight the write that lands later.
    this.deps.scroll.stopFollowing();
    this.detach();
    this.watchReader();
    this.publish({ turn: entry.id, status: "loading" });

    let pages = 0;
    try {
      for (;;) {
        if (!current()) { this.bail(interaction); return; }
        const mounted = this.deps.resolveKey(entry);
        if (mounted !== undefined) {
          if (!current()) return;
          this.deps.scroll.jump(mounted);
          this.finish(entry, interaction);
          return;
        }
        if (!this.deps.hasOlder()) {
          // History is exhausted, but the last page mounts progressively. The
          // data having covered the target is not the target being reachable
          // yet, so keep waiting for its node before declaring it missing.
          await this.drainTo(entry, interaction, current);
          return;
        }
        if (pages >= MAX_JUMP_PAGES) {
          // A budget running out is not the same as the turn not existing.
          this.fail(entry, interaction, "pageBudgetExhausted");
          return;
        }
        pages++;
        const before = this.deps.mounts.getSnapshot();
        const loaded = await this.deps.loadOlder();
        if (!current()) { this.bail(interaction); return; }
        if (loaded === "stale") {
          // The cut this jump resolved against was recycled. Replacing the body
          // is the reader's decision, not a side effect of navigation.
          this.fail(entry, interaction, "snapshotExpired");
          return;
        }
        if (loaded === "empty") {
          // A page the host could not fill while still claiming older history
          // is a dead end, not a recycled cut; only exhaustion earns the
          // progressive-mount wait.
          if (this.deps.hasOlder()) {
            this.fail(entry, interaction, "turnUnavailable");
            return;
          }
          await this.drainTo(entry, interaction, current);
          return;
        }
        await this.settleMounts(before);
        if (!current()) { this.bail(interaction); return; }
      }
    } catch (error) {
      this.fail(entry, interaction, error instanceof Error && error.message === "stale" ? "snapshotExpired" : "turnUnavailable");
    }
  }

  /**
   * Wait for the batched mount to advance, bounded so a page that adds no new
   * turn cannot stall the loop or spin the network.
   */
  private settleMounts(previous: readonly string[]): Promise<void> {
    // A page can already have advanced the mount while it was loading; the
    // published reference is what changes, so compare against it rather than
    // waiting for a publication that may never come.
    if (this.deps.mounts.getSnapshot() !== previous) return Promise.resolve();
    return new Promise((resolve) => {
      let elapsed = 0;
      let handle = 0;
      let settled = false;
      const finish = (): void => {
        if (settled) return;
        settled = true;
        if (handle) cancelAnimationFrame(handle);
        clearTimeout(timer);
        resolve();
      };
      const step = (): void => {
        if (settled) return;
        if (this.deps.mounts.getSnapshot() !== previous || elapsed >= MOUNT_SETTLE_FRAMES) { finish(); return; }
        elapsed++;
        handle = requestAnimationFrame(step);
      };
      // A hidden or occluded window stops delivering frames, so a frame count
      // alone can leave the mark pulsing until the window is shown again. Bound
      // the wait by wall clock as well and let the next attempt re-check.
      const timer = setTimeout(finish, MOUNT_SETTLE_MS);
      handle = requestAnimationFrame(step);
    });
  }

  /**
   * Wait out the progressive mount once history is exhausted: the target may
   * still be arriving on a later frame, so it is not missing yet. Ends the
   * transaction either way.
   */
  private async drainTo(entry: TranscriptOutlineEntry, interaction: number, current: () => boolean): Promise<void> {
    const mounted = await this.waitForMount(entry, current);
    if (!current()) { this.bail(interaction); return; }
    if (mounted !== undefined) {
      this.deps.scroll.jump(mounted);
      this.finish(entry, interaction);
      return;
    }
    this.fail(entry, interaction, "turnUnavailable");
  }

  /**
   * Keep polling for the target's node after history is exhausted, so a
   * progressively revealed last page is not mistaken for a missing turn.
   * Returns its key, or undefined when the wait budget expires.
   */
  private waitForMount(entry: TranscriptOutlineEntry, current: () => boolean): Promise<string | undefined> {
    const found = this.deps.resolveKey(entry);
    if (found !== undefined) return Promise.resolve(found);
    return new Promise((resolve) => {
      let handle = 0;
      let settled = false;
      const finish = (key: string | undefined): void => {
        if (settled) return;
        settled = true;
        if (handle) cancelAnimationFrame(handle);
        clearTimeout(timer);
        resolve(key);
      };
      const step = (): void => {
        if (settled) return;
        if (!current()) { finish(undefined); return; }
        const key = this.deps.resolveKey(entry);
        if (key !== undefined) { finish(key); return; }
        handle = requestAnimationFrame(step);
      };
      const timer = setTimeout(() => { finish(undefined); }, this.deps.drainMs ?? DRAIN_MOUNT_MS);
      handle = requestAnimationFrame(step);
    });
  }

  /**
   * Release the state a jump still owns after it stops early. A superseding
   * interaction owns the state itself, so only the current one may clear it —
   * otherwise a stale loop would wipe the mark a newer click just set.
   */
  private bail(interaction: number): void {
    if (this.interaction !== interaction) return;
    this.detach();
    this.publish(IDLE);
  }

  private finish(entry: TranscriptOutlineEntry, interaction: number): void {
    if (this.interaction !== interaction) return;
    this.detach();
    this.publish({ turn: entry.id, status: "idle" });
  }

  private fail(entry: TranscriptOutlineEntry, interaction: number, reason: TurnJumpReason): void {
    if (this.interaction !== interaction) return;
    this.detach();
    this.publish({ turn: entry.id, status: "failed", reason, retry: entry });
  }

  private publish(state: TurnJumpState): void {
    this.state = state;
    for (const listener of [...this.listeners]) listener();
  }
}
