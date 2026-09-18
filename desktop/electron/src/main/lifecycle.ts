import { randomUUID } from "node:crypto";
import { errorText, type Logger } from "./log.js";
import type { ShutdownPhase } from "./service.js";

export type QuitPhase = "idle" | "preparing" | "saving" | "closing" | "failed" | "completed";

export interface LifecycleService {
  beforeClose(reason: string): Promise<boolean>;
  shutdown(
    reason?: "user_quit" | "update_restart" | "system_signal",
    onProgress?: (phase: ShutdownPhase) => void,
  ): Promise<void>;
}

export interface LifecycleApp {
  quit(): void;
  exit?(code: number): void;
  relaunch(args: string[], execPath?: string): void;
}

export interface QuitSequencerDeps {
  service: LifecycleService;
  app: LifecycleApp;
  flushRenderer?: () => Promise<void>;
  resumeRenderer?: () => Promise<void>;
  onShutdownFailed?: (message: string) => Promise<boolean>;
  onCloseAllowed(): void;
  cleanup?: Array<{ name: string; run(): void }>;
  schedule?: (run: () => void, milliseconds: number) => void;
  log: Logger;
}

// Electron's before-quit fires on every app.quit(); this drives it through
// beforeClose (Go may veto) and shutdown exactly once, then lets it through.
export class QuitSequencer {
  private phase: QuitPhase = "idle";
  private approved = false;
  private relaunchArgs: string[] | null = null;
  private relaunchExecPath: string | undefined;
  private attempt = "";
  private reason: "user_quit" | "update_restart" | "system_signal" = "user_quit";
  private reasonClaimed = false;

  constructor(private readonly deps: QuitSequencerDeps) {}

  get currentPhase(): QuitPhase {
    return this.phase;
  }

  get isQuitting(): boolean {
    return (
      this.approved ||
      this.phase === "saving" ||
      this.phase === "closing" ||
      this.phase === "failed" ||
      this.phase === "completed"
    );
  }

  onBeforeQuit(): boolean {
    if (this.phase === "completed") return true;
    if (this.phase === "failed") {
      this.phase = "saving";
      void this.finish();
      return false;
    }
    if (this.phase !== "idle") return false;
    this.claimReason("user_quit");
    if (!this.attempt) this.attempt = randomUUID();
    this.deps.log.info(`exit ${this.attempt}: ${this.approved ? "saving" : "preparing"}`);
    if (!this.approved) {
      this.phase = "preparing";
      void this.ask();
      return false;
    }
    this.phase = "saving";
    void this.finish();
    return false;
  }

  requestQuit(reason: "user_quit" | "system_signal" = "user_quit"): void {
    this.claimReason(reason);
    this.deps.app.quit();
  }

  approve(): void {
    this.approved = true;
    this.deps.app.quit();
  }

  relaunch(args: string[], execPath?: string): void {
    this.relaunchArgs = args;
    this.relaunchExecPath = execPath;
    this.claimReason("update_restart");
    this.approve();
  }

  private async ask(): Promise<void> {
    let prevent = false;
    try {
      await this.deps.flushRenderer?.();
    } catch (error) {
      this.deps.log.warn(`exit ${this.attempt}: draft flush failed; quit cancelled: ${errorText(error)}`);
      this.phase = "idle";
      this.approved = false;
      this.resetTrigger();
      return;
    }
    try {
      prevent = await this.deps.service.beforeClose("quit");
    } catch (error) {
      this.deps.log.warn(`beforeClose(quit) failed, quitting anyway: ${errorText(error)}`);
    }
    this.phase = "idle";
    if (prevent && !this.approved) {
      this.deps.log.info(`exit ${this.attempt}: cancelled`);
      this.resetTrigger();
      await this.resumeRenderer();
      return;
    }
    this.approved = true;
    this.deps.app.quit();
  }

  private async finish(): Promise<void> {
    try {
      await this.deps.flushRenderer?.();
    } catch (error) {
      this.deps.log.warn(`exit ${this.attempt}: draft flush failed; shutdown cancelled: ${errorText(error)}`);
      this.phase = "idle";
      this.approved = false;
      this.resetTrigger();
      return;
    }
    try {
      await this.deps.service.shutdown(this.reason, (phase) => {
        if (phase === "preparing" || phase === "saving" || phase === "closing") this.phase = phase;
      });
    } catch (error) {
      const message = errorText(error);
      this.deps.log.warn(`exit ${this.attempt}: shutdown failed: ${message}`);
      this.phase = "failed";
      this.approved = true;
      if (await this.deps.onShutdownFailed?.(message)) {
        this.phase = "saving";
        void this.finish();
      }
      return;
    }
    this.phase = "closing";
    for (const step of [{ name: "close permission", run: () => this.deps.onCloseAllowed() }, ...(this.deps.cleanup ?? [])]) {
      try {
        step.run();
        this.deps.log.info(`exit ${this.attempt}: cleanup ${step.name} complete`);
      } catch (error) {
        this.deps.log.warn(`exit ${this.attempt}: cleanup ${step.name} failed: ${errorText(error)}`);
      }
    }
    this.phase = "completed";
    this.deps.log.info(`exit ${this.attempt}: resources cleaned; requesting final shell exit`);
    if (this.deps.app.exit) {
      const schedule = this.deps.schedule ?? ((run, ms) => {
        setTimeout(run, ms).unref();
      });
      schedule(() => {
        this.deps.log.error("shell exit deadline exceeded after service shutdown");
        this.deps.app.exit?.(1);
      }, 5000);
    }
    try {
      if (this.relaunchArgs) this.deps.app.relaunch(this.relaunchArgs, this.relaunchExecPath);
    } catch (error) {
      this.deps.log.error(`relaunch failed: ${errorText(error)}`);
    } finally {
      this.deps.app.quit();
    }
  }

  private async resumeRenderer(): Promise<void> {
    try {
      await this.deps.resumeRenderer?.();
    } catch (error) {
      this.deps.log.warn(`exit ${this.attempt}: could not resume draft editing: ${errorText(error)}`);
    }
  }

  private claimReason(reason: "user_quit" | "update_restart" | "system_signal"): void {
    if (this.reasonClaimed) return;
    this.reason = reason;
    this.reasonClaimed = true;
  }

  private resetTrigger(): void {
    this.attempt = "";
    this.reason = "user_quit";
    this.reasonClaimed = false;
  }
}
