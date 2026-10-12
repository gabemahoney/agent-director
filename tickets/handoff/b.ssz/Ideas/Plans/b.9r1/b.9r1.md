---
id: b.9r1
type: bee
title: 'Codex support in agent-director: agent-generic breaking release with Codex
  agents at Claude parity'
tags:
- codex
- agent-cli
- breaking-change
up_dependencies:
- b.ssz
parent: null
children:
- t1.9r1.sj
- t1.9r1.js
- t1.9r1.j8
- t1.9r1.u5
- t1.9r1.dn
- t1.9r1.5g
reference_materials:
- value: b.ssz
  resolver: bees
- value: t1.ssz.qi
  resolver: bees
- value: t1.ssz.pm
  resolver: bees
- value: b.nx4
  resolver: bees
created_at: '2026-10-11T07:43:17.065680'
status: active
schema_version: '0.1'
guid: 9r1yp66k33j97pcyyib523ojni93fdfp
---

# Plan: Codex support in agent-director (b.ssz)

AD gains a required `agent` input (`claude` or `codex`) and runs OpenAI Codex agents in tmux with the contracts it gives Claude Code today (state tracking, permission relay, strict kill, crash recovery), refusing or documenting every difference. It ships as one breaking release: AD's Claude-named names become generic with no aliases, AD's directory moves to `~/.config/agent-director` (bug b.nx4, folded into Epic 3), and the installer migrates existing installs with a way back. Sources: Idea Bee b.ssz, PRD t1.ssz.qi and SRD t1.ssz.pm (both approved; SR-11.7 carries b.nx4).

## Preconditions (not Epics of this plan)
- **b.146 is merged first.** The SRD's baseline is dev/v0.12.0 (`5a05f44`) with feature/b.146-relay (`5be84a8`) merged into it. b.146 is not yet merged into dev; the b.ssz build branch is cut from dev/v0.12.0 after that merge. Merging b.146 is not planned here.
- **b.3ie is conditional.** feature/b.3ie (`d2d89d3`) is baseline only if it is merged into dev/v0.12.0 when the build starts. Then the store hop is v9 instead of v8, the downgrade tests start from v8, and the Codex move to `ended` writes b.3ie's settle columns (Epics 2 and 4 say where).
- **b.zd9 (approved) removes the user-doc line caps** of `TestTrimmedUserDocs`; it reaches the branch before the first doc change. No Epic budgets, trims or raises doc lines; docs go where the doc guides put them.
- b.fmk, b.v2c and b.kdf's `unreported` note are built (PRD assumptions).

- **Launch path per agent (approved exception to parity, the User, 2026-10-11; end of b.ssz's body).** `[launch] claude_path` and `[launch] codex_path`, both optional; unset means `claude` or `codex` from PATH, as today. Nothing is refused for an unset path and there is no new error; a program that breaks today's launcher rule (e.g. the npm `codex` launcher) leaves the row `pending`, noted `unreported`, and the docs tell npm-install users to set the path. No other program check. Folded in: the setting and refusal in Epic 2, the installer's presence logic in Epic 3, the Codex launch using it in Epic 4, the native-program location (RN-1) in Epic 1.

## What blocks what
- **The Codex login (settled by the User, 2026-10-11; blocks nothing).** Epic 1 (the probe) and Epic 6 (the pre-release check) run a real Codex in a local throwaway container on this box, never on the host and never through the job scheduler (it forbids secrets), using the box's existing ChatGPT login in `~/.codex` (Codex 0.159.2), copied into the running container at run time only and never written into a ticket, log, report, image or the repo. The User does not use that login, so a sign-out is acceptable; no test login or API key is needed. Epic 4 depends on Epic 1 because its code depends on the probe's answers (the launch recipe and argument placement, the hook-trust hashes, payload shapes, report-in and rotation, relay details, help delivery), which the SRD forbids coding before they are recorded; a "stop and ask" answer holds only the Tasks that depend on it. (Epic 5 no longer depends on Epic 1: Codex registers no subagent hooks, SR-6.8 addendum.)
- **b.xjn** (Claude subagent visibility) is merged into feature/b.ssz, and the SR-6.8 addendum is approved (2026-10-12). Neither blocks anything now.
  - Codex rows show no subagents (`tracked` false). A reuse may change the agent; only a switch starts a new life (the lock of an id to its agent was withdrawn the same day; see the updates below).
  - b.xjn's names needed no rename.
  - Epic 5 builds the addendum: tests, final wording and docs for Codex-untracked rows, the life rule, resume of the latest session only and subagent records by life.
  - Epic 6 needs Epic 5 because the release needs AC-21 as amended (SR-16.3).
- **Neither.** Epics 1, 2 and 3 wait on nothing outside the plan; they start as soon as the branch exists (Epic 1 needs no branch at all). Epic 4 waits only on Epics 1 and 2.

## Epics
| # | Epic | Outcome | Depends on | Waits for |
|---|---|---|---|---|
| 1 | t1.9r1.sj | The real-Codex probe answers every probe-marked RN and captures the golden data and payloads | none | nothing (box login, local container) |
| 2 | t1.9r1.js | Agent-generic names on every surface, the store hop, a required agent input with its five refusals, and the optional per-agent launch paths | none | nothing |
| 3 | t1.9r1.j8 | The upgrade: the move to `~/.config/agent-director` (b.nx4), config and template migration, a Codex-only host, the way back, the breaking-change text | 2 | nothing |
| 4 | t1.9r1.u5 | Codex agents run under AD with Claude's contracts, the permission relay included | 1, 2 | nothing outside the plan |
| 5 | t1.9r1.dn | Codex rows report no subagents; a reuse may change the agent, and only a switch starts a new life (SR-6.8 addendum) | 2, 3, 4 | nothing (addendum approved 2026-10-12) |
| 6 | t1.9r1.5g | The pre-release real-Codex check passes on the newest and the minimum Codex | 2, 3, 4, 5 | nothing outside the plan |

The bees `up_dependencies` hold the same edges. Lanes: Epics 1 and 2 together; then Epics 3 and 4 together (4 once the probe's answers are in); then Epic 5; then Epic 6.

## Planning decisions
- **b.xjn's names (SR-6.8).** The SRD says the hop and rename tasks wait until b.xjn is merged. b.xjn is not built and every other Epic needs the hop and the renames, so Epic 2 builds them on today's code, and Epic 5, when b.xjn merges, extends the same unreleased hop (moving it past any hop b.xjn adds, SR-3.2) and the rename to b.xjn's names in the same change as the merge. The `claude_` column test and the old-names test catch any name missed. If b.xjn is already merged when Epic 2 starts, Epic 2 renames its names itself and Epic 5 keeps only the subagent view. (Superseded 2026-10-12: b.xjn's names were generic, and the SR-6.8 addendum settles Epic 5's scope.)
- **Codex between Epics 2 and 4.** Epic 2 accepts `codex` and builds every agent refusal, but not the launch. Until Epic 4, a Codex spawn (or a reuse or resume of a Codex row) that passes every check stops at launch composition, as a composition failure does today: nothing written, nothing launched.
- **The TLA+ vendoring (SR-12)** goes in Epic 4, the Epic that builds the model-checked rules (MC-1 to MC-10).
- **The breaking-change text** is written by Epic 3, finished by Epic 5 (the b.xjn placeholder removed with no rows, b.xjn's tables and `subagents.life_number` in the schema bullet, the reuse and life rule under "New"; SR-6.8.7), and checked complete by Epic 6.

## Global constraints (every Epic)
- Nothing from this repo runs on the host: tests and binaries run only in the sandbox (`make test-sandbox`, `make sandbox CMD=...`) or the Docker harness, demos with throwaway `HOME` and `TMUX_TMPDIR` (b.8dr; this branch bumps the schema). A real Codex runs only in a local throwaway container (Epics 1 and 6), never with the User's real Codex, Claude or AD folders; the only host file it reads is `~/.codex/auth.json`, copied in at run time (a sign-out of that unused login is accepted by the User). Tests never touch the real `~/.codex`.
- Every Epic leaves `make test-sandbox` green and every Docker testplan it touches passing; `make generate` leaves no diff.
- Docs and tests fold into the Epic whose behaviour they cover; there is no doc or test Epic. Docs go in the user doc `docs/user-docs-guide.md` assigns, the README per `docs/readme-guide.md`, `docs/architecture.md` per `docs/architecture-docs-guide.md`, beside the Claude material they mirror. No new doc file; no spec, RN, bee or history ids in user docs; no `docs/*-guide.md` file is edited. Reusable components get a statement in `docs/architecture.md` and doc comments.
- Each functional Epic ships Docker testplan cases (test-writing guide): Epic 2 starts one new testplan for agent and Codex cases, which Epics 4 and 5 extend.
- Error names are catalogued by the Epic that first returns them: sentinel, `pkg/api` re-export, Catalog and `catalog.json`, manifest `ErrorNames`, TypeScript class and goldens, envelope-diff row. Every advice text that tells a caller what to do gets a literal-follow test.
- `agent-director help` stays within `helpStdoutBytes`; if trimming help cannot make room, stop and ask the User before any re-record.
- Commit subjects `type(b.ssz): ...`, marked BREAKING where a name, path or contract breaks.
- Nothing under `.claude/**` is edited. The skills that drive AD (the Apiary skills, the user's installed copy of install-agent-director, repair-orphaned-transcript) and CSCB are migrated after release, outside this plan. `skills/install-agent-director/SKILL.md` in this repo is shipped product and is fully updated in this release by Epic 3. The guides and `CLAUDE.md` are not touched (follow-up pending the User).
- Bee-folder artifacts (the probe's and the pre-release check's scripts and results, the breaking-change text, the SR-6.8 addendum) stay under `tickets/Ideas/b.ssz/` and are never committed.
- An RN answer that differs from the SRD takes the RN's fallback; a "stop and ask" holds only the dependent work until the User answers.

## Out of scope
Merging b.146; building b.xjn; migrating CSCB, its bots and the skills; the post-release guide follow-up (a reverse recipe, the guide rows for Codex); AD driving Codex's app-server; agent CLIs other than Claude Code and Codex (b.ftd); a `list` filter by agent; cutting and publishing the release.


## do-bee progress

**Epics done**:
- t1.9r1.js Agent-generic names, required agent input, per-agent launch paths: commits 10b5e3e, 225ca4f, a3e70d1, 55f2df9; status `done` in the ticket store; Integration Suites: passed after fixing (epic-13-agents testplan bugs, 55f2df9), all 15 groups green with --network off.
- t1.9r1.j8 The upgrade (move to ~/.config/agent-director, config/template migration, Codex-only installer): commits b198cae5, 9e93efad, fe33c2f2 (bug b.fom, folded in), 86e118e1, f679b5a8; status `done` in the ticket store; Integration Suites: passed (tag e3int, all 15 groups green first run, --network off).
- t1.9r1.u5 Codex agents run under AD with Claude's contracts, the relay included: commits bc30c7c3 (E4-T1), d79bc772 (E4-T2), bc9bcf44 (E4-T3), 4156e971 (E4-T4), 35707897 (E4-T5); fix/b.wur merged in at 27d41cd7 (orchestrator request); E4-T6 (t2.9r1.u5.3x) dropped; status `active` (marked done at Bee close); Integration Suites: passed (tag e4int, all 15 groups first run, --network off; per-Task runs e4t1..e4t4 also all green).
**Next**: t1.9r1.dn (Epic 5): the SR-6.8 addendum is approved (2026-10-12). Epic 5 is rewritten from it and must be hatched before any do-bee run. t1.9r1.5g (Epic 6) is `draft` and depends on Epic 5.
**Open decisions/questions**:
- `.claude/**` edits waiting in tickets/Ideas/b.ssz/claude-dir-edits.md (run-tests and repair-orphaned-transcript SKILL.md); who updates repair-orphaned-transcript is still open from Epic 2.
- Follow-ups to file (Epic 3): install.sh `ad_move_print_references` substring grep can print a spurious `references:` line; install.sh `MOVE_NOTES` is dead code; `[launch]` refusal headline cosmetic; internal/hook TestRelayFirstWriteCutAtItsDeadline flakes under load.
- Follow-ups to file (Epic 4): "recorded before this release" / "from before this release" wording still in source, manifest and generated texts (SR-15.1; see epic4-report.md); new load-flake candidate internal/hook TestRelayAckKeepsItsReserve.
- Help is 14682 bytes vs helpStdoutBytes 14700 (18 free; not re-recorded downward — orchestrator's call).
- Epic 4's body records the SR-18.7 amendment (option A) twice; tidy-up.
**Notes for the next session**:
- Full Epic 4 report: tickets/Ideas/b.ssz/epic4-report.md (Epic 5 and Epic 6 carry lists, decisions, flakes). Demo transcript: tickets/Ideas/b.ssz/epic4-demo.md; Docker record: tickets/Ideas/b.ssz/epic4-docker-run.md; differences row map: tickets/Ideas/b.ssz/e4-t5-row-map.md.
- Docker groups only via the job scheduler (channel C0B3X876XSB, --network off); scripts copied in session 4850eff5's scratchpad (/tmp/claude-1000/-home-horde-projects-agent-director-project-agent-director-bssz/4850eff5-8721-4964-9e53-8e4d1d80ab5d/scratchpad/{docker-jobs.sh,jobs-wait.sh,jobs-status.sh,integration-howto.md,rules.md}).
- Never write wait loops with `pgrep -f`; never pass backticks inside a double-quoted bees --chunk (use --chunk-file).

## Update 2026-10-11 (orchestrator): base branch and b.xjn

The "b.146 merged first" and "b.3ie conditional" assumptions above are replaced:
- The build branch is `feature/b.ssz`. It was cut from feature/b.3ie (which contains all of feature/b.146-relay), with dev/v0.12.0 merged in (f4c979d). b.ssz's store hop is v8→v9.
- b.xjn (plan b.f5h, PRD and SRD approved) is built on a branch cut from feature/b.ssz and merged back **into feature/b.ssz**, not into dev/v0.12.0. Its columns fold into b.ssz's unreleased v9 hop, with no separate hop. Epic 5 (t1.9r1.dn) starts when b.xjn has merged into feature/b.ssz. b.xjn's names are generic from the start, so Epic 5's rename half shrinks to a check.
- The Release still waits for b.146 and b.3ie to land on dev, which waits on CSCB's b.u7d.

## Update 2026-10-12: the SR-6.8 addendum is approved
The User approved two decisions, and the PRD amendment (`tickets/Ideas/b.ssz/t1.ssz.qi/prd-amendment-2026-10-12-codex-subagents.md`) and the SRD's SR-6.8 addendum, which record them:
- **Codex rows show no subagents.** No Codex SubagentStart or SubagentStop hook is registered, `subagent_tracking` is 0 on every Codex launch, and `list-subagents` returns `tracked` false and `[]` on Codex rows. AC-21's subagent half is replaced.
- **An instance id is locked to its agent.** A `reuse_finished` spawn naming another agent is refused with `ErrReuseAgentMismatch`, nothing written. AC-3 reads "reuse keeps the row's agent".

Effects on this plan:
- Epic 5 no longer depends on Epic 1 and carries no b.xjn gate.
- Epic 6's subagent-view check becomes AC-21 as amended.
- No Epic re-captures golden data for subagent hooks.

## Update 2026-10-12, later the same day: the lock is withdrawn
The User changed the second decision: "This is a desired use case, the PRD should say its supported. But all calls show current life." A `reuse_finished` spawn may name either agent, and the row takes it and starts a new life, as B already does. No call shows or uses anything of an earlier life. `ErrReuseAgentMismatch` and SR-6.8.8 are withdrawn, and SR-6.8.9 replaces them (PRD amendment Decision 3).

Effects on this plan:
- Epic 5 drops the refusal and adds subagent records by life: a `subagents.life_number` column and key in the unreleased v9 hop, and a `list-subagents` read of the current life only. (Refined by the final update below: only a switch starts a life, and a TLA+ run was made.)
- Epic 6's breaking-change check names no `ErrReuseAgentMismatch`.


## Update 2026-10-12, final: Gabe's life rule
Only an agent switch starts a new life; a same-agent reuse, `resume` and session changes keep it. Session history and subagent records show only the current life; AD-agent children are not tied to a life (`list --parent` unchanged). A small TLA+ model passed first (`tickets/Ideas/b.ssz/tla/life/`, job j20261012201507-371f56).

Effects on this plan: Epic 5 changes `ResetForReuse`'s life rule (B bumps the life at every reuse), removes `resume`'s fallback to older sessions (Gabe's resume rule: resume continues the last agent's latest session, never across a switch), keeps subagent records by life, and moves the Docker cases that assert a new life per reuse (`resume-9`, `resume-10`, `spawn-16`). The `parent_life_number` design is dropped. Epic 5 is not hatched until the orchestrator confirms.
