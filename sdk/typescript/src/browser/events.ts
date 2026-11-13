/** Versioned broadcasts coalesce invalidations, never envelope batches. */
export class Invalidation implements AsyncIterable<void> {
  #version = 0; #ended = false; #error: unknown;
  #resolve!: () => void;
  #changed: Promise<void>;
  constructor() { this.#changed = new Promise(resolve => { this.#resolve = resolve; }); }
  snapshot(): Promise<void> { if (this.#error !== undefined) return Promise.reject(this.#error); return this.#ended ? Promise.resolve() : this.#changed; }
  signal(): void { if (this.#ended) return; const resolve = this.#resolve; this.#version++; this.#changed = new Promise(done => { this.#resolve = done; }); resolve(); }
  end(error?: unknown): void { if (this.#ended) return; this.#ended = true; this.#error = error; this.#resolve(); }
  [Symbol.asyncIterator](): AsyncIterator<void> {
    let seen = 0;
    return {next: async () => {
      while (!this.#ended && this.#version === seen) await this.#changed;
      if (this.#error !== undefined) throw this.#error;
      if (this.#ended) return {value: undefined, done: true};
      seen = this.#version; return {value: undefined, done: false};
    }};
  }
}
