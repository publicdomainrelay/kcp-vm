# 01 - Spin up the host that runs GitLab

Two options. Read the reachability rule in [README.md](README.md#reachability-the-single-operational-fact)
before choosing: the runner has to be able to reach the tenant apiserver at `127.0.0.1:<port>`, and
today that means the kcp host.

Not run here: `gitlab/gitlab-ce` is a multi-gigabyte image. The commands below are written from the
working prior art in `../gitlab-autoscale-socialweb-computer/setup/gitlab/` and the upstream GitLab
documentation. Where this document says a step was verified, it was verified in that sibling repo,
and the file it came from is named.

## Option 1 - GitLab on the kcp host (recommended, works today)

One host runs the control plane and GitLab. Nothing has to be exposed beyond loopback, and the
runner reaches the tenant apiserver directly.

### Docker Compose

Adapted from `../gitlab-autoscale-socialweb-computer/setup/gitlab/docker-compose.yml`, which is the
version that was actually run:

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

Two things in the prior art are deliberate and worth keeping:

- **`hostname` is short, not an FQDN.** A long container hostname breaks Docker's internal DNS, and
  the failure presents as an unreachable OIDC provider rather than as a DNS problem.
- **`GITLAB_OMNIBUS_CONFIG` only applies on first boot.** Editing it later does nothing. Change
  `/srv/gitlab/config/gitlab.rb` and run `docker exec gitlab gitlab-ctl reconfigure` instead. This
  is the single most common way a working GitLab is broken by a correct-looking edit.

`./setup/gitlab/setup.sh` in the sibling repo does all of the above, waits for health, and then
wires an optional ATProto OIDC login. Skip the OIDC half if you only want CI.

Wait for readiness rather than sleeping:

```bash
until curl -fsS http://127.0.0.1:8080/-/health >/dev/null; do sleep 5; done
```

Then open `http://127.0.0.1:8080/`, set the root password, and create a project. Create a runner in
the project's **Settings > CI/CD > Runners** and copy the authentication token for
[02](02-runner-kubernetes.md).

### Install the runner on the same host

```bash
curl -L "https://packages.gitlab.com/install/repositories/runner/gitlab-runner/script.rpm.sh" | sudo bash
sudo dnf install -y gitlab-runner
```

`packages.gitlab.com` answers from this host. The runner does not run in the cluster: see the
reachability rule.

## Option 2 - GitLab on a provisioned VM

This is the flow this project exists to provide - provision compute through the market, not by
hand. Do not `docker run` a VM into existence; drive the requester.

The requester CLI lives in the `publicdomainrelay` repo, and the compute provider and bidder stand
behind it (see that repo's `docs/SELF_HOSTED.md`):

```bash
cd /path/to/publicdomainrelay
deno run -A request-vm-ssh/mod.ts \
  --ssh-authorized-key "$(cat ~/.ssh/id_ed25519.pub)" \
  --vm-ready-timeout-sec 300
```

It prints JSON events on stdout; the one that matters is `vm_ssh_ready`, which carries the
hostname. That is the same event the Fleeting plugin in the sibling repo parses
(`../gitlab-autoscale-socialweb-computer/internal/instancegroup/vm.go`).

Then, over that SSH session, run the same `docker-compose.yml` as Option 1, with
`external_url 'http://<vm-fqdn>:8080'`.

`../gitlab-autoscale-socialweb-computer/user-data.yaml` is the cloud-config that was used for a
GitLab-adjacent VM. It is a starting point, not a requirement: the org rule is that a guest is born
from cloud-init `user_data` produced by the RFP flow, never assembled by hand.

### The wall you hit here

A GitLab on its own VM **cannot reach the tenant apiserver**, because the apiserver binds loopback
on the kcp host. The runner will report connection timeouts, and no amount of runner configuration
fixes it.

Making it work requires the kcp host to publish an authenticated front for the apiserver. That is a
trust-boundary change: the project keeps the gateway on loopback on purpose
(`cmd/socialweb-gateway/config.go:145-156`) and publishes exactly one port, whose only upstream is
the gateway (`deploy/container-entrypoint.sh`). Decide that deliberately, or use Option 1.

## Which option should you pick

| | Option 1 (on the kcp host) | Option 2 (on a VM) |
| --- | --- | --- |
| Runner reaches the apiserver | yes, loopback | no, until a front is added |
| Compute comes from the market | no | yes |
| Works today | yes | not without new work |
| Trust boundary unchanged | yes | no |

If the goal is CI that runs, use Option 1. If the goal is to demonstrate market-provisioned GitLab,
use Option 2 and read the wall above before you start.
