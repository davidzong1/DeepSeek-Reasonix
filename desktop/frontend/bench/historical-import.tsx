import React from "react";
import { createRoot } from "react-dom/client";
import { HistoricalImportList } from "../src/components/HistoricalImportList";
import { LocaleProvider } from "../src/lib/i18n";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

let status = {
  items: [
    { id: "old-a", title: "Retained conversation A", format: "legacy", status: "available" },
    { id: "old-b", title: "Retained conversation B", format: "canonical", status: "available" },
  ], running: false, paused: false, remaining: 0,
};
let imports = 0;
const controls: string[] = [];
installDesktopHostStub({
  ListHistoricalSessions: async () => structuredClone(status),
  GetHistoricalImportStatus: async () => structuredClone(status),
  ImportHistoricalSession: async (id: string) => {
    imports++;
    if (id === "old-a" && imports === 1) throw new Error("historical session is in use; retry after closing the other instance");
    status = { ...status, items: status.items.map(item => item.id === id ? { ...item, status: "imported", session: { hostId: "local", sessionId: "imported-a" } } : item) };
    return { session: { hostId: "local", sessionId: "imported-a" }, workspaceId: "global", generation: 2 };
  },
  StartHistoricalImport: async () => { controls.push("start"); status = { ...status, running: true, remaining: 2 }; return structuredClone(status); },
  ControlHistoricalImport: async (action: string) => {
    controls.push(action);
    status = { ...status, paused: action === "pause", running: action !== "cancel", remaining: action === "cancel" ? 0 : status.remaining };
    return structuredClone(status);
  },
});
createRoot(document.getElementById("root")!).render(<LocaleProvider><HistoricalImportList active onOpenSession={async () => {}} /></LocaleProvider>);
window.addEventListener("beforeunload", () => { document.body.dataset.fixture = JSON.stringify({ controls, imports }); });
