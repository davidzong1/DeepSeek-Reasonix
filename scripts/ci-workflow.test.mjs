import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import vm from "node:vm";
import test from "node:test";
import { groups as windowsDesktopGroups, testArgs as windowsDesktopTestArgs } from "./desktop-windows-go-tests.mjs";

const workflow = name => readFileSync(new URL(`../.github/workflows/${name}.yml`, import.meta.url), "utf8");
function job(source, name) {
  const body = source.match(new RegExp(`\\n  ${name}:\\n([\\s\\S]*?)(?=\\n  [a-z][a-z0-9-]*:|$)`))?.[1];
  assert.ok(body, name);
  return body;
}
function condition(body, context) {
  const expression = body.match(/^    if: (.+)$/m)[1].replace(/^\$\{\{\s*|\s*\}\}$/g, "")
    .replace(/needs\.([a-z][a-z0-9-]*)/g, 'needs["$1"]');
  return vm.runInNewContext(expression, { always: () => true, cancelled: () => false, ...context });
}
function shellStep(body, name) {
  return body.split(`      - name: ${name}\n`)[1].match(/        run: \|\n((?:          .*\n|\n)+)/)[1]
    .replace(/^          /gm, "");
}
const ci = workflow("ci");
const release = workflow("release-desktop");
const appMemory = workflow("app-memory");

test("Windows full runs use the partitioned suite without a duplicate module sweep", () => {
  const body = job(ci, "test");
  const enabled = (name, os, event, run = "true") => {
    const step = body.split(`      - name: ${name}\n`)[1].split(/\n      - /)[0];
    const expression = step.match(/^        if: (.+)$/m)[1];
    return vm.runInNewContext(expression, {
      env: { RUN_STEPS: run }, runner: { os }, github: { event_name: event },
    });
  };
  for (const event of ["pull_request", "push", "workflow_dispatch"]) {
    for (const os of ["Linux", "macOS", "Windows"]) {
      assert.equal(enabled("test", os, event), os === "Linux" || (os === "macOS" && event !== "pull_request"));
      assert.equal(enabled("test (full)", os, event), os === "Windows" && event !== "pull_request");
      assert.equal(enabled("test (Windows smoke)", os, event), os === "Windows" && event === "pull_request");
      assert.equal(enabled("test", os, event, "false"), false);
      assert.equal(enabled("test (full)", os, event, "false"), false);
    }
  }
  assert.match(body, /run: node scripts\/windows-go-tests\.mjs full/);
  assert.match(job(ci, "windows-isolated"), /group: \[agent, boot\]/);
  assert.match(job(ci, "windows-control"), /run: node scripts\/windows-go-tests\.mjs control/);
});

test("App memory workflow tiers pull requests and keeps full scheduled coverage", t => {
  assert.match(appMemory, /schedule:\n    - cron: "17 3 \* \* \*"/);
  assert.match(appMemory, /\[ "\$EVENT_NAME" = workflow_dispatch \] \|\| \[ "\$EVENT_NAME" = schedule \]/);
  assert.match(appMemory, /matrix:\n        shard: \$\{\{ fromJSON\(needs\.changes\.outputs\.memory_shards\) \}\}/);
  assert.match(appMemory, /REASONIX_APP_MEMORY_PROFILE: \$\{\{ needs\.changes\.outputs\.memory_profile \}\}/);
  const script = shellStep(job(appMemory, "changes"), "Select memory profile");
  const root = mkdtempSync(path.join(os.tmpdir(), "reasonix-memory-workflow-"));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  let index = 0;
  const run = env => {
    const output = path.join(root, `output-${index++}`);
    const result = spawnSync("bash", ["-e", "-c", script], {
      env: { ...process.env, GITHUB_OUTPUT: output, GITHUB_STEP_SUMMARY: path.join(root, "summary"), ...env }, encoding: "utf8",
    });
    return { ...result, workflowOutput: result.status === 0 ? readFileSync(output, "utf8") : "" };
  };
  for (const [env, expected] of [
    [{ EVENT_NAME: "pull_request", MEMORY: "true", MEMORY_FULL: "false" }, "profile=short\nshards=[1]\n"],
    [{ EVENT_NAME: "pull_request", MEMORY: "true", MEMORY_FULL: "true" }, "profile=full\nshards=[1,2,3]\n"],
    [{ EVENT_NAME: "push", MEMORY: "true", MEMORY_FULL: "false" }, "profile=full\nshards=[1,2,3]\n"],
    [{ EVENT_NAME: "pull_request", MEMORY: "false", MEMORY_FULL: "false" }, "profile=off\nshards=[1]\n"],
  ]) {
    const result = run(env);
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.workflowOutput, expected);
  }
});

test("macOS signing diagnostics require protected main and cannot publish", () => {
  const source = workflow("macos-signing-check");
  const verify = job(source, "verify");
  const github = { repository: "esengine/DeepSeek-Reasonix", ref: "refs/heads/main-v2", ref_protected: true };
  assert.equal(condition(verify, { github }), true);
  for (const changed of [{ repository: "fork/Reasonix" }, { ref: "refs/tags/v1.0.0" }, { ref_protected: false }]) {
    assert.equal(condition(verify, { github: { ...github, ...changed } }), false);
  }
  assert.match(verify, /environment: release/);
  assert.match(verify, /ref: \$\{\{ github.sha \}\}/);
  assert.match(source, /permissions:\n  contents: read\n/);
  assert.doesNotMatch(source, /: write|secrets\.(R2_|SIGNPATH_|MINISIGN_|NPM_)/);
  assert.match(verify, /HAS_APPLE_CERT: "true"/);
  assert.match(verify, /scripts\/desktop-build.sh darwin\/universal v0.0.0-signing-check stable/);
  assert.match(verify, /path: \$\{\{ runner.temp \}\}\/apple-notarization\/\*\.json/);
  assert.match(verify, /if: always\(\)/);
});

test("required desktop aggregate rejects every failed, cancelled or unexpectedly skipped child", () => {
  const script = shellStep(job(ci, "desktop"), "Verify desktop validation jobs");
  const success = { CHANGES_RESULT: "success", PREPARE_REQUIRED: "true", NATIVE_REQUIRED: "true", FRONTEND_REQUIRED: "true", BROWSER_REQUIRED: "true",
    PACKAGE_REQUIRED: "true", PREPARE_RESULT: "success", GO_RESULT: "success", GO_RACE_RESULT: "success", FRONTEND_RESULT: "success", BROWSER_RESULT: "success",
    MACOS_RESULT: "success", WINDOWS_RESULT: "success", WINDOWS_GO_RESULT: "success", PACKAGE_RESULT: "success" };
  const run = env => spawnSync("bash", ["-e", "-c", script], { env: { ...process.env, ...env } }).status;
  assert.equal(run(success), 0);
  for (const key of ["PREPARE_RESULT", "GO_RESULT", "GO_RACE_RESULT", "FRONTEND_RESULT", "BROWSER_RESULT", "CHANGES_RESULT",
    "MACOS_RESULT", "WINDOWS_RESULT", "WINDOWS_GO_RESULT", "PACKAGE_RESULT"]) {
    for (const value of ["failure", "cancelled", "skipped", ""]) assert.notEqual(run({ ...success, [key]: value }), 0, `${key}=${value}`);
  }
  // A pull request that cannot affect the desktop module: every child skips
  // except the browser and Windows Go aggregates, which validate their groups.
  assert.equal(run({ ...success, PREPARE_REQUIRED: "false", NATIVE_REQUIRED: "false", FRONTEND_REQUIRED: "false", BROWSER_REQUIRED: "false",
    PACKAGE_REQUIRED: "false", PREPARE_RESULT: "skipped", GO_RESULT: "skipped", GO_RACE_RESULT: "skipped", FRONTEND_RESULT: "skipped",
    BROWSER_RESULT: "success", MACOS_RESULT: "skipped", WINDOWS_RESULT: "skipped", WINDOWS_GO_RESULT: "success", PACKAGE_RESULT: "skipped" }), 0);
  // Any pull request: packaging is push-only, so it must be skipped there.
  assert.equal(run({ ...success, PACKAGE_REQUIRED: "false", PACKAGE_RESULT: "skipped" }), 0);
  assert.notEqual(run({ ...success, PACKAGE_REQUIRED: "false", PACKAGE_RESULT: "success" }), 0);
  assert.equal(run({ ...success, FRONTEND_REQUIRED: "false", BROWSER_REQUIRED: "false", FRONTEND_RESULT: "skipped", BROWSER_RESULT: "success" }), 0);
  const browserScript = shellStep(job(ci, "desktop-browser"), "Verify desktop browser groups");
  const browser = spawnSync("bash", ["-e", "-c", browserScript], { env: { ...process.env,
    CHANGES_RESULT: "success", SHOULD_RUN: "false", PREPARE_RESULT: "skipped", GROUP_RESULT: "skipped" } });
  assert.equal(browser.status, 0, "an unneeded browser aggregate succeeds after validating skipped groups");
  assert.notEqual(run({ ...success, BROWSER_REQUIRED: "false", BROWSER_RESULT: "skipped" }), 0);
  assert.notEqual(run({ ...success, NATIVE_REQUIRED: "false", WINDOWS_GO_RESULT: "skipped" }), 0);
});

test("required lint aggregates code lint and the deduplicated frontend suite", () => {
  const body = job(ci, "lint");
  const script = shellStep(body, "Verify lint and frontend validation jobs");
  const success = { CHANGES_RESULT: "success", LINT_CODE_RESULT: "success", LINT_CODE_REQUIRED: "true",
    PREPARE_RESULT: "success", FRONTEND_RESULT: "success", FRONTEND_REQUIRED: "true" };
  const run = env => spawnSync("bash", ["-e", "-c", script], { env: { ...process.env, ...env } }).status;
  assert.equal(run(success), 0);
  for (const key of ["CHANGES_RESULT", "LINT_CODE_RESULT", "PREPARE_RESULT", "FRONTEND_RESULT"])
    for (const value of ["failure", "cancelled", "skipped", ""]) assert.notEqual(run({ ...success, [key]: value }), 0, `${key}=${value}`);
  assert.equal(run({ ...success, LINT_CODE_REQUIRED: "false", LINT_CODE_RESULT: "skipped",
    FRONTEND_REQUIRED: "false", PREPARE_RESULT: "skipped", FRONTEND_RESULT: "skipped" }), 0);
  assert.equal(run({ ...success, FRONTEND_REQUIRED: "false", PREPARE_RESULT: "success", FRONTEND_RESULT: "skipped" }), 0);
  assert.doesNotMatch(job(ci, "lint-code"), /test:motion/);
  assert.match(body, /needs: \[changes, lint-code, desktop-prepare, desktop-frontend\]/);
});

test("required root aggregate covers the jobs the per-OS test legs do not", () => {
  const body = job(ci, "root");
  const script = shellStep(body, "Verify root validation jobs");
  const success = { CHANGES_RESULT: "success", CODE_REQUIRED: "true", SITE_REQUIRED: "true", COVERAGE_REQUIRED: "true",
    CONTROL_RESULT: "success", ISOLATED_RESULT: "success", SDK_RESULT: "success", SITE_RESULT: "success", COVERAGE_RESULT: "success" };
  const run = env => spawnSync("bash", ["-e", "-c", script], { env: { ...process.env, ...env } }).status;
  assert.equal(run(success), 0);
  for (const key of ["CHANGES_RESULT", "CONTROL_RESULT", "ISOLATED_RESULT", "SDK_RESULT", "SITE_RESULT", "COVERAGE_RESULT"])
    for (const value of ["failure", "cancelled", "skipped", ""]) assert.notEqual(run({ ...success, [key]: value }), 0, `${key}=${value}`);
  // A pull request unrelated to code or site: the internally-gated jobs still
  // report success, the skippable ones must actually be skipped.
  assert.equal(run({ ...success, CODE_REQUIRED: "false", SITE_REQUIRED: "false", COVERAGE_REQUIRED: "false",
    ISOLATED_RESULT: "skipped", SITE_RESULT: "skipped", COVERAGE_RESULT: "skipped" }), 0);
  // Coverage is push-only; a pull request that ran it is a routing defect.
  assert.notEqual(run({ ...success, COVERAGE_REQUIRED: "false", COVERAGE_RESULT: "success" }), 0);
  assert.match(body, /needs: \[changes, windows-control, windows-isolated, sdk, site, coverage\]/);
  // govulncheck sets continue-on-error, so needs.*.result is success even when
  // it fails; aggregating it would be a tautology that reads like coverage.
  assert.match(job(ci, "govulncheck"), /continue-on-error: true/);
  assert.doesNotMatch(body, /GOVULN/);
});

// The gap that let a failing desktop-windows-go merge was a job nobody had
// wired into a required aggregate. Keep that unrepeatable: every job must be
// reachable from a required check, or be named here with a reason.
test("every ci job is reachable from a required aggregate", () => {
  const required = ["test", "race", "lint", "desktop", "root"];
  const advisory = {
    changes: "asserted by line one of every aggregate",
    "ci-metrics": "reports queue and stage timing; failure must not block merges",
    "prune-go-cache": "push-only cache housekeeping; cannot report on a pull request",
    govulncheck: "continue-on-error by design — stdlib advisories precede Go patch releases",
  };
  const jobs = ci.slice(ci.indexOf("\njobs:\n")); // `on:` also nests two-space keys
  const names = [...jobs.matchAll(/^ {2}([a-z][a-z0-9-]*):$/gm)].map(match => match[1]);
  assert.ok(names.length > 20, `expected the full job list, got ${names.length}`);
  // A bracketed list may wrap across lines, so consume up to its closing ].
  const edges = new Map(names.map(name => [name, (job(ci, name).match(/^ {4}needs:\s*(\[[^\]]*\]|\S.*)$/m)?.[1] ?? "")
    .replace(/[[\]]/g, "").split(",").map(entry => entry.trim()).filter(Boolean)]));
  const reachable = new Set(required);
  for (const name of required) for (const dependency of edges.get(name) ?? []) reachable.add(dependency);
  for (let size = 0; size !== reachable.size;) {
    size = reachable.size;
    for (const name of [...reachable]) for (const dependency of edges.get(name) ?? []) reachable.add(dependency);
  }
  for (const name of names) {
    if (reachable.has(name)) continue;
    assert.ok(advisory[name], `${name} is gated by no required check and is not declared advisory`);
  }
  for (const name of Object.keys(advisory))
    assert.ok(names.includes(name), `${name} is declared advisory but no longer exists`);
});

test("reuse skips only build work and still gates every publisher on validation", () => {
  const context = {
    inputs: { preflight_artifact_prefix: "desktop-123-1-preflight", orchestrated: true, signing_preflight_verified: true, signing_preflight: false, production_signing_smoke: false },
    needs: { resolve: { result: "success" }, "cache-guard": { result: "success" }, "signing-contract": { result: "success" }, "mac-universal-intel": { result: "skipped" }, "windows-sign": { result: "skipped" }, build: { result: "skipped" } },
  };
  assert.equal(condition(job(release, "build"), context), false);
  assert.equal(condition(job(release, "publish"), context), true);
  for (const key of ["resolve", "cache-guard", "signing-contract", "mac-universal-intel", "windows-sign", "build"]) {
    for (const result of ["failure", "cancelled"]) {
      const changed = structuredClone(context);
      changed.needs[key].result = result;
      assert.equal(condition(job(release, "publish"), changed), false, `${key}=${result}`);
    }
  }
  for (const key of ["orchestrated", "signing_preflight_verified"]) {
    assert.equal(condition(job(release, "publish"), { ...context, inputs: { ...context.inputs, [key]: false } }), false);
  }
  for (const key of ["signing_preflight", "production_signing_smoke"]) {
    assert.equal(condition(job(release, "publish"), { ...context, inputs: { ...context.inputs, [key]: true } }), false);
  }
  const fresh = structuredClone(context);
  fresh.inputs.preflight_artifact_prefix = "";
  assert.equal(condition(job(release, "build"), fresh), true);
  assert.equal(condition(job(release, "publish"), fresh), false);
  fresh.needs.build.result = "success";
  fresh.needs["mac-universal-intel"].result = "success";
  assert.equal(condition(job(release, "publish"), fresh), false, "unsigned Windows bundles cannot publish");
  fresh.needs["windows-sign"].result = "success";
  assert.equal(condition(job(release, "publish"), fresh), true);
});

test("Certum signing preserves native builds and gates publication and attestation", () => {
  const signer = job(release, "windows-sign");
  assert.match(job(release, "build"), /runner: windows-11-arm, platform: windows\/arm64/);
  assert.match(signer, /runs-on: windows-2022/);
  assert.match(signer, /arch: \[amd64, arm64\]/);
  assert.match(signer, /max-parallel: 1/);
  assert.match(signer, /ref: \$\{\{ github.workflow_sha \}\}/);
  assert.ok(signer.indexOf("-PayloadDirectory signed-payload") < signer.indexOf("scripts/package-windows-desktop.sh"));
  assert.ok(signer.indexOf("scripts/package-windows-desktop.sh") < signer.indexOf("-FilePath"));
  assert.ok(signer.indexOf("-ExpectedThumbprint") < signer.indexOf("Sign artifacts (minisign)"));
  assert.ok(!release.includes("secrets.SIGNPATH_API_TOKEN"));
  const attestation = job(release, "attest-signing-contract");
  const context = { github: { repository: "esengine/DeepSeek-Reasonix" }, inputs: { signing_preflight: true, orchestrated: false },
    needs: { "signing-contract": { result: "success" }, build: { result: "success" }, "windows-sign": { result: "success" } } };
  assert.equal(condition(attestation, context), true);
  for (const result of ["failure", "cancelled", "skipped"]) {
    assert.equal(condition(attestation, { ...context, needs: { ...context.needs, "windows-sign": { result } } }), false);
  }
});

test("reuse never moves artifact verification past public mutation or trusts candidate scripts", () => {
  const publisher = job(release, "publish");
  assert.ok(publisher.indexOf("Verify complete signed artifact handoff") < publisher.indexOf("name: Publish GitHub release"));
  assert.ok(publisher.includes("node release-control/scripts/desktop-release-artifacts.mjs collect"));
  assert.ok(publisher.includes("ref: ${{ github.workflow_sha }}"));
  assert.ok(!publisher.includes("merge-multiple: true"));
  const stable = workflow("release-stable");
  assert.ok(job(stable, "desktop").includes("preflight_artifact_prefix: ${{ needs.signpath-preflight.outputs.artifact_prefix }}"));
  for (const name of ["desktop", "cli", "npm"]) assert.ok(job(stable, name).includes("needs: [authorize, signpath-preflight]"));
});

test("all desktop consumers verify the prepared build and reject a failed preparation", () => {
  const context = { github: { event_name: "pull_request" },
    needs: { changes: { outputs: { desktop: "true" } }, "desktop-prepare": { result: "success" } } };
  const aggregate = job(ci, "desktop");
  const verifications = ci.match(/artifact-identity\.mjs verify/g)?.length ?? 0;
  assert.equal(ci.match(/--attempt "\$\{\{ needs\.desktop-prepare\.outputs\.producer_attempt \}\}"/g)?.length, verifications);
  assert.equal(ci.match(/test -n "\$\{\{ needs\.desktop-prepare\.outputs\.producer_attempt \}\}"/g)?.length, verifications);
  for (const [name, variant] of [
    ["desktop-go", "stable"], ["desktop-frontend", "stable"], ["desktop-browser-group", "stable"],
    ["desktop-macos", "stable"], ["desktop-windows", "canary"], ["desktop-windows-go-group", "stable"],
  ]) {
    const body = job(ci, name);
    if (["desktop-go", "desktop-frontend"].includes(name)) assert.ok(aggregate.includes(name));
    assert.ok(body.includes("needs: [changes, desktop-prepare]"));
    assert.ok(body.includes(`name: \${{ needs.desktop-prepare.outputs.${variant}_artifact_name }}`));
    assert.ok(body.includes(`--shell electron --channel ${variant}`));
    assert.ok(body.includes('test -n "${{ needs.desktop-prepare.outputs.producer_attempt }}"'));
    assert.ok(body.includes('--attempt "${{ needs.desktop-prepare.outputs.producer_attempt }}"'));
    assert.doesNotMatch(body, /artifact-identity\.mjs verify[^]*?--attempt "\$GITHUB_RUN_ATTEMPT"/);
    assert.ok(!body.includes("pnpm --dir frontend build"));
    assert.equal(condition(body, context), true);
    assert.equal(condition(body, { ...context, needs: { ...context.needs, "desktop-prepare": { result: "failure" } } }), false);
  }
  for (const name of ["desktop-windows", "desktop-windows-package"]) {
    const body = job(ci, name);
    assert.match(body, /REASONIX_PACKAGE_REUSE_FRONTEND: "1"/);
    assert.match(body, /REASONIX_FRONTEND_PNPM_VERSION="\$\(pnpm --version\)"\n\s+export REASONIX_FRONTEND_PNPM_VERSION/);
    assert.match(body, /canary_artifact_name/);
  }
  assert.match(job(ci, "desktop-macos"), /REASONIX_FRONTEND_PNPM_VERSION="\$\(pnpm --version\)"\n\s+export REASONIX_FRONTEND_PNPM_VERSION/);
  const prepare = job(ci, "desktop-prepare");
  assert.match(prepare, /producer_attempt: \$\{\{ steps\.artifact-identity\.outputs\.attempt \}\}/);
  assert.match(prepare, /id: artifact-identity\n\s+run: echo "attempt=\$GITHUB_RUN_ATTEMPT" >> "\$GITHUB_OUTPUT"/);
  assert.match(prepare, /stable_artifact_name: desktop-frontend-stable-\$\{\{ github\.run_id \}\}-\$\{\{ steps\.artifact-identity\.outputs\.attempt \}\}/);
});

test("browser matrix preserves five entry points and fails closed through desktop-browser", () => {
  const groups = job(ci, "desktop-browser-group");
  assert.match(groups, /max-parallel: 2/);
  assert.match(groups, /fail-fast: false/);
  assert.match(groups, /group: \[app-settings-motion, transcript\]/);
  assert.match(groups, /REASONIX_TRANSCRIPT_MODE=native-scrollbar REASONIX_LAYOUT_ARTIFACTS="\$evidence\/native-scrollbar"/);
  assert.match(groups, /REASONIX_TRANSCRIPT_MODE=headless-reader REASONIX_LAYOUT_ARTIFACTS="\$evidence\/headless-reader"/);
  assert.doesNotMatch(groups, /group: \[app-settings, motion, transcript\]/);
  for (const command of ["test:app-browser", "test:settings-browser", "test:motion-browser", "test:transcript-browser", "test:transcript-reader-browser"])
    assert.equal(ci.match(new RegExp(`pnpm --dir frontend ${command}(?:\\s|$)`, "g"))?.length, 1, command);
  const summary = job(ci, "desktop-browser");
  assert.match(summary, /needs: \[changes, desktop-prepare, desktop-browser-group\]/);
  const script = shellStep(summary, "Verify desktop browser groups");
  const run = env => spawnSync("bash", ["-e", "-c", script], { env: { ...process.env, ...env } }).status;
  assert.equal(run({ CHANGES_RESULT: "success", SHOULD_RUN: "true", PREPARE_RESULT: "success", GROUP_RESULT: "success" }), 0);
  for (const result of ["failure", "cancelled", "skipped", ""])
    assert.notEqual(run({ CHANGES_RESULT: "success", SHOULD_RUN: "true", PREPARE_RESULT: "success", GROUP_RESULT: result }), 0);
  assert.equal(run({ CHANGES_RESULT: "success", SHOULD_RUN: "false", PREPARE_RESULT: "success", GROUP_RESULT: "skipped" }), 0);
});

test("Windows desktop Go partitions tests without verbose JSON cache overhead", () => {
  const windowsGo = job(ci, "desktop-windows-go-group");
  const context = { github: { event_name: "pull_request" }, needs: {
    "desktop-prepare": { result: "success" }, changes: { outputs: { native: "true" } },
  } };
  assert.equal(condition(windowsGo, context), true);
  assert.equal(condition(windowsGo, { ...context, cancelled: () => true }), false,
    "superseded Windows workers must release the workflow concurrency slot");
  assert.match(windowsGo, /run: node \.\.\/scripts\/desktop-windows-go-tests\.mjs \$\{\{ matrix.group \}\}/);
  const commands = windowsDesktopGroups.map(group => {
    const args = windowsDesktopTestArgs(group);
    assert.equal(args[0], "test");
    assert.equal(args.at(-1), "./...");
    assert.ok(!args.some(arg => arg.startsWith("-timeout") || arg === "-json" || arg === "-v"));
    return { run: args.includes("-run") ? args[args.indexOf("-run") + 1] : undefined,
      skip: args.includes("-skip") ? args[args.indexOf("-skip") + 1] : undefined };
  });
  assert.equal(commands.length, 4);
  // Include non-test entry points and every possible first suffix character.
  // The complement group must retain names outside the three selected ranges.
  const names = ["Example", "ExampleSession", "FuzzSession", "Test"];
  for (let code = 0; code <= 127; code++) names.push(`Test${String.fromCharCode(code)}Session`);
  names.push("Test会话", "TestΩSession", "TestWindowsTerminalProcessConPTYSmoke");
  for (const name of names) {
    const owners = commands.filter(command =>
      (!command.run || new RegExp(command.run).test(name))
      && (!command.skip || !new RegExp(command.skip).test(name)));
    if (name === "TestWindowsTerminalProcessConPTYSmoke") {
      assert.equal(owners.length, 0, `${name} must be isolated from the correctness partition`);
      continue;
    }
    assert.equal(owners.length, 1, `${name} must run in exactly one group`);
  }
  assert.doesNotMatch(windowsGo, /go test -json/);
  assert.doesNotMatch(windowsGo, /go-test-timing/);
  assert.doesNotMatch(windowsGo, /go test -run ['"]?\^\$/);

  const groups = windowsGo.match(/group: \[([^\]]+)\]/)[1].split(",").map(value => value.trim());
  assert.deepEqual(groups, windowsDesktopGroups);
  assert.match(windowsGo, /fail-fast: false/);

  assert.match(windowsGo, /name: probe \(Windows ConPTY host integration\)[\s\S]*?continue-on-error: true[\s\S]*?run: go test -run '\^TestWindowsTerminalProcessConPTYSmoke\$' \./);
  assert.match(windowsGo, /name: probe \(Windows ConPTY host integration\)\n\s+if: matrix.group == 'Q-Z'/);
  assert.match(windowsGo, /name: test \(vendored systray identity\)\n\s+if: matrix.group == 'Q-Z'/);
  assert.match(windowsGo, /steps\.conpty-smoke\.outcome == 'failure'/);
});

test("Windows desktop Go aggregate rejects incomplete matrix results", () => {
  const summary = job(ci, "desktop-windows-go");
  assert.match(summary, /needs: \[changes, desktop-prepare, desktop-windows-go-group\]/);
  const script = shellStep(summary, "Verify Windows desktop Go groups");
  const success = { CHANGES_RESULT: "success", SHOULD_RUN: "true", PREPARE_RESULT: "success", GROUP_RESULT: "success" };
  const run = patch => spawnSync("bash", ["-e", "-c", script], { env: { ...process.env, ...success, ...patch } }).status;
  assert.equal(run({}), 0);
  for (const key of ["CHANGES_RESULT", "PREPARE_RESULT", "GROUP_RESULT"])
    for (const result of ["failure", "cancelled", "skipped", ""])
      assert.notEqual(run({ [key]: result }), 0, `${key}=${result}`);
  assert.equal(run({ SHOULD_RUN: "false", PREPARE_RESULT: "skipped", GROUP_RESULT: "skipped" }), 0);
  assert.notEqual(run({ SHOULD_RUN: "false" }), 0);
});
