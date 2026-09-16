import assert from "node:assert/strict";
import { sessionIdentityKey } from "../app-runtime/sessionTarget";
import { sameSessionIdentity, sessionIdentityStableKey } from "../lib/sessionIdentity";

const a = { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "", sessionGeneration: 1 };
const b = { session: { hostId: "local", sessionId: "canonical-b" }, sessionPath: "", sessionGeneration: 1 };

assert.notEqual(sessionIdentityStableKey(a), sessionIdentityStableKey(b), "empty-path canonical sessions own different cache keys");
assert.equal(sameSessionIdentity(a, { ...a }), true, "the same SessionRef is stable across snapshots");
assert.equal(sameSessionIdentity(a, b), false, "SessionRef prevents canonical transcript cross-wiring");
assert.equal(
  sessionIdentityKey({ ...a, tabId: "reused-tab", scope: "project", workspaceRoot: "/repo", topicId: "legacy-topic" }),
  sessionIdentityStableKey(a),
  "canonical SessionRef outranks reused tab, topic and compatibility-path identities",
);

console.log("session identity: canonical SessionRef owns caches, navigation and hydration fences");
