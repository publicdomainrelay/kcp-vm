# Policy engines — gha-lite flavor

The market policies implemented as **GitHub Actions** for the gha-lite policy
engine (`lib/policy-engine-server-gha-lite`). Each policy is a bundled node20
action that imports the shared policy machinery from
[`../deno-typescript-shared`](../deno-typescript-shared) (record-centric
`createPolicyCtx`, the strongRef-keyed JSON-object cache, `runPolicy`) and the
policy factory from [`../deno-typescript`](../deno-typescript).

## Policies are perspective-named and record-driven

The perspective is baked into the policy identity — no `perspective` runtime
input. Trust policies split into a `bidder-*` / `requester-*` pair; work
policies were already side-declared. All policies take **all three market
record types — `rfp`, `bid`, `accept` — each optional**: the caller passes
whichever records exist at its stage, and the policy evaluates with what it
has.

| Policy | Side | Records it uses |
| ------ | ---- | --------------- |
| `open` | either | — (always allows) |
| `bidder-only-me` / `bidder-mutuals` / `bidder-tangled-vouch` | bidder | `rfp` (submitRFP) or `accept` (submitAccept) → requester is the counterparty |
| `requester-only-me` / `requester-mutuals` / `requester-tangled-vouch` | requester | `bid` (submitBid) → bidder is the counterparty |
| `under-4-cpus` | bidder | `rfp` → derives the demand workload |
| `bid-payload` | requester | `bid` → derives the offer |

`self-did` (the evaluator's own DID) is the only required input — no record
names you. `subjectDid` / `rootRequesterDid` / `demand` / `offer` are derived
from whichever records are present:

- requester side: `subjectDid` = bid author, `rootRequesterDid` = `selfDid`, `offer` from the bid's payload ref.
- bidder side: `subjectDid` = rfp-or-accept author (the requester), `rootRequesterDid` = `subjectDid`, `demand` from the rfp's payload ref.

A record is passed in the firehose shape `{ uri, cid, value? }`; if `value` is
absent it is hydrated over the network (and cached under its strongRef). There
is no `service` input: every repo's PDS is derived from its DID via the PLC
directory (`IdResolver` + `getPdsEndpoint`, like atproto-market's
`listRecordsPublic`) and public reads are plain HTTP fetches.

## Cache — strongRef-keyed, TTL verdicts, LRU-bounded

Policy verdicts and record resolutions are memoized in the JSON-object cache
(`Cache = key → path→file`), always keyed on the **full strongRef (URI+CID)**:

- `rec/<uri>/<cid>` — a resolved record body. Immutable (content-addressed), never expires.
- `policy/<name>/<rfp uri/cid>|<bid uri/cid>|<accept uri/cid>[:argsHash]` — a verdict for exactly the records present at that stage. Carries a **TTL** (default 30s, `cacheTtlSec` in `policy-args` overrides) because the vouch/operator graph is mutable; expired entries are deleted on read.

The store is **LRU-bounded** (`maxEntries`, default 512, oldest evicted) so a
caller that keeps persisting `.detail.cache` never grows the map without bound.
Inside the engine the map rides the `GITHUB_CACHE` command file — a real temp
file in full mode, an in-memory virtual file in the net-only worker — and
returns in `.detail.cache`.

```bash
OUT1=$(BUNDLED_ACTIONS_DIR=../policies/gha-lite/bundled-actions \
  deno task run --workflow ../policies/gha-lite/workflows/requester-only-me.yml \
  --input self-did=did:plc:5svqtrhheairglgiiyvutzik \
  --input 'bid={"uri":"at://did:plc:lpfuqerea3deuoyrn7ojser4/com.publicdomainrelay.temp.market.bid/3mm...","cid":"bafyrei...","value":{"payload":{"uri":"at://.../com.publicdomainrelay.temp.market.bids.free/x","cid":"bafyrei..."}}}')
CACHE=$(echo "$OUT1" | jq -c '.detail.cache')

# Second run reuses the verdict (from-cache=true, no network):
BUNDLED_ACTIONS_DIR=../policies/gha-lite/bundled-actions \
  deno task run --workflow ../policies/gha-lite/workflows/requester-only-me.yml \
  --input self-did=did:plc:5svqtrhheairglgiiyvutzik \
  --input 'bid={...same bid...}' \
  --context "{\"cache\":$CACHE}"
```

## Layout

```
bundled-actions/tangy/policy-<name>/
  action.yml     runs.using: node20, main: dist/index.js
  index.ts       source — imports the policy factory + action-common
  dist/index.js  deno bundle output (committed, what the engine runs)
workflows/<policy>.yml   example workflow per policy
action-common.ts         shared action logic (inlined into each bundle)
scripts/build-actions.ts rebuilds every dist/index.js
```

## Build

```bash
cd lib/policies/gha-lite
deno task build      # deno bundle each action's index.ts → dist/index.js
```

`dist/` is committed, so the engine runs the pre-bundled files without a build
step or any jsr/registry resolution at runtime. Rebuild after editing an action
source or anything in `deno-typescript-shared` / `deno-typescript`.

## Run

Each workflow is a `workflow_dispatch` workflow; inputs reach the engine via
`--input k=v`. `self-did` is required; pass the records relevant to your stage
as JSON. Trust policies hit the live network to resolve operator DIDs and vouch
sets; the work policies (bid-payload, under-4-cpus) can run fully offline when
the `bid`/`rfp` `value.payload` is supplied inline (no resolution needed):

```bash
cd lib/policy-engine-server-gha-lite
BUNDLED_ACTIONS_DIR=../policies/gha-lite/bundled-actions \
  deno task run --workflow ../policies/gha-lite/workflows/bidder-only-me.yml \
  --input self-did=did:plc:lpfuqerea3deuoyrn7ojser4 \
  --input 'rfp={"uri":"at://did:plc:5svqtrhheairglgiiyvutzik/com.publicdomainrelay.temp.market.rfp/3mm...","cid":"bafyrei..."}'
```

## Net-only mode

The actions are pre-bundled self-contained files, so they also run in the
engine's `--net-only` sandbox (the only external reference is `@atproto/api`,
which the sandboxed worker resolves). `run` steps and composite actions are
refused there, so only the node20 policy action works:

```bash
BUNDLED_ACTIONS_DIR=../policies/gha-lite/bundled-actions \
  deno task run --workflow ../policies/gha-lite/workflows/bid-payload.yml --net-only \
  --input self-did=did:plc:5svqtrhheairglgiiyvutzik \
  --input 'bid={"uri":"at://...","cid":"...","value":{"payload":{"uri":"at://.../com.publicdomainrelay.temp.market.bids.free/x","cid":"..."}}}'
```
