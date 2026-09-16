import assert from "node:assert/strict";
import { startPreviewServer } from "./vite-preview-server.mjs";
import { selectSession } from "./app-page-actions.mjs";
import { fileURLToPath } from "node:url";
import path from "node:path";
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_SIDEBAR_BROWSER_PORT ?? 4679);
const preview = await startPreviewServer(root, port);
let browser;
try {
  browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, locale: "en-US" });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`http://127.0.0.1:${port}/?mock=bench&bench=1`);
  await page.locator(".project-tree__topic-main").first().waitFor();
  assert.equal(await page.locator(".app--workbench").count(), 1);
  assert.equal(await page.locator(".workspace-browser,.app--creation").count(), 0);
  await selectSession(page, "bench:small-6t");
  await page.waitForFunction(() => document.querySelector('.transcript')?.textContent?.includes('ASYNC LAYOUT EXPANSION COMPLETE'));
  assert.ok((await page.locator('.topicbar h1').textContent()).includes('bench:small-6t'));
  const geometry = await page.locator('.project-tree__topic-label').first().evaluate(el => ({ whiteSpace: getComputedStyle(el).whiteSpace, overflow: getComputedStyle(el).textOverflow }));
  assert.deepEqual(geometry, { whiteSpace: 'nowrap', overflow: 'ellipsis' });
  await page.getByRole('button', { name: 'Trash', exact: true }).click();
  await page.getByRole('button', { name: 'Archived', exact: true }).click();
  await page.locator('.archived-sessions').waitFor();
  await page.getByRole('button', { name: 'Back to workspace', exact: true }).click();
  await page.locator('.sidebar__utility-button').filter({ hasText: 'Settings' }).click();
  await page.locator('.settings-page--general').waitFor();
  assert.equal(await page.getByText('Desktop style', { exact: true }).count(), 0);
  assert.deepEqual(errors, []);
  console.log('PASS workbench-only ProjectTree, compact labels, session navigation, archived recovery and settings');
} finally {
  await browser?.close();
  await preview.close();
}
