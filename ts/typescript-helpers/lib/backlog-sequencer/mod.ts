import { EventBus } from "@publicdomainrelay/event-bus";

export interface BacklogSequencerOptions<TIn, TFrame extends { seq: number }> {
  maxBacklog: number;
  build: (seq: number, evt: TIn) => TFrame;
}

export interface BacklogSequencer<TIn, TFrame extends { seq: number }> {
  append(evt: TIn): TFrame;
  backfill(since?: number): AsyncIterable<TFrame>;
  live(): AsyncIterable<TFrame>;
}

export function createBacklogSequencer<TIn, TFrame extends { seq: number }>(
  opts: BacklogSequencerOptions<TIn, TFrame>,
): BacklogSequencer<TIn, TFrame> {
  const { maxBacklog: max, build } = opts;
  let seq = 0;
  const backlog: TFrame[] = [];
  const bus = new EventBus<TFrame>();

  return {
    append(evt: TIn): TFrame {
      seq++;
      const frame = build(seq, evt);
      backlog.push(frame);
      if (backlog.length > max) backlog.shift();
      bus.publish(frame);
      return frame;
    },
    async *backfill(since?: number) {
      for (const f of backlog) {
        if (since !== undefined && f.seq <= since) continue;
        yield f;
      }
    },
    live(): AsyncIterable<TFrame> {
      const queue: TFrame[] = [];
      let notify: (() => void) | null = null;
      // Subscribe IMMEDIATELY (not lazily in generator) so frames arriving
      // between backfill() and the first live .next() are captured.
      const unsub = bus.subscribe((f) => {
        queue.push(f);
        notify?.();
      });
      let done = false;
      return {
        [Symbol.asyncIterator]() { return this as unknown as AsyncIterator<TFrame>; },
        async next(): Promise<IteratorResult<TFrame>> {
          if (done) return { value: undefined, done: true };
          if (queue.length > 0) return { value: queue.shift()!, done: false };
          await new Promise<void>((r) => {
            notify = r;
            if (queue.length > 0) r();
          });
          notify = null;
          // The notify promise resolved — a frame is in the queue (or the
          // immediate resolve above drained it). Re-check.
          if (queue.length > 0) return { value: queue.shift()!, done: false };
          // The promise was resolved but queue was already drained by the
          // immediate check. Loop around.
          return (this as unknown as AsyncIterator<TFrame>).next();
        },
        async return(): Promise<IteratorResult<TFrame>> {
          done = true;
          unsub();
          return { value: undefined, done: true };
        },
      } as unknown as AsyncIterable<TFrame>;
    },
  };
}
