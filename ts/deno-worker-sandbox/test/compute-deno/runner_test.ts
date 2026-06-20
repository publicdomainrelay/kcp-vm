import { assertEquals, assertRejects } from "@std/assert";
import { createDenoComputeInstanceRunner } from "@publicdomainrelay/compute-deno-atproto";
import { createDenoComputeManifestStore } from "@publicdomainrelay/compute-deno-atproto";
import { createDenoComputeInstanceStore } from "@publicdomainrelay/compute-deno-atproto";
import { createPersistentDenoWorker } from "@publicdomainrelay/sandbox-deno";

Deno.test("execute rejects with WorkerTimeout when worker never responds", async () => {
  const did = "did:plc:timeout";
  const records = new Map<string, Map<string, { uri: string; cid: string; value: Record<string, unknown> }>>();
  records.set(did, new Map());

  let seq = 0;
  function nextRkey(): string {
    seq++;
    return `r${seq.toString(16).padStart(8, "0")}`;
  }

  const pds = {
    async createRecord(_did: string, _col: string, record: Record<string, unknown>, rkey?: string) {
      const rk = rkey ?? nextRkey();
      const uri = `at://${_did}/${_col}/${rk}`;
      const h = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(record)))).slice(0, 16), (b) => b.toString(16).padStart(2, "0")).join("");
      const cid = `bafyrei${h}`;
      if (!records.has(_did)) records.set(_did, new Map());
      records.get(_did)!.set(uri, { uri, cid, value: record });
      return { uri, cid };
    },
    async getRecord(_did: string, _col: string, _rkey: string) {
      return records.get(_did)?.get(`at://${_did}/${_col}/${_rkey}`) ?? null;
    },
  };

  const manifestStore = createDenoComputeManifestStore(pds, did);
  const instanceStore = createDenoComputeInstanceStore(pds, did);
  const runner = createDenoComputeInstanceRunner({
    manifestStore,
    instanceStore,
    createWorker: createPersistentDenoWorker,
    bundler: {
      async bundle() {
        const hangScript = "self.onmessage = () => {}; self.onerror = null;";
        return { bundleJs: hangScript, stdout: "", stderr: "" };
      },
      async bundleTar() {
        return { bundleJs: "self.onmessage = () => {};", stdout: "", stderr: "" };
      },
    },
    timeoutMs: 500,
  });

  const manifest = await manifestStore.register({
    lock: "{}",
    json: "{}",
    bundle: "self.onmessage = () => {}; self.onerror = null;",
  });

  const instance = await instanceStore.register({ manifest });
  await runner.start(instance, manifest);

  await assertRejects(
    () => runner.execute(instance, { method: "GET", path: "/" }),
    "Worker timeout",
  );

  await runner.stopAll();
});

Deno.test("execute rejects with WorkerError when worker sends error message", async () => {
  const did = "did:plc:err";
  const records = new Map<string, Map<string, { uri: string; cid: string; value: Record<string, unknown> }>>();
  records.set(did, new Map());

  let seq = 0;
  function nextRkey(): string {
    seq++;
    return `r${seq.toString(16).padStart(8, "0")}`;
  }

  const pds = {
    async createRecord(_did: string, _col: string, record: Record<string, unknown>, rkey?: string) {
      const rk = rkey ?? nextRkey();
      const uri = `at://${_did}/${_col}/${rk}`;
      const h = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(JSON.stringify(record)))).slice(0, 16), (b) => b.toString(16).padStart(2, "0")).join("");
      const cid = `bafyrei${h}`;
      if (!records.has(_did)) records.set(_did, new Map());
      records.get(_did)!.set(uri, { uri, cid, value: record });
      return { uri, cid };
    },
    async getRecord(_did: string, _col: string, _rkey: string) {
      return records.get(_did)?.get(`at://${_did}/${_col}/${_rkey}`) ?? null;
    },
  };

  const manifestStore = createDenoComputeManifestStore(pds, did);
  const instanceStore = createDenoComputeInstanceStore(pds, did);
  const runner = createDenoComputeInstanceRunner({
    manifestStore,
    instanceStore,
    createWorker: createPersistentDenoWorker,
    bundler: {
      async bundle() {
        return { bundleJs: "self.onmessage = () => { self.postMessage({ type: 'error', message: 'boom' }); };", stdout: "", stderr: "" };
      },
      async bundleTar() {
        return { bundleJs: "self.onmessage = () => {};", stdout: "", stderr: "" };
      },
    },
    timeoutMs: 5000,
  });

  const manifest = await manifestStore.register({
    lock: "{}",
    json: "{}",
    bundle: "self.onmessage = () => { self.postMessage({ type: 'error', message: 'boom' }); };",
  });

  const instance = await instanceStore.register({ manifest });
  await runner.start(instance, manifest);

  await assertRejects(
    () => runner.execute(instance, { method: "GET", path: "/" }),
    "Worker error",
  );

  await runner.stopAll();
});
