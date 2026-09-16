import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Transcript } from "../src/components/Transcript";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import { getMarkdownWorkerClient } from "../src/lib/markdownWorkerClient";
import { Composer } from "../src/components/Composer";
import { canonicalMessage } from "../src/lib/canonicalTranscriptBackend";
import { historyMessagesToItems } from "../src/lib/historyItems";
import type { ControllerLiveStore, Item, LiveStream } from "../src/lib/useController";
import "../src/styles.css";

function makeTurns(count: number, start = 0): Item[] {
  return Array.from({ length: count }, (_, position): Item[] => { const index = start + position; return [
    { kind: "user", id: `u${index}`, text: `Question ${index + 1}: Explain the implementation`, checkpointTurn: index + 1 },
    { kind: "tool", id: `tool${index}`, name: "read_file", args: JSON.stringify({ path: "src/example.ts" }), output: "Evidence\n".repeat(1200), readOnly: true, status: "done", subject: "src/example.ts" },
    { kind: "assistant", id: `a${index}`, text: `### Answer ${index + 1}\n\nA stable paragraph with **formatted text** and a [reference](https://example.com).\n\n- Keep the reader's position.\n- Update only the changed node.\n\n中文内容用于验证字体与换行。` + (index % 12 === 0 ? "\n\n| Item | Result |\n|---|---|\n| Test | Passed |\n\n$x^2 + y^2$" : "") + (index % 40 === 0 ? "\n\n```ts\n" + Array.from({length: 210}, (_, n) => `const value${n} = ${n};`).join("\n") + "\n```" : ""), reasoning: "Inspect the relevant code before making a change.", streaming: false,
      createdAt: new Date(2026, 8, 12, 12, 34).getTime(), turnDurationMs: 29_000, tokensPerSecond: 183,
      turnUsage: { totalTokens: 185_225, uncachedInputTokens: 26_278, cacheReadTokens: 155_520, outputTokens: 3_427, reasoningTokens: 1_909, routes: ["deepseek-official/deepseek-flash"] } },
  ]; }).flat();
}
function weatherTurn(): Item[] {
  const end = makeTurns(1)[2];
  return [
    { kind: "user", id: "weather-user", text: "查一下今天上海的天气", checkpointTurn: 1 },
    { kind: "assistant", id: "weather-thought", text: "", reasoning: "先查询实时天气，再交叉核对来源。", streaming: false },
    { kind: "tool", id: "weather-search", name: "web_search", args: JSON.stringify({ query: "上海今天天气 实时温度" }), output: "Weather sources", searchSources: [{ title: "天气数据（测试来源）", url: "https://example.com/weather" }], status: "done" },
    { kind: "tool", id: "weather-error", name: "web_fetch", args: JSON.stringify({ url: "https://example.com/weather" }), error: "DNS lookup failed", status: "error" },
    { kind: "assistant", id: "weather-progress", text: "一个来源暂时无法访问，正在核对另一来源。", reasoning: "切换到另一来源。", streaming: false },
    { kind: "notice", id: "weather-permission", level: "info", title: "已记录决策", text: "permission saved to /tmp/weather-fixture/reasonix.toml: curl --max-time 20 https://example.com/weather" },
    { kind: "tool", id: "weather-bash", name: "bash", args: JSON.stringify({ command: "printf '25.5°C 晴\\n'", description: "获取并核对上海天气" }), output: "25.5°C 晴\n", status: "done",
      execution: { kind: "shell", shell: "bash", state: "completed", exitCode: 0, supportsAndAnd: true } },
    { kind: "tool", id: "weather-present", name: "present", args: JSON.stringify({ files: [{ path: "output/shanghai-weather.html", description: "交互式天气报告" }, { path: "output/weather-notes.md", description: "数据来源与说明" }] }), output: "Presented output/shanghai-weather.html\nPresented output/weather-notes.md", status: "done",
      presentedFiles: [{ path: "output/shanghai-weather.html", description: "交互式天气报告" }, { path: "output/weather-notes.md", description: "数据来源与说明" }] },
    { ...end, id: "weather-final", text: "今天上海天气如下。以下为界面回放测试数据。\n\n## 上海 · 今日实况\n\n| 项目 | 数值 |\n|---|---|\n| 天气 | 晴 ☀️ |\n| 气温 | **25.5 °C** |\n| 湿度 | 66% |\n\n**全天**：多云转晴，23～30 °C。", reasoning: "" } as Item,
  ];
}
declare global { interface Window { chatFixture: { authored(): void; weather(): void; replace(count: number): void; reset(count: number): void; older(): void; tick(index: number): void; settle(): void; switchSession(): void; prepend(): void; ready: number; pending(): number } } }
function Fixture() {
  const [items, setItems] = useState(() => makeTurns(20));
  const [session, setSession] = useState(0);
  const [running, setRunning] = useState(false);
  const [ready, setReady] = useState(0);
  const itemsRef = useRef(items);
  itemsRef.current = items;
  const liveRef = useRef<LiveStream>();
  const liveListeners = useRef(new Set<() => void>());
  // Production stream deltas bypass the full controller tree through this
  // store. Keep the benchmark on that path so input timing measures the UI
  // users run instead of rebuilding the complete history fixture per token.
  const liveStore = useMemo<ControllerLiveStore>(() => ({
    subscribe: (_tabId, listener) => {
      liveListeners.current.add(listener);
      return () => { liveListeners.current.delete(listener); };
    },
    getSnapshot: () => liveRef.current,
  }), []);
  const publishLive = () => liveListeners.current.forEach(listener => listener());
  const clearLive = () => { liveRef.current = undefined; publishLive(); };
  useLayoutEffect(() => {
    window.chatFixture = {
      ready, pending: () => getMarkdownWorkerClient().stats().pending,
      authored: () => {
        const messages = [
          { role: "user", origin: "host", content: '<session-context version="1">private environment</session-context>' },
          { role: "user", origin: "user", content: '<response-language>internal policy</response-language>你是谁', raw_content: "你是谁" },
          { role: "user", content: '<capability-route>legacy internal route</capability-route>旧会话问题' },
          { role: "user", origin: "user", raw_content: '<response-language>用户引用的 XML</response-language>' },
          { role: "assistant", content: "我是 Reasonix。" },
        ].map((raw, i) => canonicalMessage({ messageId: `authored-${i}`, position: i, version: 1, role: raw.role }, raw));
        clearLive(); setItems(historyMessagesToItems(messages, "authored").items); setRunning(false); setSession(value => value + 1);
      },
      weather: () => { clearLive(); setItems(weatherTurn()); setRunning(false); setSession(value => value + 1); },
      replace: count => { clearLive(); setItems(makeTurns(count)); setRunning(false); setReady(value => value + 1); },
      reset: count => { clearLive(); setItems(makeTurns(Math.min(60, count), Math.max(0, count - 60))); setRunning(false); setSession(value => value + 1); setReady(value => value + 1); },
      older: () => setItems(previous => { const start = Number(previous[0].id.slice(1)); return [...makeTurns(Math.min(60, start), Math.max(0, start - 60)), ...previous]; }),
      tick: index => {
        const assistant = [...itemsRef.current].reverse().find((item): item is Extract<Item, { kind: "assistant" }> => item.kind === "assistant");
        if (!assistant) return;
        liveRef.current = { id: assistant.id, text: `Live answer\n\n${"Stable paragraph.\n\n".repeat(index + 1)}`,
          reasoning: `thinking ${index}`, reasoningComplete: false };
        setRunning(true);
        publishLive();
      },
      settle: () => {
        const live = liveRef.current;
        liveRef.current = undefined;
        setRunning(false);
        if (live) setItems(previous => previous.map(item => item.kind === "assistant" && item.id === live.id
          ? { ...item, text: live.text, reasoning: live.reasoning, reasoningComplete: live.reasoningComplete, streaming: false } : item));
        publishLive();
      },
      switchSession: () => setSession(value => value + 1),
      prepend: () => setItems(previous => [{ kind: "user", id: `older${previous.length}`, text: "An older question" }, { kind: "assistant", id: `older-answer${previous.length}`, text: "Earlier context\n\n".repeat(20), reasoning: "", streaming: false }, ...previous]),
    };
  }, [ready]);
  return <div style={{ height: "100vh", display: "flex", flexDirection: "column", background: "var(--bg)" }}>
    <Transcript items={items} liveStore={liveStore} tabId="chat-bench" geometrySessionKey={`fixture-${session}`} running={running} onPrompt={() => {}}
      onFork={() => {}} hasOlderHistory={items[0]?.id !== "u0"} onLoadOlderHistory={() => { window.chatFixture.older(); return true; }} />
    <div style={{ flex: "none", maxHeight: "40vh", padding: 16 }}><Composer running={running} collaborationMode="normal" toolApprovalMode="ask" modelLabel="DeepSeek" tabId="chat-bench"
      onSend={() => {}} onCancel={async () => ({ discardedItemIds: [] })} onCycleMode={() => {}} onSetMode={() => {}}
      onSetCollaborationMode={() => {}} onSetToolApprovalMode={() => {}} onToggleYoloApprovalMode={() => {}}
      onClearGoal={() => {}} onPauseGoal={() => {}} onResumeGoal={() => {}} onSwitchModel={() => true} onSetEffort={() => {}} />
    </div>
  </div>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
