import assert from "node:assert/strict";
import { test } from "node:test";
import { waitForSmokeCondition } from "./smoke-poll.mjs";

test("async false does not complete a packaged RPC condition", async () => {
  let attempts = 0;
  await waitForSmokeCondition(async () => ++attempts === 3, { interval: 0 });
  assert.equal(attempts, 3);
});

test("a persistently false RPC result fails instead of advancing the smoke", async () => {
  await assert.rejects(waitForSmokeCondition(async () => false, { timeout: 0 }), /Timed out/);
});

test("RPC failures remain visible", async () => {
  await assert.rejects(waitForSmokeCondition(async () => { throw new Error("host unavailable"); }), /host unavailable/);
});
