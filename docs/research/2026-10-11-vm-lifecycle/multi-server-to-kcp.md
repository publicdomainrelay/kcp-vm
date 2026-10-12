# Multi-server processes to kcp applications: bidder, requester, gateway

Research for plans 0006, 0007 and 0008, written 2026-10-12. Read-only investigation; no plan file is
edited here. Everything below is meant to be folded into the EXISTING plans (the user said fold, not a new
plan); section 6 gives the item deltas.

Scope now (user, 2026-10-12, user-decisions.md): the atproto-market bidder and requester (hono-bidder,
request-vm-ssh, the compute-request libraries) and the gateway kcp (`serve gateway kcp`, the door in
socialweb-computer-k8s). The ssh gateway (hono-socialweb-computer-ssh, 0008 M4-RUN-GATEWAY-SSH) comes later
and is only mentioned where it shares a pattern.

File:line references are to the org root /home/johnandersen777/src/publicdomainrelay-kcp at the commits on
disk on 2026-10-12 (socialweb-computer-kcp 5a030ec). `swck` = socialweb-computer-kcp, `am` =
atproto-market, `hcp` = hono-compute-provider, `door` = swck/socialweb-computer-k8s.

## 0. Answer in one screen

The lens (user, 2026-10-12): kcp is part of each APPLICATION, not infrastructure. A bidder, a requester
(`run request`), the gateway, and a dev or test market network are each an application instance that carries its
own kcp as its runtime. Application state is kcp objects; the servers that today share one deno process
become DenoPods (TypeScript, die with the core) or HostProcesses (outlive the core) reconciled from those
objects; a MODE is a manifest set plus a profile; composition is applying sets or nesting workspaces.
Applications talk to each other only over the atproto market protocol (PLC, relay/firehose, the dispatcher's
xrpc ingress, PDS records), never by reading each other's kcp. The one traditional use of kcp is the virtual
Kubernetes clusters project (tenant k3s control planes behind kcp: clusterprovider, Cluster/Node/NodeClaim/
ClusterLink); it is called out separately (4.6) and is not the model for anything else.

What the code does today (section 2):

1. **hono-bidder is one deno process hosting a TCP market serve, one TCP guest JSR registry per
   firecracker/local provider, 1 + P ingress relays (+1 for an in-process repo) and a set of background
   loops** (P = compute providers enabled): the market serve (tcp + relay, am/hono-bidder/mod.ts:
   619-631), one relay-only serve + relay per provider (:490-600), an in-process repo with its own relay in local-key
   mode (:283-302 via am/lib/atproto-helpers/agent.ts:276-436), a guest JSR registry on 0.0.0.0:0 per
   firecracker/local provider (hcp/lib/compute-provider-firecracker/mod.ts:579-588), an OIDC issuer and
   /v1/on-network per provider (:807-861, :904-925), a loopback OAuth callback server in browser-OAuth mode
   (:161-177), firehose watchers for RFP/accept/event/trust collections (am/lib/market-bidder/mod.ts:517-586),
   the offering refresh timer (:635-663), OAuth token auto-refresh, and startup one-shots (PLC genesis,
   requestCrawl, relay visibility polling, :787-825).
2. **The user's example is half true today.** The market network's bidder does NOT use the market network's `root:bob` PDS
   DenoPod: 70-bidder.yaml:24 passes no handle, so the bidder runs its own in-process repo
   (createLocalPDSAgent) served through the dispatcher; bob's hono-pds is only probed for readiness
   (accept.sh:412-414). The "remote PDS login" path is dead code: `--atproto-handle/--atproto-password`
   also builds a LOCAL repo (mod.ts:262-282), `--atproto-pds-url` (cli-args-env.ts:76-81) is read nowhere,
   and createRemoteAgent is imported but never called (mod.ts:10). So "QR bidder uses the user's PDS, market network
   bidder uses its own PDS pod" needs one code change (M3-BIDDER-ACCOUNT-PDS below) before a profile can
   express it.
3. **Interactive steps are stdout/stdin**: the QR is printed (mod.ts:229-234, :720-726), association is a
   `prompt("[y/N]")` on stdin (:348, :743), browser OAuth spawns xdg-open (:166-171), and an expired session
   deletes the file and `Deno.exit(1)` (:122-135). A DenoPod has stdin null (70-bidder.yaml:85), so the market network
   must pass `--skip-qr`. Under kcp these become object status/spec (pending actions) that the foreground
   CLI or the door renders.
4. **Identity and addresses move on every restart**: a fresh relay keypair per relay per process
   (mod.ts:103-113, :104) so every ingressRef and the OIDC issuer URL change; the OIDC signing key store is
   in memory AND module-global (hcp/lib/oidc-issuer-hono/mod.ts:42-57, configureOidc :63-77 called by every
   createOidcIssuer :616), so two providers in one process overwrite each other's issuer URL getter.
5. **Peers are found by fixed ports, log scraping and four ad-hoc fetch patches**: market network ports 2583-2589 in
   manifests and accept.sh; accept.sh finds the bidder DID by grepping `localPDS plc` out of stdout
   (accept.sh:463-467); tests read a `bidder_ready` stdout line (mod.ts:686-691); `*.localhost` and
   plc.directory are rewritten by hand in hono-bidder (:74-95), request-vm-ssh (:48-60), market-requester
   (:82-95) and test/fetch-interceptor.ts.
6. **The door is already half kcp-native**: NodeClaims and ClusterLinks are read and written over kcp REST
   (door/hono-socialweb-computer-k8s/mod.ts:189-210), but its OAuth sessions, requester key and per-claim
   secrets servers are a JSON file, a hex file and process memory (:50, :72; node-market mod.ts:383).

Proposal (sections 4-5), in one paragraph: each application has ONE root object in its base workspace
(MarketBidder, MarketRequester, Gateway, MarketNetwork for a market network's shared atproto services) whose spec is
today's cli-args-env options minus the network, plus a MarketIdentity (new kind: who the app is on atproto
and how it signs in) and a MarketNetwork value (where PLC, relay, dispatcher and QR origin are). The core's
app controller renders the root object's profile into children (DenoPods, HostProcesses, OpenBaoServer,
ComputeVMs, ComputeContracts) with ownerReferences. Every listener binds port 0 and reports its URL (tcp and
relay ingressUrl) into the DenoPod's status; peers inside an app resolve by service name; across apps
values flow only as MarketNetwork values. Keys, relay keypairs and OAuth sessions live in the app's OpenBao.
Every CLI follows one pattern: resolve the app's kcp (own or nested base workspace), server-side apply the
root object with a profile, watch its status, render pending actions (QR, approvals) and outputs; no app
verb starts a server itself. `deno run hono-bidder` without kcp keeps working as the library composition
(all components in one process, file-backed stores), which is also what the DenoPod runs in phase 1.

## 1. Vocabulary used below

- **Application**: a bidder, a requester, the gateway, a market network (a market network's PLC + relay + dispatcher +
  PDSes), or a market network (a composition of the others). One instance = one base workspace in a kcp + one set +
  one profile.
- **The application's kcp**: the kcp URL + base workspace its core is given (`kcp-base-workspace`, already
  an option in 0006 M1-CLI and required by 0006 M2-BOOTSTRAP-LAYER "no role assumes it owns root"). A
  standalone app runs its own kcp (bootstrap layer, base `root`); a composed app gets a nested base
  workspace in the composer's kcp. The app code cannot tell the difference, and its credentials are scoped
  to its own subtree, so "never share a kcp" holds at the API: no app can read another app's objects.
- **Component**: one of today's in-process servers or loops, now a reconciled child (DenoPod, HostProcess,
  OpenBaoServer, ComputeVM, ComputeContract) or a deliberate in-process part of another component.
- **Profile**: a typed values file selecting the components and their values for one mode (prod-qr, market network,
  dev-local, harness).
- **Surface**: what a human or another app touches: CLI flags/cli-args-env options, the foreground CLI's
  rendering of status (QR, prompts), the door UI, xrpc endpoints on the market protocol.

## 2. Inventory: every process that starts servers today

### 2.1 hono-bidder (am/hono-bidder/mod.ts, 825 lines; options am/hono-bidder/cli-args-env.ts, 54 options)

| part | file:line | listener / relay / loop | optional by |
|---|---|---|---|
| fetch patch `https://*.localhost` -> `http://host:port`, plc.directory -> local PLC | mod.ts:74-95 | none (global fetch rewrite) | auto when ingress host or PLC is local |
| sign-in: browser OAuth | :138-184 | loopback callback server 127.0.0.1:<redirect port or 0> (:175), xdg-open/open/cmd spawn (:166-171) | `--atproto-oauth` + `--atproto-handle` |
| sign-in: QR OAuth | :185-261 | PLC genesis for the bidder's own did:plc (:190-209), QR to stdout (:229-234), poll qr.fedfork.com (:239-244), token auto-refresh (:247-251) | `--atproto-oauth-qr` |
| sign-in: handle+password | :262-282 | in-process repo + relay (NOT a remote PDS; see 0. item 2) | `--atproto-handle` + `--atproto-password` |
| sign-in: default local key | :283-358 | in-process repo (createRepoFactory, agent.ts:341-352) on a relay-only serve (agent.ts:356-369, directSubscriptionHandler for subscribeRepos), association QR + stdin prompt (:304-356) | default; `--skip-qr` skips the prompt |
| atproto facade | :359-364 | none | always |
| badgeBlueKeys attestation bind, associate-with record | :374-417 | one-shot writes | `--associate-with` |
| event streams client | :419-435 | websockets to relays/jetstreams (DEFAULT_RELAY_URLS unless `--firehose-url`) | always; `--firehose-mode` |
| key persistence | :469-482 | writes `--private-key-hex-path` | path set |
| provider: DigitalOcean | :490-516 | serve (relay-only) + relay (fresh keypair :104) + OIDC issuer | `--compute-provider-digitalocean-token` |
| provider: local (vm/container) | :518-544 | serve + relay + OIDC + JSR registry; docker/container guests | `--compute-provider-local` |
| provider: firecracker | :546-600 | serve + relay; OIDC issuer mounted onConnected (hcp .../compute-provider-firecracker/mod.ts:904-925); /v1/on-network (:807-861); JSR registry TCP 0.0.0.0:0 (:579-588); per-guest docker runner containers (microvm-firecracker) | `--compute-provider-firecracker` (+7 required paths, :547-565) |
| provider: deno worker | :602-613 | no serve; Workers in-process | `--compute-provider-deno-worker` |
| market serve | :619-631 | TCP `--serve-addr:--serve-port` (default 0), optional unix socket, optional TLS, `--port-file`, + market relay (fresh keypair) | `--no-ingress-proxy` drops the relay |
| oauth client metadata route | :634-641 | GET on market serve | always (it is also the market network's readiness probe, 70-bidder.yaml:123-130) |
| market bidder | :657-670 -> am/lib/market-bidder/mod.ts | market factory routes (:472), /v1/on-network (:475), watches BADGE_BLUE_KEYS/VOUCH/ASSOCIATION, RFP, ACCEPT, EVENT (:517-586), offering refresh timer (:635-663), activeContracts in memory (:175) | always |
| signals | :672-681 | SIGINT/SIGTERM -> shutdown | always |
| test handshake | :686-691 | `{"event":"bidder_ready", did, ingressRef, servePort}` on stdout | always |
| post-start one-shots | :693-825 | association QR again (:714-752), ensureImage (:755-759), requestCrawl to local relays (:761-772), refreshOffering (:776-781), requestCrawl all + verifyRelayVisibility polling (:787-825) | per mode |

State: repo key file (`private-key-hex-path`, default under $HOME, cli-args-env.ts:235-240), repo Deno KV
(`pds-state-path` default `$HOME/.cache/pdr-market/bidder-pds`, :241-246, computed with Deno.env.get in the
options file), QR session JSON (`$HOME/.cache/pdr-market/oauth-qr-session-*.json`,
am/lib/atproto-helpers/oauth-qr-session.ts:9-20), guest work dirs (`--compute-provider-firecracker-work-root`);
in memory: activeContracts (market-bidder:175), acceptToContract (mod.ts:488), the provider's guests Map and
network index (firecracker :566, :643), relay keypairs (:104), OIDC signing key (oidc-issuer-hono:53-57).
External services: PLC directory, the dispatcher (ingress proxy), relays/jetstreams, the user's PDS (OAuth
modes), qr.fedfork.com (QR modes), docker (runner containers), /dev/kvm through the runner.

Modes in the wild: production `socialweb-computer-bidder@<handle>` (swck/deploy/bidder/
socialweb-computer-bidder-run:75-111: QR, firecracker, `--pds-state-path` passed but unused in QR mode),
org-root `bob-bidder.sh` (QR, firecracker or local container), the market network DenoPod (70-bidder.yaml:24: local key,
`--skip-qr`, `--policy open`, firecracker, fixed ports), and the in-process harnesses (2.9).

### 2.2 request-vm-ssh (am/request-vm-ssh/mod.ts, 555 lines; 40 options)

| part | file:line | listener / relay / loop |
|---|---|---|
| fetch patch for *.localhost | :48-60 | global rewrite |
| secrets capability | :99-104 -> am/lib/guest-capability-secrets/mod.ts:50-75 | PER CONTRACT: a fresh keypair, its own relay and serve (secrets server for the guest) |
| transport binaries | :107-117 | downloads dumbpipe (iroh) or websocat |
| serve | :125-130 | TCP only if `--port`, optional unix, port file |
| sign-in: browser OAuth | :164-201 | createOAuthRequester (no local repo), loopback callback |
| sign-in: QR | :202-320 | PLC genesis, QR, poll, then createRequesterPDS + applyOAuthAgentToRequesterPDS (am/lib/requester-xrpc/mod.ts:1099) so records are written as the user |
| sign-in: local key | :321-345 | createRequesterPDS (requester-xrpc:168; repo KV :276-278; relay :309-311) |
| firehose watchers | :347-363 | offerings + own events |
| repo serve begins only when not OAuth | :365-367 | so in QR mode the ephemeral repo is never served |
| association wait | :430 | stdin prompt like the bidder |
| signals / hold | :481-495 | first signal releases a held VM (vm.delete) |
| contract | :516 runComputeContract -> compute-request-xrpc requestCompute :823, watches :495-575, awaitComputeNetwork :1705, releaseCompute :1768 | ssh session in the same process (createSshSessionProvider) |

State: optional key file, optional repo KV, OAuth session file
(`$HOME/.cache/pdr-market/requester-oauth-session.json`, :168-169, Deno.env.get in the CLI). Contracts are in
memory only. Modes: QR (`alice-request.sh`: `--no-ingress-proxy --firehose-mode subscriberepos
--atproto-oauth-qr`), browser OAuth, local key; per run `--hold`, `--keep-vm`, `--skip-ssh`, `--exec`,
`--user-data-transport iroh|websocket`, `--secrets`.

### 2.3 market-requester, the market network's requester (swck/deploy/market-network/market-requester/mod.ts, 163 lines)

fetch patch (:82-95); serve TCP 127.0.0.1:0 (:111); createRequesterPDS local key (:112-119); a no-ssh
session provider that records onNetwork and never connects (:122-132); runComputeContract (:135-144);
verdict to `--result-file` (:69-75). Run on the host by accept.sh:497-516 with the fixed
`--plc-directory-url https://127.0.0.1:2587 --ingress-proxy-host relay.localhost:2588` and a bidder DID
scraped from the bidder's stdout (:463-467). It is the RFP spine of every market network run.

### 2.4 The gateway today (container) and the door

swck/deploy/container-entrypoint.sh supervises: root vault `bao server` (:222), `kcp start` (:227),
socialweb-gateway (Go, :284; cmd/socialweb-gateway/main.go: kcp rest config :72, HTTP server :180-197),
socat TLS terminator (:290), socialweb-clusterprovider (Go, :295), and the door
`deno run -A ... hono-socialweb-computer-k8s/mod.ts` (:298-317, STATE_DIR=$STATE_DIR/door, port 8788).
0006 M3-SERVE-GATEWAY-KCP replaces this with the binary; 0008 M3-DOOR-DENOPOD moves the door into a DenoPod.

The door (door/hono-socialweb-computer-k8s/mod.ts, 240 lines):

| part | file:line | listener / relay / loop / state |
|---|---|---|
| state dir | :43-45 | `oauth-sessions.json` (:50), `requester-key.hex` (:72) |
| test seams | :53-67 | TEST_OWNER_SESSIONS_FILE (password sessions on the market network's alice PDS, live_m3_test.go:36-41), TEST_NODE_GUEST_HOST_ALIASES, TEST_NODE_USER_DATA_BASE_FILE |
| per-account requesters | :69-80 -> lib/socialweb-computer-requester-inproc/mod.ts:155, :172-183 | one createRequesterPDS per account, each its own serve on 127.0.0.1:0 + relay; per-account event streams (:288-316) |
| gateway client | :82-93 | service auth minted per account |
| serve | :115-124 | TCP `--http-port` (8788 in the container), port file |
| web: OAuth, cluster API, pinned downloads, static | :126-153 | routes |
| debug node ssh | :157-180 | ssh-keygen for a DEBUG operator key (log-only helper, not a provisioning path) |
| market node fulfiller | :186-210 -> lib/socialweb-computer-k8s-node-market/mod.ts | NodeClaims over kcp REST, poll timer (:253), requestCompute + a per-claim secrets capability (:238-243, :383) whose keypair is in memory (lost on restart, :491; 0007 M6-CLAIM-RESUME) |
| signals | :239-240 | |

### 2.5 The atproto network services (each a single-server CLI today)

| CLI | file:line | listener | state | market network DenoPod |
|---|---|---|---|---|
| hono-plc | am/hono-plc/mod.ts:16-41 | TCP 2587 default, port-file ./port-bound-to | MemoryPlcStore (in memory) | 20-global-plc.yaml |
| hono-atproto-relay | atproto-relay/hono-atproto-relay/mod.ts:56-68 | TCP 2584 | relay index | 30-relay-relay.yaml |
| dispatcher (did-key-ingress-proxy) | did-key-ingress-proxy/hono-did-key-ingress-proxy/mod.ts:58-80 | TCP 8080 default, optional unix; served twice in the market network (plain 2588, TLS 2589 for guests, 35/36-*.yaml) | subscribers in memory: a restart drops every app's registration until its relay reconnects | 35, 36 |
| dispatcher subscriber CLI | .../hono-did-key-ingress-proxy-subscriber/mod.ts:51-57 | relay client, password login | keypair file | none |
| hono-pds | hono-pds/main.ts:33, :61, :120-128 | TCP 2583/2585, OAuth authorization server built in (:61) | DenoKvStorage.create() with no path (Deno's default KV location, port-audit B15) | 40-alice-pds, 60-bob-pds |
| QR session relay (qr.fedfork.com) | did-key-associator/hono-backend/mod.ts:117-122, :147-239, :251-258 | TCP | Deno KV (KV_PATH via Deno.env.get) | none (external in every mode) |

### 2.6 Other CLIs that host servers (not in scope, listed for completeness)

hcp/hono-compute-provider/mod.ts:43-87 (standalone local/DigitalOcean provider factory on one serve);
am/hono-compute-contract-gateway/mod.ts:23-73 (a central gateway with its own requester repo; its README
starts a PDS by hand); socialweb-computer-ssh/hono-socialweb-computer-ssh (later: 0008 M4-RUN-GATEWAY-SSH).

### 2.7 The helpers every one of them composes

- createServe (typescript-helpers/lib/serve/mod.ts:63-179): tcp and/or unix and/or relays; onListen records
  the bound port (:114-127) and writes `portFile` (:131-136); `onConnected` hooks run after relays connect
  (:167). This is the single place a "report my listeners" hook belongs.
- createIngress (am/lib/did-key-ingress-proxy/mod.ts:38) is what CLAUDE.md calls cliCreateIngress: a relay
  subscription whose ingressRef is server-assigned; requests arrive in-process into `serve.app.fetch`, so a
  relay cannot be split from the server that answers it.
- createLocalPDSAgent (agent.ts:276-436): PLC genesis with the market service endpoints (:293-334), the repo
  (:341-352), its relay using the BIDDER key (stable subdomain, unlike the providers' relays), the
  associateConfirm route (:382-418).
- createRequesterPDS (requester-xrpc:168), guest-capability-secrets (:50-75), oidc-issuer-hono (module
  globals :53-77).

### 2.8 The market network (swck/deploy/market-network)

Four workspaces root:global (PLC), root:relay (relay, dispatcher, dispatcher-tls), root:alice (PDS, verifier),
root:bob (PDS, bidder), each with a deno-kcp OpenBao object for its TLS intermediate (README.md:7-22). Every
manifest repeats a ~60-line supervisor script (70-bidder.yaml:35-92) that runs `deno run -A` (:82) on an
absolute host path (:14-15) with fixed ports (:24, :34). Names resolve through the KCP_SHIM preload with a
DNS table from peers' SERVICE_ARGS (README.md:119-137) and, on a miss, by reading OTHER workspaces with one
token per workspace (README.md:128-130): discovery crosses workspace boundaries today. apply.sh renders with
sed (port-audit B10); accept.sh starts kcp, kine, OpenBao and the provider on probe-then-bound ports
(:76-91), applies, checks every pod, scrapes the bidder DID from stdout (:463-467) and runs market-requester
(:497-516). internal/marketnetwork/live_test.go drives accept.sh (:147) behind the one-network-per-host port check
(:18, :112-120); live_m3_test.go adds the control-plane container with socat bridges to the fixed ports
(:564-600).

### 2.9 In-process multi-server test harnesses

am/test/bidder_container_integration_test.ts (415 lines) runs, in ONE deno process: a fake PLC (:85,
served on port 0 :198), the dispatcher factory served twice (plain + TLS with a generated localhost CA,
:169-193), a fetch interceptor (:205-208, fetch-interceptor.ts), a bidder with createLocalPDSAgent on a
relay-only serve (:220-223), a container-mode local provider with its own serve + relay (:250-264),
createMarketBidder on a port-0 serve + relay (:296-302), and a requester (createRequesterPDS, :308-312) that
drives runComputeContract (the canonical RFP harness). The same shape is copied by
bidder_firecracker_integration_test.ts, bidder_ssh_relay_test.ts, bidder_cross_platform_integration_test.ts,
bidder_policy_only_me_integration_test.ts, gateway_*_integration_test.ts, registry_discovery_test.ts,
badge_blue_keys_binding_test.ts, oauth_session_transfer_test.ts; bidder_prod_integration_test.ts instead
spawns the hono-bidder CLI against real plc.directory and xrpc.fedproxy.com (:78). door/test/
live_market_node_test.ts (965 lines) adds a live k3s join. Every one of them composes components through the
same libraries the CLIs use, but each composition is hand-written again.

### 2.10 Findings that change the plans

| # | finding | evidence | consequence |
|---|---|---|---|
| F1 | market network bidder ignores bob's PDS; password path builds a local repo; pds-url unused | 70-bidder.yaml:24; mod.ts:10, :262-282; cli-args-env.ts:76-81; accept.sh:412-414 | a "market network bidder with its own PDS pod" profile needs M3-BIDDER-ACCOUNT-PDS |
| F2 | interactive QR/association/browser via stdout/stdin/xdg-open; expiry exits the process | mod.ts:133, :166-171, :229-234, :348, :743; request-vm-ssh :430 | MarketIdentity pending actions rendered by the foreground CLI / door |
| F3 | fresh relay keypair per relay per process; OIDC key in memory and module-global | mod.ts:104; oidc-issuer-hono:42-77, :616 | 0007 M4-BIDDER-VAULT must cover every relay key; new M3-OIDC-ISSUER-INSTANCE |
| F4 | one TCP serve + a TCP JSR registry per firecracker/local provider (0.0.0.0:0) and 1 + P relays per bidder process | 2.1 | per-component status.listeners; a guest-facing component split is possible once keys are in the vault |
| F5 | address discovery by fixed ports, stdout scraping, four fetch patches, cross-workspace shim lookups | 0. item 5; README.md:128-130 | port 0 + status.listeners; one market-network adapter; values across apps |
| F6 | per-user state under $HOME shared by every instance | cli-args-env.ts:235-246; oauth-qr-session.ts:9-20; request-vm-ssh :168-169; port-audit B11 | state dir per app instance; the DenoPod must not pass HOME through |
| F7 | QR-mode requester never begins its ephemeral repo's serve | request-vm-ssh :365-367 | the requester's QR profile has no repo component at all |
| F8 | the door already keeps NodeClaims in kcp; sessions, requester key and secrets keys are not | door mod.ts:50, :72, :189-210; node-market :383, :491 | gateway vault items; M6-CLAIM-RESUME stays the owner of per-claim keys |
| F9 | hono-bidder is a 825-line CLI composing everything | mod.ts | a composer (hono-factory layer) is the seam that lets the same components run in one process or as DenoPods |
| F10 | the deno-kcp provider stops every pod after --pod-timeout 5m by default | accept.sh:57-61 | already 0008 M2-DENOKCP-INPROC (timeout negative) |

## 3. Mode matrix

### 3.1 Bidder

| component (today's in-process part) | prod-qr (hat-8 bidder@handle) | market network (70-bidder.yaml) | container harness (test) | dev-local (bob-bidder.sh, standalone deno) |
|---|---|---|---|---|
| identity | user account via QR OAuth + own did:plc for service auth | local key, own did:plc on the market network PLC | local key on fake PLC | QR (default) or local key |
| repo | user's PDS (remote) | in-process repo via dispatcher relay (bob PDS pod unused) | in-process repo, relay-only | user's PDS or in-process repo |
| market serve + relay | yes, fixed serve port from launcher | yes, port 2586 | yes, port 0 | yes, port 0 |
| provider(s) | firecracker (docker runner) | firecracker | local container | firecracker or local |
| provider serve + relay + OIDC + on-network | yes | yes | yes | yes |
| guest JSR registry | yes (0.0.0.0:0) | yes | yes (container provider) | yes |
| loopback OAuth callback | no (QR) | no | no | only `--atproto-oauth` |
| firehose / event streams | default relays + jetstream | market network relay | none (direct) | per flags |
| association prompt | no (QR) | skipped (`--skip-qr`) | n/a | yes in local-key mode |
| external | plc.directory, dispatcher, relays, user PDS, qr.fedfork.com, docker, kvm | market network PLC, market network dispatcher(+tls), market network relay, docker, kvm | none (all in process) | as prod |
| state | key, session, guests dir; in-memory contracts | run dir (key, repo KV) | memory | $HOME/.cache/pdr-market |

### 3.2 Requester

| component | prod-qr (alice-request.sh) | browser OAuth | market network (market-requester) | door (per account) | harness |
|---|---|---|---|---|---|
| identity | user account via QR + own did:plc | user account | local key on market network PLC | user's door OAuth session (or test password session on alice PDS) | local key, fake PLC |
| repo | none served (F7) | none | in-process repo + relay | in-process repo per account + relay | in-process |
| contract watcher (firehose / submitEvent) | yes | yes | yes | yes | yes |
| per-contract secrets server + relay | if `--secrets` | if `--secrets` | no | yes (join secrets) | per test |
| ssh session | yes (iroh or websocket ProxyCommand) | yes | no (onNetwork only) | no (debug key log only) | some tests |
| state | session file; contracts in memory | session file | result file | NodeClaims in kcp; sessions file; keys in memory | memory |

### 3.3 Gateway

| component | prod (container today -> `serve gateway kcp`) | m3 market network (live_m3_test.go) | dev quick |
|---|---|---|---|
| kcp (bootstrap) | blue/green slots, pg, front 127.0.0.1:6443 (0006) | container kcp | sqlite single slot |
| root vault | OpenBaoServer | container bao | dev vault |
| socialweb-gateway, clusterprovider (Go, in-process) | yes | yes | yes |
| tenant clusters (traditional kcp use, 4.6) | per claim | one (m3) | stand-ins |
| door | DenoPod (0008 M3) on 8788 behind caddy | in container, socat bridges to market network | DenoPod port 0 |
| door accounts | real users' OAuth sessions | TEST_OWNER_SESSIONS_FILE on alice PDS | test sessions |
| market node fulfiller | yes | yes | yes |
| external | real PLC/relay/dispatcher, user PDSes, bidders on the market | the market network network + market network bidder | a market network |

## 4. Decomposition proposal

### 4.1 The rule: object, own component, or in-process

1. **Application state is an object** when another process, a restart, or a human needs it: identity
   (who am I, am I signed in, what must the user do), contracts (requester ComputeContract, gateway
   NodeClaim, bidder ComputeVM), the active profile and the components it implies, every listener's
   reported address.
2. **A server is its own component** (DenoPod or HostProcess) when it (a) holds state or an address others
   depend on across a restart of the rest, (b) must restart or upgrade independently, (c) carries
   process-global state that clashes with a sibling (F3), or (d) is optional per profile (a PDS, a PLC).
3. **It stays in-process** when its requests arrive through a relay into one Hono app (a relay and the app
   that answers it are one component), or it is a pure loop over that component's own state (offering
   refresh, firehose watchers of the bidder core), or it is the user's terminal (ssh session, QR rendering,
   prompts belong to the foreground CLI, never to a pod).
4. **Secrets never live in kcp objects**: keys, relay keypairs, OAuth sessions and DPoP keys live in the
   application's OpenBao; objects hold references (vault path) and non-secret status.

### 4.2 Kinds

| kind | group / owner | status | holds |
|---|---|---|---|
| DenoPod | deno.computer (owned fork, 0008 M2-DENOKCP-FORK) | exists; gains status.listeners (new M2-DENOPOD-LISTENERS) | TypeScript components |
| HostProcess | socialweb.computer (0006 M2-HOSTPROCESS-KIND) | planned | host processes that outlive the core |
| OpenBaoServer | 0006 M2-OPENBAO-SERVER | planned | each app's vault |
| ComputeVM | 0007 M1-API | planned | bidder's guests |
| ComputeContract | 0008 M4-RUN-REQUEST | planned | requester's contracts |
| NodeClaim, Cluster, Node, ClusterLink | swck api/v1alpha1/resources.go:17-22 | exist | gateway (NodeClaim = its contracts; the rest = traditional exception) |
| AtprotoIdentity | resources.go:22 | exists | the gateway's RBAC identities (revocation) - NOT an app's own identity |
| **MarketIdentity** | new (0007 M4-MARKET-IDENTITY) | proposed | an app's own atproto identity: mode, did, sign-in phase, pending action, vault refs |
| **MarketBidder / MarketRequester / Gateway / MarketNetwork** | new root objects (0006 M2-APP-CONTROLLER) | proposed | spec = the app's options + profile name; status = aggregated component readiness, did, listeners |

Root objects rather than "the CLI applies a pile of DenoPods" because a profile switch must REMOVE what the
new profile omits (prod-qr has no PDS; the app controller deletes the PDS DenoPod it owned), and because the
CLI and the door need one status to watch.

### 4.3 Bidder application

Objects in its base workspace (prod: the handle's own kcp, one slot on kine-sqlite, 0006/0007 decided;
market network: `<market network>:apps:bidder-<n>`):

| object | reconciled by | prod-qr | market network | dev-local |
|---|---|---|---|---|
| MarketBidder `bidder` (root) | core app controller (Go) | yes | yes | yes |
| MarketIdentity `self` | bidder core DenoPod (signing needs the session in-process) | mode oauthQr | mode pdsAccount (market network PDS) or localRepo | oauthQr or localRepo |
| OpenBaoServer `vault` (HostProcess) | core (0006 M2-OPENBAO-SERVER, 0007 M4-BIDDER-VAULT) | yes | yes (dev vault) | yes |
| DenoPod `bidder` (hono-bidder core: market serve + relay, market bidder loops, provider + its relay, OIDC issuer, on-network) | owned deno-kcp provider | yes | yes | yes |
| DenoPod `pds` (hono-pds) | provider | NO (user's PDS) | yes, the bidder's account | no |
| DenoPod `guest` (OIDC issuer, on-network, host-keys, provider relay) | provider | phase 2 (M5-BIDDER-COMPONENTS) | phase 2 | phase 2 |
| DenoPod `guest-jsr` (hono-jsr over the embedded closure) | provider | phase 2 | phase 2 | phase 2 |
| ComputeVM per guest -> HostProcess | Go ComputeVM controller (0007 M2-CONTROLLER) | yes | yes | yes |

The bidder core stays one DenoPod in phase 1 (what 0007 M3-TS-PROVIDER and M5-RUN-BIDDER already plan);
the split in phase 2 is justified by 4.1 (a): guests dial the provider relay for OIDC token exchange and
on-network for their whole life, so a bidder-core restart should not move or interrupt it; it becomes
possible only after the relay keypair and JWK are in the vault (M4-BIDDER-VAULT) and the OIDC issuer has no
module-global state (M3-OIDC-ISSUER-INSTANCE). Multiple providers in one bidder (provider array) remain one
core DenoPod whose `providers[]` come from MarketBidder.spec; under `run bidder` the kcp provider is the
default member (0007 M3-BIDDER-FLAG).

### 4.4 Requester application

0008 M4-RUN-REQUEST already decides ComputeContract objects and a thin hono-compute-request CLI. Under the
lens the requester is a persistent application, not a per-invocation kcp: `run request` adopts the user's
requester app (one slot on kine-sqlite like the bidder; bootstrap layer) and applies ComputeContracts to it.

| object | reconciled by | prod-qr | market network |
|---|---|---|---|
| MarketRequester `requester` (root) | core app controller | yes | yes |
| MarketIdentity `self` | requester DenoPod | oauthQr (no repo served, F7) | localRepo or pdsAccount (alice PDS) |
| OpenBaoServer `vault` | core | yes | yes |
| DenoPod `requester` (hono-compute-request: identity, firehose/contract watchers, per-contract secrets servers + relays, writes status.contract) | provider | yes | yes |
| ComputeContract per request | requester DenoPod | yes | yes |

The ssh session is NOT a component: `run request --ssh <name>` is the foreground surface; it waits for
`status.contract` onNetwork + hostKeys (0007 M4-HOST-KEYS) and runs ssh with the ProxyCommand in the CLI's
own process. Per-contract secrets servers need their keypair in the vault to survive a DenoPod recreate -
the same decision the door gets in 0007 M6-CLAIM-RESUME.

### 4.5 Gateway application

| object | reconciled by | prod | market network (m3) |
|---|---|---|---|
| Gateway `gateway` (root) | core | yes | yes |
| bootstrap: kcp pg, kine, slots, front | installed units / devsup (0006 M2-BOOTSTRAP-LAYER) | yes | sqlite or pg |
| OpenBaoServer `root-vault` | core | yes | yes |
| socialweb-gateway, clusterprovider | in-process Go (0006 M3-SERVE-GATEWAY-KCP) | yes | yes |
| DenoPod `door` (web, cluster API, downloads, per-account requesters, node fulfiller) | provider (0008 M3-DOOR-DENOPOD) | yes | yes |
| MarketIdentity per door account (mode oauthDelegated: the door holds the user's session) | door DenoPod | yes | password sessions on the market network PDS |
| NodeClaim (the gateway's contracts) | door node fulfiller | yes | yes |

Door state moves: oauth-sessions.json and requester-key.hex into the root vault (new 0008 M3-DOOR-STATE-VAULT);
per-claim secrets keys stay 0007 M6-CLAIM-RESUME. A later split of the node fulfiller (a NodeClaim
reconciler) from the web door into its own DenoPod is natural under 4.1 (b) but not needed now (question Q8).
The TEST_* seams (door mod.ts:53-67) become values of the gateway's market-network profile instead of env variables.

### 4.6 The exception: virtual Kubernetes clusters (traditional kcp use)

clusterprovider's tenant k3s control planes, Cluster/Node/ClusterLink objects and tenant workspaces use kcp
the traditional way: a control plane for Kubernetes clusters, members as HostProcesses in the cluster's home
workspace (0006 M3-CLUSTER-UNITS, M3-CP-SLOTS). Nothing in this document's application model changes it.
The only meeting point is the NodeClaim: the gateway application rents market nodes for those clusters as an
ordinary requester over the atproto market protocol.

### 4.7 The market network application and the market network

The market network's shared atproto services are one application, MarketNetwork, whose components are DenoPods `plc`
(hono-plc), `relay` (hono-atproto-relay), `dispatcher` and `dispatcher-tls` (hono-did-key-ingress-proxy),
optional `pds-<name>` (hono-pds), and the verifier DenoJob. Its status publishes the URLs other apps need
(plcDirectoryUrl, relay URL, ingressProxyHost, guest TLS URL, CA bundle ref, PDS URLs per account).

`dev up --profile market-network` = compose: apply MarketNetwork into `<instance>:apps:network`, then N MarketBidders
into `<instance>:apps:bidder-<n>` and one MarketRequester into `<instance>:apps:requester` (plus a Gateway
into `<instance>:apps:gateway` for m3-shaped runs). The composer copies the network's status URLs into each
app's MarketNetwork VALUE; no app reads the network's workspace (today's shim does, README.md:128-130).
Inside one app, components resolve each other by service name through the shim; across apps only through
those values and the atproto protocol. The market network adds no provisioning path: guests come up only through the
requester app's ComputeContract -> runComputeContract -> RFP -> bid -> accept -> cloud-init against a market network
bidder app.

### 4.8 How components find each other

- Every listener binds port 0. createServe gains a listener report: on onListen and after each relay
  connects, it writes `<runDir>/listeners.json` `{name: url}` (tcp URL, unix path, relay ingressUrl). The
  owned deno-kcp runner reads it on observe and sets `DenoPod.status.listeners[]`; kcp-libs
  servicenames.AdvertisedAddress prefers status over SERVICE_ARGS (port-audit B2, B3). The DenoPod status
  today has Outputs and Ready but no address (deno-kcp/api/v1alpha1/types_denopod.go:48-68).
- The app controller aggregates children's listeners into the root object's status (MarketBidder.status:
  did, marketIngressUrl, issuerUrl, servePort). accept.sh's DID scraping and the `bidder_ready` stdout line
  are replaced by reading that status.
- Only stable public edge entry points keep known addresses (caddy 443, the gateway's kcp front 6443, the
  door's 8788 behind caddy) - the user's port 0 rule of 2026-10-12.
- One `market-network` adapter (typescript-helpers, used by atproto-market, socialweb-computer-k8s and the
  market network requester) replaces the four fetch patches: it takes the MarketNetwork value (or flags in standalone
  mode) and does the `https://*.localhost` and plc.directory rewrites in one tested place.

### 4.9 Where keys and sessions live

| secret | today | proposed |
|---|---|---|
| bidder repo key | file (`--private-key-hex-path`) or env | app vault KV `bidder/repo-key` (0007 M4-BIDDER-VAULT) |
| relay keypairs (market, each provider, PDS relay) | fresh per process (mod.ts:104) | vault KV, one per relay name, created with CAS 0 (M4-BIDDER-VAULT, widened) |
| OIDC signing JWK | memory, module-global | vault KV `oidc-jwk` + per-instance issuer (M3-OIDC-ISSUER-INSTANCE) |
| QR / browser OAuth session + DPoP key | `$HOME/.cache/pdr-market/*.json` | vault KV `identity/session`; MarketIdentity.spec.sessionRef |
| requester key, per-contract secrets keys | file / memory | requester app vault |
| door OAuth sessions, requester key | state dir JSON / hex | gateway root vault (M3-DOOR-STATE-VAULT) |
| per-claim secrets keys | memory | root vault (0007 M6-CLAIM-RESUME, decided) |

The DenoPod gets a short-lived vault token through a token file the core renews (already the
M4-BIDDER-VAULT shape). Standalone deno keeps the file paths (no vault), under a per-instance state dir, never
$HOME defaults computed inside options files.

### 4.10 Tests as "apply a manifest set into a fresh workspace"

- Go system tests use one helper, `internal/apptest.Start(t, app, profile, values)`: create a fresh
  workspace under the dev instance's base (`<instance>:t:<test>-<rand>`), apply the app's set with the
  profile, wait for the root object's Ready, return its status (URLs, DIDs). A market network test is
  `apptest.Start(t, "market-network", ...)`, which composes 4.7. Deleting the workspace removes everything.
- N parallel market networks = N dev instances (0006 M1-DEV-INSTANCE, one kcp each); N bidders in one market network = N bidder
  apps in that market network. Nesting under a parent (0006 M4-DEV-PARENT-KCP) is the later, cheaper shape for many
  short market networks.
- The RFP spine is unchanged: every live guest in such a test comes from the requester app's
  ComputeContract (runComputeContract) against a market network bidder app.
- TypeScript in-process harnesses (2.9) stay as fast library tests, but compose through the same composer
  the DenoPods use (M3-BIDDER-COMPOSER), so a component split cannot pass the harness and fail the market network.

## 5. CLI and surface model

### 5.1 One pattern for "a CLI asks kcp for X and kcp reconciles it"

1. Resolve the app instance: `--kcp-base-workspace` + either its own kcp (standalone app: adopt or start the
   bootstrap layer, 0006 M2-BOOTSTRAP-LAYER) or an enclosing app's kcp (`dev`, market network composer).
2. Server-side apply the root object (spec from cli-args-env-equivalent options + `--profile`), field
   manager = the verb.
3. Watch the root object's status; render pending actions (MarketIdentity.status.pendingAction: QR url,
   association approval, session expired) in the terminal; write approvals back into spec.
4. Print outputs from status (did, URLs, contract state) and exit, or stay in the foreground for a session
   (`run request --ssh`) or `--fg`.
5. No app verb opens a listener, a relay, or a guest itself; components are always children reconciled by
   the core. (Test: scan the verbs' Go for listeners/Deno spawns outside the core.)

### 5.2 Per CLI

| CLI | its kcp holds | reconciled by | components per profile |
|---|---|---|---|
| `socialweb-computer run bidder --handle h --profile prod-qr` | MarketBidder, MarketIdentity(oauthQr), OpenBaoServer, DenoPod bidder, ComputeVMs (+HostProcesses) | core: app controller, supervisor, ComputeVM controller, deno-kcp provider | bidder core, vault, VMs; no PDS |
| `run bidder --profile market-network` (inside `dev up --profile market-network`) | as above + DenoPod pds, MarketIdentity(pdsAccount) | same | + PDS pod; network values from the market network |
| `run bidder --profile dev-local` | as prod-qr, local-key identity allowed | same | bidder core, vault, VMs |
| `socialweb-computer run request [--ssh] [--profile qr|market network]` | MarketRequester, MarketIdentity, OpenBaoServer, DenoPod requester, ComputeContracts | core + requester DenoPod | requester; ssh in the CLI process |
| `socialweb-computer serve gateway kcp [--profile prod|market network]` | Gateway, OpenBaoServer root, DenoPod door, NodeClaims, door MarketIdentities; plus the traditional cluster objects | core (gateway + clusterprovider in-process), door DenoPod | door, vault, Go members; clusters per claim |
| `socialweb-computer dev up --profile market-network` | the instance's base with the composed apps (4.7) | the instance's core | network app, N bidder apps, requester app (+ gateway) |
| `deno run hono-bidder` / `request-vm-ssh` / `hono-socialweb-computer-k8s` (no kcp) | nothing | nothing (library composition) | all components in one process, file-backed stores, flags instead of MarketNetwork |

### 5.3 Standalone deno without kcp: keep it, as the library composition

Keep `deno run hono-bidder` and `request-vm-ssh` working without kcp (recommended): they are the DenoPod
payloads in phase 1 anyway, the TypeScript suites and the in-process harnesses depend on them, and the
firecracker provider (the gold standard, 0007) is standalone. Rules: (1) no embedded kcp in TypeScript;
(2) kcp-dependent pieces are options that select an implementation (ContractStore file vs ComputeVM,
JwkStore memory/file vs vault, identity prompt stdin vs status) - sibling packages per the ABC rules, never
if-branches; (3) the composer takes a component spec with each component `inproc` or a URL, so the same
code runs all-in-one (standalone, harness) or as the DenoPod of one component (kcp). A thin "kcp mode" of
hono-bidder is just these options set by the DenoPod template (0007 M3-BIDDER-FLAG already decides the
bidder does not sniff its environment).

### 5.4 Surfaces mapped to objects

| surface | today | under kcp |
|---|---|---|
| cli-args-env operator options (policy, offering refresh, providers, firehose, guest TLS) | flags of the one process | MarketBidder.spec fields; the template renders them as flags of the component that uses them |
| network options (plc url, ingress host, relay url, ca) | flags + fetch patches | MarketNetwork value (profile or composer) |
| state paths (key, pds-state, session, port file) | flags with $HOME defaults | gone from the surface: run dir + vault refs |
| QR, association y/N, browser OAuth | stdout / stdin / xdg-open | MarketIdentity.status.pendingAction rendered by `run bidder|request` in the foreground or by the door UI; approval = spec patch; browser OAuth's loopback listener stays in the foreground CLI only |
| readiness / "who am I" | stdout lines, log grep | root object status |
| xrpc endpoints on the market protocol | relays into the process | unchanged wire; served by the component that owns the relay, its URL in status |
| door UI | reads its own memory and files | reads NodeClaims (already) and MarketIdentity status |

## 6. Gap list and item deltas to fold into 0006, 0007, 0008

Gaps in the current plans (what none of them says today):

- G1 No app model: no root object per application, so a profile cannot remove a component and the CLI has
  no single status to watch (0006 M1-CLI lists verbs; M2-MANIFESTS lists sets; nothing ties a verb to a set
  + profile).
- G2 No identity object and no plan for the interactive steps under a DenoPod (F2); 0007 M5-RUN-BIDDER moves
  the QR guard to Go but not the QR itself.
- G3 0007 M4-BIDDER-VAULT names repo-key, relay-keypair and oidc-jwk (singular) but the bidder has 1 + P
  relays (F4) and the QR session is not named.
- G4 The market network's "bidder with its own PDS" profile is impossible today (F1).
- G5 Port 0 has no reporting path into kcp status (0008 M1-MARKET-NETWORK-PARALLEL says "read back where the service
  reports its port (onListen logs)", which is log scraping).
- G6 No plan retires the four fetch patches or the cross-workspace shim lookup (F5).
- G7 0008 M4-RUN-REQUEST starts a kcp per invocation; under the lens the requester is a persistent app.
- G8 The door's own state (sessions file, requester key) has no vault item (F8); M6-CLAIM-RESUME covers
  only per-claim keys.
- G9 hono-bidder's composition is a 825-line CLI (F9); no item extracts a composer, so a component split
  would fork the code paths between standalone, harness and DenoPod.
- G10 oidc-issuer-hono's module-global state (F3) blocks both multiple providers per process and the guest
  component split.
- G11 Tests: 0008 M4-MARKET-NETWORK-PORT replaces accept.sh but not the log scraping, and no helper makes "apply a
  set into a fresh workspace" the one way system tests start apps.

Item deltas (decisions "decided by Claude, user may veto" unless marked; every live test keeps the RFP
spine: guests only via runComputeContract against a market network bidder):

```yaml
- plan: "0006"
  change: NEW
  id: M2-APP-CONTROLLER
  milestone: M2
  title: Each application is one root object in its base workspace; the core renders its profile into children
  depends_on: [M2-APPLY, M2-TEMPLATE, M2-HOSTPROCESS-KIND, "0008:M2-DENOPOD-LISTENERS"]
  decision: >
    The user's 2026-10-12 paradigm: an application instance (bidder, requester, gateway, market network,
    market network) is its own kcp (a base workspace) + manifest set + profile; state is objects; components are
    children. Kinds MarketBidder, MarketRequester, Gateway, MarketNetwork (socialweb.computer/v1alpha1,
    apigen) with spec = the app's options (cli-args-env one-to-one, network excluded) + profile, status =
    phase, conditions, did, aggregated listeners. internal/appctl: a pure reconciler (deno-kcp denopod.go
    style) rendering deploy/apps/<app>/<profile>.values.yaml through M2-TEMPLATE into owned children
    (ownerReferences, field manager appctl); a child the active profile omits is deleted. Apps never read
    outside their base workspace; cross-app values come only from the composer (M1-DEV-INSTANCE market network).
  files: ["api/v1alpha1/app_types.go", "internal/appctl/", "deploy/apps/", "deploy/sets.yaml"]
  test_first: >
    Offline (fake kcp): TestSwitchingABidderFromNetworkToProdQrDeletesItsPdsDenoPod,
    TestDeletingTheRootObjectRemovesEveryComponent, TestRootStatusCarriesEveryChildsReportedListener,
    TestAnAppCredentialCannotListAnotherAppsWorkspace. Red first.
  done_when: "tests green; `kubectl get marketbidders` in a dev instance shows did and listeners."

- plan: "0006"
  change: CHANGED
  id: M1-CLI
  delta: >
    Add the app verb contract (section 5.1 of multi-server-to-kcp.md): every app verb (`run bidder`,
    `run request`, `serve gateway kcp`, `dev up`) takes --profile and --kcp-base-workspace, applies its root
    object and watches it; no app verb opens a listener or spawns deno itself. New test
    TestNoAppVerbOpensAListenerOrSpawnsDeno (scan of the verbs' Go). Options gain `profile`.

- plan: "0006"
  change: CHANGED
  id: M1-DEV-INSTANCE
  delta: >
    `dev up --profile market-network` composes apps (4.7): MarketNetwork into <instance>:apps:network, N MarketBidders
    (--bidders N, default 1), one MarketRequester, optional Gateway (--with-gateway), each in its own nested
    base workspace; the composer copies the network's status URLs into each app's MarketNetwork value. New
    tests TestANetworkBidderLearnsThePlcOnlyFromItsNetworkValue and TestTwoBidderAppsInOneNetworkShareNoWorkspace.

- plan: "0006"
  change: CHANGED
  id: M3-SERVE-GATEWAY-KCP
  delta: >
    The gateway is a Gateway root object with profiles prod and market-network; the door's TEST_* seams (door
    mod.ts:53-67) become market-network profile values; the traditional cluster objects are named as the exception
    (multi-server-to-kcp.md 4.6). depends_on adds M2-APP-CONTROLLER.

- plan: "0007"
  change: NEW
  id: M3-BIDDER-ACCOUNT-PDS
  milestone: M3
  title: hono-bidder signs in to a real PDS account by handle and password, so a market network bidder can use its own PDS pod
  depends_on: []
  decision: >
    Today --atproto-handle/--atproto-password builds a local repo (hono-bidder/mod.ts:262-282),
    --atproto-pds-url is unused (cli-args-env.ts:76-81) and createRemoteAgent is never called (:10). Make
    that path a remote-PDS session (createRemoteAgent against --atproto-pds-url): records go to that PDS,
    the bidder serves no repo, its market endpoints stay on its own relay. The market network's bidder profile uses the
    market network PDS (today's bob pod); prod stays QR on the user's PDS; the local repo stays for dev-local and the
    harness.
  files: ["atproto-market/hono-bidder/mod.ts", "atproto-market/lib/atproto-helpers (remote agent)", "atproto-market/test/bidder_remote_pds_account_test.ts"]
  test_first: >
    test/bidder_remote_pds_account_test.ts on the container harness with an in-process hono-pds factory
    account: the bidder's offering is listed by that PDS's listRecords, no repo is served by the bidder,
    and runComputeContract still reaches receipt and onNetwork. Red today.
  done_when: "green; existing bidder tests green; pushed on main."

- plan: "0007"
  change: NEW
  id: M3-OIDC-ISSUER-INSTANCE
  milestone: M3
  title: oidc-issuer-hono keeps no module-global state, so two issuers in one process keep their own issuer URL and key
  depends_on: []
  decision: >
    configureOidc sets module globals (_getIssuerUrl, _jwkStore, _signingKey; oidc-issuer-hono/mod.ts:42-77)
    and every createOidcIssuer calls it (:616), so a bidder with two providers serves the last one's issuer
    URL from both. Move the state into the issuer instance; JwkStore stays injectable (M4-BIDDER-VAULT
    plugs the vault store in).
  files: ["hono-compute-provider/lib/oidc-issuer-hono/mod.ts", "hono-compute-provider/test/oidc_issuer_instances_test.ts"]
  test_first: "two createOidcIssuer in one process: each discovery document and JWKS names its own issuer; a token from one does not verify at the other. Red today."
  done_when: "green; provider suites green."

- plan: "0007"
  change: NEW
  id: M3-BIDDER-COMPOSER
  milestone: M3
  title: The bidder's composition is a factory taking a component spec; hono-bidder becomes a thin CLI
  depends_on: [M3-OIDC-ISSUER-INSTANCE]
  decision: >
    Extract hono-bidder/mod.ts's composition (825 lines) into lib/hono-factory-market-bidder-app: input =
    a component spec (identity mode, repo inproc|url, providers[], guest endpoints inproc|external, network
    value), output = the serves, relays and loops of section 2.1. hono-bidder reads cli-args-env and calls
    it; the in-process harnesses call it with an all-inproc spec instead of hand-composing; the DenoPod
    template sets the spec for one component. Interactive steps go through an IdentityPrompt port
    (stdin impl for standalone, status impl for kcp; sibling packages).
  files: ["atproto-market/lib/hono-factory-market-bidder-app/", "atproto-market/hono-bidder/mod.ts", "atproto-market/test/*_integration_test.ts"]
  test_first: >
    A composer test: the all-inproc spec reproduces today's listener and relay set (names compared), and a
    spec with repo: url builds no in-process repo; bidder_container_integration_test switched to the
    composer still passes the RFP flow. Red first.
  done_when: "hono-bidder/mod.ts under 150 lines; every bidder test green."

- plan: "0007"
  change: NEW
  id: M4-MARKET-IDENTITY
  milestone: M4
  title: MarketIdentity - an application's own atproto identity and sign-in as an object, with pending actions instead of stdin
  depends_on: [M1-API, M3-BIDDER-COMPOSER, M4-BIDDER-VAULT]
  decision: >
    Kind MarketIdentity (socialweb.computer/v1alpha1; distinct from the gateway's RBAC AtprotoIdentity):
    spec {mode oauthQr|oauthBrowser|pdsAccount|localRepo|oauthDelegated, handle, pdsUrl, keysRef,
    sessionRef, associateWith, approvedAssociations[]}, status {did, serviceDid, phase AwaitingScan|
    AwaitingApproval|Ready|SessionExpired, pendingAction {kind, url, callerDid, handle}, conditions}. Under
    kcp the identity port writes the QR URL and association requests to status and reads approvals from
    spec; an expired session is a condition, not Deno.exit(1) (hono-bidder/mod.ts:122-135). Used by the
    bidder, the requester and the door's accounts.
  files: ["api/v1alpha1/marketidentity_types.go", "atproto-market/lib (identity-prompt-kcp)", "internal/stack/bidder (render pending actions)"]
  test_first: >
    TS against a fake kcp: the QR URL appears in status.pendingAction, an approval patched into spec
    completes association, an expired session sets SessionExpired and the process stays up. Go:
    TestRunBidderRendersTheQrFromStatusAndReturnsWhenReady. Red first.
  done_when: "green; on a market network the bidder DenoPod runs without --skip-qr and the association is approved through the CLI."

- plan: "0007"
  change: CHANGED
  id: M4-BIDDER-VAULT
  delta: >
    The vault holds every relay keypair by relay name (market, each provider's, the repo's), the QR/browser
    OAuth session with its DPoP key (MarketIdentity.spec.sessionRef), and the requester app's keys use the
    same layout. depends_on adds M3-OIDC-ISSUER-INSTANCE. New test
    TestAQrSessionSurvivesABidderPodRecreateWithoutARescan.

- plan: "0007"
  change: CHANGED
  id: M5-RUN-BIDDER
  delta: >
    `run bidder` applies a MarketBidder root object (0006 M2-APP-CONTROLLER) with --profile prod-qr|market network|
    dev-local and watches it; prod-qr renders no PDS, market network renders the PDS pod and a pdsAccount identity
    (M3-BIDDER-ACCOUNT-PDS); the foreground renders MarketIdentity pending actions. depends_on adds
    "0006:M2-APP-CONTROLLER", M4-MARKET-IDENTITY.

- plan: "0007"
  change: NEW
  id: M5-BIDDER-COMPONENTS
  milestone: M5
  title: Guest-facing endpoints and the guest JSR registry as their own DenoPods
  depends_on: [M5-RUN-BIDDER, M4-ONNETWORK, M4-HOST-KEYS, "0008:M1-TS-CLOSURE"]
  decision: >
    DenoPod `guest` owns the provider relay (keypair from the vault), the OIDC issuer (JWK from the vault),
    /v1/on-network and /v1/host-keys (writing ComputeVM status.market); DenoPod `guest-jsr` runs hono-jsr
    over the embedded closure. The bidder core keeps the market relay and loops. A core restart or upgrade
    no longer moves or interrupts what guests dial.
  files: ["hono-compute-provider/hono-bidder-guest (thin CLI)", "deploy/apps/bidder/*.values.yaml"]
  test_first: "Behind livegate on a market network (guest via runComputeContract): TestABidderCoreRestartLeavesTheGuestIssuerAndRegistryServing. Red first."
  done_when: "green with readings (UTC)."

- plan: "0008"
  change: NEW
  id: M2-DENOPOD-LISTENERS
  milestone: M2
  title: Every service binds port 0 and its bound listeners land in DenoPod status; peers resolve from status
  depends_on: [M2-DENOKCP-FORK]
  decision: >
    createServe (typescript-helpers/lib/serve) gains a listener report written on onListen and after each
    relay connects: <runDir>/listeners.json {name: url} (tcp, unix, relay ingressUrl). The owned runner reads
    it on observe into DenoPod.status.listeners[]; servicenames.AdvertisedAddress prefers it (port-audit
    B2); the DNS table is re-read on a miss (KCP_DNS_TABLE_FILE). No service manifest names a port; the
    pod script sets PORT_FILE/the report path in the run dir and never passes HOME.
  files: ["typescript-helpers/lib/serve/mod.ts", "internal/denokcp (runner, status)", "internal/kcplibs/factory/servicenames"]
  test_first: >
    typescript-helpers serve test: a port-0 tcp serve with one relay writes both URLs; Go:
    TestADenoPodOnPortZeroIsResolvedByItsReportedAddress, TestNoServiceManifestNamesAPort. Red first.
  done_when: "green; the market network's services run on port 0 and every probe uses status."

- plan: "0008"
  change: NEW
  id: M2-MARKET-NETWORK
  milestone: M2
  title: One market-network value and adapter replace the four fetch patches and cross-app shim lookups
  depends_on: []
  decision: >
    typescript-helpers market-network-common (type: plcDirectoryUrl, ingressProxyHost, relayUrls,
    firehoseUrls, qrOrigin, guestTlsUrl, caBundle, localhost rewrite) + market-network-fetch (the one fetch
    adapter). hono-bidder (:74-95), request-vm-ssh (:48-60), market-requester (:82-95) and
    test/fetch-interceptor.ts use it; under kcp the template renders it from the app's MarketNetwork value.
  files: ["typescript-helpers/lib/common/market-network-common", "typescript-helpers/lib/market-network-fetch", "the four call sites"]
  test_first: "one table test: https://x.relay.localhost and https://plc.directory/did resolve identically through the adapter for every call site's inputs; red until the adapter exists."
  done_when: "no fetch patch left outside the adapter (grep test)."

- plan: "0008"
  change: CHANGED
  id: M1-MARKET-NETWORK-PARALLEL
  delta: >
    Port read-back goes through M2-DENOPOD-LISTENERS' report file, not onListen log lines; the bidder's
    key, repo KV and session live in its run dir (port-audit B11); accept.sh reads the bidder DID from the
    market network record written from status, not from stdout (accept.sh:463-467).

- plan: "0008"
  change: CHANGED
  id: M2-SERVICE-TEMPLATE
  delta: >
    Values come from app profiles (0006 M2-APP-CONTROLLER); the template renders the component spec of
    M3-BIDDER-COMPOSER, the MarketNetwork value (M2-MARKET-NETWORK) and the listener report path; no port
    and no HOME passthrough.

- plan: "0008"
  change: NEW
  id: M3-DOOR-STATE-VAULT
  milestone: M3
  title: The door's sessions and requester key live in the gateway's root vault
  depends_on: [M3-DOOR-DENOPOD, "0006:M2-OPENBAO-SERVER", "0007:M4-MARKET-IDENTITY"]
  decision: >
    oauth-sessions.json (door mod.ts:50) and requester-key.hex (:72) move to the root vault behind the
    existing session-store and key ports (sibling vault packages); each door account is a MarketIdentity
    (oauthDelegated). Per-claim secrets keys stay 0007 M6-CLAIM-RESUME.
  test_first: "door test: a recreated door DenoPod serves an existing session and the same requester DID with an empty state dir. Red first."
  done_when: "green; the door state dir holds no secret."

- plan: "0008"
  change: CHANGED
  id: M4-RUN-REQUEST
  delta: >
    The requester is a persistent application: `run request` adopts the user's requester app (one slot on
    kine-sqlite in the bootstrap layer, like a bidder handle) and applies ComputeContracts; a MarketRequester
    root object and a MarketIdentity (QR: no repo component, F7); per-contract secrets keys in the app
    vault; `--ssh` keeps the session in the foreground CLI. request-vm-ssh stays as the standalone library
    composition. depends_on adds "0006:M2-APP-CONTROLLER", "0007:M4-MARKET-IDENTITY".

- plan: "0008"
  change: CHANGED
  id: M4-MARKET-NETWORK-PORT
  delta: >
    `market-network up` = `dev up --profile market-network` composing apps (4.7); system tests start apps only through
    internal/apptest.Start(t, app, profile) in a fresh nested workspace and read DIDs and URLs from root
    status (no stdout scraping); N bidders = N bidder apps. depends_on adds "0006:M2-APP-CONTROLLER".
```

## 7. Questions for the user (recommended answers)

1. **Market network composition: one kcp per market network with each app in a nested base workspace, or a kcp per app inside the
   market network?** Recommended: nested (one kcp per dev instance, as decided; each app ~0.4-0.5 GB cheaper); prod
   apps run their own kcp; app code is identical through kcp-base-workspace, and credentials are scoped per
   app subtree.
2. **The market network bidder's repo: a PDS-pod account (needs M3-BIDDER-ACCOUNT-PDS) or keep today's in-process
   repo?** Recommended: PDS-pod account in the market-network profile (it is what the user described and exercises a
   remote-repo code path like prod's QR); keep the in-process repo for dev-local and the harness.
3. **Keep `deno run hono-bidder` / `request-vm-ssh` working without kcp?** Recommended: yes, as the library
   composition with file-backed stores; no embedded kcp in TypeScript.
4. **Split the bidder's guest endpoints and JSR registry into their own DenoPods (M5-BIDDER-COMPONENTS)?**
   Recommended: yes, after the vault and the OIDC fix; phase 1 keeps them in the bidder core.
5. **QR, association and session expiry through object status, rendered by the foreground CLI (and later
   the door UI), with the stdin prompt only in standalone mode?** Recommended: yes.
6. **The requester as a persistent app (adopted by each `run request`) rather than a kcp per invocation?**
   Recommended: persistent; a per-invocation kcp costs 6-9 s and loses contracts between invocations.
7. **OAuth sessions and every key in the app's OpenBao, never in kcp Secrets?** Recommended: yes.
8. **Split the door's node fulfiller (a NodeClaim reconciler) from the web door now?** Recommended: not now;
   after 0007 M6-CLAIM-RESUME, if a door restart still interrupts fulfilment.
9. **Cross-app discovery only through composer-copied values (the shim stops reading other workspaces)?**
   Recommended: yes; inside one app the shim keeps resolving service names.

## Sources

- atproto-market: hono-bidder/mod.ts, hono-bidder/cli-args-env.ts, request-vm-ssh/mod.ts,
  request-vm-ssh/cli-args-env.ts, hono-plc/mod.ts, hono-compute-contract-gateway/mod.ts,
  lib/atproto-helpers/agent.ts, lib/atproto-helpers/oauth-qr-session.ts, lib/market-bidder/mod.ts,
  lib/requester-xrpc/mod.ts, lib/compute-request-xrpc/mod.ts, lib/guest-capability-secrets/mod.ts,
  lib/did-key-ingress-proxy/mod.ts, test/bidder_container_integration_test.ts and the sibling tests in 2.9.
- hono-compute-provider: lib/compute-provider-firecracker/mod.ts, lib/oidc-issuer-hono/mod.ts,
  hono-compute-provider/mod.ts.
- typescript-helpers: lib/serve/mod.ts.
- hono-pds/main.ts; atproto-relay/hono-atproto-relay/mod.ts; did-key-ingress-proxy/hono-did-key-ingress-proxy*/mod.ts;
  did-key-associator/hono-backend/mod.ts; deno-kcp/api/v1alpha1/types_denopod.go.
- socialweb-computer-kcp: socialweb-computer-k8s/hono-socialweb-computer-k8s/mod.ts,
  socialweb-computer-k8s/lib/socialweb-computer-requester-inproc/mod.ts,
  socialweb-computer-k8s/lib/socialweb-computer-k8s-node-market/mod.ts, deploy/container-entrypoint.sh,
  cmd/socialweb-gateway/main.go, deploy/bidder/socialweb-computer-bidder-run,
  deploy/market-network/{README.md,70-bidder.yaml,*.yaml,accept.sh,market-requester/mod.ts},
  internal/marketnetwork/{live_test.go,live_m3_test.go}, api/v1alpha1/{resources.go,types_atprotoidentity.go},
  docs/plans/0006-0008, docs/research/2026-10-11-vm-lifecycle/{user-decisions.md,port-audit.md,dev-run-mode.md}.
- org root: alice-request.sh, bob-bidder.sh (untracked dev scripts).
