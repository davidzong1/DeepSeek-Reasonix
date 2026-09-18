import { DEVELOPMENT_FINGERPRINT_PREFIX } from "./diagnostics_v2";
import type { ReportPayload } from "./report_schema";

const GROUP_PATH_RE = /^\/stats\/group\/((?:dev:)?[0-9a-f]{64})$/;
const RESIZE_OBSERVER_NOTICE_RE = /^ResizeObserver loop (?:limit exceeded|completed with undelivered notifications\.?)$/;

export type SeverityInput = {
  kind: string;
  version?: string;
  source: string;
  label: string;
  errorType: string;
  errorMessage: string;
  topFrame: string;
  channel?: string;
  recovery?: string;
};

type ParsedVersion = {
  version: string;
  major: number;
  minor: number;
  patch: number;
};

export type RegressionDecision = "none" | "historical" | "suspected" | "confirmed";

// One-line human summary for the dashboard list. Frontend reports are formatted
// "[label]\n\n<detail>", so a bare label alone is folded together with its detail.
export function crashTitle(message: string): string {
  const lines = message
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
  let head = lines[0] ?? "";
  if (/^\[[^\]]+\]$/.test(head) && lines[1]) head = `${head} ${lines[1]}`;
  return head.slice(0, 200);
}

export function isDevelopmentReport(input: SeverityInput): boolean {
  const channel = input.channel?.trim().toLowerCase();
  return channel === "dev" || channel === "test" || input.version?.trim().toLowerCase().startsWith("dev") === true;
}

export function reportSubjectIdentity(report: Pick<ReportPayload, "version" | "channel" | "diagnostics">): {
  version: string;
  channel: string;
} {
  return {
    version: report.diagnostics?.subjectVersion || report.version,
    channel: report.diagnostics?.subjectChannel || report.channel || "",
  };
}

export function namespaceReportFingerprint(hash: string, development: boolean): string {
  return development ? `${DEVELOPMENT_FINGERPRINT_PREFIX}${hash}` : hash;
}

export function groupFingerprintFromPath(path: string): string | null {
  return path.match(GROUP_PATH_RE)?.[1] ?? null;
}

export function isKnownNonCrashDiagnostic(input: SeverityInput): boolean {
  const message = input.errorMessage.trim();
  return (
    RESIZE_OBSERVER_NOTICE_RE.test(message) ||
    /Minified React error #520\b/.test(message) ||
    message.includes("additional File object is not a file on the disk")
  );
}

export function isOpaqueScriptErrorReport(input: SeverityInput): boolean {
  return (
    input.kind === "crash" &&
    input.source === "frontend.global" &&
    input.label === "window.error" &&
    input.errorType === "string" &&
    input.errorMessage.trim() === "Script error." &&
    input.topFrame.trim() === ""
  );
}

function severityForKind(kind: string): string {
  if (kind === "crash") return "high";
  if (kind === "performance" || kind === "bot" || kind === "exception") return "medium";
  return "low";
}

export function severityForReport(input: SeverityInput): string {
  if (isDevelopmentReport(input) || isOpaqueScriptErrorReport(input) || isKnownNonCrashDiagnostic(input)) return "low";
  if ((input.source === "web.runtime.native" || input.source === "webview2.process.native") && input.recovery === "reload_succeeded") return "low";
  if ((input.source === "web.runtime.native" || input.source === "webview2.process.native") && input.kind === "exception") return "high";
  return severityForKind(input.kind);
}

export function severityRank(severity: string): number {
  return ({ low: 1, medium: 2, high: 3, critical: 4 })[severity] ?? 0;
}

export function maxSeverity(current: string, incoming: string): string {
  return severityRank(incoming) > severityRank(current) ? incoming : current;
}

function parseReleaseVersion(version: string): ParsedVersion | null {
  // The latest lane is restricted to shipped stable builds. Development,
  // prerelease, and build-metadata values remain visible only in facets.
  const match = version.trim().match(/^v?(\d+)\.(\d+)\.(\d+)$/);
  if (!match) return null;
  return {
    version,
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
  };
}

export function compareReleaseVersions(subject: string, fixedIn: string): number | null {
  const subjectVersion = parseReleaseVersion(subject);
  const fixedVersion = parseReleaseVersion(fixedIn);
  if (!subjectVersion || !fixedVersion) return null;
  return subjectVersion.major - fixedVersion.major || subjectVersion.minor - fixedVersion.minor || subjectVersion.patch - fixedVersion.patch;
}

export function regressionDecisionForReport(input: {
  status?: string;
  fixedIn?: string;
  resolutionPlatform?: string;
  resolutionRuntime?: string;
  subjectVersion: string;
  os: string;
  runtime: string;
}): RegressionDecision {
  if (input.status !== "resolved") return "none";
  if (input.resolutionPlatform && input.resolutionPlatform !== input.os) return "none";
  if (input.resolutionRuntime && input.resolutionRuntime !== input.runtime) return "none";

  const comparison = compareReleaseVersions(input.subjectVersion, input.fixedIn ?? "");
  if (comparison === null) return "suspected";
  return comparison < 0 ? "historical" : "confirmed";
}

export function newestReleaseVersion(versions: string[]): string {
  const parsed = versions
    .filter((version) => version && version.toLowerCase() !== "dev")
    .map(parseReleaseVersion)
    .filter((version): version is ParsedVersion => version !== null);
  parsed.sort(
    (left, right) =>
      right.major - left.major ||
      right.minor - left.minor ||
      right.patch - left.patch ||
      right.version.localeCompare(left.version),
  );
  return parsed[0]?.version ?? "";
}
