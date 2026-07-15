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
      let lastSeq = 0;
      let notify: (() => void) | null = null;
      const unsub = bus.subscribe((f) => {
        queue.push(f);
        notify?.();
      });
      try {
        while (true) {
          // Drain backlog frames arrived since last yield — covers gap
          // between backfill() completion and live() subscription.
          for (const f of backlog) {
            if (f.seq > lastSeq) {
              lastSeq = f.seq;
              yield f;
            }
          }
          if (queue.length > 0) {
            const f = queue.shift()!;
            lastSeq = f.seq;
            yield f;
          } else {
            await new Promise<void>((r) => {
              notify = r;
              // If a frame landed between the empty check and notify assignment,
              // resolve immediately so we don't wait for the next frame.
              if (queue.length > 0) r();
            });
            notify = null;
          }
        }
      } finally {
        unsub();
      }
    },
  };
}
