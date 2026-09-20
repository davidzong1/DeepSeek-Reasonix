import assert from "node:assert/strict";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
process.env.PLAYWRIGHT_BROWSERS_PATH ??= path.join(root, ".pw-browsers");
const { chromium } = await import("playwright");
const port = Number(process.env.REASONIX_HISTORICAL_IMPORT_PORT ?? 4691);
const server = await createServer({ root, logLevel: "error", server: { host: "127.0.0.1", port, strictPort: true, hmr: false, watch: { ignored: ["**"] } } });
await server.listen();
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1100, height: 700 }, locale: "en-US" });
const errors = [];
page.on("pageerror", error => errors.push(error.message));
try {
  await page.goto(`http://127.0.0.1:${port}/bench/historical-import.html`);
  await page.getByRole("heading", { name: "Import historical sessions", exact: true }).waitFor();
  const rows = page.locator(".archived-sessions__row");
  assert.equal(await rows.count(), 2, "listing exposes both sources without importing");
  await rows.nth(0).getByRole("button", { name: "Import and open", exact: true }).click();
  await page.getByRole("alert").waitFor();
  assert.equal(await rows.nth(0).getByRole("button", { name: "Import and open", exact: true }).count(), 1, "busy source remains retryable");
  await rows.nth(0).getByRole("button", { name: "Import and open", exact: true }).click();
  await page.waitForTimeout(50);
  await page.getByRole("button", { name: "Import all", exact: true }).click();
  await page.getByRole("button", { name: "Pause after current item", exact: true }).click();
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await page.getByRole("button", { name: "Cancel import", exact: true }).click();
  assert.deepEqual(errors, []);
  await page.screenshot({ path: process.env.REASONIX_HISTORICAL_IMPORT_EVIDENCE ?? path.join(root, "..", "..", "artifacts", "historical-import-browser.png") });
  console.log("PASS historical import browser: listing, blocked retry, commit/open and batch controls");
} finally { await browser.close(); await server.close(); }
