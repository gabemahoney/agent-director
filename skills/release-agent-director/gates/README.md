# Release Gates

This directory contains the gate-subprocess infrastructure for the
`release-agent-director` skill. Each gate is an independent executable that
enforces one release invariant. The LLM orchestrator (the `/release` skill)
runs gates as subprocesses, collects their outcomes, and ultimately writes a
structured report via the finalize helper.

## Gate Contract

Every gate script **must** adhere to the following contract:

- **Exit 0** when the gate passes. No output is required.
- **Exit non-zero** when the gate fails. Before exiting, emit **one or more
  SR-14 diagnostic objects** to **stderr**, one JSON object per line. Each
  object has the shape:

  ```json
  {
    "gate": "<gate-id>",
    "offending_file_or_artifact": "<path or null>",
    "description": "<human-readable explanation>",
    "corrective_action": "<what the operator should do to fix it>"
  }
  ```

  Use `lib/emit-diagnostic.sh` (see below) to produce correctly-escaped
  diagnostics rather than hand-rolling the JSON. The test
  `TestNoHandRolledDiagnosticJSON` fails on any `*.sh` here that `printf`s
  a `{"gate"` line.

- Gates **must not** mutate repository state. They are read-only checks.
  Any gate that needs to modify state should instead report a diagnostic
  and let the orchestrator decide whether to proceed.

- Command output, and any JSON that holds it (diagnostics, sub-checks,
  excerpts, report phases), reaches `jq` on stdin or in a file `jq` reads,
  never as `--arg` or `--argjson`. Linux caps one argument at 128 KiB; over
  that the exec of `jq` fails with "Argument list too long" and the JSON it
  was building is lost. `printf '%s' "$out" | jq -Rs .` turns raw output
  into one JSON string with no change to its content. The same cap applies
  to a script's own arguments, so a script that takes such JSON from its
  caller takes the path of a file holding it: the publish orchestrator's
  `--prior-phases` and `finalize/write-report.sh`'s phases and diagnostics
  (below). See docs/architecture.md "Command output reaches `jq` on stdin"
  for where the rule applies today.

- Every `*.sh` under this directory must pass `make release-shellcheck`,
  which the lint workflow (`.github/workflows/lint.yml`) runs on every PR
  and push to `main`. That workflow is pinned to `ubuntu-24.04`, so CI
  checks with shellcheck 0.9.0; a newer local shellcheck can report
  findings CI does not. Disable a check inline in the script, with a
  one-line reason, never globally.

## Helpers

### `lib/emit-diagnostic.sh`

A sourceable Bash library that exposes a single function, `emit_diagnostic`.
Source it at the top of any gate script:

```bash
source "$(dirname "$0")/../lib/emit-diagnostic.sh"
```

Then call it before exiting on failure:

```bash
emit_diagnostic \
  "preflight.worktree-clean" \
  "dist/agent-director" \
  "Uncommitted changes detected in dist/" \
  "Run 'git checkout -- dist/' or stash your changes before releasing."
exit 1
```

`emit_diagnostic` builds the object with `jq` and writes it to stderr as one
compact line. `jq` escapes everything JSON requires in all four fields:
quotes, backslashes, newlines, TABs, CRs and the other control characters
below U+0020. So raw command output, such as `go test` `FAIL` lines or Go
compiler errors, can go straight into a field and the line stays valid JSON.
The description reaches `jq` on stdin, so it may be any size. The other three
fields go as `jq --arg`, so each must stay under Linux's 128 KiB limit on one
argument; keep them to a gate name, a path and fixed text. The function needs
`jq` on `PATH`; without it no diagnostic is written. The
`offending_file_or_artifact` argument may be the literal string `"null"`, or
empty, to emit a JSON `null` rather than a quoted string.

### `finalize/write-report.sh`

Invoked by the LLM orchestrator at the end of a release run to write
`dist/release-report.json`. It accepts the accumulated run state as
positional arguments:

```
write-report.sh <invocation-ts> <mode> <bump-kind> \
                <source-version> <target-version> \
                <phases-json-file> [<diagnostics-json-file>] \
                <elapsed-seconds>
```

`phases-json-file` is the path of a file holding the JSON array of phase
objects (SR-15 schema). `diagnostics-json-file` is the path of a file
holding the JSON array of SR-14 payloads collected across all gate stderr
streams during the run; omit it or pass `""` if no gates failed. Relative
paths resolve against the caller's working directory.

The arrays go as files, never as the arrays themselves: they carry gate
output verbatim, and Linux's 128 KiB cap on one argument would stop the
script from starting. Each file must be a readable regular file (not a pipe
or `<(…)`) holding exactly one JSON array. Otherwise the script exits 2,
naming the argument, and writes no report. That includes an empty or
newline-only file and one holding SR-14 objects one per line (JSON Lines).

Inline JSON in place of a path, `[]` included, is refused too, in one of
two ways depending on its size:

- Under 128 KiB, the script exits 2 ("not a readable regular file").
- Over 128 KiB, the script cannot start: the calling shell reports
  "Argument list too long" and exits 126.

Neither writes a report.

## Orchestrator Usage

### Sequential phases

For phases whose gates still run one at a time, the LLM orchestrator runs
gates in the order defined for that phase. For each gate:

1. Execute the gate script.
2. **Capture stderr** into a variable/file.
3. If the exit code is non-zero, parse every line of captured stderr as a
   JSON SR-14 diagnostic and append it to the in-memory diagnostics list.
   "Finalizing" below writes the list as one JSON array, not one object per
   line.
4. Record the phase outcome (`passed`, `failed`, or `skipped`).

### Coverage phase (parallel)

> **RELEASE-READY.** Live measurement (Bee b.2mt, Epic t1.2mt.z4, 2026-09-20)
> originally showed the five coverage gates were not isolated from each other:
> they shared `HOME`, the store, and `tmp`, and no `max_parallel` >= 2 produced
> an all-green phase (`coverage.bun-test` failed from sibling file leakage at
> >= 2; `coverage.go-root`'s leaked-file detector also fired at >= 3). The
> gate-isolation fix (Bugs bee b.3jn) has since landed, so the parallel path is
> viable and the orchestrator no longer needs to serialize the coverage gates.
> The fix has five parts:
>
> - Each bun gate (`coverage.bun-test`, `coverage.bun-extra-scripts`) runs
>   under its own scratch `HOME` (`mktemp -d`), with `GOCACHE`, `GOMODCACHE`,
>   `GOPATH`, and `BUN_INSTALL_CACHE_DIR` pinned to their real locations first
>   so the caches are still shared. This keeps each gate's `$HOME`-resolved
>   writes (e.g. `~/.agent-director/ad-trail.jsonl`, templates) out of the real
>   home that `coverage.go-root`'s smoke canary watches via `user.Current()`.
> - The tree-write lock (`.tree-write.lock` at the repo root) keeps writes
>   inside the tree from racing the `coverage.docker-epic-*` children, which
>   collect the repo root as their docker build context. Tree writers hold it
>   exclusive; the children hold it shared (`flock -s`). See "Tree-write lock"
>   below.
> - `no-leak.test.ts` scopes its process count to children of its own process
>   (`pgrep -c -P $pid agent-director`) rather than the host-global
>   `pgrep -c agent-director`, so sibling gates' binary spawns no longer break
>   its strict-equality assertion.
> - `coverage.bun-test` holds an **exclusive** `flock` on
>   `${TMPDIR:-/tmp}/agent-director-ts-bun-dist-pack.lock` for its entire run.
>   go-root's four pack-first tests read `pkg/ts-bun-client/dist/` under that
>   lock, while the gate rewrites `dist/` via `bun run build`. The lock is
>   exclusive because the gate is a `dist/` **writer**, not merely a reader.
>   Every run of the gate takes the lock, so nothing may run the gate while
>   holding it: the gate would wait on its own caller. go-root's
>   `coverage-bun-test-fires` runs the gate against a fixture package in a temp
>   dir and holds no lock; it never touches the real test sources or `dist/`
>   (b.jct).
> - The pack gate scripts (`gates/pack/pack-first.sh`,
>   `gates/pack/repack-and-verify.sh`) create their `pack-staging.XXXXXX` /
>   `pack-staging2.XXXXXX` staging dirs under `${TMPDIR:-/tmp}` instead of at the
>   repo root. go-root's four `pack-first` synthetic-regression tests run those
>   scripts concurrently with `coverage.docker-epics`, whose `docker build` tars
>   the repo root as its build context; a staging dir appearing then vanishing
>   mid-tar killed the context (`Can't add file .../pack-staging.g8vcIQ to tar`).
>   Moving the staging dirs out of the tree removes the race class outright
>   rather than adding another lock — the transient dir is no longer inside the
>   tarred context at all, so no reader/writer coordination is needed. (The repo
>   has no `.dockerignore`; `pack-staging*/` is also gitignored as
>   belt-and-suspenders.)
>
> A module boundary, not a lock, keeps `coverage.go-root`'s
> `go test ./... -race` walk out of `coverage.bun-test`'s way.
> `pkg/ts-bun-client/go.mod` is a stub with no Go code. Go's `./...` patterns
> skip a directory that holds its own `go.mod`, so the walk never enters
> `pkg/ts-bun-client/`, where the gate's `bun install` rewrites `node_modules/`
> and `bun run build` deletes and recreates `dist/`. Without the stub the walk
> fails with `pattern ./...: open .../dist: no such file or directory` when
> `dist/` vanishes mid-walk. Its module path,
> `agent-director.invalid/ts-bun-client`, lies outside the root module's path,
> so a root finder that matches the root module line
> (`test/envelope-diff/harness.go`) skips it; finders that stop at the nearest
> `go.mod` (most `repoRoot` helpers) would stop there, so Go code that walks up
> that way must not run with its cwd inside `pkg/ts-bun-client/` (today the
> only `go` command run there is `go env`, in `bun-test.sh` and
> `bun-extra-scripts.sh`).
> The stub is not in the npm package (`package.json` `files` lists only `dist/`
> output and `README.md`).
>
> A diagnosability companion fix (SR-14) rides alongside these isolation
> mechanisms but is not itself an isolation mechanism. `coverage/docker-epics.sh`
> previously failed **silently** at the phase level: its child failures lived
> only in the consolidated stdout JSON that the phase executor discards, so the
> phase saw `exit=1` with empty stderr and `diagnostics:[]`. The gate now
> re-emits each failed child's diagnostic to **stderr** in SR-14 shape while
> leaving its stdout and exit code unchanged, so failures surface in the report
> instead of vanishing.
>
> The tuned `max_parallel` value for this phase is persisted separately (see
> Bee b.2mt); this note does not fix a value.

The coverage phase does not run its gates sequentially. Its five gate
scripts —

- `coverage/go-root.sh`
- `coverage/bun-test.sh`
- `coverage/docker-epics.sh`
- `coverage/bun-extra-scripts.sh`
- `coverage/go-consumer-dryrun.sh`

— run concurrently through the unchanged `lib/run-parallel.sh` executor,
driven by the phase runner `coverage/run-coverage-phase.sh`. The runner is
**phase orchestration, not a sixth gate**: it only builds the gates-config
and delegates to the executor.

Invoke it **from the repository root**:

```bash
bash skills/release-agent-director/gates/coverage/run-coverage-phase.sh
```

The runner writes its consolidated JSON output to stdout and propagates the
executor's exit code unchanged:

- **0** — all five gates passed.
- **1** — one or more gates failed.
- **2** — usage / configuration error (unknown argument, or a missing or
  malformed gates-config).

**Sibling tolerance.** All five gates run to completion even when one or more
siblings fail; nothing is masked by the first failure. Every gate's
individual outcome and its diagnostics appear in the consolidated output
regardless of sibling failures. The phase's overall outcome is `failed` if
any gate failed, `passed` only if all passed.

#### Tree-write lock

`.tree-write.lock` at the repo root (b.k42; formerly the b.2y5
`pkg/api/apitest/.seeds-mutation.lock`) keeps writes inside the repo tree from
racing the `coverage.docker-epic-*` children. Each child's
`make test-docker EPIC=<slug>` collects the whole repo root as its docker build
context (the repo has no `.dockerignore`, and `test/Dockerfile` copies
`bin/agent-director` and `bin/agent-director-admin` from it) and bind-mounts
the live worktree read-only.

**The race.** In the sandbox the tree is a bind mount, so `go build` cannot
rename its output into the tree from the container's `/tmp` (the rename
crosses filesystems). It deletes the old binary, then copies the new one in
(unless the output directory is setgid, it first also creates and deletes an
`<output>-go-tmp-umask` probe beside the output). `bun install` rewrites
`pkg/ts-bun-client/node_modules/`, and `bun run build` deletes and recreates
`pkg/ts-bun-client/dist/`. A context collection that reaches such a path
mid-write fails with `checking context: file '.../<path>' not found` (the
b.3jn class) or bakes a half-written binary into the image.

**Holders.** Tree writers take the lock exclusive; context collectors take it
shared.

| Holder | Mode | Held around |
| --- | --- | --- |
| test preload (`pkg/ts-bun-client/test/setup.ts`) | exclusive | each `make`, which rewrites `bin/agent-director`, `bin/agent-director-admin`, `bin/ts-helper` and `test/fake-tmux/tmux` |
| `coverage.bun-test` (`bun-test.sh`) | exclusive | `bun install --frozen-lockfile`, then `bun run build`, one hold each. Not `bun test`: its preload takes the lock itself |
| `docker-epics.sh` | exclusive | one `make build`, before any child starts |
| each `coverage.docker-epic-*` child | shared (`flock -s`) | its whole `make test-docker` run |

Shared holders overlap one another, so the docker epics keep their
`max_parallel` fan-out, and an exclusive holder excludes all of them. Linux
`flock` does not favour a waiting exclusive locker, so a writer can wait behind
a run of children.

No other test takes the lock. `rc-stamp.test.ts`'s `make release-binaries`
writes only to a temp `RELEASE_DIST_DIR`. No test writes a tracked file
(helper-tag-replay mutates a copy of the Go module under a temp dir). Since
b.9qj the source-of-truth tests run the gate in temp git repos, and
`check-version-coherence.test.ts` and `version-bump.test.ts` stage their
versioned `package.json` fixtures under the OS temp dir.

**`bin/` pre-build.** Each child's `make test-docker` runs `make build` first
(`test-docker` depends on `test-image`, which depends on `build`), under the
child's shared hold. In a release run `bin/` is always stale when the coverage
phase starts: `make build` stamps the HEAD commit (`COMMIT_SHA`) into both
binaries, and branch-and-bump commits just before. So `docker-epics.sh` first
runs `flock .tree-write.lock make build` once, before `run-parallel.sh` starts
any child. Each child's own build then finds both binaries current and only
updates their mtimes, which a concurrent context collection tolerates. If the
pre-build fails, the gate emits one SR-14 diagnostic (gate
`coverage.docker-epic-prebuild`, artifact `bin/`) and exits 1 without starting
a child. `--dry-run` builds nothing.

**Lock order.** The dist-pack lock first, then the tree-write lock.
`coverage.bun-test` holds the dist-pack lock for its whole run and takes the
tree-write lock per command. The preload's builds nest the same way, because
`bun test` inherits the gate's dist-pack lock fd. Nothing that holds the
tree-write lock takes the dist-pack lock, so the two cannot deadlock. A new
holder of both must keep this order.

**Reproduction.** Docker is not available in the sandbox, so `tar` stands in
for docker's context collection:

```bash
make sandbox CMD='
( while [ ! -e /tmp/stop ]; do tar -cf - bin 2>&1 >/dev/null | cat; done ) > /tmp/tar.log &
( while [ ! -e /tmp/stop ]; do [ -e bin/agent-director ] || echo "bin/agent-director: missing"; done ) > /tmp/poll.log &
for v in 1 2 3 4 5; do AGENT_DIRECTOR_BUILD_VERSION=0.0.0-race$v make build >/dev/null 2>&1; done
touch /tmp/stop; wait
sort /tmp/tar.log /tmp/poll.log | uniq -c | sort -rn
make build >/dev/null 2>&1'
```

Each version stands in for a new commit's `COMMIT_SHA`, so every `make build`
really rewrites `bin/`. The tar loop stands in for context collection, and the
poll loop reports each moment `bin/agent-director` is missing. The last
`make build` restores a normally stamped `bin/`. One run printed:

- 551 `bin/agent-director: missing`
- 13 `tar: bin/agent-director: file changed as we read it`, and 11 of the same
  for `bin/agent-director-admin`
- 6 `tar: bin: file changed as we read it`
- 2 `tar: bin/agent-director-admin: File removed before we read it`

`File removed before we read it` is tar's form of docker's
`checking context: file '…' not found`. `file changed as we read it` means a
half-written binary in the image. The control, the same loop building one
fixed version so that `bin/` stays current, printed nothing.

**Known gaps.** Two tests add a path to the tree without taking the lock:

- `TestWorktreePollution` (b.ngj, open) adds a path at the repo root.
- "README TS snippets typecheck" in
  `pkg/ts-bun-client/test/readme-snippets.test.ts` (b.1xi, open) writes
  `test/tmp-readme-check-<ms>.ts` beside itself for the few seconds `tsc`
  takes to check it, then deletes it. It runs in `coverage.bun-test`'s
  `bun test`, concurrently with the docker epics' context collection.

#### Field-mapping: executor output → report phase object

The executor's consolidated output shape differs from the report phase shape
that `finalize/write-report.sh` consumes, and `write-report.sh` validates
only that its phases file holds one JSON array. The orchestrator **must**
translate the executor output into the report phase shape before invoking
`write-report.sh`, applying the following mapping:

| Executor output field | Report phase field | Rule |
| --- | --- | --- |
| `phase_name` | `name` | direct copy |
| `duration_ms` | `elapsed_ms` | the report phase's `elapsed_ms` is the SUM of all sub-check `duration_ms` values (the executor emits `duration_ms` per sub-check only); the sum preserves the pre-parallelization semantics, where a phase's elapsed time was the sum of its sequentially-run gates |
| (per sub-check) `diagnostics` array | (per sub-check) scalar `diagnostic` | each sub-check's `diagnostics` array maps to the report's scalar `diagnostic` as the FIRST diagnostic object in the array, or `null` when the array is empty; any remaining diagnostics beyond the first are appended to the report's top-level `diagnostics[]` array |
| — (not emitted by executor) | `started_at` | an ISO-8601 UTC timestamp captured by the orchestrator immediately before invoking the phase runner, via the exact command `date -u +%Y-%m-%dT%H:%M:%SZ` |

The executor's per-sub-check `name` and `outcome` carry over unchanged to the
report sub-check's `name` and `outcome`.

**Known limitation.** The LLM's release-time translation itself is verified
only by this README table plus the codified-mapping test (which applies a
copy of this table to real executor output and feeds the result through
`write-report.sh`). A finalize-time shape lint inside `write-report.sh` would
be a stronger guarantee, but it is a separate decision and deliberately out of
scope here.

This parallel translation applies to the coverage phase only. The same shape
drift in other gates (cross-compile, per-binary-smoke, and the coherence
gates) is **not** remediated by this work — that is a PRD Non-Goal.

### Finalizing

After all phases complete, write the accumulated phase objects to one file
and the diagnostics list to another, each as one JSON array, then invoke
the finalize helper with the two paths.

- `$phases_json` holds the phase objects as one JSON array.
- `$diagnostics_json` holds the diagnostics list as one JSON array too. The
  stderr lines that "Sequential phases" step 3 parses are one SR-14 object
  each, so join them into one array (`jq -s .` turns such lines into one);
  never write them one per line. If no gate failed, `$diagnostics_json`
  may be empty or unset, and `${diagnostics_json:-[]}` below writes `[]`.

```bash
phases_file="$(mktemp)"
diagnostics_file="$(mktemp)"
printf '%s\n' "$phases_json" > "$phases_file"
printf '%s\n' "${diagnostics_json:-[]}" > "$diagnostics_file"
bash skills/release-agent-director/gates/finalize/write-report.sh \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "dry-run" \
  "patch" \
  "0.9.3" \
  "0.9.4" \
  "$phases_file" \
  "$diagnostics_file" \
  "$elapsed_seconds"
```

`printf` is a shell builtin, so writing the files is not limited by the
argument cap. If either file holds anything but exactly one JSON array (an
empty or newline-only file, or objects one per line included), the script
exits 2 and writes no report. Passing `"$phases_json"` itself in place of
`"$phases_file"` writes no report either: under 128 KiB the script exits 2;
over 128 KiB the shell cannot start it ("Argument list too long", exit
126).

The resulting `dist/release-report.json` is the canonical artifact for
post-run inspection, CI upload, and audit trail.
