# 04 - A runner for a cluster from the SPA

End state: a GitLab runner on the GitLab host, holding nothing but a kubeconfig a user downloaded
from https://k8s.socialweb.computer, placing job Pods into that user's tenant cluster through the
kcp mount.

This is the second runner shape. [02](02-gitlab-runner.md) runs on the kcp host and reaches the
tenant apiserver on its loopback port with a client certificate, because that is where the apiserver
binds. This document is for the case where the cluster is not on the operator's host at all: the
user made it in the SPA, the nodes are rented from the compute market, and the only way in is the
URL the SPA handed them.

01 and 02 are still the reference for what a job is; read [03](03-jobs.md) unchanged.

## The topology this is written for

| | |
| --- | --- |
| SPA | `https://k8s.socialweb.computer` |
| kcp front | `https://kcp.socialweb.computer` |
| cluster in a kubeconfig | `https://kcp.socialweb.computer/clusters/root:<accountHash>:<cluster>:clusters` |
| credential helper | `/usr/local/bin/socialweb-credential` |
| helper downloads | `https://k8s.socialweb.computer/downloads/socialweb-credential-<os>-<arch>` |
| mint | `https://k8s.socialweb.computer/v1/credential` |

`<accountHash>` is the first 8 characters of the base36 of `sha256(did)`
(`internal/kcpclient/names.go`, `AccountHash` and `MountPath`).

The kubeconfig's user is not a certificate and not a static token. It is an exec credential plugin:

```yaml
users:
  - name: <cluster>
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        command: /usr/local/bin/socialweb-credential
        interactiveMode: Never
        env:
          - name: SOCIALWEB_DEVICE_KEY
            value: <a service-auth JWT bound to the account>
```

Every Kubernetes request costs one run of that helper: it POSTs the device JWT to the mint, gets a
short-lived ServiceAccount token back, and prints it as an `ExecCredential`. The token is
cluster-admin in that tenant cluster.

## Where the runner runs, and why not on the kcp host

**On the GitLab host** (`ssh gitlab`).

- It is off the kcp host, which is the point. Nothing about this shape needs the operator's host,
  because nothing needs `127.0.0.1:<leased port>`: the kubeconfig carries the URL and the plugin
  carries the credential.
- GitLab is there. The runner's HTTPS to GitLab is then loopback-adjacent instead of a tailnet hop.
- A runner is already installed and registered there (see [what is already there](#what-is-already-there)).
- The mint is a public HTTPS name, so the runner host is the only host that has to reach it.

The one thing that argues for the kcp host is [the hop this document cannot prove](#the-hop-this-document-cannot-prove).
Read that before committing to anything long-lived.

## Prerequisites

- The GitLab host resolves and reaches `k8s.socialweb.computer` (the mint) and
  `kcp.socialweb.computer` (the cluster). Both are currently published **nowhere**: no A record from
  a public resolver and NXDOMAIN on the GitLab host. Until they resolve, nothing below can run.
- A user account, a cluster made in the SPA, and the kubeconfig downloaded from it. The SPA offers
  the build matching the runner host's OS and architecture.
- Outbound HTTPS from the GitLab host to the internet. Measured there: `plc.directory` answers
  `302`, `registry-1.docker.io` answers `401`. There is a route.
- A GitLab project to run jobs in, and a runner registered to it. Existing candidates are
  `streamplace/streamplace` (id 1, the real job) and `johnandersen777/kcp-ci-probe` (id 5, the
  probe).
- **The user is cluster-admin in their own cluster, so there is no RBAC for the operator to grant.**
  See [what the user still applies](#what-the-user-still-applies).

## Install socialweb-credential

The kubeconfig names an absolute path. Put the build there and make it executable on the runner
host, for the user the runner runs as:

```bash
sudo install -m 0755 socialweb-credential-linux-amd64 /usr/local/bin/socialweb-credential
```

The helper reads two variables. `SOCIALWEB_DEVICE_KEY` comes from the kubeconfig's own `exec.env`.
`SOCIALWEB_MINT_URL` is read from the process environment and must be set on the runner, because the
kubeconfig does not have to carry it:

```bash
SOCIALWEB_MINT_URL=https://k8s.socialweb.computer/v1/credential
```

If the deployed kubeconfig does carry it in `exec.env`, that value wins for that invocation and the
two should agree. They are the same URL; disagreeing means the kubeconfig is stale.

Optional, and unset by default: `SOCIALWEB_CREDENTIAL_CACHE` (a file the helper caches the minted
token in), `SOCIALWEB_MINT_TIMEOUT` (default `10s`).

Two properties of the helper worth knowing before debugging a runner:

- On failure it exits **3**. That number reaches kubectl and client-go verbatim, so
  `exec: executable socialweb-credential failed with exit code 3` is the helper's own error and its
  message is on its stderr.
- It refuses a credential the mint returns with an `expirationTimestamp` that has already passed.
  A clock-skewed runner host presents as a mint that "returns nothing".

## The kubeconfig

The SPA hands out one file, `<cluster>.kubeconfig.yaml`. Put it on the runner host where only the
runner's user can read it:

```bash
sudo install -d -m 0700 /etc/gitlab-runner
sudo install -m 0600 <cluster>.kubeconfig.yaml /etc/gitlab-runner/tenant-ci/kubeconfig.yaml
```

Check it before pointing a runner at it:

```bash
export KUBECONFIG=/etc/gitlab-runner/tenant-ci/kubeconfig.yaml
export SOCIALWEB_MINT_URL=https://k8s.socialweb.computer/v1/credential
/usr/local/bin/socialweb-credential | head -c 200; echo
kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}'; echo
kubectl auth can-i create pods --namespace gitlab-runner
kubectl auth can-i create pods/exec --namespace gitlab-runner
kubectl get nodes
```

- The helper prints an `ExecCredential` with `status.token` and `status.expirationTimestamp`.
- The server is the kcp mount URL, not an address.
- Both `can-i` calls answer `yes`, and they answer `yes` for everything else too.
- `get nodes` is the resting state: **empty**. A cluster with nothing in it has no nodes, and a
  node appears only when a Pod cannot be placed. `No resources found` is the correct answer here,
  not a broken cluster.

### The device JWT expires, and that is the rotation

`SOCIALWEB_DEVICE_KEY` is a service-auth JWT with the lifetime the user chose in the SPA
(`kubeconfig-max-ttl-sec`, default 30 days, capped by `SOCIALWEB_GATEWAY_DEVICE_KEY_MAX_TTL`). Past
it the mint answers 401 and every job fails at credential time. **Rotation is downloading a new
kubeconfig and overwriting the file.** Nothing else in the runner config changes.

A long-lived runner wants the longest TTL the SPA allows, and a calendar entry.

### KUBECONFIG is the process environment, not a config.toml key

The Kubernetes executor has no `kubeconfig` setting. It loads one through client-go, and the only
lever is the environment of the `gitlab-runner` process itself. See
[the citation](#how-the-executor-finds-the-cluster).

Do **not** reach for `environment = [...]` in `[[runners]]`. That key injects into the build
environment, which is the job container, which is already inside the cluster
(`common/config.go`: "Custom environment variables injected to build environment"). It does not
reach the runner.

## What the user still applies

Two objects, and one of them is optional.

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: gitlab-runner
```

The runner does **not** create it. `setupBuildNamespace` returns immediately unless
`namespace_per_job = true` (`executors/kubernetes/kubernetes.go`), so with the default the
configured namespace has to exist or every job fails with `namespaces "gitlab-runner" not found`.

Only if the user turns on pause pods, the PriorityClass they name, because `priorityclasses` is
cluster-scoped:

```yaml
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: gitlab-pause
value: -10
globalDefault: false
description: "Pause pods, below job pods, so a job preempts one."
```

### What is gone from 01 and why

[01 section 5](01-kcp-server.md#5-permit-that-group-in-the-tenant-cluster) applies a ClusterRole and
ClusterRoleBinding for a `gitlab-runner` group. **None of it applies here**, because the credential
is a different kind of thing:

| 01 had | Here |
| --- | --- |
| a client certificate from the ClientCA, CommonName and Organizations chosen by the operator | an exec plugin trading a device JWT for a minted ServiceAccount token |
| the identity, and therefore every RBAC rule, under the operator's control | cluster-admin by construction; the SPA's mint decides |
| `kubectl auth can-i ... --as=gitlab-runner --as-group=gitlab-runner` to check it | `kubectl auth can-i '*' '*'` answers `yes` |

Two consequences. The first is that the whole class of 403s in 02's failure table cannot happen.
The second is worse: **a mistake that RBAC would have caught now succeeds.** A job that deletes
nodes, rewrites the ClusterRole bindings, or reads every Secret in the tenant cluster will be
allowed to. There is no scoped variant of this runner shape today; the operator does not hold the
key that would make one.

## The file

Same file 02 describes (`~/.gitlab-runner/config.toml` on the GitLab host, or
`/etc/gitlab-runner/config.toml` under a service). What changed is the connection block.

```toml
concurrent = 2
check_interval = 3

[[runners]]
  name = "kcp-tenant-kubernetes"
  url = "https://gitlab.dorper-matrix.ts.net:8443/"
  token = "<<runner authentication token>>"
  executor = "kubernetes"
  run_untagged = false
  tag_list = ["kcp-tenant"]

  # Runs BEFORE the clone. Gate on DNS here, never in pre_build_script.
  pre_get_sources_script = "if command -v getent >/dev/null 2>&1; then R='getent hosts'; elif command -v nslookup >/dev/null 2>&1; then R=nslookup; fi; i=0; while [ -n \"$R\" ] && [ $i -lt 180 ]; do $R \"$CI_SERVER_HOST\" >/dev/null 2>&1 && break; i=$((i+1)); sleep 1; done; true"

  # Runs AFTER the clone, before the job's script. A STRING, not an array.
  pre_build_script = "if ! command -v pnpm >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then npm install -g pnpm@10.11.0; fi; true"

  # Kilobytes. Default is 4096, which is 4 MiB and is NOT enough for a Go test log.
  output_limit = 51200

  [runners.kubernetes]
    # No host, no cert_file, no key_file, no ca_file. The kubeconfig and the exec plugin are
    # both of those, and setting host would defeat the plugin entirely.
    namespace = "gitlab-runner"
    image_pull_policy = "if-not-present"

    cpu_request = "100m"
    memory_request = "128Mi"
    cpu_limit = "10"
    memory_limit = "20Gi"

    helper_cpu_request = "100m"
    helper_memory_request = "128Mi"
    helper_cpu_limit = "1"
    helper_memory_limit = "1Gi"

    # Seconds for the Pod to reach Running: a rented node boots, THEN the image pulls.
    poll_timeout = 3600
    cleanup_grace_period_seconds = 30

    [runners.kubernetes.affinity]
      [runners.kubernetes.affinity.pod_anti_affinity]
        [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution]]
          topology_key = "kubernetes.io/hostname"
          [runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector]
            [[runners.kubernetes.affinity.pod_anti_affinity.required_during_scheduling_ignored_during_execution.label_selector.match_expressions]]
              key = "project.runner.gitlab.com/id"
              operator = "Exists"
```

### Every setting carried forward, and what each one means now

| Key | Status | Why |
| --- | --- | --- |
| `concurrent` | carried, value changed | Now a **cost** knob, not only a behaviour one: every concurrent job is a rented node. `1` still wedges on a stale job from a cancelled pipeline. Set it to the number of nodes the user is willing to pay for. |
| `check_interval` | carried | Unchanged. |
| `run_untagged` / `tag_list` | **new** | The GitLab host already carries an `instance` runner that is online and `run_untagged`. An untagged job is eligible for it and may never reach this one. Tag this runner and give its projects the tag. |
| `pre_get_sources_script` | carried | Unchanged and still load-bearing: the Pod's resolver is cluster DNS, and CoreDNS is an addon created with the first node. [03](03-jobs.md#the-limit-that-bites-multi-node-first). |
| `pre_build_script` | carried | Unchanged. Still the place the pnpm/corepack guard lives. |
| `output_limit` | carried | Unchanged. 4 MiB is the runner's own cap, not GitLab's. |
| `host` | **removed** | Was `https://127.0.0.1:<leased port>` on the kcp host. There is no leased loopback port here, and the URL is in the kubeconfig. Setting `host` at all makes the executor take the out-of-cluster path and ignore the kubeconfig completely. |
| `cert_file`/`key_file`/`ca_file` | **removed** | Were the client certificate. The credential is now a bearer token produced by the exec plugin; a certificate is not part of this shape. |
| `namespace` | carried | Still must exist. See [what the user still applies](#what-the-user-still-applies). |
| `image_pull_policy` | carried | Unchanged. The node image still seeds some images; a rented node's seed list is the image's, not the operator's. |
| `cpu_request`/`memory_request` | carried | Unchanged, and now more expensive to get wrong: a request that fits no rented node size is a Pod that waits forever and a market bill for a node that never gets used. |
| `cpu_limit`/`memory_limit` | carried | Unchanged. Limits are enforcement, not placement. |
| `helper_*_request` / `helper_*_limit` | carried | Unchanged. A limit with no request **is** the request. |
| `poll_timeout` | carried, raised | Was 1800. Here the first Pod waits for the market to win an RFP, boot a VM, join it, and only then pull the image. Minutes, not seconds. |
| anti-affinity | carried | Unchanged, and still not optional: cross-node pod networking does not exist, so two jobs on one node is two jobs sharing a CoreDNS-less node. |
| `[runners.kubernetes.autoscaler]` | **not set** | Pause pods. See [pause pods](#pause-pods-scale-to-zero-still-loses). |

## What is already there

Measured on `ssh gitlab` (host `hat-10`, Fedora 44, tailnet address `100.119.250.32`):

- GitLab **19.2.7-ce.0** in a container, `external_url https://gitlab.dorper-matrix.ts.net:8443`,
  ports 80 and 22 published on the host.
- **gitlab-runner 19.2.1** at `/home/johnandersen777/bin/gitlab-runner`, installed from the GitLab
  package repository, config at `~/.gitlab-runner/config.toml` (mode 0600). There is no
  `/etc/gitlab-runner/` on this host; if the runner is moved to a service, create it.
- **No kubectl.** The recipe needs it for verification only; the runner does not.
- Three runners are registered:

| id | description | type | state |
| --- | --- | --- | --- |
| 1 | `socialwebcomputer` | instance_type | online, `executor = "instance"` with the Fleeting plugin |
| 2 | `kcp-tenant-kubernetes` | project_type, project 5 | offline |
| 3 | `kcp-tenant-kubernetes-streamplace` | project_type, project 1 | offline |

Runners 2 and 3 are already registered and already the right shape. They are offline because
nothing is running them: `config.toml` currently holds runner 1 and nothing else. Runner 1 is a
different mechanism (it rents VMs through the market via `request-vm-ssh` and runs jobs over SSH);
it is not this runner and the two must not both be eligible for the same job.

## Register

Only if runners 2 and 3 are not the ones being used. Text for the operator; do not run these from a
script.

Create the runner in the project's **Settings > CI/CD > Runners**, tag it `kcp-tenant`, clear
"Run untagged jobs", and copy the authentication token. Then, on the GitLab host:

```bash
gitlab-runner register \
  --non-interactive \
  --config ~/.gitlab-runner/config.toml \
  --url https://gitlab.dorper-matrix.ts.net:8443/ \
  --token <runner authentication token> \
  --executor kubernetes \
  --tag-list kcp-tenant \
  --run-untagged=false
```

Then replace the generated `[runners.kubernetes]` block with [the file](#the-file) above. `register`
writes the token and the executor and nothing else useful.

Registering runners 2 and 3 is the same command with a different token against project 5 and
project 1 respectively; their tokens are already in GitLab and are not in any config file on this
host, so a re-registration issues new ones.

## Run it

The two variables are the runner process's, so they go in its environment. Under systemd:

```bash
sudo systemctl edit gitlab-runner
```

```ini
[Service]
Environment=KUBECONFIG=/etc/gitlab-runner/tenant-ci/kubeconfig.yaml
Environment=SOCIALWEB_MINT_URL=https://k8s.socialweb.computer/v1/credential
```

Or, for a foreground run under the operator's own shell:

```bash
export KUBECONFIG=/etc/gitlab-runner/tenant-ci/kubeconfig.yaml
export SOCIALWEB_MINT_URL=https://k8s.socialweb.computer/v1/credential
gitlab-runner run --config ~/.gitlab-runner/config.toml --working-directory /var/lib/gitlab-runner
```

Pause runner 1 first, or its `run_untagged` will take jobs meant for this one.

## Verify

Three readings, in order. The first two are free; the third costs a node boot.

**1. The runner decoded its config.** Watch the runner's own output, not GitLab's view of it:

```
FATAL: Service run failed  error=decoding configuration file: toml: line 9
```

Any `FATAL` at startup means the process is gone and GitLab will still show the runner online.

**2. The kubeconfig and the plugin answer outside the runner.**

```bash
export KUBECONFIG=/etc/gitlab-runner/tenant-ci/kubeconfig.yaml
export SOCIALWEB_MINT_URL=https://k8s.socialweb.computer/v1/credential
kubectl get nodes                 # No resources found
kubectl get ns gitlab-runner      # the namespace the recipe applied
```

At rest `get nodes` is empty and stays empty: this cluster has no always-on node. Then run the
smallest pipeline that proves the integration, [03's probe](03-jobs.md#the-smallest-pipeline-that-proves-the-integration),
with the tag:

```yaml
prove-placement:
  stage: prove
  tags: [kcp-tenant]
  image: busybox:1.36
  script:
    - echo "running on $(hostname)"
```

**3. A job brings a node up, and the cluster goes back to zero.** The whole point of the shape:

```bash
kubectl get pods -n gitlab-runner -o wide -w
kubectl get nodes -w
```

What a working run reads, by the same instrument as the measured five-node run in
[03](03-jobs.md#prove-it):

```
the job pod exists, Pending, cluster at zero nodes
the scale decision read Provision: intent and no node
a node joins on demand and becomes Ready
the job pod is placed on it and runs
the job finishes success
the job pod is gone
the cluster is back at zero nodes; `kubectl get nodes` is empty again
```

The gap between "pod Pending" and "node Ready" is a market round trip plus a VM boot. Minutes.
That is the design, not a stall.

## Failure modes

In the order this shape meets them. 02's table still applies for everything about the file itself;
these are the ones the new hop adds or sharpens.

| Symptom | Cause |
| --- | --- |
| `invalid configuration: no configuration has been provided` at runner startup | `KUBECONFIG` is not in the **runner process's** environment. `[[runners]] environment = [...]` does not reach it. |
| `exec: executable socialweb-credential failed with exit code 3` | The helper's own failure. Its stderr says which: `SOCIALWEB_MINT_URL is not set`, `mint returned 401` (device JWT expired, or the account no longer owns the cluster), or a DNS failure reaching the SPA. |
| The runner talks to the wrong cluster, or to a cluster on its own host | `host` is still set somewhere in `[runners.kubernetes]`, or the process has `KUBERNETES_SERVICE_HOST` set because the runner is itself running inside a cluster. With `host` empty the executor tries in-cluster config FIRST and only then the kubeconfig. |
| Job hangs at "Preparing the environment" | The SPDY upgrade to `pods/exec` did not survive the kcp mount. See [the hop this document cannot prove](#the-hop-this-document-cannot-prove). |
| `namespaces "gitlab-runner" not found` | The namespace was never applied. The runner does not create it. |
| Job stays `pending`, no node appears | Nothing rented a node, or the market lost the RFP. Compare `kubectl get pods -A` with `kubectl get nodes`; a Pending Pod that the scale decision never sees is a provider that is not running. |
| Pod `Pending` for minutes and then Running | Normal. A rented node has to boot. `poll_timeout` has to cover it. |
| Pod `Pending` forever on a node that exists | Requests do not fit the node size the market rented. See 02's trap about a limit being a request. |
| `ImagePullBackOff` after a node boot | The job's image does not exist, or the rented node cannot reach its registry. 03's check costs one curl and saves a node boot. |
| Job fails at the clone with `Could not resolve host` | The job Pod's resolver is cluster DNS, which needs a network path between nodes that does not exist off the node CoreDNS sits on. [03](03-jobs.md#the-limit-that-bites-multi-node-first). One job per node is configured for exactly this. |
| Job fails at the clone with `unable to access '...'` where the DNS DID resolve | `CI_REPOSITORY_URL` names a GitLab the **rented** node cannot reach. This is 03's section, sharpened: in 02's shape the guest ran on the operator's kcp host, whose own resolver and routes reached the tailnet. A rented node's egress is the provider's, so `gitlab.dorper-matrix.ts.net` (a tailnet address, `100.119.250.32`) is reachable only if that provider happens to be on the tailnet. Either publish GitLab on a name the public internet resolves, or give the job `GIT_STRATEGY: none`. |
| The cluster never returns to zero | Something is Pending that is not a job Pod. Pause pods are the usual answer; see below. |
| A job's first request is `Forbidden`, ever | It should not be. The token is cluster-admin. If this ever appears, the mint changed and the runner is no longer the thing that broke. |

## Pause pods, scale-to-zero still loses

[03's pause pods](03-jobs.md#pause-pods-and-why-they-fight-scale-to-zero) apply unchanged and are
**more expensive here**, because a node is now rented rather than booted on the operator's own
hardware. In 19.2.1 the shape is:

```toml
    [runners.kubernetes.autoscaler]
      max_pause_pods = 1
      pause_pod_image = "registry.k8s.io/pause:3.10"
      pause_pod_priority_class_name = "gitlab-pause"

      [[runners.kubernetes.autoscaler.policy]]
        idle_count = 0
        idle_time = "1m"
```

A pause pod is a Pending Pod that is not a k3s addon, so the scale decision counts it as intent and
rents a node to place it. The cluster then never returns to zero while one exists. `idle_count = 0`
outside working hours is the way out, and it is a cron on the runner, not a cluster setting.

Do not set a 24x7 `idle_count > 0` on a cluster whose nodes are billed.

## The hop this document cannot prove

**Nothing in this recipe has been run end to end.** The shape is assembled from parts that are each
measured, and there is exactly one hop with no reading behind it.

The kubernetes executor does not merely create Pods. It attaches to the build container with an
HTTP upgrade:

```go
// executors/kubernetes/exec.go, gitlab-runner v19.2.1
exec, err := remotecommand.NewSPDYExecutor(config, method, url)
```

That is `POST /api/v1/namespaces/<ns>/pods/<pod>/exec` with `Connection: Upgrade` and
`Upgrade: SPDY/3.1`. In this shape it goes runner to `kcp.socialweb.computer`, through Caddy, into
kcp, and out to the tenant apiserver. [README.md](README.md#reachability-the-single-operational-fact)
already says the kcp mount is a reverse proxy and that "only a bearer credential survives it"; the
credential part is answered here (the exec plugin mints a bearer token), but **the upgrade
surviving the proxy is not**. `scripts/deploy.sh` writes the kcp vhost with `flush_interval -1`,
which is a streaming setting and not a statement about upgrades.

So the recipe assumes:

1. `pods/exec` upgrades pass through Caddy and kcp intact.
2. The tenant apiserver reached through the mount is the same apiserver the Pods are created in.
3. `kubectl get nodes` over the mount is authoritative - that the mount is not silently serving a
   different workspace.

If (1) is false, the symptom is a job that hangs at "Preparing the environment" while its Pod sits
`Running`: the Pod was created fine, and the attach never completes. Two ways out, both already in
this tree:

- **Run the runner on the kcp host** as [02](02-gitlab-runner.md) does, with the loopback URL and
  the client certificate. Correct, and incompatible with a cluster the operator's host does not
  own.
- **Tunnel.** An `ssh -R` from the runner host to a host that can reach the mount is a raw TCP
  forward, and a raw forward does not care about SPDY. Plan 0004's D1 records this as the fallback.
  It reintroduces a host, which is what this shape exists to avoid.

### How the executor finds the cluster

For the reader who wants to check the assumption rather than take it.

`executors/kubernetes/util.go`, gitlab-runner v19.2.1:

```go
func getKubeClientConfig(config *common.KubernetesConfig, overwrites *overwrites) (*restclient.Config, error) {
	if config.Host != "" {
		kubeConfig, err = getOutClusterClientConfig(config)
	} else {
		kubeConfig, err = guessClientConfig(config)
	}
	...
}

func guessClientConfig(config *common.KubernetesConfig) (*restclient.Config, error) {
	// Try in cluster config first
	if inClusterCfg, err := inClusterConfig(config); err == nil {
		return inClusterCfg, nil
	}
	// in cluster config failed. Reading default kubectl config
	return defaultKubectlConfig(config)
}

func loadDefaultKubectlConfig(config *common.KubernetesConfig) (*restclient.Config, error) {
	cmdConfig, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	...
}
```

Three things follow, and they are why this document removes `host` rather than setting it:

- `host = ""` is what selects the kubeconfig path. Any non-empty `host` takes the other branch and
  the kubeconfig is never read.
- `clientcmd.NewDefaultClientConfigLoadingRules()` is client-go's standard loader: **`$KUBECONFIG`,
  then `~/.kube/config`**. That is the only way the file is found, which is why `KUBECONFIG` is a
  process environment variable and not a config key.
- `InClusterConfig()` is tried **before** the kubeconfig and wins if it succeeds. On the GitLab host
  it fails, because there is no `KUBERNETES_SERVICE_HOST`. Inside a Pod it would not.

The exec plugin is honoured because `clientcmd` carries it into the REST config - client-go
`tools/clientcmd/client_config.go`, `DirectClientConfig.ClientConfig()`:

```go
mergedConfig.ExecProvider = configAuthInfo.Exec
```

and `restclient.Config` uses that to produce the bearer token, including re-running the helper
before `expirationTimestamp`. The runner's own docs say the same thing in prose
(`docs/executors/kubernetes/_index.md`, v19.2.1): `host` is "Optional Kubernetes API server host URL
(auto-discovery attempted if not specified)", and `context` is to be used "when you don't specify
`host`".

## What was measured on 2026-10-07

Everything below was run against the live deployment, not reasoned from the source.

**The mount carries the exec path, and Caddy had to be told to speak HTTP/1.1 to do it.** An exec or
attach is an HTTP upgrade, and Caddy's upstream transport offers HTTP/2 by default - it forces
HTTP/1.1 for websockets only, and SPDY is not a websocket. Measured against the live front, with a
bogus bearer so nothing could be provisioned:

```
$ curl -sk -i --http1.1 -X POST -H 'Connection: Upgrade' -H 'Upgrade: SPDY/3.1' \
    -H 'Authorization: Bearer bogus' \
    https://kcp.socialweb.computer/clusters/root:<acct>:<cluster>:clusters/api/v1/namespaces/default/pods/probe/exec
HTTP/1.1 502 Bad Gateway                       # before: the upgrade died at Caddy, no Audit-Id
HTTP/1.1 401 Unauthorized ... Audit-Id: 1f84af2c-...   # after `versions 1.1` on the kcp vhost
```

A 401 with an `Audit-Id` is the tenant apiserver answering, which is the proof the request arrived:
the credential is what was wrong, not the hop. So the constraint 03 recorded - that the runner must
use the apiserver's loopback with a client certificate because the mount does not carry the exec
path - is lifted for this deployment, and the kcp vhost's `transport http` block carries
`versions 1.1` for that reason.

**The runner reaches the cluster from off-host.** With the config.toml block in this document, a
pushed commit produced a pipeline whose jobs were attributed to the tagged kcp runner, and two job
pods appeared in the tenant cluster through the mount:

```
scale-check-1   running   runner=kcp-tenant-kubernetes-streamplace
scale-check-2   running   runner=kcp-tenant-kubernetes-streamplace
scale-check-3   pending            # concurrent = 2
$ kubectl get pods -n gitlab-runner
runner-ipe-baff-project-1-concurrent-0-zolp6f3a   0/2   Pending   NODE <none>
runner-ipe-baff-project-1-concurrent-1-yl375jpx   0/2   Pending   NODE <none>
```

`Pending` with no node is the design at rest, not a failure: the pods are the intent that drives the
scale decision.

**Two things the recipe above assumes and the deployment had to be told.**

- The QR/session and the operator's own bidder are the trust graph's business: a tenant created by an
  atproto account can only buy a node from a bidder that account's vouches admit. Every bid from a
  bidder outside that graph is `policy_rejected`, which reads as an empty market rather than as a
  missing vouch, so the operator's own bidder has to be the one that runs.
- `denyBidderDids` steers who is *solicited*; a bidder that was not solicited can still submit, so a
  denied bidder could win. The deny is now enforced where bids arrive.

**A guest's tunnel address has one writer.** The reporter the guest runs reads its target out of
`/root/secrets/digitalocean.com/serviceaccount/base_url`, and the only writer of that file is the
provisioning-token script, which writes it only when its token exchange succeeds. A bidder with no
OIDC issuer therefore leaves the guest falling back to the public dispatcher, whose `/v1/on-network`
answers 404 - the unit fails with `[FAILED] Failed to start guest-onnetwork.service`, the guest never
reports the FQDN it already has in `/run/guest-fqdn`, and the requester waits out its whole timeout.
The firecracker provider now writes that file itself, from the same `guest_onnetwork_url` it extracts
from the accept bundle.
