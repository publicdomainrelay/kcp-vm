# request-vm-ssh

Request compute VM over RFP market, get SSH session tunneled through
did-key-relay xrpc relay. Guest born from cloud-init only — no hand-built
provisioning.

Topology:

```
ssh -> websocat ProxyCommand -> xrpc-relay -> tunnel-subscriber (guest) -> sshd:22
       (host)                   (dispatch)     (did-key-relay agent)
```

Fedproxy-client replaced by did-key-relay tunnel-subscriber. Guest websocat
dropped — subscriber speaks raw TCP to sshd. Host websocat stays (reference
cli.ts pattern).

## Quick start (local dev)

4 terminals, all from org root.

### 1. Relay

```bash
cd did-key-relay
deno run -A hono-did-key-relay-relayer/mod.ts \
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
  --bid-window-sec 8 \
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

## Fulfillment Policy (--policy-mode)

The `--policy-mode` flag controls which bidders may fulfill your RFP. It's a
**requester-side** policy attached to the RFP record itself.

**Values:**
- `only_me` — Only your own bidder (same operator DID) may bid. Requires your
  bidder to be running with `acceptScope: "only_me"` (the desktop app default
  after OAuth login) and a `bidderAssociation` record linking the bidder to
  your operator DID.
- `direct_network` — Bidders operated by DIDs in your vouch graph may bid.
- `policy_based` — (stub, not yet implemented)
- Omit — Open to all bidders (no policy restriction).

**How `only_me` works end-to-end:**

1. You run your desktop bidder (hono-macos-runner-desktop or hono-desktop) and
   link an ATProto identity
2. On first OAuth login, the bidder sets `acceptScope: "only_me"` — it only
   responds to RFPs from your DID
3. The bidder creates a `bidderAssociation` ATProto record pointing from the
   bidder's DID to your operator DID
4. You run `request-vm-ssh --policy-mode only_me ...`
5. The requester mints a
   `com.publicdomainrelay.temp.market.policies.only_me` record with your root
   requester DID
6. The RFP is submitted to discovered bidders with the policy strongRef
   attached
7. The bidder's `only_me` policy evaluates: resolves the bidder's operator DID
   via `bidderAssociation`, checks `operatorDid === rootRequesterDid`
8. Only your own bidder passes the check and submits a bid

**Relationship with bidder-side `acceptScope`:**
- `acceptScope` (bidder-side): "which DIDs' RFPs will I even look at?" — set
  in the tray UI or via `createMarketBidder({acceptScope})`
- `policyMode` (requester-side): "which bidders may fulfill my RFP?" — set via
  `--policy-mode` or `runComputeContract({policyMode})`
- Both default to open. Setting both to `only_me` creates a mutual restriction:
  only RFPs from your DID reach your bidder, and only your bidder may fulfill
  your RFPs.

## CLI options

| Flag | Default | Description |
|------|---------|-------------|
| `--vm-name` | `compute-<random>` | VM name |
| `--bid-window-sec` | `30` | Seconds to wait for bids |
| `--vm-ready-timeout-sec` | `300` | Seconds to wait for SSH |
| `--keep-vm` | false | Keep VM after exit |
| `--exec` | `bash` | Program to run (non-interactive) |
| `--relay-port` | auto | Relay dispatch port |
| `--registry-port` | auto | JSR registry port |
| `--bidder-dids` | — | Additional bidder DIDs to include (comma-separated) |
| `--policy-mode` | open | Fulfillment policy: `only_me`, `direct_network`, `policy_based`, or omit |
