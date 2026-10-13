import process from "node:process";
const read = (k: string): string | undefined => {
  try {
    return Deno.env.get(k);
  } catch {
    return undefined;
  }
};
const env = new Proxy({} as Record<string, string>, {
  get: (_t, k) => typeof k === "string" ? read(k) : undefined,
  has: (_t, k) => typeof k === "string" && read(k) !== undefined,
  ownKeys: () => {
    try {
      return Object.keys(Deno.env.toObject());
    } catch {
      return [];
    }
  },
  getOwnPropertyDescriptor: (_t, k) => {
    const v = typeof k === "string" ? read(k) : undefined;
    return v === undefined ? undefined : { value: v, writable: true, enumerable: true, configurable: true };
  },
});
Object.defineProperty(process, "env", { value: env, writable: true, configurable: true, enumerable: true });
