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
    async *live() {
      const queue: TFrame[] = [];
      let notify: (() => void) | null = null;
      const unsub = bus.subscribe((f) => {
        queue.push(f);
        notify?.();
      });
      try {
        while (true) {
          if (queue.length > 0) {
            yield queue.shift()!;
          } else {
            await new Promise<void>((r) => { notify = r; });
            notify = null;
          }
        }
      } finally {
        unsub();
      }
    },
  };
}
