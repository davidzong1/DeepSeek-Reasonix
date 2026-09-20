import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { beneath, internalRoots, listPackages, runGoTest } from "./go-test-groups.mjs";

// Start the long filesystem suites immediately on independent runners instead
// of leaving them behind hundreds of short packages in the residual queue.
export const isolatedGroups = ["agent", "boot", "control", "serve", "session", "worktree"];
const smokeRoots = internalRoots(
  "appidentity", "checkpoint", "cli", "desktoplauncher", "extension/sidecar",
  "filelock", "fileops", "fileutil", "hook", "instruction", "mcplaunch", "notify",
  // persistentshell drives a real ConPTY and a PowerShell wrapper that no other
  // platform exercises, so Windows is the only lane that can prove it.
  "persistentshell", "proc",
  "lsp", "pathidentity", "projectiondb", "remote", "repair", "sandbox", "sessioncatalog", "sqliteuri", "sysproxy",
  "topicstate", "winaclresidue", "workspacelease",
).concat("reasonix/cmd");

export function selectPackages(packages, group) {
  if (!["full", "smoke", ...isolatedGroups].includes(group)) {
    throw new Error(`Unknown Windows test group: ${group}`);
  }
  const isolated = pkg => isolatedGroups.some(name => beneath(pkg, `reasonix/internal/${name}`));
  return packages.filter(pkg => {
    if (isolatedGroups.includes(group)) return beneath(pkg, `reasonix/internal/${group}`);
    return !isolated(pkg) && (group === "full" || smokeRoots.some(root => beneath(pkg, root)));
  });
}

export function testArgs(packages, group) {
  const selected = selectPackages(packages, group);
  if (selected.length === 0) throw new Error(`Empty Windows test group: ${group}`);
  return ["test", "-p", isolatedGroups.includes(group) ? "1" : "4", "-timeout=8m", ...selected];
}

function main(group) {
  const { packages, status } = listPackages();
  if (!packages) return status;
  return runGoTest(`Windows ${group}`, selectPackages(packages, group), testArgs(packages, group));
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv[2]);
}
