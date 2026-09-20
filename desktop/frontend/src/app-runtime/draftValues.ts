import type { PersistentComposerDraft } from "../components/Composer";
import type { SessionDraftSettings } from "../generated/desktopContract.generated";

export const cloneDraftContent = (content: PersistentComposerDraft) => structuredClone(content);
export const cloneDraftSettings = (settings: SessionDraftSettings) => structuredClone(settings);
