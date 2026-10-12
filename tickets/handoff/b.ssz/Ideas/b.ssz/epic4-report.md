# Epic 4 report: Codex agents run under AD with Claude's contracts, the relay included

Epic t1.9r1.u5 of Plan Bee b.9r1 (Idea b.ssz). Worktree agent-director-bssz, branch feature/b.ssz. Not pushed.

## Result
Done. All five Tasks are committed and marked `done`. E4-T6 (t2.9r1.u5.3x) was dropped under option B. The Epic itself stays `active` until Bee close (do-bee step 7).

**Integration Suites: passed.** The Epic run used tag e4int, ran all 15 groups through the job scheduler with `--network off`, and passed first time. epic-13-agents passed 21 of 21 (agent-1 to agent-21). Job ids are in `epic4-docker-run.md`.

| Task | Commit | Integration after the Task |
|---|---|---|
| E4-T1 t2.9r1.u5.ym: hooks from Codex agents | bc30c7c3 | e4t1: 15/15 |
| E4-T2 t2.9r1.u5.xm: Codex launch for spawn, reuse, resume | d79bc772 | e4t2: 15/15 (agent-8..10 green) |
| merge fix/b.wur (orchestrator request, before E4-T3) | 27d41cd7 | `make test-sandbox` green; covered by e4t3 |
| E4-T3 t2.9r1.u5.i4: the end of a Codex agent, every verb | bc9bcf44 | e4t3: 15/15 (agent-11..17 green) |
| E4-T4 t2.9r1.u5.39: permission relay on Codex rows | 4156e971 | e4t4: 15/15 (agent-18..21 green) |
| E4-T5 t2.9r1.u5.at: Codex callers, docs, differences audit, acceptance | 35707897 | e4int: 15/15 |

## Acceptance criteria (Epic)
- **`make test-sandbox`:** green on the final tree, run twice by the acceptance pass: bun 409/409 and every Go package ok. That includes `test/tla`'s `TestCodexProvenance` and `TestCodexHasNoJar`.
- **`make generate`:** leaves no diff.
- **Help size:** `help_size_test.go`'s constant is unchanged at `helpStdoutBytes` 14700. Help is now 14682 bytes (see Decisions).
- **Docker:** every slug passes, plus install-mode.
- **Demo steps 1–6:** all pass in the sandbox with the stand-in `codex`, a throwaway HOME and TMUX_TMPDIR, and relay_mode on. The transcript is in `epic4-demo.md`.
  - Step 3 first failed on a timing artefact: an Interrupt fired in the same second the request was recorded. That is the specified one-second rule; a 2 s wait made it pass.
  - Step 4 also confirms `list-subagents` reads `tracked: false` on a Codex row.
- **README "First spawn":**
  - The block as written leaves the row `pending` for both agents, because the stand-ins send no hooks on their own.
  - With the hook events typed in, it works for both agents.
  - Claude's `pause` then times out because the stand-in claude never answers `/exit`; this is a limit of the stand-in, not a bug.
- **The real-Codex run of these steps belongs to Epic 6.**

## Q2 TLA+ gate (t3.9r1.u5.39.jx): PASS
All 15 new runs, submitted through the job scheduler, matched their expected verdicts.
- **Probes and control:** `rx_V_review` and `r5x_V_review` fail on NeverHit, which shows the `/review` residual is reachable. `r5x_lim_rowsettles_rv` fails on plain RowSettles only.
- **Review-knob runs:** `rx_safe_rv`, `rx_keys_rv`, `rx_live_rv`, `r5x_time_rv` and `r5x_t_rv` all pass. KeysSafe and NoStaleHold hold.
- **Equivalence:** exact to the state at both levels: 109,518 and 833,740, then 64,027, 67,511, 35,589, 296,382 and 2,122,224.
- **Finding F6:** a `/review` write can leave the row `working` until the next prompt's Stop.
  - In Hold36hCx this falls under RowSettlesCx's existing exception.
  - B146R5Cx needs one named exception, RowSettlesRv.
- **Modelling choice:** a review starts only at an idle prompt. The optional over-approximation, "while the turn-end hook is still due", was not modelled.

The five optional SR-12.3 runs were submitted during vendoring and all came back as expected:

| Run | Verdict | Distinct states |
|---|---|---|
| hx_S5 | PASS | 45,361,001 |
| hx_V_endlive | FAIL NotCxEndLive (probe) | — |
| hx_V_missing | FAIL NotCxMissing (probe) | — |
| rx_V_intproof | FAIL NeverHit (probe) | — |
| rx_claude_keys | PASS | 239,820 |

Everything is vendored in `spec/tla/codex/`: 260 files pinned by `PROVENANCE.txt`, with no jars.

## Decisions taken during the Epic
- **Help bytes, option A (orchestrator, 2026-10-12).** find-missing's description now points to kill for the same-environment consequences paragraph instead of repeating it. That saves 158 bytes and loses no fact.
  - This amends SR-18.7. The amendment is recorded in the Epic body twice (mine and the orchestrator's); tidy that up.
  - Help is now 14682 of 14700 bytes. The constant was not re-recorded downward; whether to bank the 18 bytes is your call.
- **E4-T6 dropped (option B).** Codex rows keep Claude's delivery contract. `BASH_ENV=` is set on every Codex launch. The printing-`.zshenv` residual is documented in permissions.md ("Startup files that print") and in operator-actions.md.
- **No transcript heal after a relayed PermissionRequest.** A heal there would add a store write with an uncapped lock wait before the poll, which risks the ack reserve. The next applied hook heals instead (RN-10). This is documented in hooks.md and architecture.md.
- **pause's wait keys the launch on its token and life number.** An adoption write that fills in server or pane identity during the wait no longer stops the move to `ended`.
- **127 C-u in one call.** Codex rows clear the line with `tmux send-keys -N 127 C-u`, never C-c. The 64-line limit is documented.
- **The first hook on a row with no session id may record a `/review` rollout path.** Accepted: the next SessionStart overwrites it.
- **SR-15.1 applied to every hand-written user doc.** Lane 2 widened `TestTrimmedUserDocs`. About 25 older "before this release" phrases and the bee ids `b.qe2` and `b.45p` were reworded in plain words, with no fact lost.
  - A heading in permissions.md was renamed to "Requests recorded by an older binary".
- **README.** The Codex-setup link was removed per docs/readme-guide.md, which departs from Subtask 8f's text. settings.md owns the setup.
- **Reconcile at Epic start.** 36 reconcile notes were appended to 5 Tasks and 31 Subtasks after Epic 3 and the b.xjn merge.
  - b.xjn's subagent capture is gated to Claude in the classifier.
  - Codex launches store `subagent_tracking` 0.
  - The Codex move to `ended` closes subagent records, so the store now has five closing writes.

## Follow-ups to file
1. **"before this release" wording remains outside the hand-written user docs (SR-15.1).** It is not in help, because help prints only verb descriptions. Locations:
   - `pkg/api/manifest/manifest.go` field and parameter descriptions (about lines 245, 266, 267, 271, 275, 276, 558, 875 and 890), and through them `surface.json` and the generated `docs/cli-reference.md` and `mcp-reference.md`;
   - the error text at `pkg/api/decide.go:514`;
   - three JSDoc sites in `pkg/ts-bun-client/src/types.ts`, plus a comment in `public-surface.test.ts`;
   - comments in `get.go`, `relay_delivery.go` and `resume_lookup.go`.

   `tmux_socket`'s "a row from before this release" is a different legacy group (rows with no recorded socket). A durable wording would define each group by what it lacks, e.g. "a request with no hook identity". CHANGELOG is exempt (SR-4.5).
2. **New load-flake candidate:** `internal/hook` `TestRelayAckKeepsItsReserve` took 2.6–2.78 s against a 2.5 s bound and passes alone. Known flakes seen again: b.b1o and b.82r.
3. **A racy test was fixed:** the Claude `sameSecond` proof row in `internal/hook/request_proof_test.go`. Its start time could cross into the next second; it now starts at the request's second plus 999 ms, and passed 20 repeat runs.
4. **Epic 3 follow-ups are still open:** see the Bee's progress section.

## Carry to Epic 5 (t1.9r1.dn, draft, gated on the SR-6.8 addendum)

> **Update 2026-10-12: the SR-6.8 addendum is approved.** Codex rows show no subagents, and an instance id is locked to its agent (SRD t1.ssz.pm SR-6.8; PRD amendment `t1.ssz.qi/prd-amendment-2026-10-12-codex-subagents.md`). The items below now read as follows:
> - **Void:**
>   - registering Codex SubagentStart and SubagentStop;
>   - settling a new `subagent_tracking` rule (0 for Codex is final);
>   - updating `assertCodexLaunch`'s tracking (it stays 0);
>   - placing PRD differences row 15 in its old wording. It is replaced by "Codex agents show no subagents".
>   - "A sixth closing write": none is added;
>   - "Epic 5's Scope text is stale": Epic 5 has been rewritten.
> - **Changed:** "Replace the interim 'Codex tracked false' text" now means making that text final, not replacing it with records (SR-6.8.4 and SR-6.8.5). `codexNotRegistered` keeps both events, and its reason string becomes final.
> - **Kept:**
>   - keeping Codex out of b.xjn's capture paths (SR-6.8.2, now with tests);
>   - "A Codex SubagentStop must still prove nothing" (a test in `TestCodexHookProvesRequestsGone`);
>   - "`spec/tla/codex/` does not model b.xjn's records", which is settled: none is written for Codex (SR-6.8.3).
> - **Added to Epic 5:** the cross-agent reuse refusal, `ErrReuseAgentMismatch` (SR-6.8.8). *Withdrawn later on 2026-10-12: a reuse may change the agent, and Epic 5 instead adds subagent records by life (SR-6.8.9).*
- **Register Codex SubagentStart and SubagentStop.**
  - Update `codexHookEvents` and the `codexNotRegistered` list in `internal/hook/hook_payload_fixtures_test.go`.
  - Settle the final `subagent_tracking` rule for Codex rows (today it is 0, via `subagentTrackedParamSQL` and `subagentTrackedColumnSQL` in `internal/store/spawns.go`).
  - Update `assertCodexLaunch`'s expected tracking.
  - Replace the interim "Codex tracked false" text in usage.md, both client READMEs and architecture.md's "What a new agent reuses".
  - Place PRD differences row 15: some subagent fields unknown, and a missed stop stays running.
- **Keep Codex out of b.xjn's capture paths.** b.xjn's Stop-snapshot and Agent-tool capture now run only when the classifier sets `SubagentCapture`. Do not open them to Codex without the addendum: Codex gets no reconcile from a Stop snapshot.
- **A Codex SubagentStop must still prove nothing.** Releasing a Codex subagent's hold on it would change MC-9 and AC-20. The control `rx_C_subproof` would then need a new TLA+ run and the User's decision.
- **`spec/tla/codex/` does not model b.xjn's subagent records.** Model-check any change to what moves a row or releases a hold.
- **A sixth closing write** must update the "five writes" lists in the Go docs and architecture.md.
- **Epic 5's Scope text is stale.** It still says "merge dev/v0.12.0 / hop moves"; its 2026-10-11 update supersedes that, but the Scope paragraph was not edited.

## Carry to Epic 6 (t1.9r1.5g): live checks against a real Codex
- **Launch:**
  - the Q1 deny group: its hash is trusted under `no_pre_trust`, it blocks with exit 2, and the bare matcher `request_user_input_async` works;
  - the whole help list is delivered at limit 4000 with a real model's counting;
  - whether the help list is delivered again after `/compact`. The docs promise nothing until this is checked;
  - re-capture the hook-trust golden data, including the deny entry.
- **Hooks:** when `/review` is the first action, does the main SessionStart come before its hooks?
- **End of a Codex agent:**
  - 127 C-u in one `-N` call on a real Codex, including the 64-line limit, with C-c never needed;
  - `send-keys -N` works on tmux 3.2;
  - a SessionEnd lands before the process exits, and the per-session SessionEnds at `/exit` still let the poll make the move;
  - the procedure for a command left running after Esc, on macOS and on a desktop Linux host where `systemd --user` acts as a subreaper.
- **Relay:**
  - Esc while a relay hook polls, on both the newest and the minimum Codex: the hook is killed, no dialog appears later, and the Interrupt arrives;
  - the dialog keys `y`, `p`, `a` and Esc, plus the MCP and `write_stdin` dialogs;
  - parallel PermissionRequests fired in the same second;
  - a long relay hold past a short real limit (RN-9 was checked from Codex's listing only);
  - the operator-actions startup-file check commands, against a real printing `.zshenv`;
  - Q3: no Interrupt ever carries `agent_id`;
  - Q2: no PermissionRequest comes from `/review` or other internal threads.
- **Callers and setup:**
  - `env_vars` forwarding, including an unset `TMUX_TMPDIR`, on 0.156.0 and the newest;
  - the tool names Codex gives the documented MCP server;
  - the hand-made help entry's trust prompt (at start and in `/hooks`), where that trust is stored, and how to remove it;
  - double delivery of the help list with `inject_help_hook` on;
  - AC-22/23: a real Codex caller's `parent_id` and tmux server, following settings.md word for word;
  - whether MCP calls still ask under `approval_policy=never` or full access;
  - the CLI from a Codex shell as a non-root user;
  - a `CODEX_HOME` under `/tmp` on a real launch;
  - the standalone install route and path on the newest Codex;
  - the macOS arm64 paths.

## Reviewer feedback not acted on
These were noted in the PM reports and judged not worth acting on.
- **Style and generated text:** over-long comment and markdown lines; gofmt hits in two older files (`pkg/api/errnames/scan_test.go`, `pkg/api/manifest/generate.go`).
- **Code nits:**
  - a trailing value-taking option can swallow AD's first setting (a caller error that Codex rejects);
  - paths that are not valid UTF-8 cannot be written into Codex's TOML;
  - `CodexPreTrust` repeats `ConfigDirUsable`'s rule instead of calling it;
  - `CodexSettingsArgs` is exported but takes an unexported input type;
  - the argument list is parsed twice per launch;
  - `AGENT_DIRECTOR_AGENT` could reach a Claude pane, which is harmless.
- **Test helpers:** lane fixture gaps (a codexfix state-key helper, an exported Codex event list, an apitest helper to age a launch).
- **Doc wording:**
  - the usage.md `tracked` sentence is slightly ambiguous for a reuse as claude;
  - kill's description still says "find-missing marks it", which stays true;
  - a stale history comment in `help_size_test.go`.
- **Hook argument edge case:** `hook --timeout --agent codex` runs as Claude, because the flag parser hands `--agent` to `--timeout` as its value. Spawn always writes `--timeout N`, so a real launch never produces that line.

## Notes
- **No `.claude/**` edits this Epic**, so nothing was added to `claude-dir-edits.md`.
- **b.wur's schedule passages** in install.sh, uninstall.sh, the install skill, README and install.md were not edited.
- **New Bee-folder artifacts** (not committed): `epic4-demo.md`, `epic4-docker-run.md`, `e4-t5-row-map.md`.
- **Updated in the tla folder:** README.md, suite.tsv, job-ids.txt and verdicts.txt record the gate and the optional runs.
