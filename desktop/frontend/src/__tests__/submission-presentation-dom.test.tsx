import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import type { Item } from "../lib/useController";

const harness = await createTranscriptHarness({ deterministic: true });
const pending: Item = { kind: "user", id: "optimistic", submissionId: "send", text: "unique question" };
const confirmed: Item = { ...pending, id: "m:durable", messageId: "durable" };
try {
  await harness.loadModule("/src/components/ChatToolBody.tsx");
  await harness.render([pending], { tabId: "identity-dom" });
  await harness.settle();
  const selector = '[data-chat-kind="user"]';
  const before = harness.container.querySelector(selector);
  await harness.render([pending, confirmed], { tabId: "identity-dom" });
  await harness.settle();
  assert.equal(harness.container.textContent?.split("unique question").length, 2, "one bubble even when both inputs coexist");
  assert.ok(before);
  assert.equal(harness.container.querySelector(selector), before, "mounted user identity remains stable");
  await harness.render([pending, confirmed, { ...pending, id: "another", submissionId: "send-again" }], { tabId: "identity-dom" });
  await harness.settle();
  assert.equal(harness.container.textContent?.split("unique question").length, 3, "same text sent twice remains visible twice");
  for (const state of ["timed_out", "cancelled", "completed"] as const) {
    const shell: Item = { kind: "tool", id: state, name: "bash", args: '{"command":"echo test"}', output: "test", status: "done",
      execution: { kind: "shell", supportsAndAnd: true, state, exitCode: 0 } };
    await harness.render([pending, shell], { tabId: "identity-dom", geometrySessionKey: state });
    await harness.settle();
    const row = harness.container.querySelector<HTMLElement>(".chat-tool")!;
    const dot = row.querySelector(".dsh-StateDot-dot")?.getAttribute("data-state");
    assert.equal(dot, state === "completed" ? "done" : state === "cancelled" ? "warning" : "error");
    await act(async () => row.querySelector<HTMLElement>("[data-disclosure-row]")!.click());
    await harness.settle();
    assert.equal(row.dataset.state, state === "completed" ? "done" : state === "cancelled" ? "stopped" : "error");
    assert.ok(row.querySelector("[data-terminal]"), "structured shell detail is mounted");
    const dots = [...row.querySelectorAll(".dsh-StateDot-dot")];
    assert.equal(dots.length, 1, "expanded disclosure replaces its dot with a chevron");
    assert.ok(dots.every(node => node.getAttribute("data-state") === dot));
  }
  console.log("DOM: same-frame identity dedup and shared shell presentation passed");
} finally { await harness.unmount(); await harness.close(); }
