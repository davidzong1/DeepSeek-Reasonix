import { useEffect, useState, useSyncExternalStore } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import { beginManualCreation, manualCreationFailures, manualCreationSnapshot, subscribeManualCreationFailures } from "../lib/manualCreationRequests";

/** Creation survives navigation and transport loss; this never selects a page. */
export function ManualSessionRecovery() {
 const t = useT();
 const [items,setItems] = useState<ManualSessionCreationView[]>([]);
 const [error,setError] = useState("");
 useSyncExternalStore(subscribeManualCreationFailures,manualCreationSnapshot);
 const failed=manualCreationFailures();
 useEffect(() => {
  let disposed = false;
  const refresh = async () => {
   try { const next = await app.ListManualSessionCreations(); if (!disposed) { setItems(next); setError(""); } }
   catch (e) { if (!disposed) setError(String(e)); }
  };
  void refresh(); const timer=setInterval(() => void refresh(),1500);
  return () => { disposed=true; clearInterval(timer); };
 },[]);
 if (!items.length && !error && !failed.length) return null;
 return <div className="management-notice" role="status">
  {error && <div role="alert">{error}</div>}
  {failed.map(({request,error})=><div key={request.operationId}>{request.workspaceRoot || request.scope} — {error}
   <button className="btn btn--small" onClick={()=>void beginManualCreation(request).then(result=>app.RetryManualSessionCreation(result.operationId)).catch(e=>setError(String(e)))}>{t("common.retry")}</button>
  </div>)}
  {items.map(item => <div key={item.operationId}>
   {item.workspaceRoot || item.scope} — {item.phase === "failed" ? item.error : t("composer.workspaceStarting")}
   <button className="btn btn--small" onClick={() => void app.RetryManualSessionCreation(item.operationId).catch(e=>setError(String(e)))}>{t("common.retry")}</button>
  </div>)}
 </div>;
}
