# Gabe's decisions on PRD t1.ssz.qi (2026-10-10). To be applied in the next PRD revision.

1. **No default agent.** After this change, every spawn must explicitly declare its agent (Claude or Codex). Claude Code is no longer the default. This replaces the goal "Claude Code stays the default, and existing callers see no change."
2. **Account and permission groups** work the same way as Claude today: several agents share one config folder (`CODEX_HOME`, like `CLAUDE_CONFIG_DIR` via `extra_env`) and so share one login and one set of permissions; other groups use another folder. This comes from the requirement, not a set solution, and research brief-4 is checking it.
3. Codex can't take a permission list at launch. Refusing one is fine: "We are not worried about adding features to Codex."
4. **Edges of the no-default rule (agreed):** a template that names the agent counts as declaring it; a spawn with no agent and no template is refused with a clear error; resume keeps the row's agent, and old rows count as Claude; CSCB and the bots must be updated before release.
5. **Overriding goal:** Codex behaves as close to current Claude under AD as possible. This must be the first goal, and every scope decision is judged against it.
6. **`ask_user`:** AskUserQuestion is also disabled for Claude, so leaving it out for Codex keeps parity. The PRD should say so.
7. **Asked why** (and to be revisited under goal 5):
   - why `allow`/`deny`/`ask` can't be enforced through AD's hooks;
   - why Codex `list_subagents` is out of scope;
   - why fields such as `claude_instance_id` aren't renamed to generic names now.
8. **The PRD reads like an SRD, so rewrite it.** Gabe: "this is looking very much like an SRD. I think you need to do a pass where you pose problems instead of solutions." The next revision states user problems and requirements only. Codex flags, file paths, mechanisms and fixes move to the SRD.
9. **Codex permission lists are settled by shared home folders.** Each group's permissions live in that group's Codex config. AD doesn't enforce allow, deny or ask for Codex.
10. **Codex subagents are in scope for b.ssz.** Claude subagents (b.xjn, subagent visibility) get finished first, so b.ssz depends on b.xjn and Codex `list_subagents` builds on it.
11. **This is a breaking release, and the PRD must say so plainly.** AD goes agent-generic: Claude-named fields, variables, flags and docs get generic names (e.g. `claude_instance_id`, `claude_session_id`, `claude_args`). No default agent (decision 1). Callers (CSCB and the bots) must migrate.
12. **Shared Codex home per group, confirmed by research** (`tickets/research/codex-state-tracking/report-4-codex-home.md`): one `CODEX_HOME` per group gives that group one login and one set of rules. Many agents can run at once on one home. Each row records its home, and resume uses it. AD never copies a login between homes and writes nothing into a shared home; folder pre-trust and hook trust go per launch. Kill and expire delete nothing in a home.
