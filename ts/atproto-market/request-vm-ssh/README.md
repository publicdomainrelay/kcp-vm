
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
