import type { LoggerInterface } from "@publicdomainrelay/logger";

export interface RelayVisibilityResult {
  ok: boolean;
  capableRelays: string[];
  indexedBy: string[];
  failures: Array<{ url: string; reason: string }>;
}

export async function verifyRelayVisibility(opts: {
  relayUrls: string[];
  bidderDid: string;
  collection: string;
  log?: LoggerInterface;
  pollTimeoutMs?: number;
  pollIntervalMs?: number;
}): Promise<RelayVisibilityResult> {
  const { relayUrls, bidderDid, collection, log, pollTimeoutMs = 15_000, pollIntervalMs = 2_000 } = opts;
  const failures: RelayVisibilityResult["failures"] = [];
  const collectionPath = `/xrpc/com.atproto.sync.listReposByCollection?collection=${encodeURIComponent(collection)}&limit=1000`;
  const capableRelays: string[] = [];
  for (const url of relayUrls) {
    try {
      const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
      if (res.ok) {
        capableRelays.push(url);
        log?.info("relay_capable", { url });
      } else if (res.status === 404) {
        log?.info("relay_no_collection_support", { url });
        failures.push({ url, reason: "listReposByCollection not supported (404)" });
      } else {
        failures.push({ url, reason: `HTTP ${res.status}` });
      }
      await res.body?.cancel();
    } catch (err) {
      failures.push({ url, reason: String(err) });
    }
  }

  if (capableRelays.length === 0) {
    log?.warn("relay_no_capable_relays", { total: relayUrls.length, failures });
    return { ok: false, capableRelays: [], indexedBy: [], failures };
  }
  log?.info("relay_capable_relays", { count: capableRelays.length, relays: capableRelays });

  const deadline = Date.now() + pollTimeoutMs;
  const indexedBy: string[] = [];
  while (Date.now() < deadline && indexedBy.length === 0) {
    for (const url of capableRelays) {
      try {
        const res = await fetch(`${url.replace(/\/+$/, "")}${collectionPath}`);
        if (!res.ok) {
          await res.body?.cancel();
          continue;
        }
        const body = await res.json() as { repos?: Array<{ did: string }> };
        if (body.repos?.some((r) => r.did === bidderDid)) {
          indexedBy.push(url);
        }
      } catch {
        continue;
      }
    }
    if (indexedBy.length === 0) {
      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }
  }

  const ok = indexedBy.length > 0;
  if (!ok) {
    log?.warn("relay_visibility_timeout", { capableRelays, pollTimeoutMs, failures });
  } else {
    log?.info("relay_visibility_confirmed", { indexedBy, capableRelays });
  }
  return { ok, capableRelays, indexedBy, failures };
}
