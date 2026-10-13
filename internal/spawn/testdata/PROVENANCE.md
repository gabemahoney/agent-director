# Provenance of the Codex hook-trust golden data

These three files are the Codex hook-trust contract AD must reproduce
(Plan Bee b.9r1, Epic 4; SRD SR-5.2, probe answer RN-5). Tests read them;
nothing here is generated from AD's code, and nothing here may be edited by
hand.

| File | What it is |
|---|---|
| `hook-trust-golden.json` | Golden data: for 18 scenarios (211 entries per Codex version), the key and the trust hash Codex itself lists for every hook entry AD composes. |
| `hook-trust-format.json` | The same format rules, machine readable (key layout, identity object, normalisation, serialisation, hash, command quoting). |
| `FORMAT.md` | The same rules, as prose: what Codex hashes, AD's entries, the matcher decision (RN-15), independent recomputation. |

Read the golden data through `codexfix.LoadHookTrustGolden`
(`internal/testsupport/codexfix`); read AD's launch through `codexfix.Split`.

## Source

- Copied byte for byte from `tickets/Ideas/b.ssz/probe/golden/`
  (`FORMAT.md`, `hook-trust-format.json`, `hook-trust-golden.json`) on
  2026-10-12. The probe's answer is `tickets/Ideas/b.ssz/probe/answers.md`
  row RN-5 (with RN-2 for the help entry's limit, RN-9 for the relay window,
  RN-15 for the matcher, RN-23 for the merge of per-launch and folder state).
- Pinned (SHA-256 of the files as copied; a change to any file is a
  re-capture, never an edit):

  ```
  5abeafe80d55d0f24b5600578cbe31c59bbb5fbb06a76e279225655ce40aefa0  hook-trust-golden.json
  d0aa072f4e92dc898a8e5129c7101de875eb59945647edc42ff202bf7acd2eb7  hook-trust-format.json
  d958bc7b6bba8c7f9f19685ce5a4b73de93ed9591d0fc23fcc0514cd591b3a67  FORMAT.md
  ```

## Codex versions

Historical captures: 0.156.0, 0.159.2 and 0.162.1. The current supported
floor is 0.159.2; these original capture endpoints remain historical.
All three list the same
keys, hashes, timeouts and limits for every entry (`versionsAgree` is true in
the golden file; 633 entries listed in all, 0 mismatches), so the file holds
one entry per scenario row, not one per version.

## How it was captured

- Each key and hash is Codex's own answer: the `app-server` `hooks/list`
  response (`key`, `currentHash`) for a Codex started with AD's per-launch
  hook entries as `-c hooks.<Event>=[...]` and no `hooks.state`, in the probe's
  container (a `node:22-bookworm` image with Codex installed at each version, a mock model, no
  login, no model turn, 0 billed turns). The probe's `build_golden.py` folds the
  three versions' listings into the golden file and refuses to write it if they
  disagree.
- The binary paths are scrubbed stand-ins, not real paths: `plain` is
  `/home/u/.config/agent-director/bin/agent-director` and `special` is
  `/home/u/it's a dir/a\b/agent-director` (a space, a single quote and a
  backslash; its help entry uses the installed path
  `/home/u/it's a dir/a\b/.config/agent-director/bin/agent-director`). No
  session id, instance id or real home path is in these files.
- The special-path scenarios were re-captured on 2026-10-12 after the first
  capture's quoting was found wrong (the probe's own quote function escaped
  twice). The data here is the corrected capture: every command reads back
  through `sh` and `shlex.split` to the scenario's binary path exactly.
- An independent Python recomputation (`recompute.py`, standard library only,
  sharing no code with the capture) reproduces all 633 keys and hashes. A real
  Codex TUI launched with a `hooks.state` map built from these keys and hashes,
  the `projects` table and no bypass flag listed all 211 entries per version
  as `trusted`, showed no "Hooks need review" dialog and ran the hooks.

## What the data does and does not cover

- Covers: the eight hook events AD registers (SessionStart, UserPromptSubmit,
  PreToolUse, PostToolUse, PermissionRequest, Stop, Interrupt, SessionEnd)
  with their passed timeouts and, on PermissionRequest, `--timeout`; the help
  entry `exec <quoted installed path> help` with `additionalContextLimit`
  4000, with none, and absent; same-group and own-group layouts and both
  help-first orders; relay windows 86400, 3600 and 2147483; a `*` matcher on
  the tool events (the form AD does not use); and a `controls` scenario
  (explicit versus default timeouts, the SessionEnd and Interrupt clamp,
  matchers dropped on UserPromptSubmit, Stop and Interrupt, context-limit
  values 0, 2500, 3790, 4000, 100000).
- Also retains captured SubagentStart and SubagentStop entries. AD never
  composes these lifecycle entries: Codex rows do not track subagents. Tests
  of AD's composed entries compare only the events AD registers and exclude
  these retained captures.
- The historical corpus does not include the Q1 deny group. Its separate
  `hook-trust-deny-native.json` fixture is the native Codex 0.159.2
  `app-server` `hooks/list` response from Epic 6 Task1's `actual-composed`
  listing on 2026-10-13, including Codex's `currentHash`, command and matcher.
  It is extracted from `prerelease/task1/captures/native-golden-listings.json`
  by selecting the `request_user_input_async` matcher, without recomputing
  the hash or composing expected data. The native executable was
  `/usr/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex`.
  The listing used an isolated host HOME/CODEX_HOME, no login or model turn;
  the composed launch source was Task1's local mock evidence. The key's
  `/<session-flags>/config.toml` path and the deny command need no path scrub.
  Native 0.159.2 also matched all 211 historical corpus entries; their original
  versions and capture provenance above remain unchanged.
  SHA-256 of the separate fixture: `bc9cee0f4cc76e6e8deeac01d81e9f0c1a21e0ad63be7d3dc503012736e7b03f`.
- Open edges not probed (not AD's forms): `statusMessage`, a timeout of 0 or
  below on events other than SessionEnd and Interrupt, handler types other
  than `command`, `async: true`.

## Re-capture rule

Re-capture, never edit by hand, never regenerate from AD's own composer
(that would make the test compare AD with itself), when any of these happens:

1. A Codex version is added to the supported range, or the documented minimum
   changes (SR-16.2: the live checks Epic 6 runs).
2. AD's entry shape changes: a command string, a timeout, a matcher, the help
   entry's `additionalContextLimit`, a new event, or the Q1 deny group
   (re-capture the independent deny fixture).
3. A golden test fails and the cause might be Codex (a Codex release changed
   its hashing). First re-run Codex's own `hooks/list` on every supported
   version; only a listing that differs from this file justifies a new file.

To re-capture, use the probe tooling in `tickets/Ideas/b.ssz/probe/`
(`runs/golden.sh list` then `runs/golden.sh trust` for each version, then
`golden/build_golden.py` and `golden/recompute.py check`),
copy the three files here, update the SHA-256 lines and the Codex versions
above, and say why in the commit.
