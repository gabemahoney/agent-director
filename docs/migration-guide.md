# Writing a SQLite Schema Migration

How to add the next schema version to the agent-director store safely.

This guide is for engineers (and Worker Claudes) changing the DDL in
`internal/store/`. The store is the **only** package permitted to speak SQL
(`internal/store/store.go` package doc, SRD §4.5); every schema change lands
here. Read this end-to-end before bumping the version — the rules below are
not optional, and one of them (the b.8dr rule, §5) exists because it was
learned the hard way against the production database.

## 1. Architecture as it exists today

All schema logic lives in `internal/store/schema.go`, with the version
constant and typed errors in `internal/store/store.go`.

**The version contract.** `schemaVersion` (`store.go`, currently `7`) is the
version this binary writes and reads. Every opened DB carries its own version
in SQLite's `PRAGMA user_version` (0 on a brand-new file). `ensureSchema(db,
dbPath)` (`schema.go`) is called from `openDB` on every `Open`/`OpenOrInit`
(passing the *resolved* DB path) and dispatches on that pragma:

| `user_version` on disk                 | `ensureSchema` does                                                 |
| -------------------------------------- | ------------------------------------------------------------------- |
| `== schemaVersion`                     | nothing — no-op, already current                                    |
| `0`                                    | `createSchema(db)` — full latest-schema DDL + store id + stamp      |
| `0 < v < schemaVersion` (older-than-binary) | **gated** — refuse with `ErrSchemaMigrationRequired` unless an administrator authorization sentinel exact-matches; when authorized, run the chained migration steps (see §1a) |
| `> schemaVersion`                      | wrap `ErrSchemaMismatch` (typed error), run **no** DDL              |

**The store never auto-migrates on open.** An older-than-binary DB is *not*
silently upgraded — that was the b.8dr incident class (§5). The migration only
runs when an administrator has placed a valid, exact-match authorization
sentinel next to the DB file; otherwise the open is refused with the typed
`ErrSchemaMigrationRequired` (`store.go`) and the DB file is left byte-identical
(zero DB writes). See §1a for the sentinel gate and §1b for the refusal.

The newer-than-binary arm is the guard rail: a DB from a *newer* binary (say
`user_version=8` opened by a v7 binary) returns `ErrSchemaMismatch` (`store.go`)
rather than touching the file. Callers detect it with
`errors.Is(err, ErrSchemaMismatch)`.

**The store id.** Schema v5 and later have a `store_meta` table (`key TEXT PRIMARY KEY,
value TEXT NOT NULL`) whose one row, `store_id`, identifies the store: 64
random bits from `crypto/rand`, written as 16 lowercase hex characters
(`storeid.go`). It is written only when the store gets its v5 schema: by
`createSchema` for a fresh store, or by `migrateV4toV5` for a migrated one. The
insert is guarded (`insertStoreIDOnceSQL`, `INSERT … SELECT … WHERE NOT
EXISTS`), so it never replaces an id that is already there. No other statement
writes `store_meta`, no verb changes the id, and later hops (`migrateV5toV6`,
`migrateV6toV7`) keep it. After `ensureSchema` succeeds,
`openDB` reads the id once (`readStoreID`), and `(*Store).StoreID()` returns
it. A current-version store with no `store_meta` table, no `store_id` row, or a
value that is not 16 lowercase hex characters fails the open with an error that
wraps `ErrSchemaMismatch`. The error never contains the value, the DB is
closed, and nothing is written. Only a hand edit leaves a store in that state;
the v5 → v4 recipe (§5) stamps v4, which a v5, v6 or v7 binary refuses with
`ErrSchemaMigrationRequired`.

**A registry of steps, not a switch.** Version transitions are no longer
`case` arms. Each single-version upgrade is a `migrationStep{from: N, apply:
migrateV<N>toV<N+1>}` entry in the ordered `migrationSteps` registry
(`schema.go`); today that registry holds six entries, `{from: 1, apply:
migrateV1toV2}`, `{from: 2, apply: migrateV2toV3}`, `{from: 3, apply:
migrateV3toV4}`, `{from: 4, apply: migrateV4toV5}`, `{from: 5, apply:
migrateV5toV6}`, and `{from: 6, apply: migrateV6toV7}`. `migrateV1toV2`,
`migrateV2toV3`, `migrateV3toV4`, `migrateV4toV5`, `migrateV5toV6`, and
`migrateV6toV7` (`schema.go`) are the reference implementations of an `apply`
func; `migrateV6toV7` is the most recent. To add the next version (v8) you
write `migrateV7toV8` and append `{from: 7, apply: migrateV7toV8}` to
`migrationSteps` — see §1a's "Adding a step" for the full checklist.

**One transaction per step, stamp included.** Every `apply` func opens a single
`db.Begin()` transaction and does *all* of its DDL/DML **and** the
`user_version` stamp inside it. Any error rolls the whole thing back with
`tx.Rollback()`, leaving `user_version` at the previous value. This
per-step atomicity is what makes the chain safe: a crash between steps leaves
the DB stamped at the last successfully-committed version, never at an
intermediate un-stamped state, so a re-open resumes the chain cleanly from
there. Look at the shape of `migrateV1toV2`: begin → DDL → stamp → commit, with
a rollback on every error path. `createSchema` follows the same
begin/exec/stamp/commit pattern (`schemaDDL`, then the guarded store-id
insert, then the stamp) so a crash mid-creation leaves `user_version` at 0 and
the next open retries. A retry, or any run on a DB whose tables already exist
with `user_version` stamped back to 0, keeps the existing tables and store id:
the DDL is `IF NOT EXISTS` and the insert is guarded.

## 1a. The chained step engine and the sentinel gate

**The chain.** Once an older-than-binary open is authorized (below),
`runMigrationChain(db, from)` (`schema.go`) loops while `version <
schemaVersion`, looking up the registered step for the current version
(`migrationStepFrom`), applying it, and incrementing. It upgrades a DB across
*multiple* versions in a single open — a v1 DB opened by the current v7 binary
runs v1→v2→v3→v4→v5→v6→v7 in one pass, each step its own transaction. There is no
return-after-one-hop: the loop keeps going until the DB reaches `schemaVersion`.
If the loop finds itself below `schemaVersion` with no registered step for the
current version, that is a gap in the registry — it fails loudly with
`ErrSchemaMismatch` rather than silently leaving the DB behind.

This section is the canonical home for the sentinel/gate **semantics**
(the store-side contract). The install-time procedure that *writes* the
sentinel — the six-step flow install.sh runs to authorize an upgrade on
an end-user machine — is documented in
`skills/install-agent-director/SKILL.md` ("Schema migration: the
six-step sentinel flow"); consult it for how an operator triggers a
migration.

**The authorization sentinel.** `authorizeMigration(dbPath, current)`
(`migrate_auth.go`) gates the chain. It looks for a sentinel file named
`migrate-authorized` sibling to the *resolved* DB path — i.e. in the same
directory as the DB file, so it authorizes the right store even under a custom
store path (never hard-coded to `~/.agent-director`). The sentinel is a single
JSON object naming exactly one transition:

```json
{"from": 1, "to": 2}
```

It is parsed **strictly**: `DisallowUnknownFields` plus a trailing-token check,
so an unknown key or trailing garbage is a parse failure, never a silently
widened authorization. The gate authorizes the migration **only** when the
sentinel is present, valid JSON, and **exact-matches both ends**: `from` equals
the DB's *actual* `user_version` and `to` equals this binary's `schemaVersion`.
Anything wider or off is refused.

**Consumed on success, preserved on mismatch.** On a successful migration the
sentinel is consumed (deleted) *after* the chain commits, by
`consumeAuthorization` (`migrate_auth.go`) — it is a one-shot authorization, not
a standing permission. Consumption is deferred until after commit so a
mid-migration failure leaves the sentinel in place for a retry. Every other
outcome leaves the sentinel **in place as admin evidence**:

- **Missing** sentinel → plain `ErrSchemaMigrationRequired` refusal, no
  expected-vs-found trail line.
- **Malformed JSON / unreadable** file → refusal **plus** a distinct
  expected-vs-found trail line (`ad.schema.authorization_mismatch`); sentinel
  preserved.
- **Version mismatch** on either end → refusal **plus** the same
  expected-vs-found trail line reporting both ends; sentinel preserved, never
  partially honored.

A stale sentinel left behind is provably inert: after a successful migration the
next open short-circuits on the `== schemaVersion` no-op arm, and a future
version bump makes `from` mismatch the DB's now-current version → a refusal that
just preserves it as evidence. It never triggers an unwanted migration.

**Audit trail.** Migration and refusal events are emitted through the store's
existing fail-open `trail.Emit` discipline (`_ =`, never blocking the open,
never touching `state.db`) — there is no logger plumbed into the store:

- Success → `ad.schema.migrated`, message `migrated <from>→<to>, consumed
  authorization`.
- Malformed/mismatched/unreadable sentinel → `ad.schema.authorization_mismatch`
  with expected-vs-found for both ends (`found_*` of `-1` means "unknown", i.e.
  a malformed or unreadable sentinel).
- Sentinel delete failure *after* a committed migration →
  `ad.schema.authorization_delete_failed`, a loud event — but the open still
  **succeeds** (the migration is already done and correct; the stale sentinel is
  inert per above).

**Adding a step.** To add the next version (v8, generalizing to any vN→vN+1);
`migrateV6toV7` (b.146 step 2) is the most recent worked example:

1. Bump `schemaVersion` to `8` (`store.go`).
2. Evolve `schemaDDL` so a fresh DB is created directly at v8 (the two-places
   rule — §1, §4). `schemaDDL` already includes `store_meta`, and
   `createSchema` inserts a fresh store's `store_id` in the same transaction;
   keep both. A v7 → v8 hop must keep `store_meta` and its row: the id is
   created once and never changed (§1).
3. Write `migrateV7toV8(db)` following the one-transaction/validate-first
   pattern (§2).
4. Append `{from: 7, apply: migrateV7toV8}` to `migrationSteps` (`schema.go`).
5. Add the reverse recipe to §5's emergency downgrade recipes, newest first,
   with a test copy of its statements that must match it (add the heading
   and the copy to `TestDowngradeRecipe_MatchesGuide`'s table), and point
   the next-older recipe at it ("on a v8 store, run the v8 → v7 recipe
   first").

That is all — do **not** add any return-after-one-hop logic. The chain engine
walks the registry from the DB's version up to `schemaVersion` automatically, so
appending the step is what makes both a v7→v8 upgrade and a straight-through
v1→v8 upgrade work.

**Later columns of the same release.** A change that ships in the same
release as a version not yet released adds its columns to that version's hop
and `schemaDDL`, rather than a new version: schema v7 is the one migration of
b.146 steps 2, 2b and 2c, which ship together, and a later step of that
release appends its columns to `v7Columns` (`schema.go`) and the same text to
`schemaDDL`, and extends the v7 → v6 recipe (§5) and its test copy. Once a
version has shipped, its hop never changes.

## 1b. Refusal semantics — `ErrSchemaMigrationRequired`

When an older-than-binary open is not authorized, `ensureSchema` returns
`ErrSchemaMigrationRequired` (`store.go`, aliased in `pkg/api/aliases.go` per
the `ErrSchemaMismatch` precedent) and executes **zero DB writes** — the DB file
is byte-identical to before the open. Callers detect it with
`errors.Is(err, ErrSchemaMigrationRequired)`.

The user-visible message is a deliberate dead end: it names the current and
required schema versions and routes the operator to their administrator, but
names **no** command, flag, file path, or environment variable — in particular
it never mentions the sentinel or its filename. Self-service breadcrumbs are
prohibited on agent-facing runtime surfaces (SR-1.4/1.6). (This internal doc
*may* name the sentinel path because it is not an agent-facing surface — the
name is in source anyway; the prohibition applies to help text, MCP tool
descriptions, the npm README/manifest, and the error message itself.)

**The `PRAGMA user_version` quirk.** SQLite will **not** accept a bound
parameter on `PRAGMA user_version` — `PRAGMA user_version = ?` is a syntax
error. The version must be string-interpolated:

```go
tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
```

See `createSchema` in `schema.go`. This is safe *only* because the value comes
from the trusted package constant `schemaVersion`, never from user input. Never
`fmt.Sprintf` anything caller-supplied into SQL. (The test helper
`stampUserVersion` in `migration_fixtures_test.go` interpolates the same way,
for the same reason.)

**The two-places rule.** A fresh database **never replays the migration
hops** — `createSchema` runs the canonical `schemaDDL` constant (`schema.go`),
which must *always* describe the latest schema directly. So every schema change
lands in **two** places:

1. The new migration hop (e.g. `migrateV4toV5`) — upgrades an existing older DB.
2. The fresh-DB DDL (`schemaDDL` + `schemaVersion`) — produces the latest
   schema for a new DB in one shot.

Miss either and the two paths diverge: fresh installs get one schema, upgraded
installs get another. §4's identical-schema assertion is what catches this.

## 2. The validate-first phased pattern

Structure the body of a hop in four ordered phases inside the one transaction.
This ordering means a precondition failure aborts *before* any mutation, so the
DB stays cleanly at the old version.

**Phase 1 — validate (read-only).** Query for preconditions before mutating
anything: does the column already exist, are there conflicting/duplicate rows
that the new constraint would reject, is required data present? If a
precondition fails, `tx.Rollback()` and return a typed error. Because you have
not executed any DDL or stamped the version yet, `user_version` is untouched and
the next open can retry after the operator fixes the data.

**Phase 2 — DDL.** `ALTER TABLE … ADD COLUMN`, `CREATE INDEX`, `CREATE TABLE`,
`DROP TABLE`, etc. (`migrateV1toV2` does a `DROP TABLE` + `CREATE TABLE` here —
a full rebuild is a legitimate hop when rows are not being preserved.
`migrateV2toV3`, by contrast, does five `ALTER TABLE spawns ADD COLUMN` —
`pid`, `proc_starttime`, `liveness_unverified_since`, `liveness_note` (all
nullable) and `extra_env TEXT NOT NULL DEFAULT '{}'` — an additive hop that
preserves every existing row. `migrateV3toV4` (b.v2c) does a single `CREATE
TABLE IF NOT EXISTS session_history` plus its index — a new-table hop that
touches no existing row. `migrateV4toV5` (b.fmk) is an additive `ADD COLUMN`
hop across two tables: twelve `ALTER TABLE spawns ADD COLUMN` (`row_version`,
`launch_started_at`, `life_number`, `no_pre_trust`, `launch_token`,
`tmux_socket`, `tmux_server_pid`, `tmux_server_started`,
`tmux_server_starttime`, `pane_id`, `pane_pid`, `pane_starttime`) and one
`ALTER TABLE session_history ADD COLUMN life_number`. Each `ADD COLUMN` is
guarded by its own table's `pragma_table_info` probe and skipped when the
column is already there, so the hop is safe to re-enter. After the thirteen
columns it also does a new-table step: `CREATE TABLE IF NOT EXISTS
store_meta`, with `schemaDDL`'s exact text (the two-places rule), then the
guarded insert of one new random `store_id`, which writes only when no
`store_id` row exists. A second run, or a run on a store whose `store_meta`
already holds an id, keeps that id. Any failure in the columns, the table or
the insert rolls the whole hop back: `user_version` stays 4, neither table has
any new column, and there is no `store_id`. `migrateV5toV6` (b.kdf) is an
additive `ADD COLUMN` hop on one table: three `ALTER TABLE spawns ADD COLUMN`
for the launch owner, the only columns v6 adds, in this order, `launch_owner_pid` (INTEGER),
`launch_owner_starttime` (TEXT) and `launch_owner_pidns` (TEXT), all
nullable and appended after `pane_starttime` with `schemaDDL`'s exact text.
Each is guarded by a `pragma_table_info('spawns')` probe and skipped when the
column is already there. Any probe or `ALTER` failure rolls the whole hop
back: `user_version` stays 5 and none of the three columns exists.
`migrateV6toV7` (b.146 step 2) is an additive `ADD COLUMN` hop across two
tables, driven by one list, `v7Columns` (`schema.go`), that holds each
column's table, name and exact `schemaDDL` text: seventeen `ALTER TABLE
permission_requests ADD COLUMN` (the relay hook's `hook_pid`,
`hook_starttime` and `hook_pidns`; `tool_use_id`, `agent_id`,
`delivered_at`, `settled_at`, `hook_gone_at`, `attempted_decision`,
`attempted_at`; `pane_answer TEXT NOT NULL DEFAULT 'none'`, `pane_as`, the
pane-answer sender's `pane_sender_pid`, `pane_sender_starttime` and
`pane_sender_pidns`, and `pane_intent_at` INTEGER, when a pane answer's
intent was written; then `closed_at` INTEGER, when a close of the
request's spawn closed it (find-missing's mark, the terminal SessionEnd's
move to `ended`, or resume's move to `pending`); the instants in
milliseconds since the epoch), appended after `created_at`, then `ALTER
TABLE spawns ADD COLUMN idle_since`, appended after `launch_owner_pidns`.
Each is guarded by a `pragma_table_info` probe of its own table and skipped
when the column is already there. Any probe or `ALTER` failure rolls the
whole hop back: `user_version` stays 6 and none of the eighteen columns
exists.)

**Phase 3 — data backfill/transform.** `UPDATE`/`INSERT … SELECT` to populate
new columns or reshape rows, if the migration keeps data. Not every hop needs
one: `migrateV1toV2` deliberately does not preserve v1 `permission_requests`
rows (SR-2.6 single-version invariant / SR-2.3 no-backfill), and
`migrateV2toV3` needs no phase 3 either — `ADD COLUMN` populates existing rows
from the column defaults (NULL for the four nullable columns, `'{}'` for
`extra_env`), so there is nothing to backfill. `migrateV3toV4` likewise skips
phase 3: `session_history` starts empty and is populated lazily on the next
session rotation, so pre-v4 rows carry no archived history and there is nothing
to backfill. `migrateV4toV5` has no phase 3 either: its `ADD COLUMN`s give every
existing row and history entry the column's ordinary default, the value a new
row or entry gets — row version 0, life 0 (on `spawns` and on every
`session_history` entry), `no_pre_trust` 0 (pre-trust allowed), no launch start
(`launch_started_at` NULL, a `pending` row included), and NULL launch token,
socket and server/pane identity. There are no special cases: no row or entry is
treated differently for its state, its history or its origin, and no existing
value is rewritten. The `store_id` insert is not a backfill: it adds the one
row of a new table and touches no existing row. The stamp is still the last
statement of the hop. `migrateV5toV6` has no phase 3 either: its `ADD COLUMN`s
give every existing row NULL in all three launch-owner columns, which records
no launch owner. A `pending` row is no special case: with no owner,
`find-missing` judges it by its pending grace period alone, as it did before
the upgrade. No existing value is rewritten: a `pending` row keeps its
`launch_started_at` exactly as stored, and `store_meta` and its store id are
kept. `migrateV6toV7` has no phase 3 either: its `ADD COLUMN`s give every
existing permission request NULL in each new column but `pane_answer`, which
takes its default `none`, and every row NULL `idle_since`. A request with no
`settled_at` is one recorded before v7, so readers and `decide` judge it as
before the upgrade, by its `created_at` and the relay window. A NULL
`closed_at` means no close closed the request: an existing request that a
pre-v7 mark denied keeps its `find_missing` deny, which already makes it no
longer await an answer, so nothing needs backfilling. An existing request
still undecided on a row that is already `ended` is not backfilled either:
resume's move to `pending` closes it (denied, `decision_reason` `ended`)
before the row's next launch. No existing value is rewritten, and
`store_meta` and its store id are kept.

**What the upgrade changes for an existing request.** An undecided request
recorded before v7 is judged by time, and from its relay window plus 2 s on
it reads `fallen_back`. From v7 on, `send-keys` refuses a plain call (no
`request_token`) with `ErrRelayFallenBack` while any request of the agent
is fallen back with no pane answer recorded, in every live state of the
row. So a request left open before the upgrade, typically one answered at
the pane after its relay hook was killed, refuses plain `send-keys` to its
agent once the store is migrated, until it is closed: with
`record-pane-answer --request-token <T> --as unknown --expect-pane-sha256
<H>` (H the `pane_sha256` of a `read-pane` a person or an LLM looked at),
with a pane answer, or with its row (the agent ends, or `find-missing`
marks it `missing`). The refusal's `err_details` name the request
(`request_token`) and the agent's other open requests. `record-pane-answer`
is the way out: `decide` records no verdict on such a request, and once
the agent has left `check_permission` or recorded a later request it
returns `ErrNoOpenPermissionRequest` for it ("do not answer it at the
pane"), while plain `send-keys` is still refused with `ErrRelayFallenBack`
on its account. Its PostToolUse does not close it either: no `tool_use_id`
was recorded for it.

Session history belongs to a life: each `session_history` entry carries the
life of the id that was current when its session ran, and after the v5 hop
every existing entry is in its row's current life (life 0). Only a reuse
(`spawn` with the reuse opt-in on a finished id) starts a new life: its reset
advances the row's `life_number` by one, and if its launch fails and its
restore applies, the restore sets it back. No other write changes it. So the
first reuse of a row after the migration starts life 1, and a row that is
never reused keeps all its history in life 0. `resume`
and `get` read only the visible history — the entries of the row's current
life, minus the entry for the row's current session id. The migration
therefore changes nothing those verbs read for an existing row. The one
visible difference for existing rows comes from that current-session rule,
which this release applies to every row, not from the migration: a row whose
history holds its current session id no longer lists that entry in `get`'s
`prior_sessions`, and `resume` no longer tries a path recorded only on that
entry (the current session's own persisted path and recomputed fallback are
still tried).

**Phase 4 — stamp `user_version`.** The **last** statement in the transaction,
via the `fmt.Sprintf` form from §1. Stamping last guarantees the version only
advances once every preceding statement succeeded.

### SQLite idempotency patterns

Even though the transaction gives you atomicity, write each statement to be
safe to run twice. Idempotent statements make a hop retry-safe and let the
double-run test in §3 pass. The SQLite-specific idioms:

- **ADD COLUMN** — SQLite has no `ADD COLUMN IF NOT EXISTS`. Guard it by
  probing `pragma_table_info` first:
  ```sql
  SELECT 1 FROM pragma_table_info('permission_requests') WHERE name = 'new_col'
  ```
  and only `ALTER TABLE … ADD COLUMN` when the row is absent.
- **CREATE INDEX IF NOT EXISTS** — always use the `IF NOT EXISTS` form. Note
  the fresh-DB `schemaDDL` already uses `CREATE INDEX IF NOT EXISTS`
  throughout; a migration hop creating a *brand-new* table's indexes (like
  `migrateV1toV2`) can use the bare form because it just dropped/created the
  table in the same tx, but prefer `IF NOT EXISTS` when adding an index to a
  pre-existing table.
- **DROP … IF EXISTS** — `DROP TABLE IF EXISTS` / `DROP INDEX IF EXISTS` so a
  re-run does not error on an already-removed object.

## 3. Test by running the upgrade twice

A migration is not done until a test proves the upgrade is correct **and
idempotent**. The version-independent tests in `internal/store/schema_test.go`
loop over every released version (`for from := 1; from < schemaVersion;
from++`), so registering your step and bumping `schemaVersion` adds your
version to them. Each older version is built by `makeVersionedDB`
(`migration_fixtures_test.go`): `testdata/schema_v1.sql`, then the real
registered steps up to that version, in WAL mode so a refused open touches no
bytes. What they cover, step by step:

1. **A fixture DB at each older version.** `makeVersionedDB(t, dir, v)`. Do
   not commit a `testdata/schema_v<N>.sql`; `schema_v1.sql` is the only one.
2. **Authorize the upgrade, then open once.**
   `TestAuthorizedMigrationFromEveryVersion` seeds a row in each older
   version, writes an exact-match sentinel (`writeSentinel(t, dir, from,
   schemaVersion)`) and opens with `Open`. It asserts `user_version ==
   schemaVersion`, the pre-existing row read with v3's, v5's, v6's and v7's
   `spawns` columns at their defaults (the `GetSpawn` field check, which
   covers v7's `idle_since`, `assertV5Defaults` and `assertV6Defaults`; a new
   version's columns are added by hand, below), one
   store id, the sentinel
   consumed (`assertSentinel`) and one `ad.schema.migrated` line.
   `TestMigrationRefusedWithoutSentinel` opens each older version with no
   sentinel and asserts `ErrSchemaMigrationRequired` with the SR-1.4 text, the
   DB byte-identical (`snapshotDBBytes`/`assertDBBytesUnchanged`) and still at
   its version. The malformed and mismatched sentinels
   (`TestGateRefusesBadSentinel`) and the newer-than-binary arm
   (`TestNewerThanBinaryIsSchemaMismatch`) do not depend on the version.
3. **Open again.** `TestAuthorizedMigrationFromEveryVersion` reopens the
   migrated store, with no sentinel and then with a stale one: no error, the
   same store id, no trail line, and the stale sentinel left in place.
   `TestMigrationStepReentry` re-applies one step directly, twice in a row
   and over a DB the step already changed in part, and asserts the schema one
   run gives.
4. **Assert the fresh path and the migrated path converge.**
   `TestAuthorizedMigrationFromEveryVersion` compares the migrated store's
   `schemaShape` (every table's `PRAGMA table_info` and every index's
   columns) with a fresh `OpenOrInit` store's. This is the automated
   enforcement of the two-places rule (§1): if `schemaDDL` and your hop
   drifted, the shapes differ and the test fails.

**What a new version adds by hand:**

- **Re-entry rows** in `TestMigrationStepReentry`: `"vN→vN+1 run twice"`,
  and one row per partial state the step can be re-run over, with a helper in
  `migration_fixtures_test.go` that makes that part of the change first (as
  `preAddV5Columns` and `preAddStoreMeta` do for v4→v5, `preAddV6Columns`
  for v5→v6 and `preAddV7Columns` for v6→v7).
- **A rollback row** in `TestMigrationRollback`: an arrangement that makes
  the hop fail part-way (as `breakV5SessionHistoryHop`,
  `breakV5StoreMetaStep`, `breakV6PIDNSColumn` and `breakV7IdleSinceColumn`
  do), and the text the open's error must name. The test asserts a migration
  failure, not a refusal, and the version, schema, rows (`dbDump`) and
  sentinel kept. Only `from == 4`, `from == 5` and `from == 6` start with
  rows (`makeV4HistoryFixture`, `makeV5Fixture`, `makeV6Fixture`); every
  other version's `makeVersionedDB` store is empty. Seed rows for your hop's
  from-version (extend the fixture choice at the top of the subtest, as
  `tc.from == 4`, `5` and `6` do) so the `dbDump` comparison covers data, not
  only the schema.
- **Defaults and fresh shape**: a `v<N>ColumnSpecs` list of the columns your
  hop adds, and its `assertV<N>Defaults`, in `migration_fixtures_test.go`
  (as `v5ColumnSpecs` and `assertV5Defaults` are for v5, and
  `v6ColumnSpecs` and `assertV6Defaults` for v6; v7's `v7ColumnSpecs` is
  there and its `assertV7Defaults`, which also checks the new request
  columns, is in `schema_v7_test.go`). Call `assertV<N>Defaults` from
  `TestAuthorizedMigrationFromEveryVersion`'s pre-existing-row check, beside
  `assertV5Defaults` and `assertV6Defaults`, unless, as for v7, its `spawns`
  columns are checked there through `GetSpawn` and its other columns by the
  data test. In `TestFreshStoreSchema`, add the specs to its
  `assertColumnSpecs` call (today `v3ColumnSpecs`, `v5ColumnSpecs`,
  `v6ColumnSpecs` and `v7ColumnSpecs`), check where the new columns sit in
  each table (v7's: last on `spawns`, after `created_at` on
  `permission_requests`), and add any new table or index to its list of
  names. Neither test picks up a new version's columns on its own.
- **Data cases** in `schema_v<N>_test.go`, as `schema_v5_test.go`,
  `schema_v6_test.go` and `schema_v7_test.go` do: the values a seeded older
  store's rows come out with after the hop, and anything else the version
  adds (v5's store id, and each version's downgrade recipe).

v5's data test, `TestV5MigrationKeepsV4Rows`, migrates `makeV4HistoryFixture`
(`internal/store/migration_fixtures_test.go`). It builds a genuine v4 store on
the real migration chain (`makeVersionedDB`) and seeds it with session history
— a rotation entry, an `ended` row whose current session was never written, a
row whose history already holds its current session id, and two ids whose
history entries interleave in time — plus a `pending` row. It returns a
`v4HistoryFixture` holding the seeded ids and the pre-migration row and
history values. With that data in place, "every row and entry comes out at
life 0" and "the `pending` row has no launch start" are real assertions, not
vacuous ones. Seed your own fixture the same way: the inline SQL that builds
it lives in `migration_fixtures_test.go`, never in the test file itself.

v6's data test, `TestV6MigrationKeepsV5Rows`, migrates `makeV5Fixture`: a
genuine v5 store holding a `pending` row in the middle of a launch (launch
start, token, socket and pane identity, no session), a `waiting` row with
every identity and a liveness note, and an `ended` row. After the hop every
v5 value of each row reads back exactly as seeded (the `pending` row's
`launch_started_at` is checked by name), each row records no launch
owner (NULL in the three columns, read as the zero `LaunchOwner` by
`GetSpawn` and `ListLiveSpawnIdentities`), and the store id is kept. How
`find-missing` judges such a migrated row is
`v5_migrated_find_missing_test.go`'s.

v7's data test, `TestV7MigrationKeepsV6Rows`, migrates `makeV6Fixture`: a
genuine v6 store holding a relay row in `check_permission` with an open
request (its `created_at` long past any relay window) and a decided one.
After the hop every v6 value of the row and both requests reads back as
seeded, the new columns hold their defaults (`assertV7Defaults`), and the
store id is kept. Both requests read as recorded before v7 (no settle
instant, hook identity, ack or `tool_use_id`; `pane_answer` `none`), only the
undecided one still awaits an answer, and `decide`'s guarded write still
refuses it once its `created_at` is past the cutoff: a request from before
the upgrade is judged by time, as before.

**Where these tests run:** `internal/store` carries the sandbox guard
(`sandboxguard.Require()` in its `TestMain`). Run them only in the sandbox —
see §5.

## 4. `createSchema` must always be the latest schema

Restating the two-places rule because it is the most common way a migration
goes wrong: **fresh databases never replay hops.** When you add v8, update
`schemaDDL` and `schemaVersion` so a brand-new DB is created directly at v8 by
`createSchema`, *and* write `migrateV7toV8` so an existing v7 DB is upgraded to
the identical shape (as v7 did with `schemaDDL` and `migrateV6toV7`, whose
`v7Columns` list carries the same column text `schemaDDL` uses). The §3 step 4 `schemaShape` comparison is the
guard that both paths land in the same place. If you only touch the hop, fresh
installs are stuck on the old schema; if you only touch `schemaDDL`, upgrades
never happen.

## 5. The b.8dr rule — schema-bumping branches run ONLY in the sandbox

`ensureSchema` no longer auto-migrates on open — the sentinel gate (§1a) refuses
an older DB with `ErrSchemaMigrationRequired` unless an administrator
authorization is present. **That gate does not make the sandbox rule optional.**
Two open-time DB writes remain, and both are reachable from a test run:

- `createSchema` writes the full latest schema to any `user_version==0` file on
  the very first open — a schema-bumping branch stamps fresh DBs at the *new*
  version.
- The migration chain **does** fire when a valid sentinel is present, and
  migration tests write their own `migrate-authorized` sentinel next to their
  fixture DB to exercise the authorized path. A test that resolves the DB path
  to the real store, and drops a matching sentinel, will migrate the real store.

So the instant a binary or test built from a schema-bumping branch opens a store
under the right conditions, it can rewrite that store to the new version — there
is no dry-run and no confirmation once authorization is in play.

That is exactly the b.8dr incident. Running `bun test` on the b.aaj branch
(schema v3 migration code) on the host migrated the real
`~/.agent-director/state.db` from v2 to v3, breaking every consumer of the
installed v0.7.8 binary (which only understands v2 and now hits
`ErrSchemaMismatch`). Host `$HOME` redirection does **not** save you: a test
that does not redirect it, an absolute path to the real store, or a child
binary started with the host's environment still opens the real
`~/.agent-director/state.db`. A container whose HOME has no `.agent-director`
is the only isolation boundary that holds.

**The rule:** any branch that bumps `schemaVersion` or changes migration code
must **only ever be executed in the sandbox** — never on the host. Editing on
the host is always fine (nothing runs); *executing* is the danger, and every
executed artifact can open the store.

```
make test-sandbox        # full suite in the container
make sandbox CMD="…"      # any one-off (build, go run, a binary, a bun script)
```

Host `go test`, `bun test`, `go run`, and direct `bin/` executions are denied
by `.claude/settings.json`, and `sandboxguard.Require()` in the store's
`TestMain` fails fast if the sandbox marker is absent — but those are
accident-prevention gates, not a licence to reach around them. In particular,
`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` (the guard's CI escape hatch) is for
ephemeral GitHub-hosted runners with no real store; setting it on a machine that
has one — and a schema-bumping branch is exactly when that matters — recreates
b.8dr. See
docs/engineering-guide.md §10 "Sandboxed execution" for the full rationale and
the b.nh2 sandbox harness, and the **run-tests** skill for usage.

### Emergency downgrade recipe

If a newer schema has already been written to a store that an older binary must
read (the b.8dr recovery situation), reverse the last hop in place with a raw
SQLite session against the affected file, then reset the version pragma. For a
hop whose only forward change was an added column:

```sql
-- against the over-migrated state.db, e.g. ~/.agent-director/state.db
ALTER TABLE <table> DROP COLUMN <added_col>;   -- reverse the v(N-1)→vN DDL
PRAGMA user_version = <N-1>;      -- stamp back to the version the old binary reads
```

Adapt the DDL to whatever the hop actually did (dropped/renamed tables must be
recreated, not just re-versioned). Confirm with `PRAGMA user_version;` and a
schema inspection before letting the old binary open the file. This is a manual
recovery step, not something the store does automatically.

The recipes below go back one version each, newest first. To go back more
than one version, run them in order from the store's version down, each one
only once the one before it has stamped the store: a v7 store goes to v5 by
the v7 → v6 recipe, then the v6 → v5 recipe, and to v4 by those two, then the
v5 → v4 recipe. Each recipe assumes the store is at its own starting version
and changes nothing a newer version added, so never skip one.

#### v7 → v6 (reverses `migrateV6toV7`)

Use this to roll a migrated store back so a v6 binary (the releases before
schema v7) can open it. Order of operations:

1. Stop every agent and every long-running agent-director process on the host
   (each agent's `agent-director serve` runs inside its Claude session, so the
   session must stop too). No process may hold the store open.
2. Copy `state.db` together with its `-wal` and `-shm` files before you change
   anything, so you can start over if a step fails.
3. Run `sqlite3 --version`. The recipe needs SQLite 3.35.5 or later: 3.35.0
   added `ALTER TABLE … DROP COLUMN`, and 3.35.5 fixed `DROP COLUMN` defects
   that could corrupt the database file. If the version is older, stop here
   and install a newer `sqlite3` first.
4. Run the recipe below against the store, in bail mode:
   `sqlite3 -bail ~/.agent-director/state.db < recipe.sql`. The recipe also
   starts with `.bail on`, so it stops at the first error however it is run.
5. Confirm the result (see below).
6. Put the previous binary back. Do this only after the store is v6: the old
   release's `install.sh` opens the store, and on a v7 store it fails with
   `ErrSchemaMismatch`.
7. Start the agents again on the old binary.

```sql
.bail on
-- against the v7 state.db, e.g. ~/.agent-director/state.db
BEGIN;
ALTER TABLE permission_requests DROP COLUMN hook_pid;
ALTER TABLE permission_requests DROP COLUMN hook_starttime;
ALTER TABLE permission_requests DROP COLUMN hook_pidns;
ALTER TABLE permission_requests DROP COLUMN tool_use_id;
ALTER TABLE permission_requests DROP COLUMN agent_id;
ALTER TABLE permission_requests DROP COLUMN delivered_at;
ALTER TABLE permission_requests DROP COLUMN settled_at;
ALTER TABLE permission_requests DROP COLUMN hook_gone_at;
ALTER TABLE permission_requests DROP COLUMN attempted_decision;
ALTER TABLE permission_requests DROP COLUMN attempted_at;
ALTER TABLE permission_requests DROP COLUMN pane_answer;
ALTER TABLE permission_requests DROP COLUMN pane_as;
ALTER TABLE permission_requests DROP COLUMN pane_sender_pid;
ALTER TABLE permission_requests DROP COLUMN pane_sender_starttime;
ALTER TABLE permission_requests DROP COLUMN pane_sender_pidns;
ALTER TABLE permission_requests DROP COLUMN pane_intent_at;
ALTER TABLE permission_requests DROP COLUMN closed_at;
ALTER TABLE spawns DROP COLUMN idle_since;
PRAGMA user_version = 6;
COMMIT;
```

That is exactly what `migrateV6toV7` adds: seventeen columns on
`permission_requests` and `idle_since` on `spawns`. Drop all eighteen and no
other column or table; `store_meta` and its store id stay, so labels written
before the rollback still read as this store's. `PRAGMA user_version = 6` is
the last statement before `COMMIT`. `PRAGMA user_version;` should then print
`6`, and `.schema permission_requests` and `.schema spawns` should show none
of the eighteen columns. A v6 binary then opens the store.

SQLite refuses `DROP COLUMN` on a column that is a PRIMARY KEY, has a UNIQUE
constraint, is indexed, appears in a CHECK or foreign-key constraint, or is
used by a generated column, trigger or view. None of the eighteen is any of
these in the v7 DDL: each is a plain column, nullable or, for `pane_answer`,
`NOT NULL DEFAULT 'none'`. The `permission_requests` UNIQUE constraint and
its two indexes cover only `claude_instance_id`, `request_token`,
`decision` and `decided_at`, and the three `spawns` indexes cover only
`state`, `last_seen_at` and `parent_id`. So every statement in the recipe
succeeds on a v7 store. If one fails, bail mode stops the CLI at that
statement. `COMMIT` never runs, the open transaction is rolled back when the
CLI exits, and the store is still v7; check the schema for a hand-added index
or constraint before you retry. Without bail mode the CLI reports the error,
runs the remaining statements and commits them, leaving a partial downgrade
stamped v6. If that happens, restore the copy from step 2.

The recipe deletes no row. What is lost is the values in the dropped columns:
each permission request's relay hook identity, its ack (`delivered_at`) and
settle instant, the facts readers and `decide` recorded on it
(`hook_gone_at`, `attempted_decision`, `attempted_at`), its `tool_use_id` and
`agent_id`, its pane answer (`pane_answer`, `pane_as`, the sender and
`pane_intent_at`), when a close of its spawn closed it (`closed_at`), and
each row's `idle_since`. A v6 binary judges every request by its
`created_at` and the relay window, as it judged its own: a request with a
recorded `decision` reads as decided, one without as open, fallen back once
its relay window has ended. A request a close closed always has a
`decision` (a close denies an undecided one), and so does one closed at the
pane with a verdict (`sent`, `tool_ran`, or `outside` claiming `allow` or
`deny`), so each reads as decided. A request recorded answered outside
agent-director with the claim `unknown`, or left with a pane answer's
`intent`, has no `decision` and reads as open again. With every agent
stopped (step 1) no relay hook is left to deliver one.

**If the store is later migrated to v7 again**, the hop gives every request
NULL in the new columns and `pane_answer` `none`, and every row NULL
`idle_since` (no phase 3, §2): every request reads as one recorded before
schema v7 and falls back by its `created_at` and the relay window, and an
undecided one refuses plain `send-keys` until it is closed (see "What the
upgrade changes for an existing request" in §2). The store id is kept, so
no label changes owner.

#### v6 → v5 (reverses `migrateV5toV6`)

Use this to roll a migrated store back so a v5 binary (0.11.x, the releases
before schema v6) can open it. On a v7 store, run the v7 → v6 recipe above
first. Order of operations:

1. Stop every agent and every long-running agent-director process on the host
   (each agent's `agent-director serve` runs inside its Claude session, so the
   session must stop too). No process may hold the store open.
2. Copy `state.db` together with its `-wal` and `-shm` files before you change
   anything, so you can start over if a step fails.
3. Run `sqlite3 --version`. The recipe needs SQLite 3.35.5 or later: 3.35.0
   added `ALTER TABLE … DROP COLUMN`, and 3.35.5 fixed `DROP COLUMN` defects
   that could corrupt the database file. If the version is older, stop here
   and install a newer `sqlite3` first.
4. Run the recipe below against the store, in bail mode:
   `sqlite3 -bail ~/.agent-director/state.db < recipe.sql`. The recipe also
   starts with `.bail on`, so it stops at the first error however it is run.
5. Confirm the result (see below).
6. Put the previous binary back. Do this only after the store is v5: the old
   release's `install.sh` opens the store, and on a v6 store it fails with
   `ErrSchemaMismatch`.
7. Start the agents again on the old binary.

```sql
.bail on
-- against the v6 state.db, e.g. ~/.agent-director/state.db
BEGIN;
ALTER TABLE spawns DROP COLUMN launch_owner_pid;
ALTER TABLE spawns DROP COLUMN launch_owner_starttime;
ALTER TABLE spawns DROP COLUMN launch_owner_pidns;
PRAGMA user_version = 5;
COMMIT;
```

That is exactly what `migrateV5toV6` adds: the three launch-owner columns on
`spawns`. Drop all three and no other column or table; `store_meta` and its
store id stay, so labels written before the rollback still read as this
store's. `PRAGMA user_version = 5` is the last statement before `COMMIT`.
`PRAGMA user_version;` should then print `5`, and `.schema spawns` should show
none of the three columns. A v5 binary then opens the store.

SQLite refuses `DROP COLUMN` on a column that is a PRIMARY KEY, has a UNIQUE
constraint, is indexed, appears in a CHECK or foreign-key constraint, or is
used by a generated column, trigger or view. None of the three is any of
these in the v6 DDL: each is a plain nullable column, and the three `spawns`
indexes cover only `state`, `last_seen_at` and `parent_id`. So every statement
in the recipe succeeds on a v6 store. If one fails, bail mode stops the CLI at
that statement. `COMMIT` never runs, the open transaction is rolled back when
the CLI exits, and the store is still v6; check the schema for a hand-added
index or constraint before you retry. Without bail mode the CLI reports the
error, runs the remaining statements and commits them, leaving a partial
downgrade stamped v5. If that happens, restore the copy from step 2.

The recipe deletes no row. What is lost is the values in the dropped columns,
which mean something only on a `pending` row whose launch is still in
progress: that launch's owner. With every agent and agent-director process
stopped (step 1) no launch is left to hold a row, and a v5 binary judges a
`pending` row by its pending grace period alone. Every other value is kept,
`launch_started_at` included. A `liveness_note` `unreported` that a v6
`find-missing` wrote is an ordinary value of a v5 column: it stays on the
row until a hook or a v5 `find-missing` clears or replaces it.

**If the store is later migrated to v6 again**, the hop gives every row NULL
in the three columns (no phase 3, §2): no launch owner, so `find-missing`
judges every `pending` row by its pending grace period alone until a new
launch records its owner. The store id is kept, so no label changes owner.

#### v5 → v4 (reverses `migrateV4toV5`)

Use this to roll a migrated store back so a v4 binary (the release before
schema v5) can open it. On a v6 store, run the v6 → v5 recipe above first,
and on a v7 store the v7 → v6 recipe before that. Order of operations:

1. Stop every agent and every long-running agent-director process on the host
   (each agent's `agent-director serve` runs inside its Claude session, so the
   session must stop too). No process may hold the store open.
2. Copy `state.db` together with its `-wal` and `-shm` files before you change
   anything, so you can start over if a step fails.
3. Run `sqlite3 --version`. The recipe needs SQLite 3.35.5 or later: 3.35.0
   added `ALTER TABLE … DROP COLUMN`, and 3.35.5 fixed `DROP COLUMN` defects
   that could corrupt the database file. If the version is older, stop here
   and install a newer `sqlite3` first.
4. Run the recipe below against the store, in bail mode:
   `sqlite3 -bail ~/.agent-director/state.db < recipe.sql`. The recipe also
   starts with `.bail on`, so it stops at the first error however it is run.
5. Confirm the result (see below).
6. Put the previous binary back. Do this only after the store is v4: the old
   release's `install.sh` opens the store, and on a v5 store it fails with
   `ErrSchemaMismatch`.
7. Start the agents again on the old binary.

```sql
.bail on
-- against the v5 state.db, e.g. ~/.agent-director/state.db
BEGIN;
ALTER TABLE spawns DROP COLUMN row_version;
ALTER TABLE spawns DROP COLUMN launch_started_at;
ALTER TABLE spawns DROP COLUMN life_number;
ALTER TABLE spawns DROP COLUMN no_pre_trust;
ALTER TABLE spawns DROP COLUMN launch_token;
ALTER TABLE spawns DROP COLUMN tmux_socket;
ALTER TABLE spawns DROP COLUMN tmux_server_pid;
ALTER TABLE spawns DROP COLUMN tmux_server_started;
ALTER TABLE spawns DROP COLUMN tmux_server_starttime;
ALTER TABLE spawns DROP COLUMN pane_id;
ALTER TABLE spawns DROP COLUMN pane_pid;
ALTER TABLE spawns DROP COLUMN pane_starttime;
ALTER TABLE session_history DROP COLUMN life_number;
DROP TABLE store_meta;
PRAGMA user_version = 4;
COMMIT;
```

That is exactly what `migrateV4toV5` adds: the thirteen columns (twelve on
`spawns` and `session_history.life_number`) and the `store_meta` table. Drop
all thirteen columns, so that a later migration back to v5 never keeps a stale
token, socket or identity (SR-21.4), and drop the `store_meta` table with its
store id. Drop no other column or table. `PRAGMA user_version = 4` is the last
statement before `COMMIT`. `PRAGMA user_version;` should then print `4`,
`.schema spawns` and `.schema session_history` should show none of the
thirteen columns, and `.schema store_meta` should print nothing. A v4 binary
then opens the store.

SQLite refuses `DROP COLUMN` on a column that is a PRIMARY KEY, has a UNIQUE
constraint, is indexed, appears in a CHECK or foreign-key constraint, or is
used by a generated column, trigger or view. None of the thirteen columns is
any of these in the v5 DDL: each is a plain column with at most `NOT NULL
DEFAULT 0`. `session_history`'s table-level
`UNIQUE(claude_instance_id, claude_session_id)` and its
`idx_session_history_instance` index do not include `life_number`, and the
three `spawns` indexes cover only `state`, `last_seen_at` and `parent_id`.
`store_meta` has no index, trigger or view of its own beyond its primary key,
and no other table refers to it, so `DROP TABLE store_meta` succeeds too. So
every statement in the recipe succeeds on a v5 store. If one fails, bail mode
stops the CLI at that statement. `COMMIT` never runs, the open transaction is
rolled back when the CLI exits, and the store is still v5; check the schema
for a hand-added index or constraint before you retry. Without bail mode the
CLI reports the error, runs the remaining statements and commits them, leaving
a partial downgrade stamped v4. If that happens, restore the copy from step 2.

The recipe deletes no `spawns` row and no history entry. What is lost is the
values in the dropped columns (row versions, launch starts and tokens,
sockets, server and pane identities, lives and recorded pre-trust choices) and
the store id. A v4 binary
reads every history entry of an id, so after the rollback it reattaches and
reports the conversations of every life again, earlier lives' included, as it
did before schema v5.

**If the store is later migrated to v5 again**, the hop gives every row and
entry the ordinary defaults again (no phase 3, §2):

- Every history entry goes into its row's current life (life 0). A row that
  was reused before the rollback keeps its earlier lives' history visible until
  its next reuse.
- Every row gets `no_pre_trust` 0, so a recorded pre-trust opt-out is lost and
  a later `resume` of that row pre-trusts its folder. To keep an opt-out, start
  the agent again with a spawn or reuse that turns pre-trust off.
- The hop finds no `store_meta`, so it creates the table and a **new** random
  store id. The old id is gone from the store (only the step-2 copy still
  holds it). Every label carries the store id as its last field, so every
  label written before the rollback reads as another store's. That is why every agent is stopped before the rollback
  (step 1) and started again after the re-migration, never carried across it.

**Alternative: restore a backup.** Restoring a copy of `state.db` taken before
the v5 install avoids the history and pre-trust consequences, but loses every
write made since the install, for every caller on the host. It does not keep
the store id: that copy is v4 and has none, so a later migration of it also
creates a new id, and agents are stopped and started around it the same way.
`install.sh` does not make that copy; the operator must take it before
installing.

## References

- `internal/store/schema.go` — `ensureSchema`, `runMigrationChain`,
  `migrationSteps`/`migrationStep`, `migrationStepFrom`, `createSchema`,
  `schemaDDL`, `migrateV1toV2`, `migrateV2toV3`, `migrateV3toV4`,
  `migrateV4toV5`, `migrateV5toV6`, `migrateV6toV7` with its column list
  `v7Columns`, `buildMigrationRefusal`.
- `internal/store/migrate_auth.go` — the sentinel gate: `authorizeMigration`,
  `parseAuthorization`, `consumeAuthorization`, `sentinelPath`,
  `sentinelFilename` (`migrate-authorized`), the trail events.
- `internal/store/store.go` — `schemaVersion`, `ErrSchemaMismatch`,
  `ErrSchemaMigrationRequired`, `Open`/`OpenOrInit`/`openDB`.
- `internal/store/storeid.go` — the store id: `(*Store).StoreID`,
  `readStoreID` (the open-time read), `insertStoreIDOnce` /
  `insertStoreIDOnceSQL` (the guarded insert used by `createSchema` and
  `migrateV4toV5`), `newStoreID`, `validStoreID`.
- `pkg/api/aliases.go` — `ErrSchemaMigrationRequired` alias (mirrors the
  `ErrSchemaMismatch` precedent).
- `internal/store/schema_test.go` — the fresh store and the
  version-independent migration tests (§3):
  `TestAuthorizedMigrationFromEveryVersion`,
  `TestMigrationRefusedWithoutSentinel`, `TestGateRefusesBadSentinel`,
  `TestNewerThanBinaryIsSchemaMismatch`, `TestMigrationStepReentry`,
  `TestMigrationRollback` and the sentinel consume.
- `internal/store/schema_v5_test.go` — v5's own cases, the template for a
  new version's `schema_v<N>_test.go` (§3): `TestV5MigrationKeepsV4Rows`,
  the store-id tests (`TestStoreIDKept`,
  `TestStoreID_MissingOrMalformedFailsOpen`) and the downgrade tests:
  `TestDowngradeRecipe_MatchesGuide` parses the three recipe SQL blocks of
  §5, "v7 → v6", "v6 → v5" and "v5 → v4", and fails when any differs from
  its test copy (`v7ToV6RecipeStatements`, `v6ToV5RecipeStatements`,
  `v5ToV4RecipeStatements`) or lacks the `.bail on` / `BEGIN;` / `COMMIT;`
  frame, so no recipe here can drift from the statements the tests run; it
  is also the test that pins the v7 → v6 and v6 → v5 recipes to the guide.
  `TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults` takes a current
  store back to v5 first (`v7ToV6RecipeStatements`, then
  `v6ToV5RecipeStatements`), applies the recipe to `seedV5DowngradeRows`,
  checks every v4 column and history entry survives, then re-migrates and
  checks the v5 and v6 columns take their defaults (pre-trust opt-out and
  life numbers are lost; no launch owner).
- `internal/store/schema_v6_test.go` — v6's own cases (§3):
  `TestV6MigrationKeepsV5Rows` (`makeV5Fixture` migrated: every v5 value
  kept, no launch owner, the store id kept) and
  `TestDowngradeV6ToV5_KeepsRowsThenRemigrates`, which takes a current store
  back to v6 (`v7ToV6RecipeStatements`), applies `v6ToV5RecipeStatements` to
  it holding a `pending` row with a recorded owner, checks the v5 store keeps
  every other value and the store id and is refused with
  `ErrSchemaMigrationRequired`, then re-migrates and checks the row has no
  owner.
- `internal/store/schema_v7_test.go` — v7's own cases (§3):
  `TestV7MigrationKeepsV6Rows` (`makeV6Fixture` migrated: every v6 value of
  the row and its requests kept, the v7 columns at their defaults through
  `assertV7Defaults`, the store id kept, and both requests judged as recorded
  before v7) and `TestDowngradeV7ToV6_KeepsRowsThenRemigrates`, which applies
  `v7ToV6RecipeStatements` to a store holding a request the relay hook
  recorded with its identity, checks the v6 store has a v6 store's shape,
  keeps every other value of the row and the request and the store id, and
  is refused with `ErrSchemaMigrationRequired`, then re-migrates and checks
  the v7 columns take their defaults (`assertV7Defaults`: the request now
  reads as recorded before v7) and every other value is kept.
- `internal/store/migration_fixtures_test.go` — the migration-gate fixtures
  (`makeVersionedDB`, `writeSentinel` / `writeSentinelRaw`,
  `assertSentinel`, `stampUserVersion`, `snapshotDBBytes`,
  `assertDBBytesUnchanged`, `schemaShape`, `dbDump`) and the v5 helpers:
  `makeV4HistoryFixture` (the v4 store seeded with session history and a
  `pending` row), `v5ColumnSpecs`, `readTableShape`, `readRawSpawn`,
  `readRawHistory`, `assertV5Defaults`, `preAddV5Columns`,
  `breakV5SessionHistoryHop`; and the store-id helpers: `readStoreMeta`,
  `preAddStoreMeta`, `breakV5StoreMetaStep`, and `applyV5ToV4Recipe` (with
  `v5ToV4RecipeStatements`, which must match the v5 → v4 recipe statement for
  statement), and `seedV5DowngradeRows` (a v5 store with a row reused twice
  and a row with pre-trust turned off, for the downgrade test). The v6
  helpers: `v6ColumnSpecs`, `v6ColumnNames`, `assertV6Defaults`,
  `assertNoV6Columns`, `preAddV6Columns`, `breakV6PIDNSColumn`,
  `makeV5Fixture` (the v5 store with a mid-launch `pending` row, a
  `waiting` row and an `ended` row), `applyRecipe` (runs a recipe's
  statements on a closed store in one transaction) and
  `v6ToV5RecipeStatements`, which must match the v6 → v5 recipe statement for
  statement (checked by `TestDowngradeRecipe_MatchesGuide` in
  `schema_v5_test.go`). The v7 helpers: `v7ColumnSpecs`, `v7ColumnNames`,
  `assertNoV7Columns`, `preAddV7Columns`, `breakV7IdleSinceColumn` (an
  `IDLE_SINCE` column the hop's exact-name probe misses, so the hop fails at
  its last column), `makeV6Fixture` (the v6 store with a relay row in
  `check_permission`, an open request and a decided one, returned as a
  `v6Fixture`) and `v7ToV6RecipeStatements`, which must match the v7 → v6
  recipe statement for statement (checked by
  `TestDowngradeRecipe_MatchesGuide`).
- `internal/store/testdata/schema_v1.sql` — the v1 fixture `makeVersionedDB`
  builds every older version from.
- docs/engineering-guide.md §10 — sandboxed execution, the b.8dr incident.
