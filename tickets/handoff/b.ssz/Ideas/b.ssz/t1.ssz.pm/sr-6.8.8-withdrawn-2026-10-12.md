# SR-6.8.8 as approved on 2026-10-12, withdrawn the same day

The User withdrew this decision on 2026-10-12. It is kept for the record only and is not part of the SRD; SR-6.8.9 of t1.ssz.pm replaces it.

### SR-6.8.8: An instance id is locked to its agent
The User decided on 2026-10-12: "Agent IDs are locked to an agent." The PRD amendment records it, and AC-3 reads "reuse keeps the row's agent".

#### The rule
- **A row never changes agent.** While an id has a row, the id stays with the agent the row records: the one it was first spawned with. Resume already keeps the row's agent (SR-1.2), and a spawn with the reuse opt-in on a finished row now does too.
- **The lock lasts while the row exists.** There is no tombstone. After `expire` or `agent-director-admin delete` removes the row, the id has no row, and a plain spawn of it with either agent succeeds as today. The row's history, permission requests and subagent records go with it. Trail lines are not deleted, so the trail can show both agents under one id across a delete.
- **SR-1.2's last bullet** reads: "A spawn with the reuse opt-in on a finished row must resolve to the row's recorded agent, else it is refused with `ErrReuseAgentMismatch` (SR-6.8.8). A reuse that proceeds keeps the row's agent. A failed reuse whose restore applies puts back the row's previous life, its agent included (P-36)."
- **SR-7.6** reads: "A spawn with the reuse opt-in on a finished row whose resolved agent is the row's recorded agent resets the row to the spawn's pass-through arguments and `extra_env`, and clears its end report. Its agent is unchanged. A failed reuse whose restore applies puts every one of them back (P-36). A resolved agent other than the row's is refused (SR-6.8.8)."
- **Old rows.** A row from before the upgrade records `claude` (SR-3.4), so its id can be reused only for Claude.

#### The refusal
- **When.** A spawn with the reuse opt-in and an explicit id is refused with `ErrReuseAgentMismatch` when the id's row is finished and its recorded agent is not exactly the spawn's resolved agent. That includes a stored value that is not text, is empty, or is neither `claude` nor `codex` (a hand edit).
- **Where in the order.** The check comes after every SR-1.3 check, then after the collision pre-check. The collision pre-check refuses a live row, and a finished row without the opt-in, with `ErrInstanceIdCollision`, and a failed pre-check read with `ErrInternal`. The check comes before every step of reuse's own decision: the recorded-name guard, the launch socket, the tmux lookup.
  - In B, that is in `runSpawnWithReuseStore`, after `ApplyDefaults` returns `IDFinishedRow` and before `reuseExamine`'s first step.
- **What it reads.** It reads the agent from the reuse path's one pre-check read, `ReadForReuse`. Its SELECT already includes `agent`. `store.ReuseRow` gains the stored agent as a field (internal; no public surface changes): the text as stored, and "" for NULL or any value that is not text, which then never matches. No other read is made.
- **What it writes.** Nothing:
  - no tmux call, and no socket directory created;
  - no `ad.provenance.disagree` record;
  - no pre-trust entry;
  - no archive, reset or permission-request deletion;
  - no `ad.spawn.*` event and no other trail record.
  
  Everything before it in the order only reads.
- **The race.** The reset that would follow applies only while the row still holds the snapshot that was read, and every write changes `row_version`. So the agent cannot change between the check and a reset.

#### The error
`ErrReuseAgentMismatch` is new. It is the seventh new error name of SR-1.6 and follows SR-1.6's pattern:
- it is declared beside spawn's errors in `internal/spawn`, and re-exported from `pkg/api` in `aliases.go` with its own doc comment, with a row in `pkg/api/aliases_test.go`'s table;
- it is catalogued in `pkg/api/errnames` (Catalog, `catalog.json`, the generator's package map), keeping the five-way coherence test green and updating its comments that count the catalog's entries;
- it is listed in `spawn`'s `ErrorNames` in `pkg/api/manifest` and in `TestSpawnHasAllSRDErrorNames`;
- it gets a bullet in `Client.Spawn`'s "Errors:" Go doc, which `assertGoDocErrorsMatchManifest` requires to match `ErrorNames`;
- it gets an exported TypeScript error class, mapped from its `err_name` and exported from `src/index.ts`, with the public-surface goldens updated;
- it gets an `errorCases` row in `test/envelope-diff`, which strict mode (`ENVELOPE_DIFF_FULL_ERROR_COVERAGE=1`) needs;
- it gets an entry in `docs/caller-contract.md`'s error list.

#### Its description
- It names the id, quotes the row's recorded agent (`%q`) and names the requested agent.
- The advice: an instance id stays with its agent while its row exists, so spawn a new id for the requested agent (omit the id, or pick an unused one), or, when the recorded agent is `claude` or `codex`, reuse this id with that agent.
- Reusing with the row's agent while naming a template whose `agent_args` belong to the other agent gives `ErrAgentArgsMismatch`. The caller then passes its own `agent_args` (SR-2.1). The caller-contract entry says so.
- When the recorded agent is neither `claude` nor `codex`, only the new-id branch is offered, with the operator's `agent-director-admin delete` as the alternative.
- The advice is built from the existing spellings, `spawn.ReuseSpawn` and `AgentSpelling` (architecture.md's "Must use" rule).
- It is advice; the name is the contract (SR-1.6).

#### Help size
`help` prints each verb's name and Description only (`pkg/api/help.go`). `spawn`'s Description is not changed for this. The error appears in `ErrorNames`, in parameter descriptions and in the docs, none of which help prints. So help stays at its measured 14682 B, within the recorded 14700 B (P-37), and `helpStdoutBytes` is not re-recorded.

#### What does not change
- `resume` already keeps the row's agent and takes no agent input (SR-1.2, SR-7.4).
- The store's `ResetForReuse` keeps accepting either valid agent: the policy is the verb's. Its tests that reset a row to the other agent stay, with a comment that the verb refuses such a reuse.
- Spawn-again advice (SR-4.4) already tells the caller to pass the row's agent, which `get` shows. That is now the only agent a reuse accepts.

#### No TLA+ run
- The refusal comes before any write. It moves no row and releases or changes no hold.
- A reuse that proceeds is the same reuse as before, minus the agent change. The models stand a launch for reuse (SR-12.5) and have no per-row agent variable: Codex is a model-wide constant.

#### What it settles for subagents
A Codex row never holds a Claude agent's subagent records, and a Claude row never follows a Codex life. So `list-subagents` on a Codex row returns `tracked` false and `subagents` `[]`. A store written by an earlier build of this unreleased work, whose rows may already have changed agent, is the only exception.

#### Tests, in SR-14's terms
- **The fixture default.** `reuseParams` (`pkg/api/spawn_reuse_fixture_test.go`) takes its agent from the seeded row's agent by default, so a mismatch in a test is always explicit.
- **Store:** a `TestReuseReadForReuse` case in `internal/store/reuse_test.go`. It reads the stored agent into `ReuseRow` (`claude`, `codex`, a raw `Claude`, NULL), and none but the matching value equals the spawn's agent.
- **Existing `pkg/api` tests that encode cross-agent reuse.** Each moves to the new rule:
  - **`TestSpawnReuseAsCodex`** (`pkg/api/spawn_agent_test.go`): the Claude-row subtest becomes the refusal; the Codex-row subtest stays as the positive Codex-on-Codex reuse.
  - **`TestSpawnReuseTakesTheNewAgent`** (same file): renamed to `TestSpawnReuseKeepsTheRowAgent`. A Codex row reused as Claude is refused, and as Codex proceeds with agent `codex`.
  - **`TestSpawnReuseRestoreAfterEachLaunchFailure`** (`pkg/api/spawn_reuse_restore_test.go`): the Codex-row subcases reuse as Codex, and its comment is corrected.
  - **`TestSpawnReuseAsCodexRestoreAfterLaunchFailure`** (same file): the Claude-row subcase reuses a Codex row instead.
  - **`TestSpawnReuseAsCodexPreTrust`** (`pkg/api/spawn_pretrust_test.go`): the Claude-row subcases use Codex rows.
  - **`apitest.DescReuseFinishedParam`** (`pkg/api/apitest/descriptions_reuse_docs.go`): pins the new sentence "the reused row keeps its agent".
- **The refusal, in `pkg/api/spawn_agent_test.go`:**
  - Covered cases: a finished Claude row reused as Codex, a finished Codex row reused as Claude, and a pre-upgrade row (`WithAgent("claude")` with `WithNoSubagentTracking()`) reused as Codex. One more row has a stored agent that is invalid (`WithAgent("Claude")`, stored raw).
  - For each, `e.snapshotReuse` and `assertWroteNothing` show nothing was written. Every case also shows zero tmux socket calls and zero trail lines since the mark, which excludes the lookup and the `ad.provenance.disagree` records that `assertWroteNothing` alone allows.
- **The order, in `pkg/api/spawn_test.go`:**
  - a mismatched reuse that also fails the last SR-1.3 check (`ErrTmuxSessionNameInvalid`) gets that error;
  - a mismatched reuse whose pre-check read fails gets `ErrInternal`;
  - a mismatched reuse on a live row gets `ErrInstanceIdCollision`;
  - a mismatched reuse whose recorded name is unusable gets `ErrReuseAgentMismatch`, not the name guard's `ErrInternal`.
- **Surfaces:**
  - **CLI:** `cmd/agent-director/spawn_cli_test.go`, `--reuse-finished --agent codex` on a finished Claude row. It uses `assertOnlyEnvelope`, `rowColumns` and `assertInvocationKinds(t, home)`.
  - **MCP:** a case in `internal/mcp/spawn_reuse_param_test.go`, after its Go-client-parity pattern.
  - **TypeScript:** the smoke test "reuse_finished over an ended codex row, spawned as claude → the row now runs claude" (`pkg/ts-bun-client/test/smoke/spawn.test.ts`) becomes the real-CLI refusal: `ErrReuseAgentMismatch`, the row unchanged. Add the error-class and types-export tests and the public-surface goldens.
- **`test/envelope-diff`:** the `errorCases` row.
- **`pkg/api/errnames` and `pkg/api/manifest`:** the coherence, catalog, `ErrorNames`, Go-doc-errors and parameter-description tests with the new name and texts.
- **Advice:** `TestAdviceFollow_A25_ReuseAgentMismatchSpawnNewIdOrRowAgent` in `pkg/api/advice_follow_spawn_reuse_test.go`.
  - It follows each branch the advice offers, and sees the spawn succeed: a new id with the requested agent, and the same id with the row's agent.
  - For the invalid stored agent, it follows the new-id branch only.
- **Docker, `epic-13-agents`:** one t2 case, `agent-23`, after `agent-22`, placed as `agent-22` is (SR-6.8.4). The t1's sentence that every refusal case shows no row does not apply to it: it starts from an existing row. The case:
  1. A Codex row is spawned, reported in, and finished with `pause` (as `agent-14` does).
  2. Waiting out the stopping window, it is reused with `--agent claude`. The spawn exits 1 with `err_name` `ErrReuseAgentMismatch`. Through `/opt/driver/sql.sh` the row is unchanged, `tmux ls` shows no new session, the stand-in wrote no new record directory, and the trail gained no line.
  3. A reuse with `--agent codex` then proceeds: exit 0, the row `pending`, agent `codex`, `life_number` + 1.

#### Docs
Each fact goes in the doc that owns it (SR-15.1); error cases only in `docs/caller-contract.md`, `docs/operator-actions.md` and `docs/maintenance.md` (P-63).
- **`docs/settings.md`**, "The agent and its arguments": a reuse keeps the row's agent while the row exists. After `expire` or a delete, the id is free.
- **`docs/caller-contract.md`:** the new error, what the caller does, and the `agent_args` note above.
- **`docs/architecture.md`:**
  - every passage that says a reuse takes the new spawn's agent or can change the agent. That includes `ResetForReuse` "writes the new call's agent", "A reuse takes the new spawn's agent", "a Claude row can be reused as a Codex one and the reverse", and "a Codex reuse of a Claude row gets Codex's argv";
  - the reuse decision table gains a row for the refusal;
  - the lists of "the five" new agent errors become six, and the catalog's entry count goes up by one.
- **Manifest and Go docs:**
  - `get`'s `agent` field reads "set at spawn; a reuse keeps it", not "replaced by a reuse". `surface.json` and the generated `docs/cli-reference.md` and `docs/mcp-reference.md` are regenerated.
  - the public `Agent` field doc comments in `pkg/api/get.go` and `pkg/api/list.go` change the same way;
  - the `reuse_finished` parameter description and its CLI flag help: the reuse keeps the row's agent;
  - optionally, `spawn`'s `agent` parameter description, with its token test.
- **`pkg/ts-bun-client/src/types.ts`:** the `reuse_finished` JSDoc, with its public-surface golden.
- **Comments:** `internal/spawn/launch_errors.go`'s comment "the reused row takes it".
- **`pkg/api/README.md` and `pkg/ts-bun-client/README.md`:** the new error name in their error lists.
- **`pkg/ts-bun-client/CHANGELOG.md`:** the new error and the reuse rule, unless bug b.xnd removes the file before the release.
- **The breaking-change text:** the new error under "New", and that a reuse cannot change an id's agent.
