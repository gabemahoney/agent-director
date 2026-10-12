# Epic 5 brief: Gabe's final life rule (2026-10-12)

## Decisions (Gabe, in Slack, final)
1. **A new life happens only when the agent type changes** (Claude<->Codex on the same instance id). Gabe: "if you switch Agents that's a fresh start. If you launch a new Agent with or without --resume thats the same 'life'."
   - A same-agent reuse, with or without --resume, keeps the SAME life.
   - This CHANGES today's behaviour, where a same-agent reuse starts a new life (and hides earlier session history). Work out every effect: life_number bumps, session_history split, b.xjn subagent records, and anything else keyed on life.
2. **In-process subagents (list-subagents) belong to a life.** They never carry across a life (an agent switch). They do carry across launches (relaunch or resume of the same agent type).
3. **AD-agent children (rows linked by parent id) are NOT tied to life.** If Claude A starts helper H as an AD agent, and A's id then switches to Codex, H stays A's child. `list --parent` on Codex A still shows H. AD neither refuses the switch nor stops H.
4. Still standing: cross-agent id reuse is supported (the lock stays withdrawn). Codex rows show no subagents (tracked false). "Every call shows only the current life" applies to life-scoped data only (list-subagents, session history), not to parent links. list is not historical.

## Tasks, in order
1. Reconcile your 19 held list --parent edits with rule 3. Keep the ones that fit, undo the rest (items 7 and 16 restore the earlier exception text). Rewrite SR-6.8.9, PRD amendment Decision 3 and the current-life wording to match rules 1–4. Note in the PRD amendment that these decisions are Gabe's and dated 2026-10-12.
2. **Small TLA+ run first (Gabe asked for it).** Model the life/launch/parent rules: same-agent reuse keeps the life, an agent switch starts a new life, subagent records are scoped to life, AD children survive a switch, and calls show only current-life data. Check that nothing ever shows false data (Gabe's rule: coarse is fine, never false). Keep it small. Heavy TLC goes ONLY through the job scheduler (channel C0B3X876XSB, --network off); no heavy local TLC. A green run is required before the design goes into the SRD or plan.
3. Run /req-review on the changed PRD and SRD text.
4. Then report back to me. Don't hatch Epic 5 yet; I'll confirm.

## Added question (Gabe, 2026-10-12)
Gabe expects that, under rule 1, a caller could find and resume an OLD Claude session on the same id, even after a later same-agent spawn without --resume. In your report, say plainly: does resume today let a caller choose which session to resume, or only the latest? And what does the new rule change about that? Answer it; don't build anything new for it.

## Added rule (Gabe, 2026-10-12)
- Which old sessions can be resumed is agent-specific (it depends on what Claude or Codex itself allows). Say that; don't build anything for it.
- **Make it very clear in the PRD, SRD and user docs:** switching an id between agents (Claude->Codex or Codex->Claude) gives up resume for everything before the switch, permanently. There is NO path where you use Claude, switch to Codex, switch back to Claude and resume the old Claude sessions. The earlier life's sessions are gone for resume purposes, even after switching back. Make sure the TLA+ model and spec match this (a switch back is a new life, not a return to the old one). If the current model already covers it, say so in your report; if not, add it to the next run.

## Resume rule (Gabe, 2026-10-12, settles the "Added question")
AD's resume always resumes the LATEST session of the current life, nothing older. Example: Claude A runs (resumable). Then Claude B launches on the same id without --resume. After B ends, resume picks up B, not A. Older sessions stay listed in session history but AD does not offer a way to resume them. Write it that way in the docs; don't add any way to pick an older session. If today's code already works like this, say so; if not, flag the gap in your report.

## Simplest statement (Gabe, 2026-10-12, this is the one to write)
Resume always continues the last agent's latest session. Switching agents without resume is fine. The one thing you can't do is resume across a switch: e.g. Codex runs and ends, then a Claude launch with --resume on the same id. Make that one case very clear in the docs. In your report, say what AD does today in that case (a clear refusal, or something worse). If it isn't a clear refusal with a plain error, propose the fix.

## The key error case (Gabe, 2026-10-12)
Claude runs and ends, then Codex runs and ends, then Claude is launched on the same id WITH --resume. AD must NOT pick up the original Claude session, because that agent would have a gap in its memory (it would know nothing of the Codex period). AD must refuse it with a plain error. Make this the named case in the docs, the TLA+ model (an invariant that this never resumes the old Claude session) and a test. In your report, say what today's code does in this exact case.

## Correction to "The key error case" (Gabe, 2026-10-12, replaces "refuse with a plain error")
It is NOT an error. Claude runs, then Codex, then Claude launched with --resume: AD starts Claude fresh and resumes nothing. The only hard rule is that the old Claude session is never resumed. Docs must say clearly that, in this case, --resume resumes nothing. Update the PRD, SRD, model and test to match: no refusal and no error, a fresh start, and the invariant that the old session is never resumed. If the model changes, re-run it (scheduler only) and re-run req-review on the changed text.
