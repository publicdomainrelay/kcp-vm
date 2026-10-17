import type { CommitEvent, Sequencer, SequencedFrame } from "@publicdomainrelay/atproto-repo-abc";
import { EventBus } from "@publicdomainrelay/event-bus";

const MAX_BACKLOG = 10000;
const MAX_BACKLOG_BYTES = 64 * 1024 * 1024;

function now(): string {
  return new Date().toISOString();
}

export class FirehoseSequencer implements Sequencer {
  #backlog: SequencedFrame[] = [];
  #sizes: number[] = [];
  #bytes = 0;
  #seq = 0;
  #bus = new EventBus<SequencedFrame>();

  append(evt: CommitEvent): SequencedFrame {
    this.#seq++;
    const frame: SequencedFrame = {
      seq: this.#seq,
      repo: evt.repo,
      commit: { $link: evt.commit },
      rev: evt.rev,
      since: evt.since,
      blocks: evt.blocks,
      ops: evt.ops.map((op) => ({
        action: op.action,
        path: op.path,
        cid: op.cid ? { $link: op.cid } : null,
        prev: op.prev ? { $link: op.prev } : null,
      })),
      tooBig: false,
      blobs: [],
      prevData: evt.prevData ? { $link: evt.prevData } : null,
      time: now(),
    };
    const size = (evt.blocks?.byteLength ?? 0) + 512;
    this.#backlog.push(frame);
    this.#sizes.push(size);
    this.#bytes += size;
    while (this.#backlog.length > MAX_BACKLOG || (this.#bytes > MAX_BACKLOG_BYTES && this.#backlog.length > 1)) {
      this.#backlog.shift();
      this.#bytes -= this.#sizes.shift()!;
    }
    this.#bus.publish(frame);
    return frame;
  }

  appendIdentity(did: string, handle?: string): SequencedFrame {
    this.#seq++;
    const frame: SequencedFrame = {
      seq: this.#seq,
      did,
      time: now(),
      handle: handle ?? null,
    };
    this.#backlog.push(frame);
    this.#sizes.push(512);
    this.#bytes += 512;
    if (this.#backlog.length > MAX_BACKLOG) {
      this.#backlog.shift();
      this.#bytes -= this.#sizes.shift()!;
    }
    this.#bus.publish(frame);
    return frame;
  }

  appendAccount(did: string, active: boolean, status?: string): SequencedFrame {
    this.#seq++;
    const frame: SequencedFrame = {
      seq: this.#seq,
      did,
      time: now(),
      active,
      status: status ?? (active ? "active" : "deactivated"),
    };
    this.#backlog.push(frame);
    this.#sizes.push(512);
    this.#bytes += 512;
    if (this.#backlog.length > MAX_BACKLOG) {
      this.#backlog.shift();
      this.#bytes -= this.#sizes.shift()!;
    }
    this.#bus.publish(frame);
    return frame;
  }

  async *backfill(since?: number): AsyncIterable<SequencedFrame> {
    const startSeq = since ?? 0;
    for (const frame of this.#backlog) {
      if ((frame.seq as number) > startSeq) {
        yield frame;
      }
    }
  }

  live(): AsyncIterable<SequencedFrame> {
    const queue: SequencedFrame[] = [];
    let resolve: ((frame: SequencedFrame) => void) | null = null;
    // Subscribe IMMEDIATELY (not lazily) so frames arriving between
    // backfill() and the first live .next() are captured.
    const dispose = this.#bus.subscribe((frame) => {
      if (resolve) {
        resolve(frame);
        resolve = null;
      } else {
        queue.push(frame);
      }
    });
    let done = false;
    return {
      [Symbol.asyncIterator]() { return this as unknown as AsyncIterator<SequencedFrame>; },
      async next(): Promise<IteratorResult<SequencedFrame>> {
        if (done) return { value: undefined, done: true };
        if (queue.length > 0) return { value: queue.shift()!, done: false };
        const value = await new Promise<SequencedFrame>((r) => {
          resolve = r;
          if (queue.length > 0) r(queue.shift()!);
        });
        return { value, done: false };
      },
      async return(): Promise<IteratorResult<SequencedFrame>> {
        done = true;
        dispose();
        return { value: undefined, done: true };
      },
    } as unknown as AsyncIterable<SequencedFrame>;
  }
}
