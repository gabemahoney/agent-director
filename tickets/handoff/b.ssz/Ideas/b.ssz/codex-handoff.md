# Handoff: the rest of b.ssz (Codex support in agent-director), for a Codex worker

You are a Codex agent finishing a feature in the **agent-director** (AD) repository. You have no Apiary skills; this file tells you everything you need. Read it fully before you change anything. The orchestrator (a Claude agent on another machine) runs you through AD and reads your pane. When this file says "stop and ask the orchestrator", write the question in your pane and wait.

## 1. What AD is and what this feature does
- AD is a Go binary (with a TypeScript client) that spawns and supervises coding agents in tmux: state tracking from the agent's hooks, a permission relay, strict kill, crash recovery, a SQLite store. Until now it ran only Claude Code.
- **b.ssz** makes AD run OpenAI Codex agents with the same contracts as Claude agents, in one breaking release: every spawn names its agent (`claude` or `codex`), every Claude-specific name in AD's surface becomes generic, AD's directory moves to `~/.config/agent-director`, and the installer migrates existing installs.
- Branch: `feature/b.ssz`. Base commit for this handoff: `35707897` ("B"). Work only on that branch (or a local branch from it); **never push**.

## 2. Where things stand
| Epic | Ticket | State | Commits |
|---|---|---|---|
| 1. Real-Codex probe | t1.9r1.sj | done | probe results in `tickets/Ideas/b.ssz/probe/` (not in the repo) |
| 2. Generic names, required agent, launch paths | t1.9r1.js | done | 10b5e3e, 225ca4f, a3e70d1, 55f2df9 |
| 3. Upgrade, directory move, Codex-only installer | t1.9r1.j8 | done | b198cae5, 9e93efad, fe33c2f2, 86e118e1, f679b5a8 |
| 4. Codex agents with Claude's contracts, relay | t1.9r1.u5 | done | bc30c7c3, d79bc772, bc9bcf44, 4156e971, 35707897 (and b.wur merged at 27d41cd7) |
| **5. Codex subagents untracked; life and resume rule** | t1.9r1.dn | **to do (you)** | |
| **6. Pre-release real-Codex check** | t1.9r1.5g | **to do after 5** | |

b.xjn (Claude subagent records, `list-subagents`) is merged into the branch.

## 3. The spec you build from
These files are committed on `feature/b.ssz` under `tickets/handoff/b.ssz/` (force-added, since `.gitignore` excludes `tickets/*`; a copy of the orchestrator's ticket store, taken 2026-10-12; paths below are relative to `tickets/handoff/b.ssz/`). The same set is also in `codex-handoff-files.tar.gz` in the orchestrator's ticket store:
- **PRD** `Ideas/b.ssz/t1.ssz.qi/t1.ssz.qi.md`, amended by `Ideas/b.ssz/t1.ssz.qi/prd-amendment-2026-10-12-codex-subagents.md` (the amendment wins over the PRD text it names).
- **SRD** `Ideas/b.ssz/t1.ssz.pm/t1.ssz.pm.md`. Epic 5's spec is its **SR-6.8 addendum** (from "## Addendum: SR-6.8" to the end): SR-6.8.2 to SR-6.8.7 and **SR-6.8.9**. SR-6.8.8 is withdrawn; build nothing from it. Requirements are cited "SR-n"; b.xjn's SRD (`Ideas/b.xjn/t1.xjn.du/t1.xjn.du.md`) is cited "X-SR-n".
- **The User's rules** `Ideas/b.ssz/e5-brief-life-rule.md` (read it: SR-6.8.9 implements it).
- **The Epics** `Ideas/Plans/b.9r1/t1.9r1.dn/t1.9r1.dn.md` (Epic 5) and `Ideas/Plans/b.9r1/t1.9r1.5g/t1.9r1.5g.md` (Epic 6), and the plan `Ideas/Plans/b.9r1/b.9r1.md` (its "Global constraints" apply to you).
- **The TLA+ check** of the rule: `Ideas/b.ssz/tla/life/` (model `src/LifeRule.tla`, `README.md`; final job green). You do not run TLC.
- Also: `Ideas/b.ssz/epic4-report.md` (carry items), `Ideas/b.ssz/probe/answers.md` (research answers), `Ideas/b.ssz/release-notes-breaking.md` (the breaking-change text you finish).

### Gabe's rule, in one paragraph (the User; it wins)
"Resume always continues the last agent's latest session. Switching agents without resume is fine. The one thing you can't do is resume across a switch." So: a reuse (`spawn --reuse-finished`) may name either agent; only an agent switch starts a new life (a same-agent reuse keeps it); session history and subagent records show only the current life; AD-agent children are not tied to a life; a switch gives up everything before it, a switch back included; `resume` relaunches only the latest session (B's fallback to older sessions is removed); Claude, then Codex, then Claude launched with `--resume` is **not an error**: Claude starts fresh, the resume arguments are dropped and reported in `dropped_args`, and the old Claude session is never resumed. Codex rows show no subagents (`tracked` false, `[]`).

### The requirements review was stopped unfinished
The last review round of the changed PRD and SRD text (round 6, a confirmation pass) was stopped by the User before it reported. Round 5's findings were all applied but are **unconfirmed**; treat these spots with care and stop and ask the orchestrator if one looks wrong:
- SR-6.8.9: `ResumeArgs` and Claude's option table read at the documented minimum (Claude 2.1.280, Codex 0.156.0) and the newest version; the post-release gap stated; SR-16.2 and Epic 6 re-check them.
- SR-6.8.0 / SR-6.8.9: SR-18.10's and `DescReuseRecourse`'s required phrases stay, with the rule added beside them.
- The pre-trust wording targets (resume's Description and `pre_trust` field; `docs/usage.md`).
- The reset's agent guard in both the in-transaction read and the UPDATE, with a store test.
- `ad.spawn.reused`'s `dropped_args` is `null` when nothing was dropped.
- `-p` and `dropped_args` in the user refusal lists and `pkg/api/README.md`.
- An exact long-name match that is another option's value is refused, never dropped.
- Epic 5's breaking-change bullet; the PRD's Goals/Non-Goals exception.
Earlier rounds' findings were confirmed by the round after them.

### Still open with the User (do not build around them; the orchestrator will tell you)
- Whether the agent's own in-session `/resume` is outside the guarantee (SR-6.8.9 "Scope of the guarantee").
- The design choices SR-6.8.9 adds to his rules (fallback removal, drop-not-refuse on a switch, the newly refused options).
- An optional change to classify `ErrJsonlNeverWritten` over the current life's transcripts (SR-6.8.9 "Callers").
If the orchestrator says one changed, the SRD will be updated first; build from the SRD as it stands when you start a Task.

## 4. How to work (what Apiary's do-bee would do)
For each Task below, in order:
1. Read the SRD sections it names and the code they cite (file and function names are given; line numbers are from B and may have moved).
2. Implement the code change. Match the surrounding code's style, comments and naming. Keep changes to what the Task needs.
3. Write or change the tests the SRD names for it (SR-6.8.4 and SR-6.8.9 "Tests"). The repo's test rules are in `docs/test-writing-guide.md`; read its "Pre-Submit Checklist".
4. Update the docs the SRD names (SR-6.8.5 and SR-6.8.9 "Docs"). Doc rules: `docs/user-docs-guide.md`, `docs/architecture-docs-guide.md`. User docs state current behaviour only: no bee, SR, RN, Epic or date ids, no history.
5. Run `make generate` (in the sandbox, below) and commit what it changes; run `make test-sandbox` until green.
6. Commit with a subject `type(b.ssz): description` (for example `feat(b.ssz): …`; `!`/BREAKING where a name or contract breaks), body explaining what and why, ending with the co-author line the repo uses.
7. Record the Task as done: with the `bees` CLI if it is installed (`bees update-ticket --ids <id> --status done`), otherwise add a line to `tickets/handoff/b.ssz/codex-progress.md` (force-add it: `git add -f`, since `.gitignore` excludes `tickets/*` except the testplans) (create it) with the Task, the commit and the test result, for the orchestrator.
8. Tell the orchestrator in your pane: the Task, the commit, the test result, anything you could not do.

### Epic 5 (t1.9r1.dn) Tasks, in order
1. **Codex rule made final (SR-6.8.2, SR-6.8.4, SR-6.8.5).** Reword every passage that calls the Codex untracked rule temporary (SR-6.8.4's list and grep); the extended `TestCodexSubagentEventsWriteNoSubagentRows` (classifier and flag guards), `TestCodexHookProvesRequestsGone` rows, Codex rows in the CLI, MCP and TypeScript `list-subagents` tests (ts-helper `seed-spawn --no-subagent-tracking`); usage.md and hooks.md texts.
2. **Subagent records by life (SR-6.8.9 "Subagent records follow a life").** `subagents.life_number INTEGER NOT NULL` and the key `(instance_id, life_number, subagent_id)` in the v9 hop and `schemaDDL` (`internal/store/schema.go`); `insertEntry` writes it, `readEntry` finds only the current life's entry, `ListSubagentRecords` filters on it (`internal/store/subagent_writes.go`, `subagent_read.go`); `apitest` `WithSubagentLife(n)` (default: the row's life) and `SubagentEntryColumns.LifeNumber`; the tests listed (schema fixtures, per-event cases, `TestReuseResetRowComesBack` split, cap order).
3. **The life rule (SR-6.8.9 "The life rule").** `ResetForReuse` (`internal/store/reuse.go`) advances `life_number` only when the stored agent is not exactly the reuse's agent; the guard on that agent in both its in-transaction read and its UPDATE; `store.ReuseRow` gains the stored agent (as text). Then move every test that assumes each reuse starts a new life (the grep in SR-6.8.9 "Tests"; about fifty files), the `no_pre_trust` wording, b.fmk's superseded criteria and their pins (`pkg/api/apitest/descriptions_reuse_docs.go`, `recovery_wording_test.go`, `manifest/manifest_texts_test.go`).
4. **Resume continues the latest session only (SR-6.8.9).** Remove the history-walk fallback in `pkg/api/resume.go`; keep `ErrJsonlNeverWritten`'s condition; `ErrJsonlMissing` lists only the current session's tries; resume strips resume arguments and Claude's `-p` from the stored arguments it replays (`internal/spawn/relaunch.go`); the tests (`resume_transcript_test.go`, `internal/store/history_migrated_verbs_test.go`'s `TestMigratedV4HistoryResume`, advice tests).
5. **Resume arguments and the key case (SR-6.8.9 "Resume arguments, and the key case"; bug b.d6q folded in).**
   - **First, before any code:** read Claude's and Codex's own argument parsers (not `--help`) at the documented minimum (Claude 2.1.280, Codex 0.156.0) and the newest version, in a throwaway container, to fix `ResumeArgs` and Claude's option table (which options take values). If no Claude Code is available to you, **stop and ask the orchestrator**. Never use or copy any login for this.
   - Then: the `ResumeArgs` sets and the Claude argument reader in `internal/spawn`; `-p` refused everywhere on the full list; the reuse's one `ReadForReuse` moved before `Validate` so step 7 drops (switch) or refuses (everything else) in place, with SR-1.3's order unchanged; `dropped_args` on spawn's result on every surface (manifest, Go `SpawnResult.DroppedArgs`, TypeScript type, MCP), the `ad.spawn.reused` trail field (`null` when nothing dropped) and one CLI stderr line; `help` must not grow (`helpStdoutBytes` 14700: if it would, stop and ask the orchestrator).
   - The named test `TestSpawnReuseKeyCaseClaudeCodexClaudeResumeStartsFresh` and every refusal, order, cluster and replay test listed.
6. **Docs, breaking-change text and generated files.** Everything in SR-6.8.9 "Docs" and SR-6.8.7 (the breaking-change text in `tickets/Ideas/b.ssz/release-notes-breaking.md`, which is not in the repo: edit the copy under `tickets/handoff/b.ssz/` and say so in your report); `spec/tla/subagent/README.md`'s refinement bullet and the Phase4 note; the done-check greps of SR-6.8.4 and SR-6.8.9, every hit reviewed (changed, or kept as true); `make generate`.
7. **Docker testplan cases** (in the repo under `tickets/testplans/b.75s/`): `agent-22`, `agent-23`, `agent-24` in `epic-13-agents` (t1.75s.ag), and `resume-9`, `resume-10`, `spawn-16` moved to the rule. Write them as the SRD says. **You cannot run Docker groups**: they run only through the orchestrator's job scheduler. When they are written and `make test-sandbox` is green, stop and ask the orchestrator to run `make test-docker EPIC=<slug>` for the affected groups and report back.

When all seven are done and the Docker groups are green, mark Epic 5 done (bees or `codex-progress.md`) and tell the orchestrator. Do not start Epic 6 until the orchestrator says so.

### Epic 6 (t1.9r1.5g), only when the orchestrator says so
Its body lists the scope: the real-Codex pre-release check on the newest and the minimum Codex, every RN rechecked, the resume-argument lists re-read, the way-back run, the memory-consolidation check, results in `tickets/Ideas/b.ssz/prerelease/`. It needs a signed-in Codex in a throwaway container: **stop and ask the orchestrator** where and with which login before you start; never copy, print or store any login, token or `auth.json`.

## 5. How to run things
- **Never run this repo's tests or built binaries directly on the host** (`go test`, `bun test`, `go run`, `bin/…`): they can rewrite a real AD store. Use the sandbox:
  - `make test-sandbox`: the full suite (`go test ./...` and `bun test`).
  - `make sandbox CMD="…"`: any one command (a build, `go generate`, one test: `make sandbox CMD="go test ./pkg/api -run TestX -count=1"`).
- **Docker testplan groups** (`make test-docker EPIC=<slug>`; slugs from `make list-test-docker-epics`) run only through the orchestrator's job scheduler, which you cannot reach: stop and ask.
- **TLA+:** no TLC runs on any host. If a change you make would change what moves a row or what a model checks, stop and ask the orchestrator (it runs TLC through the scheduler).
- Read `CLAUDE.md` and `docs/engineering-guide.md` at the repo root: they apply to you too (commit subjects, sandboxed execution).

## 6. Don'ts
- No `git push`, no release, no GitHub changes. Commits stay local; the orchestrator fetches them (a `git bundle` or a fetch over ssh).
- No edits under `.claude/**`. If you need one, write what and why in your report.
- No edits to `docs/*-guide.md` or the repo's `CLAUDE.md` (feature work may not).
- No heavy TLC locally; no TLC at all.
- Never print, copy or store secrets: logins, tokens, `auth.json`, environment variables holding keys.
- Never touch a real AD store (`~/.config/agent-director`, `~/.agent-director`) or the job scheduler's files, directories or services.
- Ticket updates only through the `bees` CLI when it is installed; otherwise notes in `tickets/handoff/b.ssz/codex-progress.md` (force-add it: `git add -f`, since `.gitignore` excludes `tickets/*` except the testplans) for the orchestrator.
- When unsure whether something is in scope, or when the SRD and the code disagree, stop and ask the orchestrator rather than guessing.
