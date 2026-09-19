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

**The version contract.** `schemaVersion` (`store.go`, currently `2`) is the
version this binary writes and reads. Every opened DB carries its own version
in SQLite's `PRAGMA user_version` (0 on a brand-new file). `ensureSchema(db)`
(`schema.go`) is called from `openDB` on every `Open`/`OpenOrInit` and
dispatches on that pragma:

| `user_version` on disk | `ensureSchema` does                                   |
| ---------------------- | ----------------------------------------------------- |
| `== schemaVersion`     | nothing — no-op, already current                      |
| `0`                    | `createSchema(db)` — full latest-schema DDL + stamp   |
| `1`                    | `migrateV1toV2(db)` — the one existing migration hop  |
| anything else          | wrap `ErrSchemaMismatch` (typed error), run **no** DDL |

The `default` branch is the guard rail: a DB from a *newer* binary (say
`user_version=3` opened by a v2 binary), or any value that has no migration
path, returns `ErrSchemaMismatch` (`store.go`) rather than touching the file.
Callers detect it with `errors.Is(err, ErrSchemaMismatch)`.

**One function per hop.** Each version transition is one function named
`migrateV<N>toV<N+1>`. `migrateV1toV2` (`schema.go`) is the reference
implementation. To add v3 you write `migrateV2toV3` and add a `case 2:` arm to
the `ensureSchema` switch that calls it.

**One transaction per hop, stamp included.** Every hop opens a single
`db.Begin()` transaction and does *all* of its DDL/DML **and** the
`user_version` stamp inside it. Any error rolls the whole thing back with
`tx.Rollback()`, leaving `user_version` at the previous value — a clean retry
on the next `Open`. Look at the shape of `migrateV1toV2`: begin → DDL → stamp →
commit, with a rollback on every error path. `createSchema` follows the same
begin/exec/stamp/commit pattern so a crash mid-creation leaves `user_version`
at 0 and the next open retries.

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

1. The new migration hop (`migrateV2toV3`) — upgrades an existing older DB.
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
a full rebuild is a legitimate hop when rows are not being preserved.)

**Phase 3 — data backfill/transform.** `UPDATE`/`INSERT … SELECT` to populate
new columns or reshape rows, if the migration keeps data. (`migrateV1toV2` has
no phase 3: it deliberately does not preserve v1 `permission_requests` rows —
SR-2.6 single-version invariant / SR-2.3 no-backfill.)

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
2. **Run `Open`/`OpenOrInit` once.** Assert `PRAGMA user_version == N+1` and that
   the schema and data match expectations — new columns/indexes present,
   backfilled values correct, dropped data actually gone. Both funnel through
   `openDB`→`ensureSchema`, so either entry point triggers the migration;
   `TestSchemaV2Migration` uses `Open` and checks
   the `request_token` column, the composite `UNIQUE`, both indexes, and a zero
   row count.
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
goes wrong: **fresh databases never replay hops.** When you add v3, update
`schemaDDL` and `schemaVersion` so a brand-new DB is created directly at v3 by
`createSchema`, *and* write `migrateV2toV3` so an existing v2 DB is upgraded to
the identical shape. The §3.4 normalized-`sqlite_master` comparison is the
guard that both paths land in the same place. If you only touch the hop, fresh
installs are stuck on the old schema; if you only touch `schemaDDL`, upgrades
never happen.

## 5. The b.8dr rule — schema-bumping branches run ONLY in the sandbox

`ensureSchema` auto-migrates **any** older DB on **any** open, with **no
prompt**. There is no dry-run and no confirmation: the instant a binary or test
built from a schema-bumping branch opens a store, it rewrites that store to the
new version.

That is exactly the b.8dr incident. Running `bun test` on the b.aaj branch
(schema v3 migration code) on the host auto-migrated the real
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

- `internal/store/schema.go` — `ensureSchema`, `createSchema`, `schemaDDL`,
  `migrateV1toV2`.
- `internal/store/store.go` — `schemaVersion`, `ErrSchemaMismatch`,
  `Open`/`OpenOrInit`/`openDB`.
- `internal/store/schema_test.go` — the fresh-path, idempotency, and v1→v2
  migration tests; `openV1DB` fixture loader.
- `internal/store/schema_mismatch_test.go` — `ErrSchemaMismatch` coverage and
  the rollback-preserves-old-state test.
- `internal/store/testdata/schema_v1.sql` — the version-N fixture pattern.
- docs/engineering-guide.md §10 — sandboxed execution, the b.8dr incident.
