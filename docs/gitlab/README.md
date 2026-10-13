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
not done silently: `internal/gatewaymain/config.go` refuses to bind anything but loopback, and the
host's caddy publishes only the web app and the kcp front (`scripts/deploy.sh`).
Instructions stop at that boundary; see [01](01-kcp-server.md#gitlab-on-its-own-vm-does-not-work).

## The limits you will hit first

**Cross-node pod traffic and DNS are new on market clusters and not yet read live.** Every market
node resolves names on its own CoreDNS, and pods reach pods on other nodes through
`socialweb-meshd`; on the local firecracker substrate a Pod still cannot reach a Pod on another
node. Read [03](03-jobs.md#pods-on-different-nodes-and-dns-on-every-node) before scaling a cluster
past one node for anything that needs the network.

## Why the DNS wait is gone

02 and 04 used to carry a `pre_get_sources_script` that looped up to 180 seconds on `getent hosts
$CI_SERVER_HOST` before the clone. It existed because a job Pod's resolver was the cluster's one
CoreDNS, which sat on one node; a Pod on any other node had no path to it, so three of five jobs on
five fresh nodes died at the clone with `Could not resolve host`. The wait only named that failure;
it never fixed it, because the failure did not go away with time.

Plan 0005 `M5-DNS` fixes the cause. The guest's user_data puts `169.254.20.10` on a dummy interface
and starts the k3s agent with `--kubelet-arg=cluster-dns=169.254.20.10` and the
`socialweb.computer/node-local-dns=true` label; the provider applies, in every tenant, a host-network
CoreDNS DaemonSet selected by that label and bound to that address. A job Pod therefore asks a
CoreDNS on its own node, which answers cluster names from the apiserver and forwards everything
else to the node's resolver. Nothing crosses to another node, so the failure the wait named cannot
happen.

One window is stated rather than closed: on a node that has JUST become Ready, the DNS pod and the
job pod start side by side, and a clone that runs before the DNS pod is ready fails. The DNS image is
one the node image seeds, so it usually wins against the job's helper image pull, but nothing orders
the two. Closing it means a startup taint the node lifts once its DNS answers; that is open in
`docs/ARCHITECTURE.md` section 9.

What still makes a clone fail is in 04's failure table: a node that joined with an older user_data
(no label, no address), or a `CI_REPOSITORY_URL` the rented node cannot reach. The live GitLab test
on the local firecracker substrate (`cmd/socialweb-clusterprovider/live_gitlab_named_job_test.go`)
keeps its wait, because that substrate keeps flannel and one CoreDNS.

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
