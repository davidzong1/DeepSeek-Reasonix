import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { conptyProbe, groups, inventoryFromJSON, owners, testArgs, verifyInventory } from "./desktop-windows-go-tests.mjs";

test("every test prefix, example and fuzz seed has exactly one execution owner", () => {
  const names = [..."ABCDEFGHIJKLMNOPQRSTUVWXYZ"].map(letter => `Test${letter}Feature`);
  names.push("Test", "Test_Compatibility", "Test中文", "Example", "ExampleController_Open", "FuzzSession", conptyProbe);
  for (const name of names) assert.equal(owners(name).length, 1, name);
  assert.deepEqual(owners("ExampleController_Open"), ["A-D"]);
  assert.deepEqual(owners("FuzzSession"), ["A-D"]);
  assert.deepEqual(owners(conptyProbe), ["conpty-probe"]);
  const counts = verifyInventory(new Map(names.map(name => [name, name])));
  assert.equal(Object.values(counts).reduce((sum, count) => sum + count, 0), names.length);
  assert.throws(() => testArgs("missing"), /Unknown/);
  assert.throws(() => verifyInventory(new Map()), /Empty/);
});

test("inventory keeps same-named tests from different packages and rejects missing output", () => {
  const output = ["pkg/a", "pkg/b"].map(Package => JSON.stringify({ Action: "output", Package, Output: "TestActive\n" })).join("\n");
  assert.equal(inventoryFromJSON(output).size, 2);
  assert.throws(() => inventoryFromJSON(""), /no desktop test inventory/);
});

test("CI runs all groups separately and retains the aggregate and native probe", () => {
  const source = readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");
  const matrixJob = source.match(/\n  desktop-windows-go-group:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.ok(matrixJob);
  const matrix = matrixJob.match(/group: \[([^\]]+)\]/)[1].split(",").map(value => value.trim());
  assert.deepEqual(matrix, groups);
  assert.match(matrixJob, /fail-fast: false/);
  assert.match(matrixJob, /run: node \.\.\/scripts\/desktop-windows-go-tests\.mjs \$\{\{ matrix.group \}\}/);
  assert.match(matrixJob, /run: go test -run '\^TestWindowsTerminalProcessConPTYSmoke\$' \./);
  const aggregate = source.match(/\n  desktop-windows-go:\n([\s\S]*?)(?=\n  [a-z][a-z0-9-]*:|$)/)?.[1];
  assert.match(aggregate, /needs: \[changes, desktop-prepare, desktop-windows-go-group\]/);
  assert.match(aggregate, /test "\$GROUP_RESULT" = success/);
  assert.match(source, /node --test scripts\/desktop-windows-go-tests.test.mjs/);
});
