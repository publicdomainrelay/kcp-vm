import { assertEquals } from "@std/assert";
import { BIDDER_OAUTH_SCOPE, REQUESTER_OAUTH_SCOPE } from "@publicdomainrelay/oauth-scope";
import * as marketLexicons from "@publicdomainrelay/market-lexicons";
import * as marketCommon from "@publicdomainrelay/market-common";
import * as fedproxyRbacCommon from "@publicdomainrelay/fedproxy-rbac-common";
import { POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID } from "@publicdomainrelay/policy-engine-abc";

const REPO_ROOT = new URL("../", import.meta.url);

type Verb = "create" | "update";

interface WriteSite {
  file: string;
  line: number;
  argument: string;
  verb: Verb;
}

interface ResolvedWrite extends WriteSite {
  collections: string[];
}

interface RoleWriters {
  role: string;
  scope: readonly string[];
  dirs: string[];
}

const ROLE_WRITERS: RoleWriters[] = [
  {
    role: "bidder",
    scope: BIDDER_OAUTH_SCOPE,
    dirs: ["lib/market-bidder", "lib/market-bidder-compute", "hono-bidder"],
  },
  {
    role: "requester",
    scope: REQUESTER_OAUTH_SCOPE,
    dirs: ["lib/requester-xrpc", "lib/compute-request-xrpc"],
  },
];

const PASS_THROUGH_ARGUMENTS = new Set(["collection"]);

const DYNAMIC_ARGUMENTS: Record<string, string[]> = {
  nsid: [POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID],
};

const REPO_ARGUMENTS = new Set(["did", "repo"]);

function nsidConstants(): Map<string, string> {
  const out = new Map<string, string>();
  const sources: Record<string, unknown>[] = [
    marketLexicons as Record<string, unknown>,
    marketCommon as Record<string, unknown>,
    fedproxyRbacCommon as Record<string, unknown>,
    { POLICY_GHA_LITE_NSID, POLICY_TYPESCRIPT_NSID },
  ];
  for (const source of sources) {
    for (const [name, value] of Object.entries(source)) {
      if (name.endsWith("_NSID") && typeof value === "string") out.set(name, value);
    }
  }
  return out;
}

function lineOf(text: string, index: number): number {
  return text.slice(0, index).split("\n").length;
}

const ARG = String.raw`([A-Za-z_][\w]*|"[^"]+"|'[^']+')`;
const WRITE_CALL = new RegExp(
  String.raw`\b(create|put)(?:Signed)?(?:Repo)?Record\s*!?\s*\(\s*${ARG}\s*[,)]` +
    String.raw`(?:\s*${ARG}\s*[,)])?`,
  "g",
);
const APPLY_WRITES = /\bapplyWrites\s*\(/g;
const APPLY_WRITE_OP = new RegExp(
  String.raw`action:\s*"(create|update)"[^}]*?collection:\s*${ARG}`,
  "gs",
);

export function findWriteSites(file: string, text: string): WriteSite[] {
  const sites: WriteSite[] = [];
  for (const m of text.matchAll(WRITE_CALL)) {
    const verb: Verb = m[1] === "put" ? "update" : "create";
    const argument = REPO_ARGUMENTS.has(m[2]) && m[3] ? m[3] : m[2];
    if (REPO_ARGUMENTS.has(argument)) continue;
    sites.push({ file, line: lineOf(text, m.index!), argument, verb });
  }
  for (const m of text.matchAll(APPLY_WRITES)) {
    const start = m.index! + m[0].length;
    const end = text.indexOf("])", start);
    const body = text.slice(start, end < 0 ? undefined : end);
    for (const op of body.matchAll(APPLY_WRITE_OP)) {
      sites.push({
        file,
        line: lineOf(text, start + op.index!),
        argument: op[2],
        verb: op[1] as Verb,
      });
    }
  }
  return sites;
}

export function resolveWrites(
  sites: WriteSite[],
  constants: Map<string, string>,
): { resolved: ResolvedWrite[]; unresolved: WriteSite[] } {
  const resolved: ResolvedWrite[] = [];
  const unresolved: WriteSite[] = [];
  for (const site of sites) {
    const a = site.argument;
    if (PASS_THROUGH_ARGUMENTS.has(a)) continue;
    if (a.startsWith('"') || a.startsWith("'")) {
      resolved.push({ ...site, collections: [a.slice(1, -1)] });
    } else if (constants.has(a)) {
      resolved.push({ ...site, collections: [constants.get(a)!] });
    } else if (DYNAMIC_ARGUMENTS[a]) {
      resolved.push({ ...site, collections: DYNAMIC_ARGUMENTS[a] });
    } else {
      unresolved.push(site);
    }
  }
  return { resolved, unresolved };
}

export function scopeGrants(scope: readonly string[], nsid: string, verb: Verb): boolean {
  const prefix = `repo:${nsid}?`;
  return scope.some((token) =>
    token.startsWith(prefix) &&
    new URLSearchParams(token.slice(prefix.length)).getAll("action").includes(verb)
  );
}

export function uncoveredWrites(
  writes: ResolvedWrite[],
  scope: readonly string[],
): string[] {
  const out = new Set<string>();
  for (const w of writes) {
    for (const nsid of w.collections) {
      if (!scopeGrants(scope, nsid, w.verb)) {
        out.add(`${nsid}?action=${w.verb} (${w.file}:${w.line})`);
      }
    }
  }
  return [...out].sort();
}

async function readTree(dir: string): Promise<{ file: string; text: string }[]> {
  const out: { file: string; text: string }[] = [];
  const root = new URL(`${dir}/`, REPO_ROOT);
  for await (const entry of Deno.readDir(root)) {
    const rel = `${dir}/${entry.name}`;
    if (entry.isDirectory) {
      if (entry.name === "node_modules" || entry.name === "test") continue;
      out.push(...await readTree(rel));
    } else if (entry.isFile && entry.name.endsWith(".ts") && !entry.name.endsWith("_test.ts")) {
      out.push({ file: rel, text: await Deno.readTextFile(new URL(entry.name, root)) });
    }
  }
  return out;
}

async function roleWrites(
  writers: RoleWriters,
): Promise<{ resolved: ResolvedWrite[]; unresolved: WriteSite[] }> {
  const constants = nsidConstants();
  const sites: WriteSite[] = [];
  for (const dir of writers.dirs) {
    for (const { file, text } of await readTree(dir)) sites.push(...findWriteSites(file, text));
  }
  return resolveWrites(sites, constants);
}

const FIXTURE_UNSCOPED = new URL(
  "./fixtures/role-scope-coverage/bidder_writes_unscoped.ts.txt",
  import.meta.url,
);

Deno.test("fixture: a bidder write of a collection missing from the bidder role set is reported", async () => {
  const text = await Deno.readTextFile(FIXTURE_UNSCOPED);
  const { resolved, unresolved } = resolveWrites(
    findWriteSites("fixture.ts", text),
    nsidConstants(),
  );
  assertEquals(unresolved, []);
  assertEquals(
    uncoveredWrites(resolved, BIDDER_OAUTH_SCOPE).map((s) => s.split(" ")[0]),
    ["com.publicdomainrelay.temp.compute.events.vm.fixtureUnscoped?action=create"],
  );
});

Deno.test("fixture: vm.terminated is reported once it is dropped from the bidder role set", async () => {
  const text = await Deno.readTextFile(FIXTURE_UNSCOPED);
  const { resolved } = resolveWrites(findWriteSites("fixture.ts", text), nsidConstants());
  const without = BIDDER_OAUTH_SCOPE.filter((t) =>
    !t.startsWith(`repo:${marketLexicons.COMPUTE_EVENTS_VM_TERMINATED_NSID}?`)
  );
  assertEquals(
    uncoveredWrites(resolved, without).map((s) => s.split(" ")[0]),
    [
      "com.publicdomainrelay.temp.compute.events.vm.fixtureUnscoped?action=create",
      `${marketLexicons.COMPUTE_EVENTS_VM_TERMINATED_NSID}?action=create`,
    ],
  );
});

for (const writers of ROLE_WRITERS) {
  Deno.test(`every collection the ${writers.role} writes is in the ${writers.role} oauth-scope role set`, async () => {
    const { resolved, unresolved } = await roleWrites(writers);
    assertEquals(
      unresolved.map((s) => `${s.file}:${s.line} ${s.argument}`),
      [],
      "write site with an argument the scanner cannot map to an NSID; add it to the NSID constants or DYNAMIC_ARGUMENTS",
    );
    assertEquals(uncoveredWrites(resolved, writers.scope), []);
  });
}

Deno.test("the scan finds the known bidder and requester writes", async () => {
  const [bidder, requester] = await Promise.all(ROLE_WRITERS.map(roleWrites));
  const collections = (r: { resolved: ResolvedWrite[] }) =>
    new Set(r.resolved.flatMap((w) => w.collections));
  const b = collections(bidder);
  const q = collections(requester);
  for (
    const nsid of [
      marketLexicons.COMPUTE_EVENTS_VM_TERMINATED_NSID,
      marketLexicons.COMPUTE_EVENTS_VM_ONNETWORK_NSID,
      marketLexicons.BID_NSID,
      marketLexicons.RECEIPT_NSID,
      marketLexicons.EVENT_NSID,
      marketLexicons.OFFERING_NSID,
    ]
  ) assertEquals(b.has(nsid), true, `bidder scan misses ${nsid}`);
  for (
    const nsid of [
      marketLexicons.COMPUTE_VM_NSID,
      marketLexicons.RFP_NSID,
      marketLexicons.ACCEPT_NSID,
      marketLexicons.COMPUTE_EVENTS_VM_DELETE_NSID,
      marketLexicons.BADGE_BLUE_KEYS_NSID,
      fedproxyRbacCommon.FEDPROXY_RBAC_NSID,
    ]
  ) assertEquals(q.has(nsid), true, `requester scan misses ${nsid}`);
});
