# 05 - The runner's config.toml, key by key

Runner talks to GitLab over HTTPS. Runner talks to tenant apiserver over loopback.
Job becomes a Pod. Pod's Pending state drives scale decision. That is the whole path.

Read [README.md](README.md#reachability-the-single-operational-fact) first. Runner MUST reach
`127.0.0.1:<leased port>` on the kcp host. Port is leased per cluster. Read it, never hardcode:

```bash
kubectl --kubeconfig <admin> get cluster <name> -o jsonpath='{.status.url}'
```

## The file

Copy. Fill `<<...>>`. Everything else is load-bearing, table below says why.

```toml
concurrent = 8
check_interval = 3

[[runners]]
  name = "socialweb-tenant-ci"
  url = "https://gitlab.example.com/"
  token = "<<runner authentication token>>"
  executor = "kubernetes"

  # Runs BEFORE the clone. Gate on DNS here, never in pre_build_script.
  pre_get_sources_script = "if command -v getent >/dev/null 2>&1; then R='getent hosts'; elif command -v nslookup >/dev/null 2>&1; then R=nslookup; fi; i=0; while [ -n \"$R\" ] && [ $i -lt 180 ]; do $R \"$CI_SERVER_HOST\" >/dev/null 2>&1 && break; i=$((i+1)); sleep 1; done; true"

  # Runs AFTER the clone, before the job's script. A STRING, not an array.
  pre_build_script = "true"

  # Kilobytes. Default is 4096, which is 4 MiB and is NOT enough for a Go test log.
  output_limit = 51200

  [runners.kubernetes]
    host = "<<https://127.0.0.1:LEASED_PORT>>"
    cert_file = "/etc/gitlab-runner/tenant-ci/tls.crt"
    key_file = "/etc/gitlab-runner/tenant-ci/tls.key"
    ca_file = "/etc/gitlab-runner/tenant-ci/ca.crt"
    namespace = "gitlab-runner"
    image_pull_policy = "if-not-present"

    # Requests are what the scheduler places against. Set them EXPLICITLY.
    cpu_request = "100m"
    memory_request = "128Mi"
    cpu_limit = "10"
    memory_limit = "20Gi"

    # Helper and init containers too, or their limits become their requests.
    helper_cpu_request = "100m"
    helper_memory_request = "128Mi"
    helper_cpu_limit = "1"
    helper_memory_limit = "1Gi"

    poll_timeout = 1800
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

## Key table

| Key | Why |
| --- | --- |
| `concurrent` | Jobs in flight at once. One per node if anti-affinity is on. `1` wedges: a stale job from a cancelled pipeline holds the only slot. |
| `host` | Apiserver's own loopback URL. NOT the kcp mount URL - that is a reverse proxy and `pods/exec` does not survive it. |
| `cert_file`/`key_file`/`ca_file` | Tenant credential, see [06](06-runner-credential-and-rbac.md). Client cert, NOT a bearer token. |
| `namespace` | Where job Pods go. Must exist, see [06](06-runner-credential-and-rbac.md). |
| `image_pull_policy` | `if-not-present`. Node image seeds some images; a pull policy that always pulls ignores them. |
| `cpu_request`/`memory_request` | Scheduler uses THESE. Too big and the Pod never schedules: `Insufficient cpu`. |
| `cpu_limit`/`memory_limit` | Enforcement only. Scheduler ignores them, so a limit over node size is fine. |
| `helper_*_request` | Set these. Without them the helper's LIMIT becomes its REQUEST, see traps. |
| `poll_timeout` | Seconds to wait for the Pod to reach Running. Node boot + big image pull. 1800 is generous on purpose. |
| `output_limit` | Kilobytes of trace. Default 4096 = 4 MiB. |
| anti-affinity | One job per node. See [08](08-one-job-per-node.md). |

## Traps

**`pre_build_script` is a string, not an array.** An array is a startup failure, and the runner
says so only in its own output:

```
FATAL: Service run failed  error=decoding configuration file: toml: line 9
(last key "runners.pre_build_script"): incompatible types: TOML value has type []any;
destination has type string
```

Runner exits, nothing picks the job up, and a test that only asks GitLab "is the runner online"
reads a stale online and waits for a job that never runs. Fail on `FATAL` in the runner's own
output instead of trusting GitLab's view.

**A limit with no request IS the request.** Kubernetes defaults a container's request to its
limit. `helper_cpu_limit = 1` alone made every job Pod ask for about 2.1 CPU - the build
container's request plus the helper's and the init container's, each pulled up from its limit - and
a two-vCPU node answers that with:

```
Unschedulable: "0/1 nodes are available: 1 Insufficient cpu."
```

Reads as a slow cluster. Is a Pod that cannot fit.

**`pre_build_script` runs AFTER the clone, `pre_get_sources_script` runs BEFORE it.** A DNS wait
in `pre_build_script` is too late - the clone already failed. Anything that guards the clone goes
in `pre_get_sources_script`.

**The 4 MiB log cap is the runner's, not GitLab's.** `Job's log exceeded limit of N bytes. Job
execution will continue but no more output will be collected.` That string is in the
`gitlab-runner` binary. GitLab's own ceiling is `ci_jobs_trace_size_limit`, a PLAN limit
(`project.actual_limits`), and it is not in `/application/settings` at all. Check both before
assuming which one bit you.

**`corepack`, `pnpm`, `npm` are per-image.** A `pre_build_script` that assumes them fails the job
with `exit code 127` on an image that has none. Guard on the tool existing, and end with `true` so
the guard never fails the job it protects:

```toml
pre_build_script = "if ! command -v pnpm >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then npm install -g pnpm@10.11.0; fi; true"
```

## Start it

```bash
gitlab-runner run --config /etc/gitlab-runner/config.toml --working-directory /var/lib/gitlab-runner
```

Under a service, or by hand. Run it on the kcp host. Nothing else can reach the apiserver's
loopback port.
