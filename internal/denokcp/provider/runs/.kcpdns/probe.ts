// Probe run by the provider's exec readiness check, with the kcpdns shim
// preloaded so it resolves the same names a workload does.
//
//   deno run --allow-env=<shim's variables> --allow-net \
//     --preload <shim> probe.ts <fqdn> [path] [scheme]
//
// Tries https first and falls back to http, so the same probe works whether the
// service is serving TLS or not, and a plain-HTTP service does not need a
// different probe from a TLS one. Exits non-zero when neither answers 2xx.

const fqdn = Deno.args[0];
const path = Deno.args[1] ?? "/";
const scheme = Deno.args[2] ?? "https";

if (!fqdn) {
  console.error("probe: usage: probe.ts <fqdn> [path] [scheme]");
  Deno.exit(2);
}

const order = scheme === "http" ? ["http", "https"] : ["https", "http"];
const failures: string[] = [];

for (const proto of order) {
  try {
    const res = await fetch(`${proto}://${fqdn}${path}`);
    await res.body?.cancel();
    if (res.ok) Deno.exit(0);
    failures.push(`${proto} returned ${res.status}`);
  } catch (err) {
    failures.push(`${proto}: ${err instanceof Error ? err.message : String(err)}`);
  }
}

console.error(`probe: ${fqdn}${path} failed: ${failures.join("; ")}`);
Deno.exit(1);
