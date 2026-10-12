import type {
  Microvm,
  MicrovmContainerState,
  MicrovmExit,
  MicrovmResult,
  MicrovmSpec,
} from "@publicdomainrelay/microvm-abc";

export interface VmruntimeOptions {
  command: string[];
  handle: string;
  hostAddress?: string;
  watchIntervalMs?: number;
  logger?: { info(event: string, extra?: Record<string, unknown>): void };
}

export interface RuntimeRef {
  backend: string;
  unit: string;
  pid: number;
  vmmID: string;
  slot: number;
  vmmUID: number;
  workDir: string;
  socket: string;
  guestAddress: string;
  hostAddress: string;
  imageSha256: string;
}

export interface VmObservation {
  verdict: "Running" | "Lost" | "Unknown";
  reason?: string;
  detail?: string;
  stopped?: boolean;
  state?: string;
}

const DEFAULT_HOST_ADDRESS = "192.0.2.1";
const DEFAULT_WATCH_INTERVAL_MS = 2_000;

export const VERB_TOKEN = "{verb}";

export function verbArgv(command: string[], handle: string, verb: string[]): string[] {
  const at = command.indexOf(VERB_TOKEN);
  if (at < 0 || command.indexOf(VERB_TOKEN, at + 1) >= 0) {
    throw new Error(
      `the VM runtime command must name ${VERB_TOKEN} exactly once, where start, inspect, stop or list goes`,
    );
  }
  return [
    ...command.slice(0, at),
    verb[0],
    ...command.slice(at + 1),
    "--handle",
    handle,
    ...verb.slice(1),
  ];
}

async function runArgv(
  argv: string[],
  stdin?: string,
): Promise<{ code: number; stdout: string; stderr: string }> {
  const child = new Deno.Command(argv[0], {
    args: argv.slice(1),
    stdin: stdin === undefined ? "null" : "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  if (stdin !== undefined) {
    const w = child.stdin.getWriter();
    await w.write(new TextEncoder().encode(stdin));
    await w.close();
  }
  const out = await child.output();
  const dec = new TextDecoder();
  return { code: out.code, stdout: dec.decode(out.stdout), stderr: dec.decode(out.stderr) };
}

function nameOf(workDir: string): string {
  return workDir.split("/").filter((p) => p !== "").pop() ?? "";
}

export function createVmruntimeMicrovm(opts: VmruntimeOptions): Microvm {
  const hostAddress = opts.hostAddress ?? DEFAULT_HOST_ADDRESS;
  const watchIntervalMs = opts.watchIntervalMs ?? DEFAULT_WATCH_INTERVAL_MS;
  verbArgv(opts.command, opts.handle, ["list"]);

  async function mustVerb<T>(verb: string[], stdin?: string): Promise<T> {
    const r = await runArgv(verbArgv(opts.command, opts.handle, verb), stdin);
    if (r.code !== 0) {
      throw new Error(`vm ${verb.join(" ")} exited ${r.code}: ${r.stderr.trim().slice(-4000)}`);
    }
    return JSON.parse(r.stdout) as T;
  }

  async function observe(name: string): Promise<VmObservation> {
    return await mustVerb<VmObservation>(["inspect", name]);
  }

  return {
    name: "vmruntime",
    async boot(spec: MicrovmSpec): Promise<MicrovmResult> {
      const userData = await Deno.readTextFile(spec.userDataFile);
      const ref = await mustVerb<RuntimeRef>(
        ["start"],
        JSON.stringify({
          name: spec.name,
          userData,
          vcpu: spec.vcpu,
          memMiB: spec.memMib,
          diskMiB: spec.diskMib,
        }),
      );
      opts.logger?.info("microvm_vmruntime_started", {
        name: spec.name,
        unit: ref.unit,
        backend: ref.backend,
        vmmPid: ref.pid,
        slot: ref.slot,
        vmmUid: ref.vmmUID,
        guestIp: ref.guestAddress,
      });
      return {
        name: spec.name,
        fingerprint: ref.imageSha256,
        vmmPid: ref.pid,
        guestIp: ref.guestAddress,
        socket: ref.socket,
        console: `${ref.workDir}/console.log`,
        workDir: spec.workDir,
        container: spec.name,
      };
    },
    async inspect(container: string): Promise<MicrovmContainerState> {
      const obs = await observe(container);
      if (obs.verdict === "Lost") return { exists: obs.reason !== "UnitGone", running: false };
      return { exists: true, running: true };
    },
    hostAddressForGuest(): Promise<string> {
      return Promise.resolve(hostAddress);
    },
    async stop(workDir: string): Promise<void> {
      const name = nameOf(workDir);
      if (!name) throw new Error(`cannot tell which guest works in ${workDir}`);
      const res = await mustVerb<Record<string, unknown>>(["stop", name]);
      opts.logger?.info("microvm_vmruntime_stopped", { name, ...res });
      await Deno.remove(workDir, { recursive: true }).catch((err: unknown) => {
        if (!(err instanceof Deno.errors.NotFound)) throw err;
      });
    },
    watch(container: string, onExit: (exit: MicrovmExit) => void): () => void {
      let cancelled = false;
      let timer: ReturnType<typeof setTimeout> | undefined;
      const schedule = () => {
        if (cancelled) return;
        timer = setTimeout(check, watchIntervalMs);
        Deno.unrefTimer(Number(timer));
      };
      const check = async () => {
        timer = undefined;
        if (cancelled) return;
        const obs = await observe(container).catch(() => undefined);
        if (cancelled) return;
        if (obs === undefined || obs.verdict !== "Lost") return schedule();
        cancelled = true;
        onExit({
          container,
          reason: obs.reason === "UnitGone" ? "removed" : "exited",
          logs: `${obs.reason ?? ""} ${obs.detail ?? ""}`.trim(),
        });
      };
      schedule();
      return () => {
        cancelled = true;
        if (timer !== undefined) clearTimeout(timer);
      };
    },
  };
}
