import assert from "node:assert/strict";
import { initialState, reducer } from "../lib/useController";
import { entryToRecord, convertRecord } from "../lib/transcriptRecordProjection";
import { toolPresentation, type ToolItem } from "../lib/chatToolPresentation";

const record = entryToRecord({ entryId: "m:backend-user", turn: 1, order: 0, refs: [], message: {
  role: "user", content: "same input", messageId: "backend-user", submissionId: "submit-1",
} });
const converted = convertRecord(record, { records: [record], indexOf: new Map([[record.entryId, 0]]), toolResultOwners: new Map() }, new Set());
const projection = { items: converted.items, removeIds: [], startTurn: 0, endTurn: 1, totalTurns: 1,
  hasOlder: false, hasNewer: false, revision: 1, revisionKnown: true, digest: "cut" };
for (const eventFirst of [true, false]) {
  let state = reducer({ ...initialState, transcriptProtocol: 2 }, { type: "user", seq: 0, text: "same input", submissionId: "submit-1" });
  const event = { type: "event" as const, e: { kind: "user_message" as const, source: "executor" as const, messageId: "backend-user", submissionId: "submit-1", text: "same input" } };
  if (eventFirst) state = reducer(state, event);
  state = reducer(state, { type: "transcript_records", projection });
  if (!eventFirst) state = reducer(state, event);
  state = reducer(state, { type: "transcript_records", projection });
  const users = state.items.filter(item => item.kind === "user");
  assert.equal(users.length, 1);
  assert.equal(users[0].id, "u0");
  assert.equal(users[0].messageId, "backend-user");
  state = reducer(state, { type: "user", seq: 1, text: "same input", submissionId: "submit-2" });
  assert.equal(state.items.filter(item => item.kind === "user").length, 2);
}
const shell: ToolItem = { kind: "tool", id: "call", name: "bash", args: "{}", status: "done", readOnly: false };
assert.equal(toolPresentation(shell).label, "chat.unknown");
assert.equal(toolPresentation({ ...shell, status: "stopped" }).dot, "warning");
assert.equal(toolPresentation({ ...shell, execution: { kind: "shell", supportsAndAnd: true, state: "timed_out", exitCode: 0 } }).label, "chat.timedOut");
assert.equal(toolPresentation({ ...shell, execution: { kind: "shell", supportsAndAnd: true, state: "completed", exitCode: 0 } }).dot, "done");
console.log("submission identity ordering and authoritative tool states passed");
