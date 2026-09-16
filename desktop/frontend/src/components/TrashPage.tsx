import { useState } from "react";
import { Archive, ArrowLeft, History } from "lucide-react";
import type { SessionMeta } from "../lib/types";
import { useT } from "../lib/i18n";
import { ManagementPageShell } from "./ManagementPageShell";
import { ArchivedSessionsList } from "./ArchivedSessionsList";
import { HistoricalRecoveryList } from "./HistoricalRecoveryList";
import "./TrashPage.css";

export function TrashPage({ active, onBack, list, restore, purge, onOpenSession }: {
  onOpenSession: React.ComponentProps<typeof ArchivedSessionsList>["onOpenSession"];
  active: boolean; onBack: () => void; list: () => Promise<SessionMeta[]>;
  restore: (path: string) => Promise<void>; purge: (path: string) => Promise<void>;
}) {
  const t = useT();
  const [historical, setHistorical] = useState(false);
  return <ManagementPageShell active={active} onBack={onBack} className="trash-center"
    title={t("history.trashTitle")} description={t("history.archiveExplanation")}>
    <div className="trash-center__heading">
      <div><span className="trash-center__eyebrow"><Archive size={16} />{t("history.trashTitle")}</span>
        <h2>{t(historical ? "history.historicalSection" : "history.archivedConversations")}</h2>
        <p>{t(historical ? "history.historicalDescription" : "history.archiveExplanation")}</p>
      </div>
      <button className="btn btn--small" onClick={() => setHistorical(!historical)}>
        {historical ? <ArrowLeft size={14} /> : <History size={14} />}
        {t(historical ? "history.backToTrash" : "history.historicalSection")}
      </button>
    </div>
    <div className="trash-center__body">
      <div hidden={!historical}><HistoricalRecoveryList active={active && historical} onOpenSession={onOpenSession} /></div>
      <div hidden={historical}><ArchivedSessionsList active={active && !historical} onOpenSession={onOpenSession} legacyList={list} legacyRestore={restore} legacyPurge={purge} /></div>
    </div>
  </ManagementPageShell>;
}
