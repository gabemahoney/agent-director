# PRD amendment, 2026-10-12: Codex agents show no subagents; resume continues the last agent's latest session, never across a switch

**Status: approved:yes (approved by the User on 2026-10-12).**

**Update, 2026-10-12, later the same day: the User changed Decision 2** (its "every call shows only the current life" wording is superseded by the final updates below). The lock of an instance id to its agent is withdrawn. In its place, Decision 3: a reuse may name either agent, and every call shows only the row's current life. The User: "This is a desired use case, the PRD should say its supported. But all calls show current life." Decision 1 is unchanged. The approval stands for the amendment as changed.

**Update, 2026-10-12, final: Gabe's life rule.** Gabe (the User) gave his final rule in Slack on 2026-10-12, and Decision 3 now records it: only an agent switch starts a new life; session history and subagent records show only the current life; AD-agent children are not tied to a life. It replaces the earlier wordings of Decision 3, which were never built. The approval stands for the amendment as changed.

**Update, 2026-10-12, Gabe's added rule:** a switch gives up resume of everything before it, permanently, a switch back included (Decision 3). The approval stands for the amendment as changed.

**Update, 2026-10-12, Gabe's resume rule and simplest statement:** "Resume always continues the last agent's latest session. Switching agents without resume is fine. The one thing you can't do is resume across a switch." `resume` never takes an older session, and resume never crosses a switch (Decision 3; how a switching launch that asks to resume is handled was corrected later the same day, below). The approval stands for the amendment as changed.

**Update, 2026-10-12, Gabe's key case:** Claude, then Codex, then Claude launched with `--resume` on the same id never resumes the old Claude session. Gabe first asked for a refusal, then corrected it the same day: it is not an error; AD starts Claude fresh and `--resume` resumes nothing (Decision 3). The approval stands for the amendment as changed.

This note amends PRD t1.ssz.qi with the User's decisions of 2026-10-12, relayed by the orchestrator. It wins over the PRD text it names. The SRD's SR-6.8 addendum (t1.ssz.pm) applies it.

## Decision 1: Codex agents show no subagents
- The User ruled that the subagent view must never show false information. Coarse states are fine; false ones are not.
- Codex does not send enough to report a subagent's state accurately. The reasons, from the probe and from Codex's source (SRD t1.ssz.pm, SR-6.8.1):
  - Codex sends SubagentStop at the end of each of a subagent's turns, not once when it ends;
  - it sends no SubagentStart when a subagent runs again, after a resume or a session change included;
  - it sends no hook when the main agent interrupts a subagent, and may send none when it closes one;
  - a tool-less turn on Codex's newer multi-agent tools sends nothing until its SubagentStop.
- A design was checked that claimed only what AD knows (`seen`, `unknown`, `gone`). It needed an `unknown` state after a resume. The User's answer: "If we have to have an unknown state then lets just not show them for Codex."
- So **Codex agents show no subagents.** `list-subagents` on a Codex agent reports `tracked` false and `subagents` `[]`.
  - AD registers no Codex SubagentStart or SubagentStop hook, and writes no record for a Codex subagent.
  - The verb keeps answering on Codex agents, with no new error. The User confirmed this on 2026-10-12.
  - Codex subagents' approvals are still relayed and held as before (AC-20).
  - Claude agents are unchanged.

## Decision 2 (withdrawn): an instance id is locked to its agent
The User first decided "Agent IDs are locked to an agent": a `reuse_finished` spawn naming another agent would be refused with a new error, `ErrReuseAgentMismatch`. The User withdrew this the same day (Decision 3). There is no such refusal and no such error. The PRD text this decision changed reads as Decision 3 says.

## Decision 3 (final): resume continues the last agent's latest session; never across a switch
These are Gabe's decisions, final, given in Slack on 2026-10-12. They replace the earlier same-day wordings of this decision ("every call shows only the current life", and a `list --parent` limited to the parent's current life), which were never built.

**The rule, as Gabe put it (the one to write):** "Resume always continues the last agent's latest session. Switching agents without resume is fine. The one thing you can't do is resume across a switch: e.g. Codex runs and ends, then a Claude launch with --resume on the same id." Corrected the same day: such a launch is not an error; it starts fresh and resumes nothing.

What that means in AD:
- **Switching agents is fine.** A `reuse_finished` spawn may name the row's agent or the other one, and the row takes the new spawn's agent. AD refuses no switch (Decision 2 stays withdrawn).
- **`resume` continues the last agent's latest session, never an older one.** `resume` takes only the id and relaunches the row's agent with the row's current session: the latest session the agent reported. Older sessions stay listed in `prior_sessions`, but AD offers no way to resume them: no parameter picks one, Claude's and Codex's resume arguments are refused on a plain spawn and a same-agent reuse (Claude's short forms `-r` and `-c` included, bug b.d6q) and dropped on a switching reuse, `resume` does not replay them, and `resume` no longer falls back to an older session when the latest one's transcript is gone (it reports the missing transcript instead). Example: Claude session A runs and ends; the id is reused for Claude without resume and session B runs and ends; `resume` continues B, never A. Whether the agent can reopen even its latest session is up to Claude or Codex.
- **No resume across a switch, ever.** A switch gives up resume of every session from before it, permanently, a switch back included: agent-director offers no path where an id runs Claude, switches to Codex, switches back to Claude and resumes the old Claude sessions. (The agent's own in-session `/resume` picker is the agent's, not agent-director's; agent-director then follows whatever session the agent reports.) A launch of the other agent that asks to resume (for example Codex runs and ends, then a reuse naming Claude with `--resume`) is not an error: AD starts the new agent fresh and resumes nothing.
- **The key case (Gabe, named; corrected the same day).** Claude runs and ends, then Codex runs and ends, then Claude is launched on the same id with `--resume` (in any spelling the agent accepts). It is not an error: AD starts Claude fresh, and `--resume` resumes nothing. The one hard rule: the original Claude session is never resumed, because that agent would have a gap in its memory, knowing nothing of the Codex period. The docs say plainly that in this case `--resume` resumes nothing; a test and the TLA+ model pin it.
- **Bookkeeping behind it (AD's "life").** A switch starts a new life; a same-agent reuse, `resume` and the agent's own session changes keep it. Two kinds of data belong to a life and show only for the current one: the session history (`prior_sessions`) and the subagent records (`list-subagents`). A same-agent reuse keeps them; a switch starts both empty. A Claude row with subagent records and history, reused as Codex, reads `tracked` false, `subagents` `[]` and `prior_sessions` `[]`.
- **AD-agent children are not tied to a life.** If Claude agent A starts helper H as an AD agent and A's id then switches to Codex, H stays A's child: `list --parent` on A still shows H. AD neither refuses the switch nor stops H. `list` is not historical: it shows the rows that exist now.
- **When a reuse's launch fails** and AD can undo it, the row's previous agent and life come back. When AD cannot (for example a launch that timed out), the row stays as the reuse left it.
- **Unchanged:** a reuse still deletes the row's permission requests, as today; `resume` takes only the id; Codex rows show no subagents (Decision 1).
- **The trail** is AD's append-only log, not a call. It keeps every life's lines, so it can show both agents under one id.

## Changes to the PRD text
- **"Choosing the agent"**, the sentence "`resume` takes only the row id and reuses the row's agent, arguments and `extra_env`." reads "… the row's agent, arguments (without resume arguments or Claude's `-p`) and `extra_env`."; and the sentence "A `reuse_finished` row takes its agent from its new spawn." reads: "A `reuse_finished` spawn may name either agent, and the row takes it. `resume` continues the last agent's latest session, never an older one. Resume never crosses a switch: a launch of the other agent that asks to resume starts it fresh and resumes nothing, and after a switch, a switch back included, no earlier session can be resumed or is listed. A reuse with the same agent keeps the earlier sessions listed and the subagent records shown. A row's children stay its children across a switch."
- **AC-3** reads: "`resume` accepts only the id and relaunches the row's agent, arguments (without resume arguments or Claude's `-p`) and `extra_env` with its latest session, never an older one (a missing transcript is reported, with no fallback to an older session); a `reuse_finished` spawn may name either agent and the row takes it; a reuse naming the other agent with an argument that asks to resume starts that agent fresh and resumes nothing, with no error, the key case included (Claude, then Codex, then Claude with `--resume`: the old Claude session is never resumed); after a switch `get`, `list-subagents` and `resume` show or use nothing from before it, a switch back included (a Claude row with subagent records and history, reused as Codex, reads `tracked` false, `subagents` `[]` and `prior_sessions` `[]`), while `list --parent` still lists the row's children; a same-agent reuse keeps `prior_sessions` and the subagent records; a reuse whose failed launch is undone shows the earlier agent and life again."
- **"Documentation"** gains an item: "Gabe's rule in plain words: resume always continues the last agent's latest session; switching agents without resume is fine; resuming across a switch is impossible, shown with the case 'Claude runs, then Codex, then a Claude launch with --resume on the same id', where `--resume` resumes nothing and Claude starts fresh; a switch back does not bring old sessions back; older sessions are listed but never resumed; children stay their parent's across a switch."
- **"Pass-through arguments"**, the bullet on refused arguments: it gains "Arguments that pick or resume another session are refused on a plain spawn and a same-agent reuse, short forms included; on a reuse that switches the agent they are dropped instead, so the new agent starts fresh and resumes nothing."
- **AC-6** gains: "a session-picking argument (Claude's `--resume`/`-r`, `--continue`/`-c` and the other resume options; Codex's `resume`/`fork`) is refused on a plain spawn and a same-agent reuse, and on a reuse that switches the agent is dropped with no error, reported in spawn's `dropped_args`, and the agent starts fresh; Claude's `-p` is refused on every spawn and reuse, and `resume` strips it."
- **"Installing and launching"**, the pre-trust bullet: "`no_pre_trust` honoured for the row's life" reads "`no_pre_trust` honoured until the next spawn or reuse (a `resume` keeps it)": each reuse sets its own choice, as today, while a same-agent reuse no longer starts a new life.
- **Goals, "Feature parity"** ("Today's Claude behaviour settles every scope question") and **Non-Goals** ("Any behaviour AD does not have for Claude today"): Decision 3 is a User-approved exception to both, as the launch path is: the life rule, the drop of resume arguments on a switch with spawn's `dropped_args`, and resume continuing only the latest session apply to Claude rows too.
- **"Errors, renames and upgrade"**, first bullet: unchanged. No refusal is added for a reuse that changes the agent.
- **"Lifecycle, subagents and help"**, first bullet. "Codex rows report subagents through the b.xjn subagent view." reads: "Codex rows show no subagents: the subagent view reports them untracked (see the differences table)."
- **"Where Codex differs from Claude"**, the row "Some subagent fields unknown; a missed subagent stop stays running until the agent ends | Codex does not send them | Documented" reads:

  | Codex agents show no subagents | Codex does not send enough to report their state accurately | Nothing; `list-subagents` reports the agent untracked |

- **AC-21** is the acceptance criterion that begins "`pause`, `kill`, `find-missing`, `expire`, labels, the trail and the admin tool work on Codex rows…". The User's decision of 2026-10-12 removes its Codex subagent half, "Codex subagents appear in the subagent view, with unsent fields unknown". In its place: "a Codex row's `list-subagents` reports `tracked` false and `subagents` `[]`".
- **Unchanged:**
  - the subagent relay rules: AC-20, and the differences row "A subagent's approval, allowed or denied, holds plain `send-keys` and `pause` until the agent ends";
  - the assumption that b.xjn is finished first. Its names are agent-neutral, and nothing is renamed.
