import assert from "node:assert/strict";
import { test } from "node:test";
import { QuitSequencer } from "./lifecycle.js";

const silent = { info() {}, warn() {}, error() {} };
const tick = () => new Promise((resolve) => setImmediate(resolve));

function fakeApp(sequencer: () => QuitSequencer) {
  const calls: string[] = [];
  // Mirrors Electron: app.quit() re-enters before-quit until the sequencer lets it pass.
  const app = {
    quit: () => {
      calls.push("quit");
      if (sequencer().onBeforeQuit()) calls.push("exit");
    },
    relaunch: (args: string[], execPath?: string) => calls.push(`relaunch:${args.join(",")}${execPath ? `@${execPath}` : ""}`),
  };
  return { app, calls };
}

function build(options: { prevent?: boolean; beforeCloseError?: Error; flushError?: Error; } = {},) {
  const log: string[] = [];
  let sequencer!: QuitSequencer;
  const { app, calls } = fakeApp(() => sequencer);
  const service = {
    beforeClose: async (reason: string) => {
      log.push(`beforeClose:${reason}`);
      if (options.beforeCloseError) throw options.beforeCloseError;
      return options.prevent ?? false;
    },
    shutdown: async () => {
      log.push("shutdown");
    },
  };
  sequencer = new QuitSequencer({
    service,
    app,
    flushRenderer: async () => {
      log.push("flush");
      if (options.flushError) throw options.flushError;
    },
    resumeRenderer: async () => { log.push("resume"); },
    onCloseAllowed: () => log.push("closeAllowed"),
    log: silent,
  });
  return { sequencer, app, calls, log };
}

test("a plain quit asks Go, shuts the service down once, then exits", async () => {
  const { sequencer, app, calls, log } = build();
  app.quit();
  assert.equal(sequencer.currentPhase, "preparing");
  await tick();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "quit", "quit", "exit"]);
  assert.equal(sequencer.currentPhase, "completed");
});

test("Go can veto the quit; the shell stays running", async () => {
  const { sequencer, app, calls, log } = build({ prevent: true });
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume", "flush", "beforeClose:quit", "resume"], "a later quit asks again");
});

test("re-entrant quits while asking do not ask twice", async () => {
  const { app, calls, log } = build({ prevent: true });
  app.quit();
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "resume"]);
  assert.deepEqual(calls, ["quit", "quit"]);
});

test("host/app.quit approval skips beforeClose and goes straight to shutdown", async () => {
  const { sequencer, calls, log } = build({ prevent: true });
  sequencer.approve();
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "quit", "exit"]);
});

test("a failed beforeClose does not trap the user in a shell that cannot quit", async () => {
  const { app, calls, log } = build({ beforeCloseError: new Error("service dead"), });
  app.quit();
  await tick();
  await tick();
  assert.deepEqual(log, ["flush", "beforeClose:quit", "flush", "shutdown", "closeAllowed"]);
  assert.equal(calls[calls.length - 1], "exit");
});

test("a failed renderer flush cancels quit before Go is asked", async () => {
  const { sequencer, app, calls, log } = build({ flushError: new Error("draft conflict"), });
  app.quit();
  await tick();
  assert.deepEqual(log, ["flush"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  assert.equal(sequencer.isQuitting, false);
});

test("direct approval also withholds shutdown when renderer flush fails", async () => {
  const { sequencer, calls, log } = build({ flushError: new Error("draft conflict"), });
  sequencer.approve();
  await tick();
  assert.deepEqual(log, ["flush"]);
  assert.deepEqual(calls, ["quit"]);
  assert.equal(sequencer.currentPhase, "idle");
  assert.equal(sequencer.isQuitting, false);
});

test("relaunch runs the shutdown and re-spawns with the requested args", async () => {
  const { sequencer, calls, log } = build();
  sequencer.relaunch(["--after-update"]);
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "relaunch:--after-update", "quit", "exit"]);
});

test("relaunch waits for shutdown then starts the committed stable launcher", async () => {
  const { sequencer, calls, log } = build();
  sequencer.relaunch(["--after-update"], "/opt/reasonix/reasonix-launcher");
  assert.deepEqual(calls, ["quit"]);
  await tick();
  assert.deepEqual(log, ["flush", "shutdown", "closeAllowed"]);
  assert.deepEqual(calls, ["quit", "relaunch:--after-update@/opt/reasonix/reasonix-launcher", "quit", "exit"]);
});

test("the first exit trigger keeps ownership of the shutdown reason", async () => {
  const reasons: string[] = [];
  let q!: QuitSequencer;
  const { app } = fakeApp(() => q);
  q = new QuitSequencer({
    service: {
      beforeClose: async () => false,
      shutdown: async (reason) => { reasons.push(reason ?? ""); },
    },
    app,
    flushRenderer: async () => {},
    onCloseAllowed() {},
    log: silent,
  });

  q.relaunch(["--after-update"]);
  q.requestQuit("system_signal");
  await tick();
  await tick();

  assert.deepEqual(reasons, ["update_restart"]);
});

test("cleanup failure cannot skip later cleanup or the final quit deadline", async () => {
  const events: string[] = [];
  let deadline: (() => void) | undefined;
  let q!: QuitSequencer;
  q = new QuitSequencer({
    service: { beforeClose: async () => false, shutdown: async () => { events.push("stopped"); }, },
    app: { quit: () => { if (q.onBeforeQuit()) events.push("quit"); }, exit: () => events.push("forced"), relaunch() {}, },
    onCloseAllowed: () => { throw new Error("window cleanup"); },
    cleanup: [{ name: "tray", run: () => { events.push("tray"); }, },],
    schedule: (run, ms) => { assert.equal(ms, 5000); deadline = run; }, log: silent,
  });
  q.approve(); await tick();
  assert.deepEqual(events, ["stopped", "tray", "quit"]);
  deadline?.(); assert.equal(events.at(-1), "forced");
});

test("service termination failure withholds shell exit and permits an exit retry", async () => {
  let attempts = 0;
  let exits = 0;
  let resumes = 0;
  let q!: QuitSequencer;
  q = new QuitSequencer({
    service: { beforeClose: async () => false, shutdown: async () => { if (++attempts === 1) throw new Error("child alive"); }, },
    app: { quit: () => { if (q.onBeforeQuit()) exits++; }, relaunch() {}, },
    resumeRenderer: async () => { resumes++; },
    onCloseAllowed() {}, log: silent,
  });
  q.approve(); await tick();
  assert.equal(exits, 0); assert.equal(q.isQuitting, true); assert.equal(q.currentPhase, "failed");
  assert.equal(resumes, 0);
  q.approve();
  await tick();
  assert.equal(exits, 1);
});

test("shutdown failure prompt can retry without restoring renderer editing", async () => {
  let attempts = 0;
  let prompts = 0;
  let exits = 0;
  let
  q!: QuitSequencer;
  q = new QuitSequencer({
    service: {
      beforeClose: async () => false,
      shutdown: async () => {
        if (++attempts === 1) throw new Error("disk full");
      },
    },
    app: {
      quit: () => {
        if (q.onBeforeQuit()) exits++;
      },
      relaunch() {},
    },
    onShutdownFailed: async (message) => {
      prompts++;
      assert.match(message, /disk full/);
      return true;
    },
    onCloseAllowed() {},
    log: silent,
  });
  q.approve(); await tick();
  await tick();
  await tick(); assert.equal(attempts, 2);
  assert.equal(prompts, 1);
  assert.equal(exits, 1);
  assert.equal(q.currentPhase, "completed");
});
