# request-vm-ssh

Request compute VM over RFP market, get SSH session tunneled through
xrpc relay. Guest born from cloud-init only -- no hand-built
provisioning.

Topology:

```
ssh -> websocat ProxyCommand -> xrpc-relay -> tunnel-subscriber (guest) -> sshd:22
       (host)                   (dispatch)     (xrpc tunnel agent)
```

Fedproxy-client replaced by xrpc tunnel-subscriber. Guest websocat
dropped -- subscriber speaks raw TCP to sshd. Host websocat stays (reference
cli.ts pattern).

## Quick start (local dev)

4 terminals, all from org root.

### 1. Relay

```bash
cd did-key-ingress-proxy
deno run -A hono-did-key-ingress-proxy/mod.ts \
  --hostname localhost \
  --port 5555
```

### 2. JSR registry

Guest pulls tunnel-subscriber at boot via `deno run jsr:`.

```bash
cd hono-jsr
deno run -A hono-package-registry/main.ts \
  --store local \
  --base-dir .. \
  --port 5556
```

### 3. Bidder

```bash
cd atproto-market
deno run -A hono-bidder/mod.ts \
  --compute-provider-local \
  --compute-provider-local-container-mode container \
  --relay-dispatcher-host localhost:5555 \
  --serve-port 0 \
  --offering-refresh-sec 0
```

Wait for: `{"message":"bidder ready","did":"did:plc:..."}`. Copy the DID.

### 4. Request VM + SSH

```bash
cd request-vm-ssh
BIDDER_HANDLE_0000=<bidder-did-from-step-3> deno run -A mod.ts \
  --relay-port 5555 \
  --registry-port 5556 \
  --policy-args '{"bidWindowSec":8}' \
  --vm-ready-timeout-sec 300 \
  --keep-vm \
  --exec "hostname; echo PASS; id -un; exit"
```

Drops into interactive SSH when VM ready. `--exec` runs non-interactive command
instead.

### 5. Cleanup

Ctrl-C relay, registry, bidder. Remove leftover containers:

```bash
container ls -a | grep pdr- | awk '{print $1}' | xargs container rm -f
```

## How it works

1. `request-vm-ssh` starts relay + JSR registry (or connects to existing)
2. Creates requester PDS, registers DID with real PLC directory
3. Discovers bidders from `BIDDER_HANDLE_NNNN` env vars
4. Generates cloud-init via `buildTunnelUserData` (tunnel-subscriber replacing
   fedproxy-client)
5. Creates VM record + signed RFP, broadcasts to bidders
6. Winning bidder provisions guest (container/VB) from cloud-init
7. Guest boots: installs sshd, pulls tunnel-subscriber from JSR registry,
   registers with relay, bridges relay tunnel to sshd:22
8. Host opens SSH through `websocat ProxyCommand -> relay -> subscriber -> sshd`

## Fulfillment Policy (--policy)

The `--policy` flag names the policy attached to your RFP record, which decides
which bidders may fulfill it. Policies are data, not modes: the name is written
into a policy record, and whoever evaluates it looks the name up in a shared
registry.

**Registry names (trust policies -- who may bid):**
- `only-me` -- Only bidders whose operator DID is yours.
- `tangled-vouch` -- Bidders whose operator is in your Tangled vouch graph.
- `mutuals` -- Bidders whose operator mutually follows you on Bluesky.
- `open` -- No restriction.

**Work policies (what is on the table)** declare which side they run on:
`under-4-cpus` (bidder-only) and `bid-payload` (requester-only). A wrong-side
`--policy` fails at startup instead of silently doing nothing.

**Arguments** ride along on the policy record via `--policy-args` (JSON):

| Key | Default | Meaning |
|-----|---------|---------|
| `bidWindowSec` | `30` | Seconds to collect bids |
| `firstFree` | `false` | Accept the first policy-allowed free bid immediately instead of waiting out the window |

```bash
request-vm-ssh --policy only-me --policy-args '{"bidWindowSec":10,"firstFree":true}'
```

With `firstFree`, a `com.publicdomainrelay.temp.market.bids.free` bid that passes
policy wins as soon as it arrives. A free bid from a rejected bidder does not --
it is discarded and collection continues. If the window elapses with no allowed
free bid, the lowest-cost allowed bid wins.

**Where a policy runs.** The record's `$type` says which engine evaluates it:

| Record type | Evaluated by |
|---|---|
| `...market.policies.builtin` | in-process Deno worker sandbox running the first-party registry bundle |
| `...market.policies.service` | remote policy engine named by `--policy-engine <did:web:host>` |
| `...market.policies.denoWorker` | in-process sandbox running a caller-supplied bundle |

Local execution is sandboxed: the worker gets **no Deno permissions**, and every
record read (`resolve`, `resolveOperatorDid`, `getVouchedDids`) is brokered back
to the host over postMessage. Policy code cannot reach the network.

- `--only-remote-policy-exec` refuses to run anything locally; only
  `policies.service` records are evaluated.
- `--allow-untrusted-policy-exec` is required before a `policies.denoWorker`
  record's caller-supplied bundle will run at all.

**How `only-me` works end-to-end:**

1. You run your desktop bidder and link an ATProto identity
2. The bidder's own scope gate defaults to `only-me` -- it only responds to RFPs
   from your DID
3. The bidder creates a `bidderAssociation` record pointing from the bidder's
   DID to your operator DID
4. You run `request-vm-ssh --policy only-me ...`
5. The requester mints a
   `com.publicdomainrelay.temp.market.policies.builtin` record with
   `name: "only-me"` and your root requester DID
6. The RFP is submitted to discovered bidders with the policy strongRef attached
7. The bidder loads the policy record, runs `only-me` in its sandbox: resolves
   the bidder's operator DID via `bidderAssociation`, checks
   `operatorDid === rootRequesterDid`
8. Only your own bidder passes and submits a bid

The bidder is the enforcement point for a `policies.builtin` record -- it is the
side that holds the trust data. The requester re-checks before accepting only
when it delegated to an engine via `--policy-engine`.

**Two independent gates:**
- Bidder-side `--policy`: "which DIDs' RFPs will I even look at?"
- Requester-side `--policy`: "which bidders may fulfill my RFP?"

Setting both to `only-me` creates a mutual restriction: only RFPs from your DID
reach your bidder, and only your bidder may fulfill your RFPs.

## CLI options

| Flag | Default | Description |
|------|---------|-------------|
| `--vm-name` | `compute-<random>` | VM name |
| `--vm-ready-timeout-sec` | `300` | Seconds to wait for SSH |
| `--keep-vm` | false | Keep VM after exit |
| `--exec` | `bash` | Program to run (non-interactive) |
| `--relay-port` | auto | Relay dispatch port |
| `--registry-port` | auto | JSR registry port |
| `--bidder-dids` | -- | Additional bidder DIDs to include (comma-separated) |
| `--policy` | `only-me` | Policy name: `only-me`, `tangled-vouch`, `mutuals`, `open`, `bid-payload` |
| `--policy-args` | `{}` | Policy arguments as JSON, e.g. `{"bidWindowSec":30,"firstFree":true}` |
| `--policy-engine` | -- | Policy engine DID (`did:web:host`); mints a `policies.service` record |
| `--only-remote-policy-exec` | false | Refuse to evaluate any policy locally |
| `--allow-untrusted-policy-exec` | false | Permit caller-supplied policy bundles to run |

## Policy Engines

### Market fulfillment policy (`--policy-engine`)

A `policies.service` record delegates the decision to an external HTTP server.
The engine implements one XRPC endpoint:

```
POST /xrpc/com.publicdomainrelay.temp.market.evaluatePolicy
Authorization: Bearer <serviceAuth JWT>
Content-Type: application/json

{"name":"only-me","args":{"bidWindowSec":30},
 "subjectDid":"did:plc:bidder","rootRequesterDid":"did:plc:requester",
 "counterpartyDid":"did:plc:requester","policyRef":{"uri":"at://...","cid":"..."}}

-> {"allow":true,"violations":[]}
-> {"allow":false,"violations":[{"msg":"reason","policyId":"nsid-or-uri"}]}
```

Trust is via AT Protocol serviceAuth -- the caller signs a JWT with their
identity key targeting the engine's DID as audience. The engine verifies this
JWT to confirm the caller's identity before making decisions.

The engine resolves `name` against the same registry used for local execution,
so a policy name means the same thing whether it runs in-process or remotely.

**Example: Minimal engine**

```ts
Deno.serve({ port: 8080 }, async (req) => {
  const { subjectDid, rootRequesterDid } = await req.json();
  const allowed = myCheck(subjectDid, rootRequesterDid);
  return Response.json({ allow: allowed, violations: allowed ? [] :
    [{ msg: "not allowed", policyId: "my-policy" }] });
});
```

**Running:**

```sh
# Requester creates RFP with a remote policy engine
request-vm-ssh --policy only-me \
  --policy-engine did:web:127.0.0.1%3A8080

# Bidder gates its own scope with the same registry name
hono-bidder --policy only-me
```

### Worker permission policy (`allow-net`)

Controls which Deno Worker manifests a bidder will accept. Evaluated in the
bidder's `onRfp` callback before creating a bid. The bidder resolves the RFP's
worker manifest, checks its `permissions` field against the configured handler,
and skips bids for manifests requesting disallowed permissions.

`SandboxPermissions`: `net`, `read`, `write`, `env`, `run`, `ffi`, `sys`,
`import` -- each `boolean | string[]`.

**Built-in: `allow-net`** -- allows manifests with no `permissions` field or
only `permissions: { net: true }`. Any other permission key (`read`, `write`,
`run`, etc.) is denied.

**CLI:**

```sh
hono-bidder --compute-provider-deno-worker --worker-permission-mode allow-net
```

**Desktop:** Tray -> toggle "Deno Workers" ON -> select "Allow net only" from
the Worker Permissions dropdown.

**Custom handler:** Implement `PermissionPolicyHandler` interface
(`evaluate(manifest: WorkerManifestRecord): Promise<PermissionPolicyResult>`).
Register in `BUILTIN_HANDLERS` (in `deno-worker-sandbox/lib/compute-deno-atproto/builtin-policy-handlers.ts`)
or run as a remote service via `--policy-handler-service did:web:<host>#gate_registry_worker_manifest_permissions`.

### Two policy layers

| Layer | Interface | When | Where |
|-------|-----------|------|-------|
| Market fulfillment | `FulfillmentPolicy` | Bid time + accept time | `lib/market-policy/` |
| Worker permission | `PermissionPolicyHandler` | Worker manifest registration | `deno-worker-sandbox/` |

Both layers use the same serviceAuth trust model. Both return `{ allow,
violations }`. Market policy controls **who** can bid. Worker permission
controls **what** workers they can run.
