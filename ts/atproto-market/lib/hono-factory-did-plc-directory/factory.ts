import { Hono } from "@hono/hono";
import { cors } from "@hono/hono/cors";
import { MemoryPlcStore, type PlcStore } from "./storage/plc-store.ts";
import { mountHandlers, type HandlerDeps } from "./handlers.ts";

export interface PlcDirectoryOptions {
  store?: PlcStore;
  version?: string;
  verifySig?: HandlerDeps["verifySig"];
}

export interface PlcDirectoryFactory {
  app: Hono;
  store: PlcStore;
}

export type { PlcStore } from "./storage/plc-store.ts";

export function createPlcDirectoryFactory(
  opts: PlcDirectoryOptions = {},
): PlcDirectoryFactory {
  const store = opts.store ?? new MemoryPlcStore();
  const version = opts.version ?? "0.1.0";

  const app = new Hono();

  app.use("*", cors());

  app.onError((err, c) => {
    const message = err instanceof Error ? err.message : "Internal Server Error";
    const status = err instanceof Error && "status" in err
      ? (err as Error & { status: number }).status
      : 500;
    return c.json({ message }, status as Parameters<typeof c.json>[1]);
  });

  mountHandlers(app, { store, version, verifySig: opts.verifySig });

  return { app, store };
}
