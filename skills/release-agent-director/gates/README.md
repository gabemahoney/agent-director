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

The LLM orchestrator runs gates in the order defined for each phase. For
each gate:

1. Execute the gate script.
2. **Capture stderr** into a variable/file.
3. If the exit code is non-zero, parse every line of captured stderr as a
   JSON SR-14 diagnostic and append it to the in-memory diagnostics list.
4. Record the phase outcome (`passed`, `failed`, or `skipped`).

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
