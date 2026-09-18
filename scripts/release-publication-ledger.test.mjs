import assert from "node:assert/strict";
import test from "node:test";
import { createCoreLedger, createSiteLedger, mergeLedgers, npmPackageNames } from "./release-publication-ledger.mjs";

const sha = "a".repeat(40);
const release = { isDraft: false, isPrerelease: false, assets: [{ name: "asset.zip", size: 42, digest: "sha256:abc" }] };
const packages = latest => npmPackageNames.map(name => ({
  name,
  version: "1.2.3",
  reasonixCandidateSha: sha,
  gitHead: sha,
  integrity: "sha512-example",
  latest,
}));

test("records immutable files and exact current pointers", () => {
  const core = createCoreLedger({
    version: "1.2.3", sourceSHA: sha, operation: "publish",
    cliRelease: release, desktopRelease: release, npmPackages: packages("1.2.3"),
  });
  assert.equal(core.surfaces.tags.items.length, 3);
  assert.ok(core.surfaces.npm.packages.every(item => item.pointerState === "public-entry-updated"));
  assert.equal(core.surfaces.cli.assets[0].digest, "sha256:abc");
});

test("recovery preserves newer public pointers", () => {
  const core = createCoreLedger({
    version: "1.2.3", sourceSHA: sha, operation: "recover",
    cliRelease: release, desktopRelease: release, npmPackages: packages("1.3.0"),
  });
  assert.ok(core.surfaces.npm.packages.every(item => item.pointerState === "newer-entry-preserved"));
  assert.throws(() => createCoreLedger({
    version: "1.2.3", sourceSHA: sha, operation: "publish",
    cliRelease: release, desktopRelease: release, npmPackages: packages("1.3.0"),
  }), /npm latest is inconsistent/);
});

test("site evidence merges only for the same candidate", () => {
  const core = createCoreLedger({
    version: "1.2.3", sourceSHA: sha, operation: "publish",
    cliRelease: release, desktopRelease: release, npmPackages: packages("1.2.3"),
  });
  const site = createSiteLedger({
    version: "1.2.3", sourceSHA: sha, operation: "publish", manifest: { version: "v1.2.3" },
  });
  assert.equal(mergeLedgers(core, site).surfaces.homepage.state, "public-entry-updated");
  assert.throws(() => mergeLedgers(core, { ...site, sourceSHA: "b".repeat(40) }), /one release/);
});
