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

**The version contract.** `schemaVersion` (`store.go`, currently `3`) is the
version this binary writes and reads. Every opened DB carries its own version
in SQLite's `PRAGMA user_version` (0 on a brand-new file). `ensureSchema(db,
dbPath)` (`schema.go`) is called from `openDB` on every `Open`/`OpenOrInit`
(passing the *resolved* DB path) and dispatches on that pragma:

| `user_version` on disk                 | `ensureSchema` does                                                 |
| -------------------------------------- | ------------------------------------------------------------------- |
| `== schemaVersion`                     | nothing — no-op, already current                                    |
| `0`                                    | `createSchema(db)` — full latest-schema DDL + stamp                 |
| `0 < v < schemaVersion` (older-than-binary) | **gated** — refuse with `ErrSchemaMigrationRequired` unless an administrator authorization sentinel exact-matches; when authorized, run the chained migration steps (see §1a) |
| `> schemaVersion`                      | wrap `ErrSchemaMismatch` (typed error), run **no** DDL              |

**The store never auto-migrates on open.** An older-than-binary DB is *not*
silently upgraded — that was the b.8dr incident class (§5). The migration only
runs when an administrator has placed a valid, exact-match authorization
sentinel next to the DB file; otherwise the open is refused with the typed
`ErrSchemaMigrationRequired` (`store.go`) and the DB file is left byte-identical
(zero DB writes). See §1a for the sentinel gate and §1b for the refusal.

The newer-than-binary arm is the guard rail: a DB from a *newer* binary (say
`user_version=3` opened by a v2 binary) returns `ErrSchemaMismatch` (`store.go`)
rather than touching the file. Callers detect it with
`errors.Is(err, ErrSchemaMismatch)`.

**A registry of steps, not a switch.** Version transitions are no longer
`case` arms. Each single-version upgrade is a `migrationStep{from: N, apply:
migrateV<N>toV<N+1>}` entry in the ordered `migrationSteps` registry
(`schema.go`); today that registry holds two entries, `{from: 1, apply:
migrateV1toV2}` and `{from: 2, apply: migrateV2toV3}`. `migrateV1toV2` and
`migrateV2toV3` (`schema.go`) are the reference implementations of an `apply`
func. To add the next version (v4) you write `migrateV3toV4` and append
`{from: 3, apply: migrateV3toV4}` to `migrationSteps` — see §1a's "Adding a
step" for the full checklist.

**One transaction per step, stamp included.** Every `apply` func opens a single
`db.Begin()` transaction and does *all* of its DDL/DML **and** the
`user_version` stamp inside it. Any error rolls the whole thing back with
`tx.Rollback()`, leaving `user_version` at the previous value. This
per-step atomicity is what makes the chain safe: a crash between steps leaves
the DB stamped at the last successfully-committed version, never at an
intermediate un-stamped state, so a re-open resumes the chain cleanly from
there. Look at the shape of `migrateV1toV2`: begin → DDL → stamp → commit, with
a rollback on every error path. `createSchema` follows the same
begin/exec/stamp/commit pattern so a crash mid-creation leaves `user_version`
at 0 and the next open retries.

## 1a. The chained step engine and the sentinel gate

**The chain.** Once an older-than-binary open is authorized (below),
`runMigrationChain(db, from)` (`schema.go`) loops while `version <
schemaVersion`, looking up the registered step for the current version
(`migrationStepFrom`), applying it, and incrementing. It upgrades a DB across
*multiple* versions in a single open — a v1 DB opened by a future v4 binary runs
v1→v2→v3→v4 in one pass, each step its own transaction. There is no
return-after-one-hop: the loop keeps going until the DB reaches `schemaVersion`.
If the loop finds itself below `schemaVersion` with no registered step for the
current version, that is a gap in the registry — it fails loudly with
`ErrSchemaMismatch` rather than silently leaving the DB behind.

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

**Adding a step.** To add the next version (v4, generalizing to any vN→vN+1):

1. Bump `schemaVersion` to `4` (`store.go`).
2. Evolve `schemaDDL` so a fresh DB is created directly at v4 (the two-places
   rule — §1, §4).
3. Write `migrateV3toV4(db)` following the one-transaction/validate-first
   pattern (§2).
4. Append `{from: 3, apply: migrateV3toV4}` to `migrationSteps` (`schema.go`).

That is all — do **not** add any return-after-one-hop logic. The chain engine
walks the registry from the DB's version up to `schemaVersion` automatically, so
appending the step is what makes both a v3→v4 upgrade and a straight-through
v1→v4 upgrade work.

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
`setUserVersion` in `schema_mismatch_test.go` interpolates the same way, for
the same reason.)

**The two-places rule.** A fresh database **never replays the migration
hops** — `createSchema` runs the canonical `schemaDDL` constant (`schema.go`),
which must *always* describe the latest schema directly. So every schema change
lands in **two** places:

1. The new migration hop (e.g. `migrateV3toV4`) — upgrades an existing older DB.
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
preserves every existing row.)

**Phase 3 — data backfill/transform.** `UPDATE`/`INSERT … SELECT` to populate
new columns or reshape rows, if the migration keeps data. Not every hop needs
one: `migrateV1toV2` deliberately does not preserve v1 `permission_requests`
rows (SR-2.6 single-version invariant / SR-2.3 no-backfill), and
`migrateV2toV3` needs no phase 3 either — `ADD COLUMN` populates existing rows
from the column defaults (NULL for the four nullable columns, `'{}'` for
`extra_env`), so there is nothing to backfill.

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
idempotent**. The recipe (see `schema_test.go` for the working v1→v2 version):

1. **Build a fixture DB at version N.** Commit a small `testdata/schema_v<N>.sql`
   (v1's lives at `internal/store/testdata/schema_v1.sql`; it ends with
   `PRAGMA user_version = 1`) and load it into a temp file, as `openV1DB` does.
   Seed a couple of representative rows so post-migration data assertions are
   non-vacuous.
2. **Authorize the upgrade, then run `Open`/`OpenOrInit` once.** The store no
   longer auto-migrates (§1a), so an older-DB open now *refuses* with
   `ErrSchemaMigrationRequired` unless a valid sentinel is present. Write a
   `migrate-authorized` file next to the fixture DB with an exact-match payload
   (`{"from": N, "to": N+1}`) before opening. Then assert `PRAGMA user_version
   == N+1`, that the schema and data match expectations (new columns/indexes
   present, backfilled values correct, dropped data actually gone), **and that
   the sentinel was consumed** (deleted) by the successful open.
   `TestSchemaV2Migration` uses `Open` and checks the `request_token` column,
   the composite `UNIQUE`, both indexes, and a zero row count. Cover the refusal
   path too: open the same fixture with a missing/malformed/mismatched sentinel
   and assert `errors.Is(err, ErrSchemaMigrationRequired)`, the DB stays
   byte-identical at `user_version == N`, and a preserved sentinel is left in
   place on the malformed/mismatched cases.
3. **Run `OpenOrInit` again.** Assert nothing changed: no error, `user_version`
   still `N+1`, no data mutation. This is the idempotency proof — it fails if
   your hop is not safe to re-enter or if the `== schemaVersion` no-op arm is
   wrong. (`TestOpenIsIdempotent` covers the double-open for the fresh path.)
4. **Assert the fresh path and the migrated path converge.** Create one DB via
   `createSchema` (fresh `OpenOrInit` on an empty file) and one via the
   migration, then compare their normalized `sqlite_master` SQL. This is the
   automated enforcement of the two-places rule (§1) — if `schemaDDL` and your
   hop drifted, the schemas differ and the test fails. Normalize whitespace/case
   before comparing, and query `sqlite_master` for both `table` and `index`
   objects (`objectExists` in `schema_test.go` shows the lookup shape).

Also keep a rollback test: `TestMigrationFailurePreservesV1State`
(`schema_mismatch_test.go`) injects a mid-transaction failure and asserts the
DB stays at `user_version=1` with the old table shape intact. Add the
equivalent for your hop.

**Where these tests run:** `internal/store` carries the sandbox guard
(`sandboxguard.Require()` in its `TestMain`). Run them only in the sandbox —
see §5.

## 4. `createSchema` must always be the latest schema

Restating the two-places rule because it is the most common way a migration
goes wrong: **fresh databases never replay hops.** When you add v4, update
`schemaDDL` and `schemaVersion` so a brand-new DB is created directly at v4 by
`createSchema`, *and* write `migrateV3toV4` so an existing v3 DB is upgraded to
the identical shape. The §3.4 normalized-`sqlite_master` comparison is the
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
`ErrSchemaMismatch`). Host `$HOME` redirection does **not** save you:
`store.expandTilde` resolves the DB path through `user.Current()` (reading
`/etc/passwd`), which ignores the `$HOME` env var entirely. A container whose
HOME has no `.agent-director` is the only isolation boundary that holds.

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
accident-prevention gates, not a licence to reach around them. See
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

## References

- `internal/store/schema.go` — `ensureSchema`, `runMigrationChain`,
  `migrationSteps`/`migrationStep`, `migrationStepFrom`, `createSchema`,
  `schemaDDL`, `migrateV1toV2`, `migrateV2toV3`, `buildMigrationRefusal`.
- `internal/store/migrate_auth.go` — the sentinel gate: `authorizeMigration`,
  `parseAuthorization`, `consumeAuthorization`, `sentinelPath`,
  `sentinelFilename` (`migrate-authorized`), the trail events.
- `internal/store/store.go` — `schemaVersion`, `ErrSchemaMismatch`,
  `ErrSchemaMigrationRequired`, `Open`/`OpenOrInit`/`openDB`.
- `pkg/api/aliases.go` — `ErrSchemaMigrationRequired` alias (mirrors the
  `ErrSchemaMismatch` precedent).
- `internal/store/schema_test.go` — the fresh-path, idempotency, and v1→v2
  migration tests; `openV1DB` fixture loader.
- `internal/store/schema_mismatch_test.go` — `ErrSchemaMismatch` coverage and
  the rollback-preserves-old-state test.
- `internal/store/testdata/schema_v1.sql` — the version-N fixture pattern.
- docs/engineering-guide.md §10 — sandboxed execution, the b.8dr incident.
