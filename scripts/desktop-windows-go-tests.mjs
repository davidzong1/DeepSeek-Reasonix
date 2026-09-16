import { spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const groups = ["A-D", "E-H", "I-P", "Q-Z"];
export const conptyProbe = "TestWindowsTerminalProcessConPTYSmoke";
export const filters = {
  "A-D": { skip: "^Test[E-Z]" },
  "E-H": { run: "^Test[E-H]" },
  "I-P": { run: "^Test[I-P]" },
  "Q-Z": { run: "^Test[Q-Z]", skip: `^${conptyProbe}$` },
};

export function owners(name) {
  const assigned = groups.filter(group => {
    const { run, skip } = filters[group];
    return (!run || new RegExp(run).test(name)) && (!skip || !new RegExp(skip).test(name));
  });
  if (name === conptyProbe) assigned.push("conpty-probe");
  return assigned;
}

export function inventoryFromJSON(output) {
  const names = new Map();
  for (const line of output.split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line);
    const name = event.Output?.trim();
    if (event.Action === "output" && /^(Test|Example|Fuzz)\S*$/u.test(name ?? "")) {
      names.set(`${event.Package}/${name}`, name);
    }
  }
  if (!names.size) throw new Error("Go returned no desktop test inventory");
  return names;
}

export function verifyInventory(inventory, requireProbe = process.platform === "win32") {
  const counts = Object.fromEntries([...groups, "conpty-probe"].map(group => [group, 0]));
  for (const [key, name] of inventory) {
    const assigned = owners(name);
    if (assigned.length !== 1) throw new Error(`${key} belongs to ${assigned.length} test groups`);
    counts[assigned[0]]++;
  }
  for (const group of groups) {
    if (!counts[group]) throw new Error(`Empty desktop Windows test group: ${group}`);
  }
  if (requireProbe && counts["conpty-probe"] !== 1) throw new Error("Missing or duplicated native ConPTY probe");
  return counts;
}

export function testArgs(group) {
  if (!groups.includes(group)) throw new Error(`Unknown desktop Windows test group: ${group}`);
  const { run, skip } = filters[group];
  return ["test", ...(run ? ["-run", run] : []), ...(skip ? ["-skip", skip] : []), "./..."];
}

function main(group) {
  const args = group === "--verify" ? null : testArgs(group);
  // Ask Go for the current platform's inventory, including build-tagged tests,
  // examples and fuzz seeds. JSON is used only for listing, not test execution.
  const listed = spawnSync("go", ["test", "-list", ".", "-json", "./..."], { encoding: "utf8", maxBuffer: 16 << 20 });
  if (listed.error) throw listed.error;
  if (listed.status !== 0) {
    process.stderr.write(listed.stdout + listed.stderr);
    return listed.status ?? 1;
  }
  console.log("Desktop Windows test partition:", verifyInventory(inventoryFromJSON(listed.stdout)));
  if (!args) return 0;
  console.log(`go ${args.join(" ")}`);
  const result = spawnSync("go", args, { stdio: "inherit" });
  if (result.error) throw result.error;
  return result.status ?? 1;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exitCode = main(process.argv[2]);
}
