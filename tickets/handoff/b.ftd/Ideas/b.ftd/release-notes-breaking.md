# Breaking release-note handoff — b.ftd (unreleased)

This is staged work for the next release, with no assigned release version.
Released 0.12.0 uses store schema v9; this work moves the store to v11 through
the distinct, explicitly authorized v9→v10 and v10→v11 hops. The released
v8→v9 hop is unchanged.

## Store upgrade and recovery

- Schema v10 adds `cursor_runtimes`, the seventh store table, for current Cursor
  launch runtime bindings. Fresh stores are created directly at v11.
- The v9→v10 hop validates any existing binding table, creates it when absent,
  stamps v10 and commits in a single transaction. Failure rolls back the hop.
  Existing spawns, permissions, session history, subagents and the store id
  are preserved; no existing-row backfill is required.
- Schema v11 adds durable main-turn proof eligibility independently of nullable
  child identity. The v10→v11 transaction adds/backfills/validates/stamps the
  column; compatible reentry preserves explicit false. Historical non-Cursor
  main/child semantics, including NULL/malformed stored agents, remain intact.
  Historical Cursor/unknown requests and orphans remain ineligible; known child
  requests cannot acquire main-turn proof. Failed hops preserve data/version
  and authorization for corrected retry. No Cursor authorization is admitted
  by this persistence change.
- A schema-v9 or schema-v10 binary cannot read a schema-v11 store. Before
  upgrading, stop agents, servers and callers and copy the actual database
  (`[store] db_path` when configured), including its `-wal` and `-shm` companions. The installer
  does not make this copy.
- To roll back, stop writers again, restore the matching pre-upgrade database
  and companions (remove a `-wal` or `-shm` the copy lacks), then install the
  matching previous release with that release's own installer and the original
  flags. All writes since the backup are lost. There is no supported in-place
  v11→v10 or v10→v9 downgrade; a binary swap or `--keep-prior` cannot reverse
  the schema.
  Without a pre-upgrade copy, stop and obtain human help.

See [Going back to the previous release](https://github.com/gabemahoney/agent-director/blob/main/docs/operator-actions.md#going-back-to-the-previous-release)
for current-layout recovery and the separately applicable historical
old-directory move procedure.

## Staged Cursor scope and acceptance

This work stages fresh owned Cursor launches on Linux. It is not a full
Cursor parity or release-completion claim. E1 owned-launch implementation at
f4f41fd3 was accepted with external Docker 15/15, TLC g6/launch/subagent groups and both authorized live cases.
E2 Task1 targeted behavioral checks are green, but installer synchronization,
full units and its external Docker/TLC and live acceptance gates remain pending.
No E2 completion or full permission/plan relay is claimed by fixture evidence.
Deferred lifecycle and platform capabilities must retain their staged status in published notes.

## Release author handoff

The release-notes generator reads commit subjects and bodies from git history;
this file is a tracked editorial handoff, not an automatically generated note.
Use the Plan Bee scope `b.jf1` in the commit subject and carry the schema
migration, backup/restore and staged acceptance facts in the commit body as
well. Idea Bee `b.ftd` identifies this artifact; Plan Bee `b.jf1` identifies the
implementation work and its release-notes commit scope. Do not
invent a release version or publish pending acceptance as complete.
