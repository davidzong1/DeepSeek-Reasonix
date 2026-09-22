import { app } from "./bridge";
import type { ManualSessionCreationRequest } from "../generated/desktopContract.generated";

// Unacknowledged requests remain retryable with the same identity even when
// the creation-intent database could not be written. They are not successes.
const failed = new Map<string,{request:ManualSessionCreationRequest;error:string}>();
const listeners = new Set<()=>void>();
let version=0;
const notify=()=>{version++;listeners.forEach(listener=>listener());};
export const manualCreationFailures=()=>[...failed.values()];
export const manualCreationSnapshot=()=>version;
export const subscribeManualCreationFailures=(listener:()=>void)=>{listeners.add(listener);return()=>{listeners.delete(listener);};};
export function acknowledgeManualCreation(id:string){if(failed.delete(id))notify();}
export async function beginManualCreation(request:ManualSessionCreationRequest) {
 try {const result=await app.BeginManualSessionCreation(request);acknowledgeManualCreation(request.operationId);return result;}
 catch(error){failed.set(request.operationId,{request,error:String(error)});notify();throw error;}
}

export async function createManualSession(request: ManualSessionCreationRequest) {
 let operation;
 try { operation = await beginManualCreation(request); }
 catch (error) {
   // A lost response is resolved against the original persisted identity.
   try { operation = await app.GetManualSessionCreation(request.operationId); acknowledgeManualCreation(request.operationId); }
   catch { throw error; }
 }
 while (operation.phase === "reserved" || operation.phase === "starting") {
   await new Promise(resolve => setTimeout(resolve, 150));
   operation = await app.GetManualSessionCreation(request.operationId);
 }
 return operation;
}
