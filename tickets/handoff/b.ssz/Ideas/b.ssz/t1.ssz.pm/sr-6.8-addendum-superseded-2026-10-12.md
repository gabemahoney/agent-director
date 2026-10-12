# Superseded draft of SRD t1.ssz.pm's SR-6.8 addendum (2026-10-12)

Not part of the SRD. Kept for the record: the User decided on 2026-10-12 that Codex rows show no subagents, and the addendum in t1.ssz.pm.md replaces this draft. Its review history (four /req-review rounds) found the Codex facts that led to the decision.

## Addendum: SR-6.8, Codex subagents in the subagent view

**Status: draft, awaiting approval.** This is the addendum SR-6.8 requires. Once it is approved as this SRD was, it replaces the gated parts SR-6.8.0 lists, and Epic 5's subagent-view Tasks are planned from it. Until then no plan task builds SR-6.8.2 to SR-6.8.6.

Approving it also approves two Codex differences that go beyond the PRD's difference row (SR-6.8.5). Both show a wrong state for a subagent that is working:
- after it stops once, a subagent given another turn reads `stopped`;
- after a resume (AD's `resume`, or Codex's own `/resume` inside the agent), a subagent of the resumed session that the agent sends work to keeps its earlier state while it works. That state is `stopped` or `lost`, never `running`. A subagent this row never recorded never shows.

If the User declines them, SR-6.8.5 lists the alternatives.

### SR-6.8.0: Inputs, what this replaces, and the parity rule
- **Inputs.**
  - b.xjn's approved SRD, t1.xjn.du, with its Amendment of 2026-10-11, which folds b.xjn's tables and column into this work's v9 hop. Its requirements are cited "X-SR-n" and its narrowings "X-N".
  - Its "Agent scope" makes these parts agent-neutral, so they bind Codex rows too: the gate, the record fields, the closes from liveness, the tracking flag, the tables and the verb. It leaves each other agent's events, fields, sources and confirmation rule to that agent's SRD, which here is this addendum.
  - b.xjn's code, merged into feature/b.ssz at `f705c9c6`. This addendum cites feature/b.ssz at `35707897`, cited "B".
  - The probe's answers and captures on Codex 0.156.0, 0.159.2 and 0.162.1: `tickets/Ideas/b.ssz/probe/answers.md` (RN-5, RN-8, RN-17), `probe/logs/rn8/<version>/` and `probe/payloads/`.
  - Codex's source at 0.162.1 (commit `092d3ac`), read where the probe could not reach.
  - `tickets/Ideas/b.ssz/epic4-report.md`, section "Carry to Epic 5".
  - Epic t1.9r1.dn and its updates.
- **What this replaces.** It does not edit the approved text above. Where it is approved, these parts read as below:
  - SR-6.6's gated bullet: SR-6.8.2 to SR-6.8.6.
  - SR-5.3's sentence "Whether and how Codex's SubagentStart and SubagentStop are registered is gated on b.xjn (SR-6.8)": SR-6.8.2.
  - SR-6.8's second bullet: this addendum. SR-6.8's third bullet: b.xjn's names are recorded in SR-6.8.12, and none needed a rename.
  - SR-12.5's sentence "Codex's other subagent lifecycle hooks are likewise assumed marked and ignored, as for Claude": SR-6.8.8.
  - SR-14.4's last bullet: SR-6.8.9.
  - The RESEARCH NEEDED table gains RN-25: SR-6.8.11.
  - In Appendix A, the row for b.xjn's names: SR-6.8.12.
  - In Appendix C, AC-21's cell: SR-6.8.11.
  - SR-6.3's bullets that record a Codex row's session id and transcript path, and its heal bullet, gain one exception: a Codex SubagentStart or SubagentStop records neither and never heals the transcript path, with or without `agent_id` (SR-6.8.3).
  - SR-15.3's "The differences" gains the Codex subagent differences of SR-6.8.5.
  - SR-3.1's "No other column, table or index changes" and SR-3.3's list of what the hop does read with b.xjn's tables, index and column folded into the same v9 hop (t1.xjn.du's Amendment). This addendum changes neither.
- **The parity rule here.**
  - Codex subagents appear in b.xjn's view through b.xjn's agent-neutral rules. Those rules are fed only by what Codex sends in its SubagentStart and SubagentStop hooks, and the records are closed by AD's process liveness.
  - Nothing is added that AD lacks for Claude: no verb, parameter, result field, value, error name, table, column, schema change, trail event or config key. No Codex tool payload is parsed.
  - A field Codex does not send reads null, which means unknown. It is never guessed.
  - Codex rows get no reconcile from a Stop snapshot.
  - A subagent whose SubagentStop is missed stays `running` until its next stop event, or until its agent's row ends, goes missing or begins a new launch.
  - A Codex SubagentStop proves nothing and closes nothing for the relay. MC-9, AC-20 and SR-8.2 are unchanged.

### SR-6.8.1: Facts about Codex's subagent hooks
Each fact held on all three probed versions unless it names the versions it was seen on. Where it comes from Codex's source instead, it is marked "source", and RN-25 checks it on a real Codex before release.
- **C1: the two events and what they carry.** A subagent that the model spawns with Codex's multi-agent tools sends SubagentStart when its thread starts, fresh or forked. It sends SubagentStop at the end of each of its turns.
  - Both carry `agent_id` (the subagent's thread id), `agent_type` (its role, `default` when it has none), the main session's `session_id`, `model`, `turn_id`, `transcript_path`, `cwd` and `permission_mode`.
  - SubagentStart's `transcript_path` is the subagent's own rollout. SubagentStop's is the rollout of the thread that spawned the subagent: the main session's, except for a nested subagent (`agents.max_depth` above 1).
  - SubagentStop adds `agent_transcript_path`, `last_assistant_message` and `stop_hook_active`.
  - Sources: the probe's captures, and the source (`core/src/hook_runtime.rs`, `run_pending_session_start_hooks` and `run_turn_stop_hooks`).
- **C2: what no Codex hook carries.**
  - Neither event carries a description, a parent, a tool-call id or a run mode.
  - No Codex hook carries `background_tasks`, neither Stop nor SubagentStop.
  - The spawn tool's hooks depend on which multi-agent tools Codex runs. Codex's `multi_agent_v2` feature chooses them, and it is off by default on all three versions (`features/src/lib.rs`).
    - **v1 tools, the default.** The spawn tool is `spawn_agent`; on newer versions its hook name is `multi_agent_v1spawn_agent`. Its input holds the spawn message, which is prompt text, and its PostToolUse result carries the child's `agent_id` and a nickname. The probe saw these on 0.156.0 (`probe/logs/rn8/0.156.0/happy`), and the source shows the same on 0.162.1 (`core/src/tools/handlers/multi_agents/spawn.rs`). The other v1 tools are `send_input` (which can also interrupt), `wait`, `close_agent` and `resume_agent`.
    - **v2 tools, with `multi_agent_v2` on.** The probe turned this on for 0.159.2 and 0.162.1 (`probe/runs/u6-rn8.sh`). The spawn tool is `collaborationspawn_agent`. Its input holds the message and a `task_name`, and its result holds no `agent_id` for the child. The other v2 tools include `send_message`, `followup_task`, `interrupt_agent` and `wait`.
    - AD reads none of these. SR-6.6 limits a Codex record's sources to SubagentStart and SubagentStop, so no tool payload is parsed. The five unsent fields (SR-6.8.4) stay null with either generation of tools.
  - Sources: the probe's captures and `probe/logs/rn8/<version>/happy/hooks.jsonl`; the Codex research's feature report, item 34.
- **C3: the model.** SubagentStart's `model` is the model of the subagent's own session. Codex builds the hook's request from the subagent session's turn context, which also gives the subagent's own `turn_id` and rollout path (source). The probe ran one model for both agents, so it could not tell the two apart.
- **C4: `stop_hook_active`.** It means what Claude Code's does: it is true on a stop that follows one a stop hook blocked (source, `core/src/session/turn.rs`; RN-25 (f)).
- **C5: only spawned subagents send them.**
  - Only subagents spawned this way send SubagentStart and SubagentStop. Codex has no in-process teammates.
  - Codex's internal threads send neither.
    - The `/review` thread was probed (RN-8).
    - Compaction and guardian threads are internal subagents. The source skips their start hooks ("Internal/system subagents skip start hooks"), and their stop hooks too.
    - A memory-consolidation thread is an internal session, not a subagent. The source sends it no SubagentStart or SubagentStop.
  - So a Codex SubagentStart always names a subagent.
- **C6: the start comes first.** Codex waits for the SubagentStart hooks to finish before the subagent's first turn runs (source; the probe's hook order). So a subagent's SubagentStop never arrives before its SubagentStart hook has run.
- **C7: later turns send no start.** A subagent thread can run again without starting fresh or forking, and it then sends no SubagentStart (source). This happens in two ways:
  - **More work in the same launch.** The main agent sends a subagent more work: `send_input` with the v1 tools, `send_message` or `followup_task` with v2. Or it reopens one it closed (v1's `resume_agent`). The subagent sends SubagentStop at the end of each such turn.
  - **After AD's `resume`.** AD's `resume` runs `codex resume`. Codex then brings back a subagent of the earlier session only when the resumed main agent sends it work: with v1 tools through `resume_agent`; with v2 tools, Codex restores the subagents' identities (`restore_v2_agent_metadata`) and loads one when the agent messages it. Either way the subagent's session source is a resume, so it sends no SubagentStart (`core/src/session/session.rs`). It sends SubagentStop at the end of each turn.
  - **After Codex's own `/resume` inside the agent** (SR-6.4). Codex restores the resumed session's subagents the same way (`thread_manager.rs`, "Both resume entry points must restore identities"). A subagent of that session that the agent sends work to sends no SubagentStart, and sends SubagentStop at the end of each turn.
- **C8: the gate.** Every subagent hook's parent process is the Codex process, so it passes the gate as the agent's own (probe: the parent of SubagentStart's and SubagentStop's hooks is the pane's `codex` process).
- **C9: interrupts.**
  - Esc while a subagent runs sends one main Interrupt, with no `agent_id`, and the subagent's SubagentStop about 10 s later.
  - When the main agent interrupts a subagent with the v2 tool `interrupt_agent`, only that tool's own PreToolUse and PostToolUse fire. The subagent sends no SubagentStop.
  - Source: probe RN-8, seen on 0.162.1 with v2 tools. Neither an interrupt with the v1 tools (`send_input` with `interrupt`) nor a close (`close_agent`) was probed (RN-25 (c)).
- **C10: other marked hooks.** A subagent's other hooks carry its `agent_id`. On 0.156.0 these include a UserPromptSubmit (`probe/logs/rn8/0.156.0/intr`). Each such hook sets the row's state as SR-6.2 says and writes no subagent entry (SR-6.6, SR-6.8.4).

### SR-6.8.2: Registration
This replaces SR-5.3's gated sentence.
- Every Codex launch registers one entry for SubagentStart and one for SubagentStop. Spawn, reuse and resume all do. These are in addition to SR-5.3's eight `hook --agent codex` entries, the help entry and, while `disable_ask_user` is on, the question tool's deny entry (SR-9).
- Each runs `exec <quoted path> hook --agent codex`, with SR-5.3's command and quoting rule. Each has no matcher, no timeout of AD's own (Codex's default applies) and no `additionalContextLimit`.
- They are AD's hooks for every rule that names AD's hooks:
  - they are trusted per launch, each by its own key and hash in the launch's hook-trust state (SR-5.2);
  - they are among the must-win settings (SR-5.2's precedence);
  - SR-2.3 refuses a `-c` override at or under `hooks`, so it refuses one that names them;
  - they are written into no Codex folder (SR-5.6).
- Their keys and hashes are the probe's golden data, already in `internal/spawn/testdata/hook-trust-golden.json`. Every scenario there except `controls` holds both entries, for the plain and the special binary path (RN-5).
  - Their hashes do not depend on b.xjn's design: AD's Codex entries have their own command form. So nothing is captured again now.
  - The pre-release check captures them again on the newest Codex, with the rest (SR-16.2).
- Claude launches are unchanged (SR-13).

### SR-6.8.3: The row write of a Codex SubagentStart or SubagentStop
- **The write.** It is b.xjn's one-transaction gated write (X-SR-3.2): the soft refresh of an ordinary hook, with SR-6.8.4's entry writes in the same transaction. The soft refresh:
  - keeps the state;
  - sets `last_seen_at`;
  - clears the liveness markers;
  - records the pane start time when it is NULL;
  - advances `row_version` by one;
  - writes one `ad.spawn.state_transition` with `soft_refresh` true.
  The invocation also writes its one `ad.hook.fired`, with X-SR-3.2's `upsert_outcome`.
- **For a Codex hook, also.** A Codex hook is one run with `--agent codex`, which is how the hook knows its agent (SR-6.1); in SR-6.8.3 and SR-6.8.4, "on a Codex row" means such hooks. For such a hook, the write also obeys these rules:
  - It keeps the end report: it neither sets nor clears `end_reported_at`.
  - It records no session id and no transcript path, and makes no transcript heal (SR-6.3). This holds whether or not the payload carries `agent_id`, because SubagentStart's `transcript_path` is the subagent's own rollout (C1). The classifier makes this rule: for these two Codex events its result carries no session id and no transcript path, and does not heal.
  - It writes no `idle_since`. No `idle_since` is ever set on a Codex row (SR-8.3).
- **No relay effect.** With or without `agent_id`, a Codex SubagentStart or SubagentStop is none of these:
  - a turn end;
  - a tool-ran proof;
  - an end report;
  - a relay request.
  It proves no permission request gone, closes none and releases no hold. MC-7 to MC-9 are unchanged: a subagent's request keeps holding plain `send-keys` and `pause` until the agent ends (SR-8.2, AC-20).
- **Fail-open.** It fails open as every Codex state hook does (SR-6.7, X-SR-12.1). Any failure logs one line with no content; the hook exits 0 and prints nothing.
- **A known event.** It writes no "unknown event" log line.
- **No `agent_id`.** A Codex SubagentStart or SubagentStop whose `agent_id` is not supplied (X-SR-5.1) makes only this row write. It writes no entry.

### SR-6.8.4: Records from Codex's events
These are Codex's instances of the rules b.xjn leaves to each agent's SRD: which events write entries, the fields read, the sources and the confirmation rule.
- **Which events write entries.** On a Codex row, only SubagentStart and SubagentStop write subagent entries.
  - No other Codex hook writes an entry or opens a claimable launch.
  - That covers Stop: it takes no snapshot, even one carrying a `background_tasks` array. That field is never read on a Codex row.
  - It covers every tool hook, whatever the tool, an `Agent`-named tool and Codex's spawn tools included.
  - It covers PostToolUseFailure, Interrupt, SessionEnd and every other event.
  - A Codex SubagentStop applies no listing.
  - In B, the classifier's `SubagentCapture` (Claude only) gates the SubagentStart and SubagentStop entry writes, the Stop snapshot and the `Agent`-tool capture. Only the first of those is opened to Codex rows. The Stop snapshot and the `Agent`-tool capture stay Claude-only (epic4-report).
- **The fields read.**
  - From SubagentStart: `agent_id`, `session_id`, `agent_type` and `model`.
  - From SubagentStop: `agent_id` and `stop_hook_active`.
  - "Supplied" has X-SR-5.1's meaning.
  - `agent_id` and `session_id` are classifier fields, for both agents in B. They keep the classifier's rule: a value of another JSON type fails classification as today. The hook logs one line, exits 0, and writes no row and no entry.
  - For Claude hooks, B's `session_id` type check differs from X-SR-5.1, which made `session_id` a field only this feature reads. That check came in with Epic 4, not with this addendum, and this addendum does not settle it. It is reported to the orchestrator for a bug ticket. This addendum's Codex rule holds either way.
  - Only this feature reads `agent_type`, `model` and `stop_hook_active`. A value of another JSON type reads as not supplied, and it never fails the hook.
  - Every string is cut by X-SR-5.3's 1 KiB rule before it is compared or stored.
- **Never read, kept or written.** This is X-SR-5.2's Codex instance: `last_assistant_message`, `agent_transcript_path`, `transcript_path` (for entries), `turn_id`, `cwd`, `permission_mode`, every other field of these two events, and every field of every other Codex hook, for entries.
- **Confirmation.** A Codex SubagentStart that creates an entry creates a record, confirmed at once. It creates no observation and claims nothing, because a Codex SubagentStart always names a subagent (C5). A Codex row never has a claimable launch.
- **Start.** This is X-SR-7.1 with Codex's sources.
  - For an id with no entry, the start creates a record of the current launch, `running`, with these values:
    - `first_started_at`, `started_at` and `opened_at`: the receipt time;
    - `session_id`: the start's `session_id`;
    - `subagent_type`: its `agent_type` (source `start`);
    - `model`: its `model`.
  - For an entry of any launch, X-SR-7.1 applies as written. A running entry gets only its unknown start times and fields filled. A stopped or lost entry runs again and moves to the current launch.
- **Stop.** X-SR-7.2 applies as written.
  - An entry of the current launch that is `running` or `lost` becomes `stopped`, with `stopped_at` the receipt time and `close_reason` `stop_event`.
  - A `stopped` entry takes the receipt time as its `stopped_at` only when the stop carries `stop_hook_active` true (C4).
  - With no entry, or an entry of an earlier launch, the stop changes nothing.
- **Sources on a Codex record.** Each field has at most one Codex source:
  - `subagent_type`: the start's `agent_type`.
  - `model`: the start's `model`. It fills an unknown value and is never replaced. On a Claude record the launch result stays its only source (X-SR-6.3), so `model` keeps one source per agent and needs no source column.
  - `session_id`: the `session_id` of the start that created the record.
  - `description`, `parent_type`, `parent_id`, `tool_use_id` and `run_mode`: no Codex source, so they are always null (C2). These are the PRD's "unsent fields".
  - Times: as X-SR-6.2, X-SR-6.5 and X-SR-7 state them.
- **Close reasons.** A Codex record's `close_reason` is `stop_event` or `agent_gone`. It is never `absent_from_snapshot`, because Codex rows get no snapshot.
- **Order and repeats.** C6 gives X-N6's order: a stop never arrives before its start. So X-SR-7.7 holds. A repeated event changes nothing, and the fields, state and confirmation do not depend on the order.
- **The rest of b.xjn's agent-neutral rules apply unchanged to Codex rows:**
  - the gate and the row conditions on every entry write (X-SR-3.1). A hook from any process but the row's recorded pane process, or one arriving while the row records no pane, writes no entry and writes one `ad.hook.ignored`;
  - terminal rows (X-SR-3.4) and clocks (X-SR-3.5);
  - the key and launches (X-SR-4.1);
  - the record fields, times and invariants (X-SR-6.1, X-SR-6.2, X-SR-6.5);
  - X-SR-6.4's first bullet, the unknown rule;
  - the cap (X-SR-10.3);
  - the wait bound (X-SR-12.2) and the 1 MiB payload limit (X-SR-12.1);
  - performance (X-SR-12.3). A Codex row adds store work only to its SubagentStart and SubagentStop, one transaction each; its Stop and tool hooks add none;
  - security (X-SR-17.1): the hook gate is the only authorization for an entry write;
  - the verb (X-SR-11).

### SR-6.8.5: Liveness, and no snapshot
- **The closing writes.** X-SR-8.1's close turns every running entry `lost` with `agent_gone` and deletes every claimable launch. On a Codex row it runs in each write that ends or restarts the launch:
  - the move to `ended` by `find-missing` or `pause` (SR-6.5, `MarkEndedIfSameLife`);
  - `find-missing`'s mark to `missing`;
  - resume's move to `pending`;
  - reuse's reset.
  
  With Claude's terminal SessionEnd write, which never applies to a Codex row (SR-6.5), these are the five closing writes that B's code and docs already list. This addendum adds no closing write.
  
  The move to `ended` takes its instant as `find-missing`'s mark does (X-SR-8.1): the store's clock, read once inside the move's transaction and shared with its request close. B built this in Epic 4. This addendum makes it a requirement and names its tests.
- **The end report closes nothing.** A Codex SessionEnd records the end report and keeps the state (SR-6.5). A running entry stays `running` until one of the closing writes. X-SR-8.2 holds for Codex rows: `kill` and every find-missing note leave entries as they are.
- **No snapshot.** Codex rows get no reconcile from a Stop snapshot (C2, SR-6.8.4).
- **A missed SubagentStop** leaves the record `running` until its next stop event (a later turn's SubagentStop, C7) or a closing write. For example:
  - the main agent interrupts the subagent with its tools (C9);
  - the SubagentStop is over the 1 MiB payload limit (its `last_assistant_message`);
  - the SubagentStop is dropped behind a busy store (X-SR-12.1, X-SR-12.2).
- **A missed SubagentStart means the subagent never shows.** This is a dropped-hook residual. It happens when the SubagentStart hook writes nothing: the store stays busy past its timeout, the config or the store fails to open, or the transaction rolls back (X-SR-12.1, X-SR-12.2).
  - Its stops find no entry and change nothing (X-SR-7.2), and no later start comes (C7).
  - Codex has no other source for the record. A Claude record also has its launch result and the snapshot.
- **A later turn reads `stopped`.** A subagent that runs another turn after its SubagentStop sends no SubagentStart (C7). So its record reads `stopped` while that turn runs. The turn's own SubagentStop leaves `stopped_at` at the first stop, unless it carries `stop_hook_active` true (X-SR-7.2).
- **A subagent working after a resume keeps its earlier state, or never shows.** The resume is AD's `resume` or Codex's own `/resume` inside the agent (C7).
  - After Codex's `/resume`, a subagent this row recorded keeps the state it had. A subagent this row never recorded (one from a session run outside this row) never shows, and its SubagentStops change nothing (X-SR-7.2).
  - After AD's `resume`:
    - Resume's move to `pending` closes the row's running records as `lost`, with `agent_gone` (X-SR-8.1). A record already `stopped` stays `stopped`. That is the usual case: the main agent waits for its subagents, and `pause` needs a `waiting` row.
    - A subagent of the earlier session that the resumed agent sends work to sends no SubagentStart (C7).
    - Its SubagentStops hit an entry of an earlier launch, so they change nothing (X-SR-7.2).
    - So while it works it reads `stopped` or `lost`, whichever the earlier launch left, and never `running`. A Claude subagent restarted in a later launch sends SubagentStart, which reopens its record.
- **These are differences, documented, not changed.** All four are documented (SR-6.8.10) and placed in SR-15.3's "The differences".
  - **Why they stay.** Codex does send marked hooks for a subagent's later turns: PreToolUse, PostToolUse and PermissionRequest carry its `agent_id` when a turn runs a tool, and so does UserPromptSubmit on 0.156.0 (C10). What keeps those hooks from reopening a record is this addendum's rule that records come only from SubagentStart and SubagentStop. That rule matches b.xjn's for Claude, where a tool hook carrying `agent_id` writes no entry (X-SR-4.4), and it fits b.xjn's agent-neutral entry rules (X-SR-4.1, X-SR-6.5). A turn that runs no tool would be missed anyway.
  - **Against the PRD.** The missed stop fits the PRD's row "Some subagent fields unknown; a missed subagent stop stays running until the agent ends". The missed start is a dropped-hook residual of the kind b.xjn documents for Claude. The last two show a wrong state for a subagent that is working, or none for one the row never recorded. That goes beyond the PRD's row; for subagents given several turns it is the common case.
  - **The User's approval.** The last two need the User's approval as differences, asked for with this addendum. The recommended answer is (a).
    - (a) Approve them as documented differences.
    - (b) Let a subagent's own marked hooks reopen or create its record. That lifts the SubagentStart-and-SubagentStop-only rule, needs an amendment of b.xjn's X-SR-4.1, X-SR-4.4 and X-SR-6.5 with tests, and still misses turns that run no tool.
    - (c) Leave Codex rows untracked. That drops AC-21's subagent half, which SR-16.3 makes a release condition.

### SR-6.8.6: The tracking flag (the final rule)
- **The rule.** Every launch write stores `subagent_tracking` 1 for both agents: `InsertPending`, `MoveToPending` and `ResetForReuse`. Every launch of either agent registers its subagent start and stop events (X-SR-9, SR-6.8.2).
  - The interim rule stored 0 for a Codex launch, through `subagentTrackedParamSQL` and `subagentTrackedColumnSQL` in `internal/store`. That rule is removed.
  - The failed-launch restores write back the earlier value, as X-SR-9 says.
- **What `list-subagents` shows.** It shows `tracked` true for a Codex row launched, resumed or reused by this release.
  - A row that reads 0 keeps 0 until its next launch, as X-SR-9 says, and gets no entry until then (X-SR-3.1). That is a row from before the hop, or a Codex row launched by an earlier build of this unreleased work.
- **Descriptions.** The manifest's `tracked` description says the flag is false "for a row whose launch registers no subagent events". That clause is dropped. Its Go doc comment and the TypeScript JSDoc follow.
  - This does not change help's size: help shows only each verb's description (X-SR-11.5).

### SR-6.8.7: What does not change
- The relay: every proof, close and hold (SR-8; MC-7 to MC-10; AC-19, AC-20).
- The Codex state mapping (SR-6.2) and session-end rule (SR-6.5), and SR-6.6's ungated rules.
- b.xjn's Claude capture: claims, the launch request and result, the snapshot, the listings, and their tests.
- The `list-subagents` verb on every surface: its parameter, result fields, value sets, errors and order.
- `get`, `list` and `status`.
- The store schema: no column, table or hop changes. The v9 hop already holds b.xjn's tables (t1.xjn.du's Amendment).
- Help and `helpStdoutBytes`.
- The trail's events and fields.

### SR-6.8.8: Model check: no new run
None of SR-6.8.2 to SR-6.8.6 changes what moves a row or releases a hold. So SR-12's rule asks for no TLA+ run, and none was made. Each part is checked against the vendored models (`spec/tla/codex/`, and `spec/tla/Phase5Hook.tla` with b.xjn's change):
- **The row write (SR-6.8.3)** keeps the state and the end report, and proves nothing.
  - In `Phase5HookCx` it changes none of the row's state, session id or end report (`st`, `sid`, `er`). Its one change to the model's row is the pane start time it records when that is NULL (`pst`). `HookAct` makes the same `pst` write, also gated. It is not `HookAct` itself, which writes `live` and records a session id.
    - In `hx_S`, `pst` can be 0 (`AllowNullStart`), but pids are unique (`PidPool` 0), so recording a start time never changes a `Match` verdict.
    - In `hx_S5`, start times are readable, so `pst` is never 0 and the write changes nothing the model keeps.
    - The gate is unchanged, so `RowOnlyByAgent` and `CxNoEndedWhileAlive` still hold.
  - The soft refresh with its entry write is b.xjn's `SubHook` in `spec/tla/Phase5Hook.tla`. That model checked it, including after a SessionEnd (X-SR-14.1).
  - In `Hold36hCx` with `CxRelay` on, the subagent's start (`SubStart`) and end (`SubEnd`) already write nothing to the row state, the proofs or the requests. That is exactly what SR-6.8.3 requires, so `rx_safe`, `rx_keys` and `rx_live` cover it.
  - Its `row_version` advance can make a snapshot-guarded write not apply. Such a write is handled as when any other hook overtakes it (X-SR-3.2):
    - find-missing's mark and the move to `ended` write nothing, and the next run or poll makes them again (SR-7.3);
    - SessionStart's identity write re-reads and retries once, then drops.
  - So, on a Codex row whose subagents keep sending hooks across a `/clear` or `/new`, the new session's SessionStart can lose both tries. The row then keeps showing the earlier session until the agent's next applied SessionStart (SR-6.4). b.xjn accepted this churn for Claude rows (X-SR-3.2), and it is accepted here on the same terms. No model checks the retry, and this addendum adds no model of it.
- **The entry writes (SR-6.8.4)** touch only subagent entries, never a row's state or a request. b.xjn's argument (X-SR-14.1) covers the confirmation at start and the Codex sources: its model does not tell records from observations, and field values do not change which process writes or which entries are running.
- **The closes (SR-6.8.5).** The Codex move to `ended` closes entries in its own transaction, as find-missing's mark does (X-SR-8.1).
  - In `Phase5HookCx` that move is `FM`'s write. It is also `PauseWait`'s, which is `FM`'s write made at another moment (MC-4).
  - So b.xjn's argument for `SubNoStaleRunning` holds for it: every write that ends a launch closes that launch's running entries.
- **The flag (SR-6.8.6)** gates entry writes only.
- **What would need a run.** A later change that lets a Codex subagent lifecycle event move a row's state, prove or close a request, or release a hold needs a TLA+ run on the job scheduler first.
  - One example is treating a Codex SubagentStop as a proof of its subagent's requests. No existing run covers that. `rx_C_subproof`'s knob makes the main agent's turn end prove the subagent's request, not the subagent's own end.
  - Such a change needs a new knob on `Hold36hCx`'s `SubEnd`, and new runs: `KeysSafe` and `NoStaleHold` with `LastGasp`, and `KeysSettle`.
  - It also needs the User's decision, since it would change MC-9.
- **SR-12.5's sentence** on Codex's subagent hooks now reads: "Codex's SubagentStart and SubagentStop are soft refreshes that write subagent records (SR-6.8.3, SR-6.8.4). Like every subagent lifecycle event they change no state and prove nothing, which `Hold36hCx`'s `SubStart` and `SubEnd` model under `CxRelay`. A subagent's tool hooks set states as SR-6.2 says."

### SR-6.8.9: Tests, in SR-14's terms
These replace SR-14.4's last bullet. Every test runs in the sandbox (SR-14.1).

**Fixtures and helpers.**
- **Payloads.** The captured Codex payloads `internal/hook/testdata/codex-hook-payloads/subagent-start.json` and `subagent-stop.json` are already rows of `payloadFixtures`.
- **Variants.** Each is built with `payloadWith`, never as a JSON literal in a test:
  - SubagentStart with no `agent_id`;
  - SubagentStart with no `agent_type`, and with no `model`;
  - SubagentStart with a number for `agent_type`, and for `model`;
  - either event with a number for `agent_id`, and with a number for `session_id`;
  - SubagentStop with `stop_hook_active` true;
  - a second subagent id;
  - a `background_tasks` array added to `subagent-stop.json` and to `stop.json`, as a listing that must be ignored;
  - every string read with a value over 1 KiB, with a multi-byte character across the 1024-byte boundary;
  - a SubagentStop over 1 MiB, filled in `last_assistant_message`.
  - Codex `post-tool-use-bash.json` and a PostToolUseFailure, each with `tool_name` `Agent` and a Claude launch result's shape: `tool_input.subagent_type`, `tool_response.agentId` and `tool_response.status` `async_launched`, then `completed`;
  - the 0.156.0 `spawn_agent` PostToolUse, whose result carries the child's `agent_id`, and the same result under the newer v1 hook name `multi_agent_v1spawn_agent`.
- **No-content sentinel.** X-SR-15.1's helper sets its sentinel in `last_assistant_message`, `agent_transcript_path`, `transcript_path`, `turn_id`, `cwd` and `permission_mode`.
- **`codexNotRegistered`** (`internal/hook/hook_payload_fixtures_test.go`) drops SubagentStart and SubagentStop. `TestPayloadFixtureEventsMatchRegistered` then requires their fixtures as registered Codex events.
- **Golden data.** `internal/spawn/testdata/hook-trust-golden.json` is used as it stands.
- **Seeds.** `apitest.SeedSpawn` with `WithAgent("codex")` seeds `subagent_tracking` 1, as a Claude seed does. `WithNoSubagentTracking` still seeds an untracked row.
- **Store helpers.** `storefix`'s `ApplySubagentStart` and `ApplySubagentStop`, and their foreign forms, take a Codex start: confirmed at creation, with a model. `apitest` re-exports them as it does today.
- **Docker driver.** `test/driver/pane-hook.sh --agent codex` needs no change. Each new case passes it the two events' minimal payloads: `hook_event_name` and SR-6.8.4's fields, with no content field.

**Existing tests and testplan cases this changes.** Each moves to the new rule; none is deleted without a replacement that pins the new behaviour.
- **These fail on the new rule:**
  - Docker `agent-9` (`tickets/testplans/b.75s/t1.75s.ag/t2.75s.ag.cl`, and its "Cases" row) asserts eight `hook --agent codex` entries and eight trusted hashes. It becomes ten.
  - `internal/hook/hook_codex_handle_test.go`'s `TestCodexSubagentEventsWriteNoSubagentRows` asserts that Codex subagent events write no rows. It is replaced by `hook_codex_subagent_test.go` below.
  - `pkg/api/resume_subagent_tracking_test.go` expects a Codex launch to store 0. It now expects 1.
  - `test/smoke/go/smoke_test.go`'s `assertCodexLaunched` requires a Codex launch's tracking to be 0. It now requires 1.
  - `internal/spawn/codex_hook_trust_test.go` leaves SubagentStart and SubagentStop out of its comparison: its `notCodexEvent` filter, its entry count `len(sc.Entries)-2`, and its skip of those labels. It now compares them.
  - `internal/spawn/codex_compose_test.go`'s `codexTestLabels` lists eight events. It now lists ten.
  - `pkg/api/codex_launch_test.go`'s `assertCodexLaunch` expects tracking 0. It now expects 1.
  - `internal/store`'s `spawns_test.go`, `resume_move_test.go` (its `launchTracking` helper) and `reuse_test.go` expect a Codex launch to store 0. They now expect 1.
  - `pkg/api/manifest/manifest_texts_test.go`'s `TestManifestTextCases` pins the `tracked` clause that SR-6.8.6 drops. It now pins the new text.
- **These still pass but now describe the old rule:**
  - `codexRowOpts()` in `pkg/api/codex_launch_test.go` seeds Codex rows untracked;
  - `list_subagents_test.go`'s case "a codex row, launched with no subagent events registered";
  - the Codex seeds in `cmd/agent-director-admin`'s `kill_finished_test.go` and `delete_test.go`;
  - the doc comment of `apitest.WithNoSubagentTracking`, which says a Codex launch stores 0;
  - `TestSpawnReuseAsCodex`'s doc comment, which says 0;
  - the comment in `pkg/api/spawn_reuse_restore_test.go` on a Codex launch's tracking;
  - the Codex skip in `internal/store/resume_restore_test.go`;
  - `TestClassifyEventAgentSelection`'s doc comment, which says only Claude's hooks feed subagent capture.
  Each is updated to the new rule. A test that still needs an untracked Codex row keeps `WithNoSubagentTracking` and says why.
- **These also move with the change:**
  - The TypeScript public-surface golden `pkg/ts-bun-client/test/fixtures/public-surface/types.d.ts.golden`, which holds the `tracked` JSDoc. It is regenerated in the sandbox with `scripts/update-surface-golden.ts`.
  - `TestPayloadFixtureParses`'s expected session id and transcript path for the Codex SubagentStart and SubagentStop fixtures: both are empty, because the classifier makes SR-6.8.3's rule.

**Test files.**
- **`internal/spawn`:**
  - `codex_compose_test.go`, through `codexfix`: on spawn, reuse and resume, ten `hook --agent codex` entries plus the help entry. SubagentStart and SubagentStop have no timeout and no matcher, and none of AD's settings stands after a `--`.
  - `codex_hook_trust_test.go`: the two entries' keys and hashes equal the golden data, for the plain and the special binary path.
- **`internal/hook`:**
  - `classify_test.go`: Codex SubagentStart and SubagentStop, with and without `agent_id`. Each is a known soft refresh with no session id, no transcript path and no heal, and is not `TurnEnded`, `ToolRan` or `EndReport`.
  - A new `hook_codex_subagent_test.go` drives `hook.Handle` with `--agent codex`, with the helpers of `hook_subagent_helpers_test.go` and `hook_codex_handle_test.go`. Its cases:
    - a start gives a `running` record: `subagent_type` `default`, with `model` and `session_id` set; `description`, `parent_type`, `parent_id`, `tool_use_id` and `run_mode` null; `tracked` true;
    - its stop makes the record `stopped`, `stop_event`;
    - a stop with `stop_hook_active` true moves a `stopped` record's `stopped_at`; one with false leaves it;
    - a stop with no start creates nothing;
    - an earlier launch's records, one `lost` and one `stopped`. Resume's move to `pending` runs first, then the new launch's pane identity is recorded, so the hook passes the gate. A SubagentStop for each then applies its soft refresh (`upsert_outcome` `updated`, `row_version` + 1), and each record keeps its state, its times and its earlier `launch_token`;
    - each applied event writes its soft refresh's trail records: one `ad.spawn.state_transition` with `soft_refresh` true, and one `ad.hook.fired`;
    - a start after a stop reopens the record;
    - two subagents' events interleaved;
    - with no `agent_id`: one soft refresh and no entry, and a row that records no session id or transcript path still records none;
    - a number for `agent_id` or `session_id`: one log line, exit 0, and no row write (`row_version` and `last_seen_at` unchanged) and no entry;
    - with `agent_type` or `model` missing, or a number: the record is still created, with that field null;
    - a start on a row with an end report keeps the end report;
    - no snapshot or listing is applied. A Codex Stop, and a Codex SubagentStop for the second subagent id, each carry a `background_tasks` variant that lists the record's id and an id with no entry. After each, the record is unchanged (`run_mode` still null, still `running`) and no new record appears;
    - kept from the replaced `TestCodexSubagentEventsWriteNoSubagentRows`: a Codex Stop with an empty `background_tasks` array leaves untouched a seeded `running` `background` record opened more than 2 s earlier, and an old claimable launch;
    - Codex tool hooks open no claimable launch and write no entry: `pre-tool-use-bash.json`, `pre-tool-use-subagent.json`, and a variant named `Agent` that carries `tool_input.subagent_type`;
    - neither do the `Agent`-named PostToolUse and PostToolUseFailure variants with a launch result's shape, nor a v1 spawn result (under `spawn_agent` or `multi_agent_v1spawn_agent`), whether or not a SubagentStart for that id came first;
    - a foreign parent, and a row with no recorded pane, write no entry and one `ad.hook.ignored`;
    - an `ended` row and a `missing` row get no entry;
    - an untracked Codex row (`WithNoSubagentTracking`) gets no entry;
    - the sentinel reaches no table, trail line or log line;
    - with `WriteFailSubagentRecord`: exit 0, empty stdout, the row and entries unchanged, `upsert_outcome` `error`;
    - a SubagentStop over 1 MiB is dropped, and the record stays `running`;
    - the 1 KiB cut.
  - `hook_codex_relay_test.go`: after a relayed subagent request (`permission-request-subagent.json`), allowed and denied, the subagent's SubagentStop leaves the request unproven and still holding (MC-9).
- **`internal/store`:**
  - `subagent_transitions_test.go`: rows for the Codex start. It creates a confirmed record without claiming and without reading any claimable launch. `model` is filled once and never replaced, and the start's other fields are as SR-6.8.4 says.
  - `spawns_test.go`, `resume_move_test.go` and `reuse_test.go`: a Codex launch stores `subagent_tracking` 1.
  - `resume_restore_test.go` and `reuse_restore_test.go`: the restore writes back the earlier value, 0 or 1.
  - `find_missing_ended_test.go` keeps its close assertions.
- **`pkg/api`:**
  - `codex_launch_test.go` (`assertCodexLaunch`): tracking 1.
  - `list_subagents_test.go`: a tracked Codex row with a seeded Codex record, whose unsent fields read null and whose keys are all present.
  - `find_missing_test.go` and `pause_codex_test.go`: a Codex row with a running record and an end report, its agent gone. The move to `ended` turns the record `lost` with `agent_gone`, its `lost_at` equal to the requests' `closed_at`. In `find_missing_test.go` only: without an end report, the `missing` mark does the same.
  - `spawn_reuse_test.go` and `resume_test.go`: a Codex row's reuse and resume close a running record.
- **`pkg/api/manifest` and the generated references:** the `tracked` description's new text. `make generate` refreshes the references.
- **`test/realtmux`:** no new case. The subagent events run as every Codex hook does (C8), and the existing Codex gate case covers that.
- **Docker, `epic-13-agents` (t1 `t1.75s.ag`):** new t2 cases after `agent-21`. Each gets a row in the t1's "Cases" table and is named in the intro's case ranges and its "Driver mode" paragraphs. Each reads the store only through `/opt/driver/sql.sh` or `list-subagents`. Every case but `agent-24` fires hooks only through `pane-hook.sh --agent codex`; `agent-24` pipes them from the driver's shell on purpose.
  - `agent-22`: after report-in and a UserPromptSubmit, a SubagentStart. `list-subagents` shows `tracked` true and one `running` record, with `subagent_type` and `model` set and the five unsent fields null. A Codex Stop then leaves the record unchanged. The SubagentStop makes the record `stopped`, `stop_event`.
  - `agent-23`: a SubagentStart with no SubagentStop. `pause` ends the row through the end report, and the record reads `lost`, `agent_gone`. On a second row whose stand-in is killed with no end report, `find-missing` marks it `missing` and its record reads `lost`.
  - `agent-24`: the same hooks piped from the driver's shell write no entry, and each writes one `ad.hook.ignored` `pid_mismatch`.
  - `agent-25`: with relay on, a subagent's PermissionRequest is allowed through `decide`, then its SubagentStop arrives. Plain `send-keys` still gets `ErrDialogMaybeOpen` (AC-20).
- **Docs:** the old-names and old-path tests are unchanged, and no name is added to them. Any `pkg/api/readme_*` test that pins wording moves with the wording.
- **TLA+:** none (SR-6.8.8).

### SR-6.8.10: Documentation
Each item goes in the doc that owns it (SR-15.1). docs/usage.md owns what a consumer sees in records; docs/hooks.md owns what each hook writes.
- **`docs/hooks.md`:**
  - The Codex hook set gains SubagentStart and SubagentStop: ten `hook --agent codex` entries, besides the help entry and the question tool's deny entry.
  - "The shell between Codex and the hook" says what a login shell's startup files that print now do to these events. Codex reads a SubagentStart hook's plain output as model context for the subagent (`hooks/src/events/session_start.rs`), and a SubagentStop hook's printed output fails that hook. docs/permissions.md's "Startup files that print" links to it.
  - What the two events write on a Codex row (SR-6.8.3, SR-6.8.4).
  - A Codex Stop takes no snapshot, and Codex's tool hooks write no record.
  - The rule: every passage that says a Codex launch registers no SubagentStart or SubagentStop entry, that Codex hooks write no subagent records, that entries are written only for a Claude launch's `--settings`, or that these two events are a plain soft refresh in the Codex states table, moves to the new rule. That covers today's text "A Codex agent's hooks write no subagent records…". The doc writer greps for these passages.
  - It links to usage.md for what a consumer sees.
- **`docs/usage.md`**, in "When an agent is tracked" and the records section. It replaces the paragraph saying a Codex row reads `tracked: false`. The new text says:
  - a Codex row is tracked;
  - a Codex subagent shows as a record from its start. Compare "Which subagents show as records", which describes Claude's confirmation;
  - a Codex record never has a description, parent, tool-call id or run mode: they read null;
  - a Codex record's `stopped_at` stays at its first stop after later turns, unless a later stop carries `stop_hook_active` true. This qualifies the field table's "until its next stop";
  - a subagent whose start hook wrote nothing (for example behind a busy store) never shows;
  - its `model` comes from the subagent's start;
  - its `close_reason` is never `absent_from_snapshot`;
  - the PRD's difference row "Some subagent fields unknown; a missed subagent stop stays running until the agent ends", in plain words, with SR-6.8.5's examples;
  - a subagent given another turn after it stopped reads `stopped` during that turn;
  - after a resume of a Codex agent (AD's `resume`, or Codex's own `/resume`), a subagent of the resumed session that the agent sends work to keeps its earlier `stopped` or `lost` state while it works, and one the row never recorded never shows.
- **`docs/settings.md`:** the count and list of a Codex launch's registered hook entries.
- **`docs/architecture.md`.** The rule: every passage that says a Codex launch registers no subagent events, stores `subagent_tracking` 0, counts eight Codex events, gets no subagent entry write, or names `subagentTrackedParamSQL` or `subagentTrackedColumnSQL` moves to the new rule. That covers at least:
  - lines 81, 1374, 1406–1412 (the Must-use rule), 1431, 1568, 1624, 1687, 3757, 4593–4596, 5079, 6186, 19105–19110 and 20526–20530 of B's file;
  - "What a new agent reuses". Its claim that a second agent changes no store code becomes false: the store's start event gains a model and a confirmed-at-start input. The passage is corrected to say so;
  - the Codex instance in "Subagent capture": the two events, confirmation at start, the sources and no snapshot;
  - the agent-aware classifier's Codex table.
  The doc writer greps for these terms rather than relying on the line numbers.
- **`pkg/api/README.md` and `pkg/ts-bun-client/README.md`:** the `tracked` text drops its clause that a Codex row is never tracked.
- **Field descriptions.** Three texts describe `started_at` as the latest run's start, `stopped_at` as the last stop and `lost` as judged gone: the manifest's `subagentsDescription`, the Go doc of `SubagentRecord` and the TypeScript JSDoc of `SubagentRecord`. Each stays a generic description and adds one short clause: the times are those of the events the agent reported, and they can miss a run the agent did not report, with a link to an agent-neutral usage.md anchor. That anchor must not contain "codex", because `TestManifestTextCases` (`listSubagentsTextCases`) refuses the word in any case, anchors included. This is not part of help's output, so `helpStdoutBytes` is unaffected (X-SR-11.5). The generated references are regenerated, and the TypeScript golden with them.
- **`pkg/ts-bun-client/CHANGELOG.md`:** its sentence that a Codex row reads `tracked: false` is corrected, unless bug b.xnd has deleted the file before the release (t1.xjn.du's Amendment: the project keeps no changelogs).
- **The breaking-change text** (`tickets/Ideas/b.ssz/release-notes-breaking.md`, SR-11.6):
  - Its Epic 5 placeholder and the line "b.xjn renames: to be added by Epic 5" are removed, with no rows added, because nothing of b.xjn's is renamed (SR-6.8.12).
  - b.xjn's verb and record fields are new names, not renames, so the text needs no entry for them.
  - Its schema bullet also names the tables `subagents` and `subagent_launches` and the column `spawns.subagent_tracking`, which the same v9 hop adds.
- No `docs/*-guide.md` file is edited. No doc names an SR, an RN, a bee or a model run (SR-15.1).

### SR-6.8.11: RN-25, and Appendix C
The RESEARCH NEEDED table gains this row. The pre-release check (SR-16.2) settles it on the newest and the minimum Codex, and SR-16.3 applies.

| RN | Question | Settled by | If not as assumed |
|---|---|---|---|
| RN-25 | (a) SubagentStart's `model` names the subagent's own model, checked with a subagent that runs on a model other than the main agent's (C3). (b) A subagent given more work after it stopped sends no SubagentStart, and sends one SubagentStop per turn (C7). (c) The main agent interrupting or closing a subagent sends no SubagentStop (C9). (d) No internal thread sends SubagentStart, checked with `/review` and with a compaction (C5). (e) After AD's `resume` of a Codex row, and after Codex's own `/resume` inside the agent, a subagent of the resumed session that the agent sends work to sends no SubagentStart (C7). (f) A SubagentStop that follows one a stop hook blocked carries `stop_hook_active` true (C4). (g) Under `no_pre_trust`, the subagent hooks run trusted, with no review dialog. (h) Each of (a) to (f) holds with the default v1 multi-agent tools as well as with `multi_agent_v2` on, on the newest Codex: SubagentStart and SubagentStop carry `agent_id` with either; (c) is checked through v1's `send_input` interrupt and `close_agent`; (e) through v1's `resume_agent`. | Pre-release. Codex's source answers each part. The probe already saw, on 0.162.1 with v2 tools, the `interrupt_agent` half of (c) and the `/review` half of (d). | (a) Named code change: a Codex record's `model` is not read and stays null, and the docs say so. (b), (c), (e) and (f): doc only. (d) and (g): stop and ask the User. For (d), such a thread would show as a subagent record. For (g), an untrusted entry makes Codex show its hook-review dialog at launch, for the whole agent: the row stays `pending` and is noted `unreported`. (h): each part takes its own fallback for the tool generation that differs. |

Appendix C's AC-21 row reads: "SR-7.5, SR-8.3, SR-12; the subagent view: SR-6.6 and the SR-6.8 addendum (SR-6.8.1 to SR-6.8.11), needed for release (SR-16.3)".

### SR-6.8.12: Appendix A, b.xjn's names
These rows replace Appendix A's row "Every Claude-named name b.xjn adds".
- b.xjn's names were agent-neutral when it merged (t1.xjn.du, "Names"; the Epic t1.9r1.dn update of 2026-10-11).
- Checked in B: none is Claude-named. So none is renamed, none is refused as an old name, and SR-4.5's old-names test needs no entry for them.
- SR-14.4's test that no column starts with `claude_` covers `subagents` and `subagent_launches`.
- They are listed here so that Appendix A is complete. Every row is in the "Unchanged (generic already)" class and applies on every surface where the name occurs.

| Surface | Name (b.xjn's, unchanged) |
|---|---|
| Verb (manifest, CLI, help) | `list-subagents`, placed right after `list` |
| MCP tool | `list_subagents` |
| Parameter (MCP, TypeScript); CLI flag | `instance_id`; `--instance-id` (already this work's new name, Appendix A) |
| Result fields | `tracked`, `subagents` |
| Fields of each `subagents` element | `subagent_id`, `instance_id`, `session_id`, `subagent_type`, `description`, `model`, `parent_type`, `parent_id`, `tool_use_id`, `run_mode`, `first_started_at`, `started_at`, `stopped_at`, `lost_at`, `state`, `close_reason` |
| Values | `state`: `running`, `stopped`, `lost`; `close_reason`: `stop_event`, `absent_from_snapshot`, `agent_gone`; `parent_type`: `agent`, `subagent`; `run_mode`: `background`, `foreground` |
| Go library (`pkg/api`) | `Client.ListSubagents`, `ListSubagents`, `ListSubagentsStore` (method `ListSubagentRecords`), `SubagentRow` (the alias of the store's row type in `aliases.go`, with the same field names as `SubagentRecord`; its times are integer milliseconds), `ListSubagentsResult` (`Tracked`, `Subagents`), `SubagentRecord` (`SubagentID`, `InstanceID`, `SessionID`, `SubagentType`, `Description`, `Model`, `ParentType`, `ParentID`, `ToolUseID`, `RunMode`, `FirstStartedAt`, `StartedAt`, `StoppedAt`, `LostAt`, `State`, `CloseReason`) |
| `pkg/api/apitest` | `SeedSubagent`, `SeedSubagentLaunch`, `SubagentOption` and its `WithSubagent…` options, `SubagentLaunchOption` and its `WithClaimable…` options, `WithNoSubagentTracking`, `SeededSubagentMs`, `ReadSubagentRows`, `SubagentRows`, `SubagentEntryColumns`, `SubagentLaunchColumns`, `SpawnColumns.SubagentTracking`, the `Apply…Subagent…` and `ApplyForeign…Subagent…` helpers, and the aliases `SubagentLifecycleEvent`, `SubagentLaunchRequest`, `SubagentLaunchDetails`, `SubagentLaunchFailure`, `SubagentListed`, `SubagentSnapshot`, `MainAgentStop` |
| TypeScript client | `listSubagents`, `ListSubagentsParams`, `ListSubagentsResult`, `SubagentRecord`, the `VERBS` entry `list-subagents`; the keys of the result-field rows above |
| Store: `spawns` column | `subagent_tracking` |
| Store: table `subagents` | `created_seq`, `instance_id`, `subagent_id`, `launch_token`, `confirmed`, `session_id`, `subagent_type`, `description`, `model`, `parent_type`, `parent_id`, `tool_use_id`, `run_mode`, `first_started_at`, `started_at`, `stopped_at`, `lost_at`, `state`, `close_reason`, `opened_at`, `activity_at`, `subagent_type_source`, `run_mode_source`, `stopped_at_source` (source values `start`, `details`, `snapshot`, `stop`) |
| Store: table `subagent_launches`; index | `created_seq`, `instance_id`, `launch_token`, `tool_use_id`, `subagent_type`, `opened_at`; `idx_subagent_launches_claim` |
| Trail, error names, config keys, template keys | none added by b.xjn |




