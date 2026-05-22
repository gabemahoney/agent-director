# Non-deterministic fields — selector manifest

`nondeterministic.json` records which envelope fields the diff harness must skip per verb — fields where `cmd/agent-director` (subprocess) and `*pkg/api.Client` (in-process) legitimately produce different values at invocation time. Because both sides read from the same fixture store, values written into the DB at fixture-creation time are frozen and identical on both paths; only values generated fresh at call time (UUIDs, build stamps, live timestamps) require entries here.

## Selector grammar

A selector is a dot-separated sequence of field names and optional array accessors. Both JSON paths and selectors root at the empty string, so a top-level field has a leading dot (`.field`). The leading dot is stripped before matching, making the two forms interchangeable.

| Form | Example | Matches |
|---|---|---|
| Bare field | `.version` | the top-level `version` field |
| Nested field | `.parent.child` | `child` inside `parent` |
| Exact array index | `.arr[0].field` | `field` on the first array element only |
| Wildcard array | `.arr[*].field` | `field` on any array element |

`[*]` in a selector matches any `[N]` accessor in the JSON path; all other segments match by exact string equality. Both sides of the match must be fully consumed — there is no suffix matching.

## Decision tree

```
Is the value read from the fixture DB (written at fixture-creation time)?
├── Yes → deterministic. Leave it in the diff.
│         All timestamps, IDs, paths, and results stored in the DB row are
│         frozen — both CLI and pkg/api.Client read the same value.
└── No  → generated at invocation time. Classify further:
    ├── UUID / per-call ID?
    │     → non-deterministic. (e.g. spawn's claude_instance_id,
    │       tmux_session_name, tmux_pid)
    ├── Timestamp set to time.Now() at call time?
    │     → non-deterministic.
    │       Contrast: timestamps READ from the fixture DB are deterministic —
    │       both sides see the same frozen row.
    ├── Build stamp (version string, commit hash)?
    │     → non-deterministic. The CLI binary is stamped with -ldflags at
    │       build time; pkg/api.Version() in-process returns the package
    │       default.
    ├── PID-bearing value set at call time?
    │     → non-deterministic.
    │       PID-bearing values already stored in the fixture DB are
    │       deterministic.
    └── OS-resolved absolute path?
          → deterministic in practice. Paths are HOME-based and derived
            from the fixture; add an entry only if you can prove divergence.
```

## Error-path semantics

When a verb fails, the harness compares the error envelope fields as follows:

- **`err_name`** — compared by verbatim equality. Both CLI and Client must return the identical error name string.
- **`err_description`** — compared by prefix: the substring up to and including the first `:`, or the full string if no colon is present. OS-specific wrapped-error wording can drift in the suffix across CLI and Client paths, so only the prefix is treated as canonical.

**Selectors in `nondeterministic.json` do not need per-`err_name` `err_description` entries.** The prefix-match policy already accounts for the suffix variance. If a diff reports an `err_description` mismatch, investigate the prefix logic — do not add a selector to paper over it.

## Worked examples

**Example 1 — non-deterministic field (`spawn`)**

`spawn` returns `claude_instance_id`, generated as a UUID at call time. The CLI's spawn invocation produces UUID A; the Client's spawn invocation produces UUID B. The fixture DB has no row for either value — it is generated, not read. Decision: **non-deterministic**. Selector: `.claude_instance_id`.

**Example 2 — fully deterministic verb (`status`)**

`status` reads a spawn row from the DB by `claude_instance_id`. Both CLI and Client open the same fixture DB copy, read the same row, and return identical values for `state`, `cwd`, `started_at`, and all other fields. No value is generated at call time. Decision: **deterministic** — empty selector list `[]`.

## Per-verb summary

Every callable verb in `manifest.CallableVerbs()` is a key (15 total). Verbs whose output is entirely fixture-derived carry `[]` and are diffed in full.

| Verb | Non-deterministic selectors | Reason |
|---|---|---|
| `spawn` | `.claude_instance_id` | UUID generated per call |
| `version` | `.version`, `.commit` | CLI stamped with -ldflags; `pkg/api.Version()` returns package default |
| `decide` | — | all values fixture-derived |
| `delete` | — | all values fixture-derived |
| `expire` | — | all values fixture-derived |
| `find-missing` | — | all values fixture-derived |
| `get` | — | all values fixture-derived |
| `kill` | — | all values fixture-derived |
| `list` | — | all values fixture-derived |
| `make-template` | `.path` | output path resolved at call time |
| `pause` | — | all values fixture-derived |
| `read-pane` | — | all values fixture-derived |
| `resume` | — | all values fixture-derived |
| `send-keys` | — | all values fixture-derived |
| `status` | — | all values fixture-derived |
