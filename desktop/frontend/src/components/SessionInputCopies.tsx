import { useT } from "../lib/i18n";

export function SessionInputCopies({copies,blocked,restore}:{copies:string[];blocked:boolean;restore:(index:number)=>Promise<void>}) {
 const t=useT();
 if (!copies.length) return null;
 return <details className="management-notice"><summary>{t("draft.conflict")}</summary>
  {copies.map((copy,index)=><div key={index}>
   <pre style={{whiteSpace:"pre-wrap",maxHeight:160,overflow:"auto"}}>{copy}</pre>
   <button type="button" disabled={blocked} onClick={()=>void restore(index)}>{t("draft.keepLocal")}</button>
  </div>)}
 </details>;
}
