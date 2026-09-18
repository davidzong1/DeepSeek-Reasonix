import type { TranscriptSessionFollowerRuntime } from "./transcriptSessionFollowerRuntime";

/** Load the synchronization engine only when a session starts following. */
export class TranscriptSessionFollower {
  private runtime?: TranscriptSessionFollowerRuntime;
  private generation = 0;
  private readonly args: ConstructorParameters<typeof TranscriptSessionFollowerRuntime>;

  constructor(...args: ConstructorParameters<typeof TranscriptSessionFollowerRuntime>) {
    this.args = args;
  }

  get metrics() { return this.runtime?.metrics ?? { entries: 0, inlineBytes: 0 }; }

  async start(): Promise<void> {
    const generation = ++this.generation;
    this.runtime?.stop();
    const { TranscriptSessionFollowerRuntime } = await import("./transcriptSessionFollowerRuntime");
    if (generation !== this.generation) return;
    const runtime = new TranscriptSessionFollowerRuntime(...this.args);
    this.runtime = runtime;
    await runtime.start();
  }

  stop(): void {
    this.generation++;
    this.runtime?.stop();
    this.runtime = undefined;
  }
}
