# AGENTS.md - working rules for this repo

Read this file first. It names the one small file that answers each question, so an agent does not
start by reading the whole of `docs/ARCHITECTURE.md`. That document is still the single source of
truth; this file only points at it and states the working rules.

Commit and push as you go.

## Orientation: which file answers which question

| Question | File |
| --- | --- |
| What is this, in one paragraph? | `README.md` |
| The objective, stated once | `docs/ARCHITECTURE.md` section 1 |
| Where a concept lives; the package layout | `docs/ARCHITECTURE.md` section 12 |
| Rules a change must not break | `docs/ARCHITECTURE.md` section 13 |
| Why a decision was made, and what it closed | `docs/ARCHITECTURE.md` section 15 |
| The live backlog (what is open now) | `NEXT.yaml` |
| Env the clusterprovider reads | `cmd/socialweb-clusterprovider/config.go`, `deploy/container.env.example` |
| Env the gateway reads | `cmd/socialweb-gateway/config.go`, `deploy/socialweb-gateway.env.example` |
| How to run the one-container stack | `deploy/run-container.sh`, `deploy/container.env.example` |
| What a host must hand the container | `socialweb-computer-kcp-check-host`, `docs/ARCHITECTURE.md` section 9 |
| The operator handoff | `handoff.yaml` |

Open a section of `docs/ARCHITECTURE.md` by its heading when a change needs the surrounding
argument. Do not read it end to end.

## The loop

- Every iteration: `deploy/check.sh --fast`. To run the proofs this host carries (kcp, kine,
  k3s, postgres, openbao): `deploy/check.sh --local`, which also NAMES every skip it took.
  Before saying done: `deploy/check.sh --full`, which fails on any skip of a live input and so
  only passes when the live artifacts are present. README.md's Test section holds the detail.
- The slow and live tests are skipped by a bare `go test ./...`. That command exits 0 on SKIP,
  so it is NEVER the gate, and `--fast` is not the gate either. Only `--full` may be called
  "everything ran"; `--local` says exactly what it did not run.
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
