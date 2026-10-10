# GitLab on the virtual k3s clusters

Three steps, one document each:

| | |
| --- | --- |
| [01 - The kcp server](01-kcp-server.md) | kcp up, tenant cluster, node sizing, the runner's credential and RBAC |
| [02 - The GitLab runner](02-gitlab-runner.md) | GitLab instance, register, `config.toml`, failure modes |
| [03 - Jobs](03-jobs.md) | job images, pipelines, one job per node, scaling, the cross-node limit |

01 leaves you holding three files and a URL. 02 points a runner at them. 03 is what happens when a
job runs.

Measured readings for all of this live in `docs/ARCHITECTURE.md` section 15, with the defects each
one closed.

## Reachability: the single operational fact

Tenant apiserver **binds loopback on the kcp host**. Not on a network interface. Two routes exist,
and they are not interchangeable:

| Route | URL | Credential that works |
| --- | --- | --- |
| Direct, host-local | `https://127.0.0.1:<k3s-secure port>` | a client certificate signed by that cluster's ClientCA |
| Through the kcp mount | `https://<kcp-host>/clusters/root:<accountHash>:<cluster>:clusters` | a bearer token only |

The mount is a reverse proxy: kcp terminates TLS and proxies to the apiserver. **Only a bearer
credential survives it**; a client certificate does not. Right route for a human with `kubectl` and
a minted token. Wrong route for GitLab CI, which needs the Kubernetes exec and attach paths
(`pods/exec`), an upgraded SPDY connection through a reverse proxy.

So: **run the GitLab Runner where `127.0.0.1:<port>` is reachable - on the kcp host.**

Port is leased per cluster and is not a constant. Read it from the cluster's own status:

```bash
kubectl --kubeconfig <admin> get cluster <name> -o jsonpath='{.status.url}'
```

That value is the `host` you put in the runner config.

### If GitLab must live on a separate VM

Then the kcp host has to publish an authenticated front for the apiserver, and that widens a trust
boundary this project keeps at loopback on purpose. Nothing in this repo does it today, and it is
not done silently: `cmd/socialweb-gateway/config.go` refuses to bind anything but loopback, and
`deploy/container-entrypoint.sh` publishes exactly one port, whose only upstream is the gateway.
Instructions stop at that boundary; see [01](01-kcp-server.md#gitlab-on-its-own-vm-does-not-work).

## The limits you will hit first

**A Pod on one node cannot reach a Pod on another node**, so a cluster with more than one has
cluster DNS only for the Pods on the node CoreDNS sits on. Anything that resolves a name therefore
works on a ONE-node cluster and fails elsewhere, permanently. Measured, and in `NEXT.yaml` as a
limit. Read [03](03-jobs.md#the-limit-that-bites-multi-node-first) before scaling a cluster past
one node for anything that needs the network.

**The default ceiling is ten nodes, and `scripts/deploy.sh` passes ten with one Pod per node.**
`SOCIALWEB_CLUSTERPROVIDER_SCALE_MAX_NODES` defaults to 10 and
`SOCIALWEB_CLUSTERPROVIDER_SCALE_PODS_PER_NODE` to 8; a ceiling below one is refused rather than
clamped. Every node is a booted guest, so the pair is a cost knob and not only a behaviour one, and a
cluster serving a runner should set `podsPerNode` from a measured job: the runner this document is
about uses pod anti-affinity, so one job Pod occupies one node whatever the setting says. With the
ceiling above one, `concurrent` in `config.toml` may exceed one too - it does not have to stay at
1, and `concurrent = 1` is itself a hazard, because a job left over from a cancelled pipeline
holds the only slot. Measured 2026-10-07: five jobs at once, each on a distinct node, all green, with
the count never above the ceiling. See [03](03-jobs.md#one-job-per-node).

Also the hold: past the idle grace (`SOCIALWEB_CLUSTERPROVIDER_SCALE_HOLD`, default 2m) a cluster
with nothing running goes back to zero nodes. A job arriving after that pays the node boot again
(about 10 s, measured). GitLab's pre-warmed pause pods
([03](03-jobs.md#pause-pods-and-why-they-fight-scale-to-zero)) exist to hide exactly that latency.
