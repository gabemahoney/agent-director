---
name: repair-orphaned-transcript
description: Manually re-attach a Claude conversation transcript that was orphaned under an old session id the agent-director store no longer points at. This is a rare, manual recovery for PRE-schema-v4 rotations only — rotations from schema v4 onward self-archive into session_history and need no repair. Use when `agent-director get` reports a session whose transcript is lost but you have located (or suspect) an on-disk .jsonl that belongs to it, and `resume` cannot find it automatically.
---

# Repairing an orphaned Claude transcript by hand

## When this applies (and when it does NOT)

agent-director tracks, per spawn row, a single `(claude_session_id, jsonl_path)`
pair pointing at the live Claude transcript. When a session **rotates** (Claude
hands the process a new session id — typically after a bot-fleet restart), the
store archives the prior pair into the `session_history` table **before**
overwriting the row. A later `resume` walks that archived history and reattaches
to the newest recoverable transcript on its own.

**That archiving exists only from schema v4 onward.** A rotation that happened
*before* `session_history` was added left no archive row, so its transcript can
be stranded on disk under a session id the store no longer references. That —
and only that — is what this skill repairs.

Before doing anything, confirm you are in the pre-v4-orphan case:

- Run `agent-director get --claude-instance-id <id>` and look at
  `transcript_status` and `prior_sessions[]`.
  - `rotated` with the transcript listed under `prior_sessions[]` → **STOP.**
    `resume` will recover this automatically. Do not hand-repair it.
  - `never_written` → the bot simply hasn't been messaged yet; there is no
    transcript to repair. Message it instead.
  - The transcript is genuinely orphaned (no `prior_sessions[]` entry points at
    it) and you have found an on-disk `.jsonl` you believe belongs to the row →
    this skill applies.

If in doubt, do not write to the database. A wrong repair rewrites a live row's
session id and loses the current conversation.

## Warnings — read before touching the database

- **The store is live and shared.** `~/.agent-director/state.db` is the single
  SQLite database every running bot reads and writes (there are ~7 bots in the
  fleet). You are editing a production database out from under live processes.
- **Back it up first.** With the fleet briefly quiesced if possible:
  ```sh
  cp ~/.agent-director/state.db ~/.agent-director/state.db.bak-$(date +%Y%m%d-%H%M%S)
  ```
- **The target row must be terminal first.** Only repair a row whose `state` is
  `ended` or `missing`. Rewriting the `claude_session_id` of a live row
  (`pending`, `waiting`, `working`, `ask_user`, `check_permission`) mid-session
  corrupts tracking and can strand the in-flight conversation. If the row is not
  terminal, run `agent-director find-missing` (which reaps dead sessions to
  `missing`) or wait for it to end — do not force it.
- **Do not migrate the schema by accident.** Open the DB with a plain `sqlite3`
  client (below). Do not run agent-director test binaries or `go run` against
  your real `$HOME` — see the `run-tests` skill for why (the b.8dr incident).

## Step 1 — find the row and its current pair

```sh
sqlite3 ~/.agent-director/state.db \
  "SELECT claude_instance_id, state, cwd, claude_session_id, jsonl_path
     FROM spawns WHERE claude_instance_id = '<id>';"
```

Note the `cwd` (you need it to locate transcripts), the current
`claude_session_id` and `jsonl_path` (the pair you will archive), and confirm
`state` is `ended` or `missing`.

## Step 2 — locate the candidate transcript on disk

Claude writes each session's transcript to:

```
<config-dir>/projects/<slug-of-cwd>/<claude_session_id>.jsonl
```

Two config dirs are in play, because orchestrators and workers run under
different `CLAUDE_CONFIG_DIR` values:

- `~/.claude/projects/` — the default config dir.
- `~/.claude-infhub/projects/` — the infhub worker config dir.

Search **both**. The `<slug-of-cwd>` folder name is the row's `cwd` with every
path separator and non-alphanumeric character replaced by `-` (e.g.
`/home/foo/my_repo` → `-home-foo-my-repo`). You do not need to compute the slug
by hand — list the project dirs and match by eye, or grep for the session id:

```sh
# List candidate project dirs under both config roots:
ls -d ~/.claude/projects/*/ ~/.claude-infhub/projects/*/ 2>/dev/null

# Or find any transcript whose basename looks like a session id:
find ~/.claude/projects ~/.claude-infhub/projects -name '*.jsonl' 2>/dev/null
```

Confirm a candidate actually belongs to the row before using it — open it and
check the conversation content matches what the instance was doing, and that the
`cwd`/first-message context lines up. The basename (without `.jsonl`) is the
**recovered session id** you will record; its absolute path is the
**recovered jsonl_path**. Both must be values you have verified on disk.

## Step 3 — archive the old pair and re-point the row, in ONE transaction

Archiving the row's current pair before overwriting it is what keeps the repair
non-destructive: if you ever mis-identify the transcript, the pointer you
replaced is still recoverable from `session_history`. Do the archive INSERT and
the row UPDATE in a **single transaction** so a bot's `SessionStart` landing
between them cannot overwrite the just-recorded pair without it having been
archived.

Substitute your verified values for `<id>`, `<recovered-session-id>`, and
`<absolute-recovered-jsonl-path>`:

```sql
-- run inside: sqlite3 ~/.agent-director/state.db
BEGIN IMMEDIATE;

-- 1. Archive the row's CURRENT (session id, jsonl_path) so the pointer being
--    replaced is never silently discarded. COALESCE keeps an already-known
--    path and refreshes recorded_at if this pair was archived before.
INSERT INTO session_history (claude_instance_id, claude_session_id, jsonl_path)
SELECT claude_instance_id, claude_session_id, jsonl_path
  FROM spawns
 WHERE claude_instance_id = '<id>'
   AND claude_session_id IS NOT NULL
   AND claude_session_id <> ''
   AND claude_session_id <> '<recovered-session-id>'
ON CONFLICT(claude_instance_id, claude_session_id) DO UPDATE SET
  jsonl_path  = COALESCE(excluded.jsonl_path, jsonl_path),
  recorded_at = CURRENT_TIMESTAMP;

-- 2. Re-point the row at the recovered transcript. Guarded on terminal state so
--    a row that went live again between Step 1 and now is left untouched.
UPDATE spawns
   SET claude_session_id = '<recovered-session-id>',
       jsonl_path        = '<absolute-recovered-jsonl-path>'
 WHERE claude_instance_id = '<id>'
   AND state IN ('ended', 'missing');

COMMIT;
```

If the `UPDATE` reports `0 rows changed`, the row was not terminal (or the id was
wrong) — the `COMMIT` still archived nothing harmful, but investigate before
retrying; do **not** drop the `state IN ('ended','missing')` guard to force it.

## Step 4 — verify, then resume

```sh
sqlite3 ~/.agent-director/state.db \
  "SELECT claude_session_id, jsonl_path FROM spawns WHERE claude_instance_id='<id>';
   SELECT claude_session_id, jsonl_path, recorded_at FROM session_history WHERE claude_instance_id='<id>';"
```

Confirm the row now points at the recovered transcript and the old pair is in
`session_history`. Then relaunch normally:

```sh
agent-director resume --claude-instance-id <id>
```

`resume` will `os.Stat` the recorded `jsonl_path` and launch `claude --resume`
against it.

## Reminder

This is manual recovery for **pre-`session_history` (pre-schema-v4) orphans
only**. Every rotation from schema v4 onward is archived automatically, so the
orphaning this repairs cannot recur; a `rotated` transcript on a current store
is recovered by a plain `resume` with no database surgery.
