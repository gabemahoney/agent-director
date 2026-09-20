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
  diagnostics rather than hand-rolling the JSON.

- Gates **must not** mutate repository state. They are read-only checks.
  Any gate that needs to modify state should instead report a diagnostic
  and let the orchestrator decide whether to proceed.

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

`emit_diagnostic` handles JSON-escaping of all four fields. The
`offending_file_or_artifact` argument may be the literal string `"null"` to
emit a JSON `null` rather than a quoted string.

### `finalize/write-report.sh`

Invoked by the LLM orchestrator at the end of a release run to write
`dist/release-report.json`. It accepts the accumulated run state as
positional arguments:

```
write-report.sh <invocation-ts> <mode> <bump-kind> \
                <source-version> <target-version> \
                <phases-json-array> [<diagnostics-json-array>] \
                <elapsed-seconds>
```

`phases-json-array` is a JSON array of phase objects (SR-15 schema).
`diagnostics-json-array` is a JSON array of SR-14 payloads collected across
all gate stderr streams during the run; omit or pass `[]` if no gates failed.

## Orchestrator Usage

### Sequential phases

For phases whose gates still run one at a time, the LLM orchestrator runs
gates in the order defined for that phase. For each gate:

1. Execute the gate script.
2. **Capture stderr** into a variable/file.
3. If the exit code is non-zero, parse every line of captured stderr as a
   JSON SR-14 diagnostic and append it to the in-memory diagnostics list.
4. Record the phase outcome (`passed`, `failed`, or `skipped`).

### Coverage phase (parallel)

> **NOT YET RELEASE-READY.** The parallel coverage phase described below is
> implemented and tested — the runner, executor semantics, and report
> translation all work. But live measurement (Bee b.2mt, Epic t1.2mt.z4,
> 2026-09-20) showed the five coverage gates are **not filesystem-isolated**:
> they share `HOME`, the store, and `tmp`, and no `max_parallel` >= 2 currently
> produces an all-green phase. At >= 2, `coverage.bun-test` fails from sibling
> file leakage; at >= 3, `coverage.go-root`'s leaked-file detector also fires.
> Until the gate-isolation fix lands (Bugs bee b.3jn), the LLM orchestrator
> **MUST keep running the coverage gates sequentially**, following the
> "Sequential phases" section above. The field-mapping table below remains
> authoritative for whenever the parallel path is enabled.

Once enabled, the coverage phase will not run its gates sequentially. Its five
gate scripts —

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

#### Field-mapping: executor output → report phase object

The executor's consolidated output shape differs from the report phase shape
that `finalize/write-report.sh` consumes, and `write-report.sh` validates
only that the phases value is a JSON array. The orchestrator **must**
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

After all phases complete, invoke the finalize helper with the accumulated
phase objects and diagnostics list:

```bash
bash skills/release-agent-director/gates/finalize/write-report.sh \
  "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  "dry-run" \
  "patch" \
  "0.9.3" \
  "0.9.4" \
  "$phases_json" \
  "$diagnostics_json" \
  "$elapsed_seconds"
```

The resulting `dist/release-report.json` is the canonical artifact for
post-run inspection, CI upload, and audit trail.
