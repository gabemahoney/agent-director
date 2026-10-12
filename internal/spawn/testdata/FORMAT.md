# Codex hook-trust format (RN-5)

Machine-readable twin: `hook-trust-format.json` (this directory; copy it to `internal/spawn/testdata/`). Golden data: `hook-trust-golden.json`.
Verified on Codex 0.156.0, 0.159.2 and 0.162.1: all three list the same keys and the same hashes for every entry (211 entries per version, 633 in all; 0 mismatches with `recompute.py`).

## 1. What Codex hashes

One hash per HANDLER. The key and the hash are what `app-server` `hooks/list` returns as `key` and `currentHash`.

Key: `/<session-flags>/config.toml:<event_label>:<group_index>:<handler_index>` (the source string is literal, angle brackets included; indices 0-based).
Event labels: `session_start user_prompt_submit pre_tool_use post_tool_use permission_request stop interrupt session_end subagent_start subagent_stop pre_compact post_compact`.

Identity object (then normalised):

```
{"event_name": <label>, ["matcher": <string>,] "hooks": [ {"type":"command", "command": <string>, "timeout": <int>, "async": false, ["additionalContextLimit": <int>]} ]}
```

Normalisation, each rule settled by a control in the golden data (`controls` scenario):

| Field | Rule |
|---|---|
| `timeout` | always present. Not given: 600, except SessionEnd and Interrupt: 1. SessionEnd and Interrupt are clamped into 1..3 (0 -> 1, 2 -> 2, 3 -> 3, 600 -> 3). Other events: the value as given (86400 relay window listed as 86400; RN-9: no cap). |
| `matcher` | hashed when the group has one and the event takes one. Ignored (not hashed) on UserPromptSubmit, Stop and Interrupt. On the tool events `*`, `Bash` and `""` each hash differently from no matcher. |
| `additionalContextLimit` | hashed only when given, not 2500 (Codex's default: hashes like none) and the event honours it: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, SubagentStart. Ignored (not hashed, Codex warns "ignoring additionalContextLimit") on Stop and the rest. 0, 3790, 4000, 100000 each hash differently. |
| `command` | the string as TOML-decoded, byte for byte (the special-path commands carry `'\''` and `'\\'` as written). |

Serialisation: compact JSON, keys sorted ascending at every level, separators `,` `:` without spaces, non-ASCII as itself (not `\uXXXX`), UTF-8.
Hash: `sha256:` + lowercase hex SHA-256 of those bytes. Example:
`{"event_name":"permission_request","hooks":[{"async":false,"command":"exec '/x' hook --agent codex --timeout 5","timeout":5,"type":"command"}]}`
hashes to `sha256:1049b8d0b6a31c6c2fe44795599a8730a632db112fcc9ed062f36586ba4de17d`.

The hash does not depend on layout: the group and handler indices live in the key only (checked for both paths). So AD's help entry has the same hash in the same-group and the own-group layout, and only its key moves
(`session_start:0:1` same-group; `session_start:1:0` own-group, where the AD entry stays `session_start:0:0`).

## 2. AD's entries (what AD passes, what Codex lists)

| Event | Command | timeout passed | timeout hashed | Notes |
|---|---|---|---|---|
| SessionStart | `exec <q path> hook --agent codex` | 600 | 600 | |
| SessionStart (help) | `exec <q installed path> help` | none | 600 | `additionalContextLimit` 4000 (RN-2); in the hash. Without a limit, or 2500: not in the hash |
| UserPromptSubmit, PostToolUse, Stop, SubagentStart, SubagentStop | `exec <q path> hook --agent codex` | none | 600 | |
| PreToolUse | same | relay window | relay window | default 86400, max 2147483 |
| PermissionRequest | `... hook --agent codex --timeout <relay window>` | relay window | relay window | |
| SessionEnd, Interrupt | same | 3 | 3 | |

No help entry on PreCompact or PostCompact (RN-3). No matcher anywhere (RN-15).

## 3. RN-15 (matcher form for Epic 4)

Use NO matcher on PreToolUse, PostToolUse and PermissionRequest. Both forms match every tool (RN-15, all three versions), so the choice is free; no matcher keeps one identity shape for all events (no per-event matcher rule can bite) and is today's AD form.
The other form changes the hash: `*` gives different hashes for all three tool events (golden scenario `plain-same-group-matcher-star`; keys unchanged). A later switch between the forms must therefore change the hashes in the same release.

## 4. Independent recomputation

`recompute.py` (standard library only; shares no code with `h/launch.py`, `h/goldenlist.py` or the research's `hookflags.py`) rebuilds every key and hash from the entries' inputs alone. `recompute.py check raw/<v>...` compares with Codex's listing: 633 entries, 0 mismatches.
`recompute.py state-from-inputs` writes the `hooks.state` map for a real launch; launches carrying that map, with `projects` for the folder but WITHOUT `--dangerously-bypass-hook-trust`, show every AD entry `trusted` (211 of 211 per version via hooks/list) and no "Hooks need review" dialog in the TUI (logs/golden-launch, logs/rn17).

## 5. Open edges (not AD's forms; not probed)

`statusMessage` on an entry (an optional field of the identity); a `timeout` of 0 or below on events other than SessionEnd and Interrupt; handler types other than `command`; `async: true`.
