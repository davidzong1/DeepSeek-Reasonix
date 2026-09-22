import assert from "node:assert/strict";
import { dirname, resolve } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { firstExisting, iconCandidates } from "./icons.js";

const appPath = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

test("development Dock uses the macOS artwork and never a full-canvas fallback", () => {
  const icons = iconCandidates({ platform: "darwin", appPath, resourcesPath: "/unused", packaged: false });
  const macIcon = resolve(appPath, "../build/darwin/appicon.png");
  assert.deepEqual(icons.dock, [macIcon]);
  assert.equal(firstExisting(icons.dock), macIcon);
  assert.ok(!icons.window.includes(macIcon));
});

test("packaged macOS preserves the bundle icon and other platforms do not set a Dock icon", () => {
  for (const platform of ["darwin", "win32", "linux"] as const) {
    for (const packaged of [false, true]) {
      if (platform === "darwin" && !packaged) continue;
      const icons = iconCandidates({ platform, appPath, resourcesPath: "/unused", packaged });
      assert.equal(firstExisting(icons.dock), null, `${platform}, packaged=${packaged}`);
    }
  }
});
