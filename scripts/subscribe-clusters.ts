import { decodeCborSequence } from "jsr:@std/cbor@0.1";
import { parseArgs } from "jsr:@std/cli@1/parse-args";

const NSID = "com.publicdomainrelay.temp.cluster.subscribeClusters";

const args = parseArgs(Deno.args, {
  string: ["token", "gateway", "name", "until", "fail-on"],
  default: { gateway: "https://k8s.socialweb.computer" },
});

if (!args.token) {
  console.error(
    "usage: subscribe-clusters.ts --token <service JWT, lxm " + NSID +
      "> [--gateway https://k8s.socialweb.computer] [--name <cluster>] [--until cluster=Ready] [--fail-on nodeClaim=Failed]",
  );
  Deno.exit(64);
}

function matcher(spec: string | undefined): ((t: string, p: Record<string, unknown>) => boolean) | null {
  if (!spec) return null;
  const [type, phase] = spec.split("=");
  return (t, p) => t === type && (!phase || p.phase === phase);
}

const until = matcher(args.until);
const failOn = matcher(args["fail-on"]);

const url = new URL(args.gateway.replace(/^http/, "ws"));
url.pathname = `/xrpc/${NSID}`;
if (args.name) url.searchParams.set("name", args.name);

const ws = new WebSocket(url, { headers: { Authorization: `Bearer ${args.token}` } });
ws.binaryType = "arraybuffer";

let exitCode = 3;
ws.onmessage = (ev) => {
  if (!(ev.data instanceof ArrayBuffer)) return;
  const parts = decodeCborSequence(new Uint8Array(ev.data)) as unknown[];
  if (!Array.isArray(parts) || parts.length !== 2) {
    console.error("bad frame: not a header and a payload");
    exitCode = 4;
    ws.close();
    return;
  }
  const [header, payload] = parts as [{ op: number; t?: string }, Record<string, unknown>];
  if (header.op === -1) {
    console.log(JSON.stringify({ type: "error", ...payload }));
    exitCode = 1;
    ws.close();
    return;
  }
  if (header.op !== 1 || !header.t) return;
  const type = header.t.replace(/^#/, "");
  console.log(JSON.stringify({ type, ...payload }));
  if (failOn?.(type, payload)) {
    exitCode = 2;
    ws.close();
  } else if (until?.(type, payload)) {
    exitCode = 0;
    ws.close();
  }
};
ws.onerror = (ev) => {
  console.error("websocket error:", (ev as ErrorEvent).message ?? ev.type);
};
ws.onclose = (ev) => {
  if (exitCode === 3) console.error(`stream closed without an error frame (code ${ev.code} ${ev.reason})`);
  Deno.exit(exitCode);
};
