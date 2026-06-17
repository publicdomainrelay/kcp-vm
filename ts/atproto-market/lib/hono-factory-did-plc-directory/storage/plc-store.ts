import type { LogEntry } from "@publicdomainrelay/did-plc";

export interface PlcStore {
  getCurrentOps(did: string): Promise<LogEntry[]>;

  getAuditLog(did: string): Promise<LogEntry[]>;

  getOpByCid(did: string, cid: string): Promise<LogEntry | null>;

  insertOp(entry: LogEntry): Promise<void>;

  nullifyOps(did: string, cids: string[]): Promise<void>;

  exportLogs(after?: Date, count?: number): Promise<LogEntry[]>;
}

export class MemoryPlcStore implements PlcStore {
  private logs = new Map<string, LogEntry[]>();

  async getCurrentOps(did: string): Promise<LogEntry[]> {
    const ops = this.logs.get(did);
    if (!ops) return [];
    return ops.filter((e) => !e.nullified);
  }

  async getAuditLog(did: string): Promise<LogEntry[]> {
    return this.logs.get(did) ?? [];
  }

  async getOpByCid(did: string, cid: string): Promise<LogEntry | null> {
    const ops = this.logs.get(did);
    if (!ops) return null;
    return ops.find((e) => e.cid === cid) ?? null;
  }

  async insertOp(entry: LogEntry): Promise<void> {
    let ops = this.logs.get(entry.did);
    if (!ops) {
      ops = [];
      this.logs.set(entry.did, ops);
    }
    ops.push(entry);
  }

  async nullifyOps(did: string, cids: string[]): Promise<void> {
    const ops = this.logs.get(did);
    if (!ops) return;
    const cidSet = new Set(cids);
    for (const entry of ops) {
      if (cidSet.has(entry.cid)) {
        entry.nullified = true;
      }
    }
  }

  async exportLogs(after?: Date, count?: number): Promise<LogEntry[]> {
    const all: LogEntry[] = [];
    for (const ops of this.logs.values()) {
      for (const entry of ops) {
        if (after && new Date(entry.createdAt) <= after) continue;
        all.push(entry);
      }
    }
    all.sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    if (count !== undefined) return all.slice(0, count);
    return all;
  }
}
