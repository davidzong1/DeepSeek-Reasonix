import assert from "node:assert/strict";
import test from "node:test";
import { inspectRecord, requireNotRevoked, selectRecordArtifact, validateCandidateRun } from "./resolve-release-candidate.mjs";

const id = `v1.2.3-${"a".repeat(12)}-${"b".repeat(12)}`;
const artifact = { id: 22, name: `release-candidate-record-${id}`, expired: false, workflow_run: { id: 11 } };
const run = { id: 11, run_attempt: 2, repository: { full_name: "esengine/DeepSeek-Reasonix" }, path: ".github/workflows/release-candidate.yml", head_branch: "main-v2", head_sha: "c".repeat(40), event: "workflow_dispatch", status: "completed", conclusion: "success" };
const record = { candidateId: id, version: "1.2.3", sourceSHA: "a".repeat(40), control: { buildSHA: run.head_sha }, signing: { desktopFingerprint: "v1:example" }, validity: { createdAt: "2026-01-01T00:00:00Z", expiresAt: "2027-01-01T00:00:00Z", revoked: false }, source: { runId: "11", runAttempt: "2", desktopPrefix: "desktop-11-2-preflight", payloadArtifactId: "33", payloadArtifactName: `release-candidate-payload-${id}`, evidenceArtifactId: "34", evidenceArtifactName: `release-candidate-evidence-${id}` } };

test("selects the newest active exact-name record", () => {
  assert.equal(selectRecordArtifact([{ ...artifact, id: 20 }, artifact, { ...artifact, id: 30, expired: true }], id).id, 22);
  assert.equal(selectRecordArtifact([], id, false), null);
  assert.throws(() => selectRecordArtifact([], id), /not found/);
});

test("accepts a successful protected producer and exact record", () => {
  assert.doesNotThrow(() => validateCandidateRun(run, artifact, "esengine/DeepSeek-Reasonix"));
  const inspected = inspectRecord(record, id, artifact, run);
  assert.equal(inspected.payloadArtifactId, "33");
  assert.equal(inspected.evidenceArtifactId, "34");
});

test("rehearsal records cannot be selected or inspected for publication", () => {
  const rehearsalArtifact = { ...artifact, name: `release-candidate-rehearsal-record-${id}` };
  const rehearsalRecord = {
    ...record, purpose: "rehearsal",
    source: {
      ...record.source,
      payloadArtifactName: `release-candidate-rehearsal-payload-${id}`,
      evidenceArtifactName: `release-candidate-rehearsal-evidence-${id}`,
    },
  };
  assert.equal(selectRecordArtifact([rehearsalArtifact], id, false), null);
  assert.equal(selectRecordArtifact([rehearsalArtifact], id, true, "rehearsal"), rehearsalArtifact);
  assert.throws(() => inspectRecord(rehearsalRecord, id, rehearsalArtifact, run), /purpose mismatch/);
  assert.throws(() => inspectRecord(rehearsalRecord, id, artifact, run, new Date(), "rehearsal"), /artifact identity/);
  assert.equal(inspectRecord(rehearsalRecord, id, rehearsalArtifact, run, new Date(), "rehearsal").payloadArtifactId, "33");
});

test("accepts automatic preparation after the reviewed Notes PR merges", () => {
  assert.doesNotThrow(() => validateCandidateRun({ ...run, event: "push" }, artifact, "esengine/DeepSeek-Reasonix"));
});

for (const change of [
  { path: ".github/workflows/other.yml" }, { head_branch: "topic" }, { event: "pull_request" },
  { conclusion: "failure" }, { repository: { full_name: "fork/Reasonix" } },
]) {
  test(`rejects untrusted producer ${JSON.stringify(change)}`, () => {
    assert.throws(() => validateCandidateRun({ ...run, ...change }, artifact, "esengine/DeepSeek-Reasonix"));
  });
}

test("rejects a payload artifact substituted after sealing", () => {
  assert.throws(() => inspectRecord({ ...record, source: { ...record.source, payloadArtifactId: "" } }, id, artifact, run));
});

test("rejects expired or record-revoked candidates before payload reuse", () => {
  assert.throws(() => inspectRecord(record, id, artifact, run, new Date("2027-01-02T00:00:00Z")), /expired/);
  assert.throws(() => inspectRecord({ ...record, validity: { ...record.validity, revoked: true } }, id, artifact, run,
    new Date("2026-01-02T00:00:00Z")), /revoked/);
});

test("rejects a candidate on the repository revocation list", () => {
  assert.throws(() => requireNotRevoked(id, `v9.9.9-${"c".repeat(12)}-${"d".repeat(12)}, ${id}`), /is revoked/);
  assert.doesNotThrow(() => requireNotRevoked(id, ""));
});
