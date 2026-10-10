# 02 - The GitLab runner

End state: a runner registered to a project, running on the kcp host, with `host` set to the
tenant apiserver's loopback URL.

Needs [01](01-kcp-server.md) finished: a cluster, a URL, three files, a group name.

## GitLab

Already have an instance? Skip to registering a runner. Otherwise, on the kcp host:

```yaml
services:
  gitlab:
    image: gitlab/gitlab-ce:latest
    container_name: gitlab
    restart: always
    hostname: 'gitlab'
    shm_size: '256m'
    environment:
      GITLAB_OMNIBUS_CONFIG: |
        external_url 'http://gitlab:8080'
        gitlab_rails['gitlab_shell_ssh_port'] = 2222
        nginx['listen_port'] = 80
        nginx['listen_https'] = false
    ports:
      - '8080:80'
      - '2222:22'
    volumes:
      - /srv/gitlab/config:/etc/gitlab
      - /srv/gitlab/logs:/var/log/gitlab
      - /srv/gitlab/data:/var/opt/gitlab
```

Two things there are deliberate:

- **`hostname` is short, not an FQDN.** A long container hostname breaks Docker's internal DNS, and
  the failure presents as an unreachable OIDC provider rather than as a DNS problem.
- **`GITLAB_OMNIBUS_CONFIG` only applies on first boot.** Editing it later does nothing. Change
  `/srv/gitlab/config/gitlab.rb` and run `docker exec gitlab gitlab-ctl reconfigure` instead. This
  is the most common way a working GitLab is broken by a correct-looking edit.

Wait for readiness rather than sleeping:

```bash
until curl -fsS http://127.0.0.1:8080/-/health >/dev/null; do sleep 5; done
```

Install the runner on the same host:

```bash
curl -L "https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.rpm.sh" | sudo bash
sudo dnf install -y gitlab-runner
```

The runner does not run in the cluster. It runs here, because the apiserver binds loopback here.

## Register

Create the runner in the project's **Settings > CI/CD > Runners** and copy the authentication token.
The legacy registration-token flow is deprecated.

```bash
gitlab-runner register \
  --non-interactive \
  --config /etc/gitlab-runner/config.toml \
  --url https://gitlab.example.com/ \
  --token <runner authentication token> \
  --executor kubernetes
```

`register` writes a config with the token and nothing else useful. Replace the rest with the file
below.

## The file

Copy. Fill `<<...>>`. Everything else is load-bearing, table after says why.

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

| Key | Why |
| --- | --- |
| `concurrent` | Jobs in flight at once. One per node if anti-affinity is on. `1` wedges: a stale job from a cancelled pipeline holds the only slot. |
| `host` | The apiserver's own loopback URL, from [01](01-kcp-server.md#hand-back). NOT the kcp mount URL - that is a reverse proxy and `pods/exec` does not survive it. |
| `cert_file`/`key_file`/`ca_file` | The tenant credential. Client cert, NOT a bearer token. |
| `namespace` | Where job Pods go. Must exist. |
| `image_pull_policy` | `if-not-present`. Node images seed some images; always-pull throws that away. |
| `cpu_request`/`memory_request` | The scheduler uses THESE. Too big and the Pod never schedules. |
| `cpu_limit`/`memory_limit` | Enforcement only. The scheduler ignores them, so a limit over node size is fine. |
| `helper_*_request` | Set these. See the traps. |
| `poll_timeout` | Seconds to wait for the Pod to reach Running. Node boot plus a big image pull. |
| `output_limit` | Kilobytes of trace. Default 4096 = 4 MiB. |
| anti-affinity | One job per node, see [03](03-jobs.md#one-job-per-node). |

## Traps

**`pre_build_script` is a string, not an array.** An array is a startup failure, and the runner says
so only in its own output:

```
FATAL: Service run failed  error=decoding configuration file: toml: line 9
(last key "runners.pre_build_script"): incompatible types: TOML value has type []any;
destination has type string
```

Runner exits, nothing picks work up, and GitLab still reports the runner online, so a watcher waits
for a job that never runs. Fail on `FATAL` in the runner's own output, not on GitLab's view.

**A limit with no request IS the request.** Kubernetes defaults a container's request to its limit.
`helper_cpu_limit = 1` alone made every job Pod ask about 2.1 CPU - the build container's request
plus the helper's and the init container's, each pulled up from its limit - and a two-vCPU node
answers:

```
Unschedulable: "0/1 nodes are available: 1 Insufficient cpu."
```

Reads as a slow cluster. Is a Pod that cannot fit.

**`pre_build_script` runs AFTER the clone. `pre_get_sources_script` runs BEFORE it.** A DNS wait in
`pre_build_script` is too late - the clone already failed. Anything guarding the clone goes in
`pre_get_sources_script`.

**The 4 MiB log cap is the runner's, not GitLab's.**

```
Job's log exceeded limit of 4194304 bytes. Job execution will continue but no more output will be collected.
```

That string is in the `gitlab-runner` binary. GitLab's own ceiling is `ci_jobs_trace_size_limit`, a
PLAN limit (`project.actual_limits`), and it is not in `/application/settings` at all. Check both
before assuming which one bit you.

**Tools are per-image.** A `pre_build_script` assuming `corepack`, `pnpm` or `npm` fails the job
with `exit code 127` on an image that has none. Guard on the tool existing, and end with `true` so
the guard never fails the job it protects:

```toml
pre_build_script = "if ! command -v pnpm >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then npm install -g pnpm@10.11.0; fi; true"
```

## Start it

```bash
gitlab-runner run --config /etc/gitlab-runner/config.toml --working-directory /var/lib/gitlab-runner
```

Or as a service under a user that can read `/etc/gitlab-runner/tenant-ci/`.

## Check the credential before blaming the runner

```bash
openssl x509 -in /etc/gitlab-runner/tenant-ci/tls.crt -noout -subject
kubectl --kubeconfig <tenant-admin> auth can-i create pods \
  --namespace gitlab-runner --as=gitlab-runner --as-group=gitlab-runner
```

## Failure modes, in the order you will meet them

| Symptom | Cause |
| --- | --- |
| Runner exits at startup | `config.toml` did not decode. Its own output says which key. |
| Job stays `pending`, no node appears | The runner reached a DIFFERENT apiserver, or the Pod went to a namespace the observer does not watch. Compare `kubectl get pods -A` with the cluster's own port. |
| `x509: certificate signed by unknown authority` | Wrong `ca_file`, or a `host` port belonging to another cluster. Ports are leased per cluster and are not shared. |
| `Unauthorized` at job start | The certificate is from another cluster's ClientCA, or the Organizations do not match the RBAC group. |
| `Forbidden` creating pods | The ClusterRoleBinding's subject does not match the certificate's Organizations. |
| Job hangs at "Preparing the environment" | `pods/exec` unreachable. This is what happens when the runner points at the kcp mount URL instead of loopback. |
| Job passes, then a retry fails | The credential expired. Check `ValidFor`. |
| `Insufficient cpu`, no node appears | Pod requests do not fit a node. See the traps. |

Next: [03 - Jobs](03-jobs.md).
