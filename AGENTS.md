# AGENTS.md - working rules for this repository

Read this file first. It is the one guide for agents in this repository. `CLAUDE.md` imports it.

This repository (socialweb-computer-kcp) is the monorepo of the socialweb-computer project. It is the
single source of truth for its Go code and for all of its TypeScript. On 2026-10-12 the TypeScript of
ten repositories was vendored here with its git history, under `ts/<repository>/`. Those original
repositories are frozen. Do not edit them for a path that is under `ts/`. Make the change here.

`docs/ARCHITECTURE.md` holds the decisions, the measurements and the invariants. This file points at it
and states the working rules. Open a section of `docs/ARCHITECTURE.md` by its heading when a change
needs the argument behind it. Do not read it end to end.

## Orientation: which file answers which question

| Question | File |
| --- | --- |
| What is this, in one paragraph? | `README.md` |
| The objective, stated once | `docs/ARCHITECTURE.md` section 1 |
| Where a concept lives; the package layout | `docs/ARCHITECTURE.md` section 12, and Layout below |
| Rules a change must not break | `docs/ARCHITECTURE.md` section 13 |
| Why a decision was made, and what it closed | `docs/ARCHITECTURE.md` section 15 |
| The plans and the status of each item | `docs/plans/*.yaml` |
| The live backlog (what is open now) | `NEXT.yaml` |
| Which TypeScript entries ship | `deploy/ts-entries.yaml` |
| The TypeScript workspace, its tasks and versions | `deno.json`, `deno.lock` |
| Env the clusterprovider reads | `internal/providermain/config.go`, `deploy/container.env.example` |
| Env the gateway reads | `internal/gatewaymain/config.go`, `deploy/socialweb-gateway.env.example` |
| How to run the one-container stack | `deploy/run-container.sh`, `deploy/container.env.example` |
| What a host must hand the container | `socialweb-computer-kcp-check-host`, `docs/ARCHITECTURE.md` section 9 |
| The market test bed | `deploy/examples/atproto/market/README.md` |
| The operator handoff | `handoff.yaml` |

## Layout

Go:

| Directory | What it holds |
| --- | --- |
| `cmd/` | one directory per binary (`socialweb-computer`, `socialweb-gateway`, `socialweb-tsbundle`, ...) |
| `internal/` | the Go packages; `internal/tsbundle` finds and embeds the TypeScript |
| `api/` | the kcp API types; `go generate ./api/...` writes the schemas and the TypeScript wire types |
| `deploy/` | manifests, sets, scripts, `check.sh`, `ts-entries.yaml` |
| `docs/` | `ARCHITECTURE.md`, `plans/`, inventories, research notes |
| `test/` | repository-wide Go tests (no comments, ASCII, fixed ports, the bidder deploy) |
| `rust/` | socialweb-meshd |

TypeScript:

| Directory | What it holds |
| --- | --- |
| `deno.json` | the one workspace: every package below, the shared npm and jsr versions, the tasks |
| `deno.lock` | the one lockfile for the whole workspace |
| `socialweb-computer-k8s/` | the socialweb-computer-k8s web app: `hono-socialweb-computer-k8s/` (CLI), `lib/`, `web/`, `test/` |
| `ts/<repository>/` | the vendored code of one original repository, at its original path |

The vendored repositories are atproto-market, atproto-relay, deno-worker-sandbox, did-key-associator
(hono-backend only), did-key-ingress-proxy, hono-compute-provider, hono-pds, policy-engine,
socialweb-computer-ssh and typescript-helpers. Only the packages that the entries in
`deploy/ts-entries.yaml` reach came in, plus the tests of those packages and the files they read at run
time. TypeScript that is not in that set stays in its own repository (digitalocean-bidder,
deno-macos-runner-desktop, the did-key-associator frontend, open-architecture).

One package name exists once. The packages that socialweb-computer-ssh and socialweb-computer-k8s
shared have one copy, in `socialweb-computer-k8s/lib/`.

## The deno workspace and the lockfile

- Every package is a member of the root `deno.json` workspace. A package keeps its own `deno.json`
  (name, version, `exports: "./mod.ts"`, its own imports). No `deno.json` under `ts/` has a
  `workspace` field.
- Import another package as `jsr:@publicdomainrelay/<name>@^0`, through a bare `@publicdomainrelay/<name>`
  in the package's `imports`. The workspace resolves it to the member. Never import a relative path
  into another package.
- A new package: add its directory to `workspace` in the root `deno.json`, in its layer group
  (common, abc, implementation, hono-factory, CLI).
- Change a dependency, then refresh the lock at the root (`deno install --node-modules-dir=none`) and
  commit `deno.lock` in the same commit. The release build of `cmd/socialweb-tsbundle` refuses a
  lockfile that the fill created or changed, and `deploy/check.sh` runs `deno install --frozen`.
- Run deno with `--node-modules-dir=none`, as `internal/denorun` does. The root tasks pass it.

## TypeScript rules

These rules come from the org-root CLAUDE.md. They apply to all TypeScript here.

- ABC layering. One concept has up to five packages, and the path tells the layer:
  `lib/common/<concept>-common` (types, constants, pure helpers, external imports only),
  `lib/abc/<concept>` (`-abc`: interfaces and pure state, no I/O),
  `lib/<concept>-<transport>` (the implementation: timers, crypto, fetch, sockets, `Deno.*`),
  `lib/hono-factory-<concept>-<transport>` (the Hono factory, composed and never subclassed),
  `hono-<concept>` (the thin CLI). Imports go one way: common, abc, implementation, factory, CLI.
- A new transport is a sibling package, never a flag or an if-branch in an existing one.
- No cross-concept imports. Shared code goes to a common package. Cross-repository helpers are in
  `ts/typescript-helpers/lib/`; look there before you write a new common package.
- One package, one `mod.ts` export. No sub-module exports. Never name a package bare `common`.
- CLI: one `new Command(...)` from `@publicdomainrelay/cli-args-env` with `cli-args-env.json` and an
  optional `config.json`. No `Deno.env.get()` in a CLI. No hard-coded port, host or path in a CLI.
- `createLogger` and `createServe` from typescript-helpers; no raw `Deno.serve()`. Read the bound port
  from `onListen`; never listen on port 0, read the port, close and listen again.
- Signal handlers only in a CLI, never in a library.
- No comments in code. ASCII only.

## The kcp paradigm, port 0, and the RFP flow

- kcp is part of each application, not infrastructure. An application instance is its own kcp, a
  manifest set and a profile. State is kcp objects. Components are DenoPods and HostProcesses
  reconciled from those objects. Applications talk only over the atproto market protocol, never by
  sharing a kcp. Only the virtual Kubernetes clusters project uses kcp in the traditional way.
- Port 0 behind the edge: every listener binds port 0 and reports its bound address into the status of
  its kcp object. Peers find it by service name through kcp. Only the stable public entry points
  (caddy on 443, an application's front) keep a known address.
- The RFP flow is the only way a guest comes up: the requester posts a `compute.vm` record and a signed
  `market.rfp`, bidders bid, the requester accepts, and the winner boots the guest from cloud-init
  `user_data` (`buildUserData` and the module registry in `ts/atproto-market/lib/common/cloud-init-common`).
  The requester reaches the guest only through the relay. Never start a container or VM by hand in
  code or in a test, never install a guest agent outside cloud-init, never add a transport that the
  RFP cloud-init does not install. A test that needs a live guest drives `runComputeContract` against a
  local bidder (`ts/atproto-market/test/bidder_container_integration_test.ts` is the harness).

## How to verify

- `go test ./...` for the Go unit tests.
- `deploy/check.sh --fast` on every iteration: gofmt, go build, apigen drift, go vet, `deno task check`,
  `deno install --frozen`, the cross-OS builds, the size budget, staticcheck, go test. `--local` runs
  the proofs this host can run and names every skip. `--full` fails on any skip. Only `--full` means
  "everything ran"; a bare `go test ./...` exits 0 on SKIP and is never the gate.
- `deno task check` type-checks every entry of `deploy/ts-entries.yaml` from the root workspace.
- `deno task test` runs the TypeScript tests. The offline RFP tests need docker.
- The release closure: `go run ./cmd/socialweb-tsbundle -release -deno ~/.deno/bin/deno -out <dir>`.
  It needs a committed, clean tree and a current `deno.lock`, and it starts every entry offline.

## Where and how to work

- Work in a plain git worktree or branch of this repository. Do not put a worktree inside another
  worktree. The org-root `scripts/task-tree.ts` is for the polyrepo work that is left, not for this
  repository.
- Commit small logical changes and push after every commit. On `main`, push to origin main as work
  lands (user, 2026-10-12).
- Never use `/tmp`; it is tmpfs and spills to swap. Use `~/.tmp` (set `TMPDIR` under it).
- A one-off script is a `.ts` file under `$TMPDIR`, run with `deno run -A`, using `Deno.Command` for git
  and go. Keep each shell call to one plain command.
- Plans live in `docs/plans`. Flip an item's status only. Do not add evidence lists; the git history is
  the evidence.
- The original repositories in the org root are frozen. Never edit them for a path under `ts/`.

## The loop

- To wait for a container: `deploy/wait-ready.sh <container> [timeout-seconds]`. It polls
  `docker logs` at 1s under a hard timeout (default 60), prints the elapsed time and exits 0 when
  the readiness line appears, and prints the last 40 log lines and exits non-zero on timeout or
  container exit.
- NEVER write a sleep-poll loop such as `for i in $(seq ...); do sleep ...; docker logs ...; done`.
  It blocks a turn for 300-400s and cannot fail. Use `deploy/wait-ready.sh`.
- Read the state with `docker logs <container>`; the process already logs a status line. Do not
  reach for repeated `docker exec` or `docker inspect` for progress.

## Turns and background work

- Never end a turn with a build, test or container run in flight. Background a long command and
  keep working, or wait for it, but do not hand back a turn that says "still running".
- Parallelize reads, greps and edits. SERIALIZE builds, tests and container runs: 16 cores, shared
  ports, one liveport lock. Cap how many heavy agents run at once.
- Launch subagents for parallel work, then verify the artefact (the file, the exit code, the log
  line) rather than the summary.
- Nested subagents hit a depth limit. Do not spawn a subagent from inside a subagent; do that
  reading inline.

## Edit discipline

- Read the exact bytes before editing. The edit tool matches text, and a guessed string is a
  failed edit.
- A block that repeats: use `replaceAll`, not one edit per copy.
- One failed match is fixed by re-reading and retrying. Do not guess the same string again.
- Do not revert another agent's work. Run `git status` first; the tree is shared.

## Standing constraints

- No comments in code except the `ponytail:` form naming a ceiling and its upgrade path, and
  `//go:` directives. `test/nocomments_test.go` fails on any other comment and on any byte above
  0x7F.
- ASCII only, everywhere, including this file and `docs/ARCHITECTURE.md`.
- Decisions, measurements and invariants live in `docs/ARCHITECTURE.md`, never in code comments.
  An item opens in section 14 and moves to section 15, with its before/after readings, when it
  closes.
- The invariants in section 13 are load-bearing; a change must not break one.
- Runtime runs as USER 1000:1000. Nothing on the L0 host needs root.

## Names: use the real name

Write the real name of a thing: the repository, directory, package, binary, command or plan item id.
Do not make up a short name. Write plain, short sentences, with one meaning for each word. The plans
and older notes use the short names below; read them as the real names.

| Short name in the plans | Real name |
| --- | --- |
| the door | the socialweb-computer-k8s web app, `socialweb-computer-k8s/hono-socialweb-computer-k8s` |
| the ssh door | `ts/socialweb-computer-ssh/hono-socialweb-computer-ssh` |
| the bed | the market test bed, `deploy/examples/atproto/market` (`apply.sh`, `accept.sh`) |
| the core | the `socialweb-computer` binary process that runs kcp and supervises the DenoPods and HostProcesses |
| a fold, folded | one pass that merged a set of decisions into the plans (for example "fifth fold 2026-10-12") |
| the spine | the RFP flow: requester, RFP, bid, accept, cloud-init `user_data` |
| the front | the stable entry point in front of a service: the kcp front before the kcp slots (0006 M2-KCP-SLOTS), the TLS front of the market test bed |
| the edge | caddy and the other stable public entry points that keep a known port |
| the ratchet | `deploy/fixed-ports-ratchet.yaml`, the list of fixed-port violations that may only get shorter |
| a stand-in | a small test executable used in place of k3s, kine or postgres |
| the soak | the last run against the real accounts (the bobvmbuilder bidder and alice), after the market test bed passes |
| a gate | a check that must pass before an item or a merge goes on (`deploy/check.sh`, a plan's done_when) |
| the seed | the first example that a model grows from |
