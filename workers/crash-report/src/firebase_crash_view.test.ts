import { describe, expect, it } from "vitest";
import { firebaseMeta, firebaseSamples, type FirebaseGroupRow } from "./firebase_crash_view";

describe("Firebase diagnostic read conversion", () => {
  it("preserves structured attribution and event identity", () => {
    const diagnostics = {
      subjectVersion: "v1.38.7",
      subjectBuildCommit: "subject-build",
      subjectChannel: "stable",
      observerVersion: "v1.38.10",
      incidentId: "incident-1",
    };
    const reports = firebaseSamples({
      first: {
        eventId: "a".repeat(32),
        receivedAt: "2026-09-18T00:00:00Z",
        groupCount: 1,
        writerGeneration: 1,
        sampleEpoch: 1,
        version: "v1.38.10",
        buildCommit: "observer-build",
        channel: "stable",
        errorFamily: "react.maximum_update_depth",
        diagnostics,
      },
    });

    expect(reports).toHaveLength(1);
    expect(reports[0]).toMatchObject({
      version: "v1.38.7",
      build_commit: "subject-build",
      channel: "stable",
      error_family: "react.maximum_update_depth",
      event_id: "a".repeat(32),
      incident_id: "incident-1",
    });
    expect(JSON.parse(reports[0]!.diagnostics ?? "{}")).toEqual(diagnostics);
  });

  it("carries regression metadata into Firebase group snapshots", () => {
    const row = {
      fingerprint: "f".repeat(64),
      kind: "crash",
      count: 2,
      first_seen: "2026-09-17T00:00:00Z",
      last_seen: "2026-09-18T00:00:00Z",
      first_version: "v1.38.7",
      last_version: "v1.38.10",
      status: "resolved",
      title: "failure",
      source: "react",
      label: "",
      error_type: "Error",
      top_frame: "render",
      severity: "high",
      last_os: "windows",
      last_arch: "amd64",
      last_build_commit: "build",
      last_channel: "stable",
      regressed_at: "",
      regression_review: "suspected",
      resolution_platform: "windows",
      resolution_runtime: "electron",
      resolution_basis: "fixed by change 123",
      last_category: "crash",
    } satisfies FirebaseGroupRow;

    expect(firebaseMeta(row)).toMatchObject({
      regressionReview: "suspected",
      resolutionPlatform: "windows",
      resolutionRuntime: "electron",
      resolutionBasis: "fixed by change 123",
      lastCategory: "crash",
    });
  });
});
