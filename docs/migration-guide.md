# Writing a SQLite Schema Migration

How to add the next schema version to the agent-director store safely.

This guide is for engineers (and Worker Claudes) changing the DDL in
`internal/store/`. The store is the **only** package permitted to speak SQL
(`internal/store/store.go` package doc, SRD §4.5); every schema change lands
here. Read this end-to-end before bumping the version — the rules below are
not optional, and one of them (the b.8dr rule, §5) exists because it was
learned the hard way against the production database.

## 1. Architecture as it exists today

Schema dispatch and fresh-store DDL live in `internal/store/schema.go`;
`schemaVersion` in `store.go` is **20**. The twelve tables are `spawns`,
`permission_requests`, `session_history`, `store_meta`, `subagents`,
`subagent_launches`, `cursor_runtimes`, `input_launches`, `input_attempts`,
`input_imports`, `cursor_inputs` and `cursor_proofs`.
`inputDeliveryDDL` in `input_delivery_schema.go` is shared by
fresh creation and the administrator import in `input_delivery_migration.go`.
`permission_authority.go` owns the v16→v17 permission provenance step.
`cursor_obligations_schema.go` owns the v17→v18 evidence step and its shared
fresh-store `cursorObligationsDDL`. `confirmations_schema.go` owns the v18→v19
main-field evidence step and the v19→v20 display-evidence step.
Earlier hops retain their owning files: `cursor_runtime.go`,
`request_eligibility.go`, `family_schema.go`, `codex_startup.go` /
`claude_capacity.go`, `display_schema.go` and `codex_turn.go`.

**The version contract.** Every open reads `PRAGMA user_version` and checks:

| Version on disk | Result |
|---|---|
| 0, empty schema | Fresh v20 DDL, store id, native-input authority and stamp commit together with FULL on the actual connection. |
| 0, nonempty schema | `ErrSchemaMismatch`; resetting a version is not fresh creation. |
| 1–15 | `ErrSchemaMigrationRequired` before any hop unless both exact schema authorization and the administrator input-migration window are present. |
| 16–19 | Exact schema authorization is required for the remaining hops to v20; no input import or input-migration window is needed. |
| 20 | Validate the store id and native-input authority; never reimport files or replay Cursor evidence. |
| Above 20 | `ErrSchemaMismatch`; no DDL. |

Ordinary opens never construct the maintenance window. An exact preexisting
sentinel therefore cannot trigger even an earlier hop on the way to v16. The
administrator includes the invoking HOME's current and old input roots plus
all declared roots, verifies real process identity/access, and requires the
operator to maintain exclusion through committed verification. A per-store
lock alone cannot exclude old or untracked mutators.

**Store identity and authority.** `store_meta.store_id` remains 16 lowercase
hex characters, created once by fresh creation or the v4→v5 hop and retained
by later hops. Invalid identity refuses. The second metadata fact,
`native_input_authority=sqlite-v1:<store_id>`, commits with fresh DDL or the
complete v15→v16 import. Every open and input operation checks it; missing or
wrong-store authority is corruption, not permission to use legacy files.

**The registry.** Nineteen entries cover v1→v2 through v19→v20. The
`{from:15, inputCutover:true, apply:migrateV15toV16}` entry uses the administrator
importer; `{from:16, apply:migrateV16toV17}` adds permission provenance without
reimporting input; `{from:17, apply:migrateV17toV18}` adds Cursor evidence tables;
`{from:18, apply:migrateV18toV19}` adds the state/process evidence columns;
`{from:19, apply:migrateV19toV20}` adds `display_evidence` and `observation_writer`.
Earlier `apply` steps remain unchanged and individually
transactional. Inspect the actual registry for later work;
no next schema number is reserved here.

**One transaction per step, stamp included.** Each hop commits its DDL/DML
and version together. A failed hop rolls back; earlier committed hops remain.
Retry authorization must match that resulting version. The v16 transaction
also commits the complete import and authority marker. An uncertain commit
does not authorize replay: reopening verifies committed authority. Fresh
creation shares the latest DDL and FULL commit discipline; it never accepts
a nonempty hand-reset schema as a fresh store.

## 1a. The chained step engine and the sentinel gate

**The chain.** Once an older-than-binary administrator open is authorized (below),
`runMigrationChainWithInputWindow(db, from, window)` (`schema.go`) loops while `version <
schemaVersion`, looking up the registered step for the current version
(`migrationStepFrom`), applying it, and incrementing. It upgrades a DB across
*multiple* versions in a single open — a v1 DB opened by the current v20 administrator
runs v1→v2→v3→v4→v5→v6→v7→v8→v9→v10→v11→v12→v13→v14→v15→v16→v17→v18→v19→v20 in one pass, each step its own transaction. There is no
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
(`migrate_auth.go`) gates the chain, with an additional administrator window
for a pre-v16 store. It looks for a sentinel file named
`migrate-authorized` sibling to the *resolved* DB path — i.e. in the same
directory as the DB file, so it authorizes the right store even under a custom
store path (never hard-coded to `~/.config/agent-director`). The sentinel is a single
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
mid-migration failure leaves the sentinel in place as evidence. A retry
requires it to exact-match the DB's resulting version: if 9→10 commits and
10→11 fails, the retained `{from:9,to:11}` is stale, and an ordinary reopen
is refused until authorization is refreshed to `{from:10,to:11}`. Rerun
the supported installer to read the current version and reauthorize. Every other
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
next open validates current authority without importing, and a future
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

**Adding a step.** For a later vN→vN+1, use the validate-first pattern (§2):

1. Bump `schemaVersion` to the chosen next version (`store.go`).
2. Evolve fresh DDL to that version (the two-places rule — §1, §4), preserving
   the store id and existing authority.
3. Write the new hop using one transaction, validation before mutation and
   the version stamp last (§2).
4. Append its entry to `migrationSteps` (`schema.go`).
5. Document the recovery policy in §5. Where an in-place reverse recipe is
   supported, add its matching test copy to `TestDowngradeRecipe_MatchesGuide`
   and chain the applicable older recipe to it. Otherwise record the technical
   exception and require a stopped-writer pre-upgrade backup and restore;
   never imply that an older recipe can operate on the new schema.

The engine walks the actual registry; do not fold later changes into settled hops.

### Current schema facts: v19 → v20

`migrateV19toV20` appends nullable, default-free `spawns.display_evidence TEXT`
and then `spawns.observation_writer TEXT`, after `process_evidence` on a normal
v19 store and in fresh DDL. `display_evidence` records the shown display's
evidence even when no Displayed State is present; `observation_writer` holds
the random token of the observer commit that last wrote the display
observation or record, so an observer whose COMMIT went unanswered confirms
only its own write. Neither changes `display_observation` or the v18→v19 step.
There is no backfill: newly added columns are NULL on existing rows, which then
read `unknown`/`no_evidence` for display confirmation and match no observer's token.
Neither upgrade time, display `since` nor the pane baseline supplies evidence.

The administrator sentinel gate applies. Before DDL, the transaction validates
each existing column (exact name and case, TEXT, nullable, no default, not a
primary key) and all non-null `display_evidence` values (TEXT containing a
valid JSON object). Incompatible shape or data returns `ErrSchemaMismatch` with
"v19→v20 incompatible spawns.<column>" or
"v19→v20 invalid spawns.display_evidence"; the v19 schema, data and stamp remain.
A compatible partial reentry keeps present columns and values in place and adds
the missing one. Semantic record validity and ownership are checked on read;
malformed or inapplicable records give unknown rather than failing the read.

The additions and stamp 20 commit together. Store identity, input authority,
permission/proof tables and existing evidence columns are unchanged.
`confirmations_schema_test.go` covers convergence, no backfill, reentry,
refusal and reopen. Recovery is [restore-only](#emergency-downgrade-recipe):
a v19 binary refuses a v20 store. Authorized re-migration of a restored v19
backup adds the empty columns again.

### Current schema facts: v18 → v19

`migrateV18toV19` appends each missing one of three nullable, default-free
`spawns` columns, `state_evidence TEXT`, `settled_evidence TEXT` and
`process_evidence TEXT`, in that order; fresh DDL keeps that order before
the v20 columns.
Each holds NULL or one JSON object (see "v19 columns" under
[`internal/store`](architecture.md#internalstore)). There is no backfill: no
stored timestamp establishes the exact value and owner a record names, so a
newly added column is NULL on every existing row, which reads `unknown` until
an owned event, command or check records evidence. Upgrade time is never used.

Before any DDL the hop validates any existing column of those names (exact
name and case, TEXT, nullable, no default, not a primary key) and every
present value (TEXT holding a valid JSON object). It refuses with
`ErrSchemaMismatch`, "v18→v19 incompatible spawns.<column>" or "v18→v19 invalid
spawns.<column>". A compatible partial reentry keeps each existing column
where it is, with its values, so the three may follow another order than a
fresh store's; a kept record still applies to a read while it names the row's
exact current value and owner. The additions and the v19 stamp share one
transaction; refusal or rollback leaves the v18 schema, data, stamp and authorization intact. An
unknown commit does not prove rollback: inspect the resulting version before
reauthorization. Authorization must match the actual starting version and
the current binary's target (20); this step stamps 19 before the next hop.
Store identity, input authority and the permission and Cursor
tables are unchanged.

Recovery is restore-only (§5). An older binary refuses a v19 store with
`ErrSchemaMismatch`. Restoring the stopped-writer pre-upgrade backup loses
every later write, evidence included; the evidence carries no delivery or
command authority. Authorized re-migration of the restored v18 store adds
empty columns under the same store id. `confirmations_schema_test.go` covers
fresh/migrated convergence, reentry, incompatible-shape and invalid-value
refusal, rollback and retry, and that restore and re-migration;
`confirmations_test.go` covers the records.

### Current schema facts: v17 → v18

`migrateV17toV18` adds `cursor_inputs` and `cursor_proofs`, keyed by store,
connection and submission/proof id, with `instance_id` foreign keys cascading
on spawn deletion. Both retain protected JSON records; proofs also retain the
original source bytes as a BLOB. Fresh DDL uses the same declarations. There
is no backfill from transcripts, permission rows or earlier input history.

Before any DDL, the hop validates the store id and any existing table's exact
normalized declaration, including keys and constraints, then SQLite value
types, decoded record shape and matching store/connection/instance/id. Nested
metadata and raw proof envelopes reject repeated or Unicode-simple-fold-equivalent
field names before mutation; valid raw bytes and unknown provider extension
fields remain intact. Compatible partial tables retain their values. Both
table additions and the final v18 stamp share one transaction; refusal or
rollback leaves the v17 schema/data and authorization intact. An unknown commit
does not prove rollback: inspect the resulting version before reauthorization.
Authorization must match the actual starting version and the current target,
20. Earlier hops, native-input authority/import and v17 permission provenance
are unchanged.

Committed records retain input ordering, payload, original owner and phase
identity, or exact proof source and first busy deadline. They grant no replay
or replacement-helper authority. The runtime contract and atomic proof/request
effects are in [Cursor authorization dataflow and authority](architecture.md#cursor-authorization-dataflow-and-authority).
Recovery is restore-only (§5): dropping the tables would discard uncertainty
needed before an older runtime starts. `cursor_obligation_schema_test.go`
covers authorization, compatible reentry, incompatible/ambiguous evidence
refusal, rollback/retry, fresh convergence and stopped-writer restore with
original identity and reauthorization; live record cases are in
`cursor_obligation_test.go`.

### Current schema facts: v16 → v17

`migrateV16toV17` adds five nullable, default-free permission columns:
`decision_origin TEXT`, `response_state TEXT`, `owner_life_number INTEGER`,
`owner_session_id TEXT` and `owner_cursor_connection TEXT`. Fresh DDL matches.
Existing columns must have the exact declared shape; all their non-null values
are validated before any DDL. Origins are caller/timeout/pane/observation;
response states are relay_claimed/native_claimed/relay_issued/native_issued.
Life numbers are nonnegative integers, sessions text, and connections nonempty
text. Compatible reentry retains values. Missing columns stay NULL, with no
backfill from historical verdicts or pane records and no new response authority.
The additions and stamp commit atomically; a refusal or failed hop retains v16.
This hop stamps 17; authorization names the actual starting version and the
current chain's target 20. The v16 input
tables, authority and import remain unchanged. Recovery is restore-only (§5).

### Worked example: v15 → v16

The administrator-only input cutover imports legacy launch/current/history
records into the shared input tables. The original current file takes precedence
over history after an interrupted legacy save. Exact normalized payloads and
raw proof bytes/provenance are retained; readable ambiguous/corrupt/conflicting
or bad-proof evidence remains an unresolved blocker. Unreadable evidence or
wrong-store/launch attribution refuses rather than assigning it to another owner.

The import, current/history ownership, `input_imports` source bytes/hash,
store-bound authority marker and v16 stamp share one transaction. FULL is
verified on its actual connection. Precommit files remain authoritative; after
commit SQLite is authoritative even if postcommit verification or retirement
fails. Reentry verifies committed authority and cannot reimport files over newer
SQLite state. Original launch-lock inodes, provider logs and transcripts survive;
retirement affects only imported operational JSON. Imported unknown confirmation
times are not invented. Fresh and migrated stores converge on the same schema.

Before any hop the administrator creates an exclusive stopped-writer backup
of the actual DB/WAL/SHM and complete legacy roots, with manifest and `COMPLETE`
written last. Lock files are retained in place, not copied; provider evidence
outside the roots is not rewritten or included automatically. Failure leaves
an incomplete backup for inspection, never a reusable destination. Recovery
is restore-only (§5), including later-write loss and uncertain-attempt
reconciliation before an older runtime restarts.

### Previous worked example: v14 → v15

`migrateV14toV15` (`codex_turn.go`) appends two nullable `spawns` columns,
`codex_turn TEXT` and `codex_turn_ended TEXT`, with no default and no
backfill, after v14's `pane_epoch`: the Codex turn a row's hooks recorded as
current and the latest one recorded as ended. Fresh `schemaDDL` creates the
same shape directly at v15, and the registered `{from: 14, apply:
migrateV14toV15}` step runs after the v13→v14 hop; authorization must match
the actual starting version and target 15. The runtime behaviour is in "The
Codex turn" under
[The agent-aware hook classifier](architecture.md#the-agent-aware-hook-classifier).

The hop invents no turn: an upgraded row records none until its agent's
next `UserPromptSubmit`, `Stop` or `Interrupt`, so a turn running across the
upgrade ends only by its own hook, as before. It validates first and changes
nothing until every existing column of these names passes: exactly that
name, type TEXT, nullable, no default and no primary key (otherwise
`ErrSchemaMismatch`, "v14→v15 incompatible spawns.<column>"), with every
value NULL or TEXT of 1 to `MaxCodexTurnBytes` (256) bytes (otherwise
"v14→v15 invalid spawns.<column>"). Compatible reentry keeps those columns
and values and adds only a missing one.

Validation, any ADD COLUMN, the v15 stamp and commit share one transaction.
A refusal or failed commit leaves the hop's v14 schema/data/version and
authorization intact; earlier committed hops remain, and rerunning the
supported installer refreshes authorization to the resulting version before
retry. The store id and all existing rows and columns in all seven tables
are preserved. Successful migration consumes authorization; reopening v15
does not replay it.

`schema_v15_test.go` covers preservation and reentry from no turn column,
one, or both (open and ended turns, a 256-byte turn), fresh/migrated
convergence, incompatible shape and value refusal, and commit rollback and
retry, and `TestV15RefusalFollowsInstallAdvice` follows the install skill's
"Authorized migration refuses Codex turn columns" advice step by step;
`TestAuthorizedMigrationFromEveryVersion` checks every older store
comes out with no turn recorded. Use the shared fixtures in
`migration_fixtures_test.go` (`v15ColumnSpecs`, `v15ColumnNames`,
`assertV15Defaults`, with `preAddV14Columns` for columns already present),
not new raw SQL, for any later change to these columns. Recovery is
restore-only (§5); no v15 reverse recipe is implemented.

### Previous worked example: v13 → v14

`migrateV13toV14` (`display_schema.go`) adds the columns behind the report-only
Displayed States (`idle_screen` for an unchanged working pane, and
`needs_intelligence`) and the permission requests' fallback clock.
On `spawns`:
`permission_epoch INTEGER NOT NULL DEFAULT 0`,
`observation_version INTEGER NOT NULL DEFAULT 0`, and nullable
`display_observation TEXT`, `display_exit TEXT`,
`idle_screen_timeout_seconds INTEGER` (the per-spawn override),
`pane_sha256 TEXT`, `pane_since_ms INTEGER` and `pane_epoch INTEGER` (the
committed pane baseline). On `permission_requests`, all nullable:
`owner_launch_token TEXT`, `owner_permission_epoch INTEGER`, `admitted_at
INTEGER`, `fallback_at INTEGER` and `fallback_basis TEXT`. Fresh `schemaDDL` creates
the same shape directly at v14, and the registered `{from: 13, apply:
migrateV13toV14}` step runs after the v12→v13 hop; authorization must match
the actual starting version and target 14. Existing migrations through v13
stay unchanged. The runtime behaviour is in
[Displayed State reporting](architecture.md#displayed-state-reporting).

The hop invents no history: no displayed episode, no observation, no fallback
clock, no process-death observation, no pane baseline and no override. The
pane and override columns start NULL, so an upgraded row uses each reader's
configured timeout and its first successful pane sample starts a fresh
period. The hop validates first, and changes nothing until every column of
these names that already exists passes. A case-conflicting name, a wrong
type, a primary key, or a nullability or default that differs from the list
above is refused with `ErrSchemaMismatch`. Existing values must have their
column's type, and integers must be non-negative; `display_observation` and
`display_exit` must decode as their strict bounded JSON; a `fallback_basis`
must be `process`, `deadline` or `admission`; and `fallback_at` and
`fallback_basis` must be both set or both NULL on every row. Compatible
reentry keeps all of these unchanged.

The one backfill is conservative ownership. Only if neither ownership column
already existed, a request of a spawn currently in `check_permission` acquires
that spawn's launch token and `permission_epoch`, and only when the row's
interval start is known (`state_changed_ms > 0`) and the request's
whole-second `created_at` falls in a later whole second than that start. Every
other request, including a same-second or earlier request that could predate
the interval and any request of a row with an unknown start, keeps NULL
ownership and never qualifies for the report. Its record, hold and fallback
clock stay unchanged; requests created after the upgrade qualify normally.

Validation, any ADD COLUMN, the backfill, the v14 stamp and commit share one
transaction. A refusal or failed commit leaves the hop's v13
schema/data/version and authorization intact. Earlier committed hops remain;
rerun the supported installer to refresh authorization to the resulting version
before retry. The store id and all existing rows and columns in all seven tables
are preserved. Successful migration consumes authorization; reopening v14 does
not replay it.

`schema_v14_test.go` covers physical-v13 preservation and reentry (pane and
override columns included), fresh/migrated schema convergence, commit
rollback and corrected retry, incompatible
partial-state refusal, no enrolment of earlier or unknown-start permission
intervals, no invented pane baseline or override, historic fallback
usability, and new admissions qualifying beside ambiguous history. It builds
its partial and physical-v13 stores through the shared fixtures in
`migration_fixtures_test.go` (`v14ColumnSpecs`, `preAddV14Columns`,
`seedV13PermissionFixture`); reuse them, not new raw SQL, for any later change
to these columns. Recovery is restore-only (§5); no v14 reverse recipe is
implemented, and the current-layout recovery text is exercised by
`test/install-sh/advice_follow.sh` and `current_layout_rollback_test.go`.
Targeted tests do not establish full-unit, installer, Docker or live
acceptance.

### Earlier worked example: v12 → v13

`migrateV12toV13` (`codex_startup.go`) appends nullable
`spawns.codex_startup TEXT` and `spawns.claude_capacity TEXT`, with no default
or backfill, after the three v12 family-history columns. Schema v13 shipped
with release 0.15.0; these are one hop, and existing migrations through v12 stay
unchanged. Fresh `schemaDDL` creates the same shape directly at v13. The registered `{from: 12, apply: migrateV12toV13}` step runs after
the separate runtime, eligibility and family-history hops; authorization
must match the actual starting version and target 13.

`codex_startup` stores a strictly validated JSON `CodexStartup`: launch token,
session id, absolute rollout path, writer PID and process start time. It
records admitted fresh-history origin, never token usage or a numeric
reading. `claude_capacity` stores strictly validated JSON `ClaudeCapacity`:
the same launch/session/path/process identity, version/provider, and optional
main/child model, positive window, response UUID, observation time and
invalidation marker. It contains capacity/provenance only; Claude usage is
read from the transcript on demand. Existing rows retain NULL, so migration
never qualifies an old or resumed rollout by its creation-only `cli_version`. Cursor remains unknown;
this migration adds no Cursor context admission, checkpoint reader or
usage persistence. See [Experimental context readers](architecture.md#experimental-context-readers)
for runtime admission and invalidation.

The hop probes both columns inside its transaction. Compatible reentry keeps
NULL and valid explicit markers unchanged; it does not synthesize or
refresh provenance. A case-conflicting name, non-TEXT type, NOT NULL,
primary key or any default is refused with `ErrSchemaMismatch`. Existing
non-NULL values must be TEXT containing exactly one JSON object with only
the known fields: a valid launch token, nonempty session (at most 256
bytes), absolute path (at most 4096 bytes), positive PID and nonempty start
time (at most 128 bytes). The complete JSON is bounded to 8192 bytes;
malformed, trailing, unknown-field, oversized or invalid values are refused.
Claude JSON is bounded to 128 KiB and 128 children, with version at most 64
bytes and provider `anthropic` or `unknown`. Each optional main/child entry
requires a model (at most 256 bytes), positive window, response UUID (at
most 128 bytes), and nonzero representable observation time. Child IDs are
at most 128 ASCII letters, digits, hyphens or underscores. Unknown fields,
trailing JSON, invalid bindings or entries and incompatible column shapes
are refused. Invalidated entries are valid provenance, not usable readings.

Validation, any ADD COLUMN, the v13 stamp and commit share one transaction.
A refusal or failed commit leaves the hop's v12 schema/data/version and
authorization intact. Earlier committed hops remain; rerun the supported
installer to refresh authorization to the resulting version before retry.
The store id and all existing rows/columns in all seven tables are preserved.
Successful migration consumes authorization; reopening v13 does not replay
it. A retained marker on reopen still requires current runtime bindings and
a live original writer before it can qualify any reading.

`schema_v13_test.go` covers physical-v12 preservation, absent/NULL/valid
partial columns, bounded marker reentry, incompatible shape/value refusal,
commit rollback and corrected retry, fresh/migrated convergence and reopen.
The shared migration matrices include the new step and every prior starting
version. Recovery is restore-only (§5); no v13 reverse recipe is implemented.
Targeted tests do not establish full-unit, installer, Docker or live acceptance.

### Earlier worked example: v11 → v12

`migrateV11toV12` (`family_schema.go`) appends `family_state`,
`family_settled_state` and `family_changed_ms` after `subagent_tracking`.
It validates compatible existing definitions and values, then initializes
family history from the stored tree without changing own-state history or
store identity. This remains a distinct registered hop before v12→v13.
Fresh DDL keeps the same column definitions and order before the two
context provenance columns. See [Family reporting](architecture.md#family-reporting)
for aggregation and initialization; `schema_v12_test.go` checks preservation,
reentry/refusal and the tested family-only v12 → v11 recovery recipe (§5).

### Historical worked example: v10 → v11

**CSCB upgrade caveat (2026-10-18):** The relay rewrite shipped in AD
0.12.0. CSCB 0.16.1 pins AD 0.12.0 but has incomplete handling of
delivery, dialog holds and pause recovery. An AD upgrade does not fill
those consumer gaps. Follow AD's documented
[delivery and recovery semantics](permissions.md#send-keys-interaction).
These disclosed gaps do not block AD releases.

Released 0.12.0 uses schema v9. The settled E1 v9→v10 hop creates the
seventh table, `cursor_runtimes`, validating any pre-existing table before
stamping v10 in its own transaction. The released v8→v9 rename hop and its
threshold 9 remain immutable. Task1 adds the separate registered v10→v11
hop; an authorized v9→v11 open runs both transactions. The sentinel must
match the actual starting version and target 11.

`migrateV10toV11` (`request_eligibility.go`) appends
`permission_requests.main_turn_end_eligible INTEGER NOT NULL DEFAULT 0`,
after `proven_gone_how`. The column's DDL, conditional backfill, validation,
version stamp and commit share one transaction. Only a newly added column
is backfilled: requests with `agent_id IS NULL` and an existing owning spawn
whose `agent IS NOT 'cursor'` become eligible. This NULL-safe comparison
preserves supported historical NULL/malformed non-Cursor agents without
selecting a public default agent. Historical children, all historical Cursor
requests and orphan requests remain false.

A compatible existing column keeps every valid explicit value, including
false for a non-Cursor main request; reentry never recomputes eligibility.
The hop refuses case-conflicting names, incompatible INTEGER/null/default/
primary-key shape, non-integer or out-of-range values, and true with a known
child id. Such refusals wrap `ErrSchemaMismatch`. Any failure rolls back the
hop, preserving its v10 version, data and authorization for corrected retry.
A chain may already have committed earlier hops; per-hop rollback does not
reverse those earlier commits. After successful authorization consumption,
reopen at v11 does not replay migration.

Fresh DDL retains `cursorRuntimeDDL`, the identical eligibility column,
v12 family history, v13 startup/capacity provenance, v14 display
observation and ownership columns and v15 Codex turn columns.
`schema_v11_test.go` covers physical-v10 preservation,
NULL/malformed historical agents, Cursor/child/orphan false, compatible
reentry, incompatible shape/value refusal, injected post-DDL backfill failure
and corrected retry, fresh/migrated convergence and restart. The dynamic
schema tests cover every older starting version. `schema_v10_test.go` retains
physical-v9/v10 runtime-table checks. Targeted behavioral results do not
establish full-unit, installer, external TLC/Docker or live acceptance.

**Later columns of the same release.** Ordinarily, a change that ships in the same
release as a version not yet released adds its columns to that version's hop
and `schemaDDL`, rather than a new version: schema v7 is the one migration of
b.146 steps 2, 2b and 2c, which ship together, and a later step of that
release appends its columns to `v7Columns` (`schema.go`) and the same text to
`schemaDDL`, and extended that release's supported v7 → v6 recipe (§5) and its
test copy. For a restore-only version, retain the documented recovery exception
instead: v14's pane baseline and override columns joined its permission
columns this way, and v14 stays restore-only. Once a version has shipped, its
hop never changes. The settled Cursor runtime, proof and context provenance
changes explicitly use distinct v9→v10, v10→v11, v11→v12, v12→v13,
v13→v14 and v14→v15 hops; do not fold them into earlier hops.

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
column's table, name and exact `schemaDDL` text: nineteen `ALTER TABLE
permission_requests ADD COLUMN` (the relay hook's `hook_pid`,
`hook_starttime` and `hook_pidns`; `tool_use_id`, `agent_id`,
`delivered_at`, `settled_at`, `hook_gone_at`, `attempted_decision`,
`attempted_at`; `pane_answer TEXT NOT NULL DEFAULT 'none'`, `pane_as`, the
pane-answer sender's `pane_sender_pid`, `pane_sender_starttime` and
`pane_sender_pidns`, and `pane_intent_at` INTEGER, when a pane answer's
intent was written; then `closed_at` INTEGER, when a close of the
request's spawn closed it (find-missing's mark, the terminal SessionEnd's
move to `ended`, or resume's move to `pending`); then `proven_gone_at`
INTEGER and `proven_gone_how` TEXT, when and how Claude Code proved the
request's permission dialog gone (b.146 step 2c: `tool_ran`, `turn_end`
or `agent_gone`); the instants in
milliseconds since the epoch), appended after `created_at`, then `ALTER
TABLE spawns ADD COLUMN idle_since`, appended after `launch_owner_pidns`.
Each is guarded by a `pragma_table_info` probe of its own table and skipped
when the column is already there. Any probe or `ALTER` failure rolls the
whole hop back: `user_version` stays 6 and none of the twenty columns
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
kept. `migrateV6toV7` has a phase 3 of one statement. Its `ADD COLUMN`s
give every existing permission request NULL in each new column but
`pane_answer`, which takes its default `none`, and every row NULL
`idle_since`. A request with no `settled_at` is one recorded before v7, so
readers and `decide` judge it as before the upgrade, by its `created_at`
and the relay window. A NULL `closed_at` means no close closed the
request: an existing request that a pre-v7 mark denied keeps its
`find_missing` deny, which already makes it no longer await an answer, so
nothing needs backfilling. An existing request still undecided on a row
that is already `ended` is not closed by the hop either: resume's move to
`pending` closes it (denied, `decision_reason` `ended`) before the row's
next launch. Then phase 3 (`v7ProveFinishedSQL`, after every column and
before the stamp, in the same transaction) backfills one fact: every
request of a row already `ended` or `missing` with NULL `proven_gone_at`
is proven gone, `proven_gone_how` `agent_gone` and `proven_gone_at` the
hop's one instant, as the close of a Spawn's requests proves them when a
row ends from v7 on. A finished row has no agent left, so none of its
dialogs is on a pane; without the backfill its requests would read
unproven (`get-permission`'s `unproven_since`) until a resume of the row
proved them. It writes only where `proven_gone_at`
is NULL, so a second run rewrites nothing, and it sets nothing else: an
undecided request of a finished row stays undecided. A request on a live
row keeps NULL `proven_gone_at` and is proven by its agent's next `Stop`
or idle-prompt Notification (it has no `agent_id`) or by the row's end.
No other existing value is rewritten, and `store_meta` and its store id
are kept. A failure of the backfill rolls the whole hop back, like a
failed `ALTER`: `user_version` stays 6, none of the twenty columns exists
and no request is proven.

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
with a pane answer, or with its row (the agent ends, or shared recovery
marks it `missing` on a state read or `find-missing`). The refusal's
`err_details` name the request (`request_token`) and the agent's other open requests. `record-pane-answer`
is the way out: `decide` records no verdict on such a request, and once
the agent has left `check_permission` or recorded a later request it
returns `ErrNoOpenPermissionRequest` for it ("do not answer it at the
pane"), while plain `send-keys` is still refused with `ErrRelayFallenBack`
on its account. Its PostToolUse does not close it either: no `tool_use_id`
was recorded for it.

Every request recorded before v7 on a live row, decided or not, also has
no proof that its permission dialog is gone (`proven_gone_at` NULL). From
v7 on, `send-keys` refuses a plain call without `--expect-pane-sha256`
with `ErrDialogMaybeOpen` while any request of the agent is unproven, so
once the store is migrated every live relay-on agent with a request from
before the upgrade is held, until its main agent's next `Stop` or
idle-prompt Notification (such a request has no `agent_id`) or the row's
end. Closing the request with `record-pane-answer` does not lift this
hold. An agent already idle at its prompt, its idle-prompt Notification
already sent, sends neither until a new turn starts, so its first plain
`send-keys` needs a person or an LLM to `read-pane` and send with that
read's `pane_sha256` (never passed from an automatic flow). The requests
of a row already `ended` or `missing` are proven gone (`agent_gone`) by
the migration itself (phase 3 above), not later at its resume, so they
never read unproven.

`pause` is held as a plain `send-keys` without `--expect-pane-sha256` is,
so each request above that refuses such a `send-keys`, with
`ErrRelayFallenBack` or `ErrDialogMaybeOpen`, refuses `pause` to its agent
too. `pause` takes no pane hash, and a pane hash gets a plain `send-keys`
past `ErrDialogMaybeOpen` only, never past `ErrRelayFallenBack`. To stop
such an agent, `kill` it, or have a person or an LLM `read-pane` and first
close a fallen-back request (`record-pane-answer` as above, or a pane
answer with its `--request-token` if its prompt still waits), answer any
other dialog on the pane, `read-pane` again and check the prompt is empty,
send `/exit` with `send-keys` and that read's `pane_sha256`, and poll `get`
or `status` until the row is `ended` (see "Send-keys interaction" in
docs/permissions.md).

Session history belongs to a life: each `session_history` entry carries the
life of the id that was current when its session ran, and after the v5 hop
every existing entry is in its row's current life (life 0). Reuse
(`spawn` with the reuse opt-in on a finished id) starts a new life only
when the exact stored agent changes: its reset advances `life_number` by
one, and an applied failed-launch restore sets it back. Same-agent reuse
keeps the life and its visible history. No other write changes it. The
first agent-changing reuse after the migration starts life 1; rows without
an agent-changing reuse retain their history in life 0. `resume`
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
- **Backfill UPDATE** — select only rows not yet backfilled, so a re-run
  rewrites nothing: `migrateV6toV7`'s `v7ProveFinishedSQL` sets
  `proven_gone_at` only `WHERE proven_gone_at IS NULL`, and a second run
  keeps the first run's instant.

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
   schemaVersion)`) and opens with `openMigrationTestStore` from
   [`migration_fixtures_test.go`](../internal/store/migration_fixtures_test.go).
   That adapter supplies the test-owned input-migration window; ordinary
   refusal tests still use `Open`. It asserts `user_version ==
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
  `breakV5StoreMetaStep`, `breakV6PIDNSColumn`, `breakV7IdleSinceColumn`
  and, for a phase 3, `breakV7Backfill` do), and the text the open's error
  must name. The test asserts a migration
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
  `v6ColumnSpecs`, `v7ColumnSpecs`, `v8ColumnSpecs` and `v9ColumnSpecs`), check where the new
  columns sit in each table (v9's: `agent`, `end_reported_at` and
  `subagent_tracking`, last on `spawns`; v8's: after v7's
  `idle_since`; v7's: after `created_at` on `permission_requests`), and add
  any new table or index to its list of names. Neither test picks up a new
  version's columns on its own.
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
request (its `created_at` long past any relay window) and a decided one,
and an `ended` and a `missing` row with the same two requests each. After
the hop every v6 value of the live row and both its requests reads back
as seeded, the new columns hold their defaults (`assertV7Defaults`), and
the store id is kept. Both requests read as recorded before v7 (no settle
instant, hook identity, ack or `tool_use_id`; `pane_answer` `none`), only the
undecided one still awaits an answer, and `decide`'s guarded write still
refuses it once its `created_at` is past the cutoff: a request from before
the upgrade is judged by time, as before. Its phase 3 test,
`TestV7MigrationProvesFinishedRowsRequests`, migrates the same fixture and
checks that every request of the `ended` and `missing` rows is proven
`agent_gone` at one instant within the hop, its v6 values kept (the
undecided one still undecided and awaiting an answer), that the live
row's requests stay unproven, and that a second run of the hop changes
nothing (`dbDump` before and after).

**Where these tests run:** `internal/store` carries the sandbox guard
(`sandboxguard.Require()` in its `TestMain`). Run them only in the sandbox —
see §5.

## 4. `createSchema` must always be the latest schema

Restating the two-places rule because it is the most common way a migration
goes wrong: **fresh databases never replay hops.** The current v20 fresh DDL
includes `cursorRuntimeDDL`, the eligibility column, the three v12
family-history columns, nullable `spawns.codex_startup TEXT` and
`spawns.claude_capacity TEXT`, the eight `spawns` and five
`permission_requests` columns of the v14 display observation and
ownership, and nullable `spawns.codex_turn TEXT` and
`spawns.codex_turn_ended TEXT`; the v9→v10, v10→v11, v11→v12, v12→v13,
v13→v14 and v14→v15 hops use the same respective definitions; v16 shares
`inputDeliveryDDL` and commits its input authority with fresh creation. The v17
permission columns match `v17PermissionColumns`; v18 shares
`cursorObligationsDDL` with its hop; the three v19 evidence columns match
`v19EvidenceColumns`, followed by v20's nullable `display_evidence TEXT` and
`observation_writer TEXT` (`v20Columns`). Future changes must update
both fresh DDL and the next hop. The §3 step 4
`schemaShape` comparison guards that both paths land in the same place. If you
only touch the hop, fresh installs are stuck on the old schema; if you only
touch `schemaDDL`, upgrades never happen.

## 5. The b.8dr rule — schema-bumping branches run ONLY in the sandbox

`ensureSchema` no longer auto-migrates on open — the sentinel gate (§1a) refuses
an older DB with `ErrSchemaMigrationRequired` unless an administrator
authorization is present. **That gate does not make the sandbox rule optional.**
Two open-time DB writes remain, and both are reachable from a test run:

- `createSchema` writes the full latest schema to a fresh, empty
  `user_version==0` store. A nonempty version-zero store is refused; fresh DBs
  are stamped at the branch's new version.
- The migration chain runs with an exact sentinel and the required
  input-migration window. Migration fixtures supply both through
  `openMigrationTestStore`; ordinary `Open` still refuses the cutover. A test
  that targets the real store with those authorizations can migrate it.

So the instant a binary or test built from a schema-bumping branch opens a store
under the right conditions, it can rewrite that store to the new version — there
is no dry-run and no confirmation once authorization is in play.

That is exactly the b.8dr incident. Running `bun test` on the b.aaj branch
(schema v3 migration code) on the host migrated the real
`~/.agent-director/state.db` (agent-director's directory then; it is
`~/.config/agent-director` now) from v2 to v3, breaking every consumer of the
installed v0.7.8 binary (which only understands v2 and now hits
`ErrSchemaMismatch`). Host `$HOME` redirection does **not** save you: a test
that does not redirect it, an absolute path to the real store, or a child
binary started with the host's environment still opens the real
`~/.config/agent-director/state.db`. A container whose HOME has neither
`.config/agent-director` nor the old `.agent-director` (a host not yet moved
still has it) is the only isolation boundary that holds.

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

Only use an in-place reverse procedure for a schema version with an explicitly
supported and tested recipe below. The historical b.8dr recovery pattern for a
hop whose only forward change was an added column was:

```sql
-- against the over-migrated state.db, e.g. ~/.config/agent-director/state.db
ALTER TABLE <table> DROP COLUMN <added_col>;   -- reverse the v(N-1)→vN DDL
PRAGMA user_version = <N-1>;      -- stamp back to the version the old binary reads
```

This illustrates the historical pattern, not a recipe for v9 through v20. Never
merely reset the version pragma. Confirm the starting version and use only the
matching tested recipe before letting an older binary open the file. The store
does not downgrade automatically.

The recipes below go back one version each, newest first. To go back more
than one version, run them in order from the store's version down, each one
only once the one before it has stamped the store: a v8 store goes to v6 by
the v8 → v7 recipe, then the v7 → v6 recipe, and to v5 by those two, then the
v6 → v5 recipe. Each recipe assumes the store is at its own starting version
and changes nothing a newer version added, so never skip one.

**Current recovery policy.** Schemas v20, v19, v18, v17 and v16 are restore-only.
The v20 display columns and v19 evidence columns have no reverse recipe:
restore the stopped-writer pre-upgrade set, losing all later writes, and
never drop the columns or reset the version. Display evidence and the
observation writer token are freshness and commit-confirmation metadata only;
they carry no authority, delivery or proof fact. A v19 binary
refuses a v20 store with `ErrSchemaMismatch`. The v18
Cursor input/proof records preserve accepted work and uncertain issue evidence;
dropping them cannot establish non-delivery or authorize replay. The v17
permission origin, owner and one-use response claims have no reverse recipe;
use the stopped-writer pre-upgrade recovery set and matching binary. For v16,
neither a reverse DDL recipe nor a replayable file-journal export can preserve native input
authority and uncertainty. Keep all writers excluded, restore the matching
pre-upgrade DB/WAL/SHM and legacy evidence, preserve original lock inodes and
use the matching prior binary. Later writes are lost; reconcile uncertain
attempts against retained post-upgrade evidence before any older runtime starts.
Never reset `user_version`, synthesize a replayable journal or infer non-delivery
from missing restored proof. The cutover's completed backup supplements the
operator's copies; it does not cover earlier directory/config rewrites or
external provider evidence. Without the matching recovery set, stop for help.

Schemas v15, v14 and v13 also have no supported reverse recipe. The v12→v11
recipe below is only for genuine family-only v12, never v13–v20 or a hand-reset
store. No supported reverse path crosses v11→v10 or v10→v9. The v8-and-older
recipes remain limited to their stated starting versions. The complete
current-layout and directory-move procedure is owned by
[Going back to the previous release](operator-actions.md#going-back-to-the-previous-release).

There is no v9 → v8 recipe. The v8 → v9 hop (`migrateV8toV9`, b.ssz) renames
six columns, adds three columns to `spawns` (`agent`, `end_reported_at` and
`subagent_tracking`), backfills `agent` and creates the `subagents` and
`subagent_launches` tables, so no recipe reaches a v9 store; the way back is a copy of
`state.db` (with its `-wal` and `-shm` files) that you took yourself before
installing. The installer does not make that copy.

#### v12 → v11 (reverses `migrateV11toV12`)

Use this only on a genuine family-only v12 store whose `PRAGMA user_version;`
prints `12`, to allow a schema-v11 binary to open it. A v13–v20 store cannot use
this recipe, even if someone resets its version: the context provenance
columns (and, from v14, the permission columns; from v15, the Codex turn
columns, v16 input authority/tables, v17 permission provenance, v18 Cursor
evidence tables, v19 state/process evidence and v20 display evidence) would
remain and the result would not match schema v11. Stop every agent, server and other caller
that can hold or write the store. Back up the actual configured `state.db`
and its `-wal` and `-shm` companions before changing anything. Use SQLite
3.35.5 or later, as required by the older `DROP COLUMN` recipes below.

Save this as `recipe.sql`, then run
`sqlite3 -bail /path/to/configured/state.db < recipe.sql`, replacing the
path with the actual configured store:

```sql
.bail on
BEGIN;
ALTER TABLE spawns DROP COLUMN family_changed_ms;
ALTER TABLE spawns DROP COLUMN family_settled_state;
ALTER TABLE spawns DROP COLUMN family_state;
PRAGMA user_version=11;
COMMIT;
```

The three columns have no index or constraint preventing `DROP COLUMN` in
the v12 schema. Their removal and the final stamp share one transaction.
If any statement fails, bail mode exits without committing; restore the
backup if the store has been changed outside that transaction. Never stamp
the version alone or apply an older recipe directly to v12.

Confirm `PRAGMA user_version;` prints `11` and `.schema spawns` has none of
the three family columns before installing the schema-v11 binary. This
deletes only family aggregate history: every row, stored relationship,
own-state settle value and clock, permission record, session history entry
and store id remains. A later authorized v11 → v12 migration initializes
family history afresh from the current raw stored tree. The recovery test
`TestV12RecoveryRecipePreservesOwnHistoryAndRemigrates` checks the reverse
schema and preservation/remigration behavior.

This recipe stops at v11. To return to schema v10 or v9, restore the
matching stopped-writer pre-upgrade backup and use that release's binary
and installer; the v11→v10 and v10→v9 exceptions still apply.

#### v8 → v7 (reverses `migrateV7toV8`)

Use this to roll a migrated store back so a v7 binary (the releases before
schema v8) can open it. Order of operations:

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
6. Put the previous binary back. Do this only after the store is v7: the old
   release's `install.sh` opens the store, and on a v8 store it fails with
   `ErrSchemaMismatch`.
7. Start the agents again on the old binary.

```sql
.bail on
-- against the v8 state.db, e.g. ~/.agent-director/state.db
BEGIN;
ALTER TABLE spawns DROP COLUMN settled_state;
ALTER TABLE spawns DROP COLUMN state_changed_ms;
PRAGMA user_version = 7;
COMMIT;
```

That is exactly what `migrateV7toV8` adds: `settled_state` and
`state_changed_ms` on `spawns`. Drop both and no other column or table;
`store_meta` and its store id stay, so labels written before the rollback
still read as this store's. `PRAGMA user_version = 7` is the last statement
before `COMMIT`. `PRAGMA user_version;` should then print `7`, and `.schema
spawns` should show neither column. A v7 binary then opens the store.

SQLite refuses `DROP COLUMN` on a column that is a PRIMARY KEY, has a UNIQUE
constraint, is indexed, appears in a CHECK or foreign-key constraint, or is
used by a generated column, trigger or view. Neither column is any of these in
the v8 DDL: each is a plain `NOT NULL` column with a default, and the three
`spawns` indexes cover only `state`, `last_seen_at` and `parent_id`. So every
statement in the recipe succeeds on a v8 store. If one fails, bail mode stops
the CLI at that statement. `COMMIT` never runs, the open transaction is rolled
back when the CLI exits, and the store is still v8; check the schema for a
hand-added index or constraint before you retry. Without bail mode the CLI
reports the error, runs the remaining statements and commits them, leaving a
partial downgrade stamped v7. If that happens, restore the copy from step 2.

The recipe deletes no row. What is lost is each row's settled state and the
time of its last state change. A v7 binary has no settle time and reports
every row's raw state, so the loss changes nothing it reads.

**If the store is later migrated to v8 again**, the hop gives every row
`settled_state` its own state and `state_changed_ms` 0 (the hop's backfill), so
every row reads settled at once, whatever `[state] settle_ms` is, until its
next state change. The store id is kept, so no label changes owner.

#### v7 → v6 (reverses `migrateV6toV7`)

Use this to roll a migrated store back so a v6 binary (the releases before
schema v7) can open it. On a v8 store, run the v8 → v7 recipe above first.
Order of operations:

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
ALTER TABLE permission_requests DROP COLUMN proven_gone_at;
ALTER TABLE permission_requests DROP COLUMN proven_gone_how;
ALTER TABLE spawns DROP COLUMN idle_since;
PRAGMA user_version = 6;
COMMIT;
```

That is exactly what `migrateV6toV7` adds: nineteen columns on
`permission_requests` and `idle_since` on `spawns`. Drop all twenty and no
other column or table; `store_meta` and its store id stay, so labels written
before the rollback still read as this store's. `PRAGMA user_version = 6` is
the last statement before `COMMIT`. `PRAGMA user_version;` should then print
`6`, and `.schema permission_requests` and `.schema spawns` should show none
of the twenty columns. A v6 binary then opens the store.

SQLite refuses `DROP COLUMN` on a column that is a PRIMARY KEY, has a UNIQUE
constraint, is indexed, appears in a CHECK or foreign-key constraint, or is
used by a generated column, trigger or view. None of the twenty is any of
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
`pane_intent_at`), when a close of its spawn closed it (`closed_at`), when
and how its dialog was proven gone (`proven_gone_at`, `proven_gone_how`),
and each row's `idle_since`. A v6 binary has no dialog hold, so the loss
changes nothing it reads. A v6 binary judges every request by its
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
`idle_since`, then its phase 3 (§2) proves gone again (`agent_gone`, at the
re-migration's instant) every request of a row that is `ended` or
`missing` by then. Every request reads as one recorded before schema v7
and falls back by its `created_at` and the relay window, an undecided one
refuses plain `send-keys` until it is closed, and every one of a live row
is unproven again, so it holds a plain `send-keys` without a pane hash to
its agent until its next proof (see "What the upgrade changes for an
existing request" in §2). The store id is kept, so no label changes owner.

#### v6 → v5 (reverses `migrateV5toV6`)

Use this to roll a migrated store back so a v5 binary (0.11.x, the releases
before schema v6) can open it. On a v7 store, run the v7 → v6 recipe above
first, and on a v8 store the v8 → v7 recipe before that. Order of
operations:

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
on a v7 store the v7 → v6 recipe before that, and on a v8 store the v8 → v7
recipe before those. Order of operations:

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
  its next agent-changing reuse.
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

- `internal/store/codex_turn.go` — guarded v14→v15 Codex turn hop
  (`migrateV14toV15`, `MaxCodexTurnBytes`).
- `internal/store/schema_v15_test.go` — preservation, reentry, refusal and
  rollback/retry checks; fixtures (`v15ColumnSpecs`, `v15ColumnNames`,
  `assertV15Defaults`) in `internal/store/migration_fixtures_test.go`.
- `internal/store/display_schema.go` — guarded v13→v14 display observation
  and ownership hop, with conservative ownership backfill.
- `internal/store/schema_v14_test.go` — preservation, reentry, refusal,
  rollback/retry and ownership checks; fixtures in
  `internal/store/migration_fixtures_test.go`.
- `internal/store/codex_startup.go` — guarded v12→v13 startup provenance hop
  and bounded `CodexStartup` validation.
- `internal/store/claude_capacity.go` — Claude capacity addition in the same
  v13 transaction, bounded metadata validation and gated runtime writes.
- `internal/store/schema_v13_test.go` — preservation/no-backfill, reentry,
  refusal, commit rollback/retry, convergence and reopen checks.
- `internal/store/family_schema.go` — guarded v11→v12 family-history hop.
- `internal/store/schema_v12_test.go` — family initialization, preservation,
  compatible reentry/refusal and genuine-v12 recovery/remigration checks.
- `internal/store/request_eligibility.go` — guarded v10→v11 proof metadata hop.
- `internal/store/schema_v11_test.go` — eligibility migration, reentry, refusal,
  rollback/retry and NULL-safe historical compatibility checks.

- `internal/store/cursor_runtime.go` — `migrateV9toV10` and
  `cursorRuntimeDDL`, shared by the hop and fresh-store DDL.
- `internal/store/schema_v10_test.go` —
  `TestV10MigrationReentryAndPreservation` and
  `TestV10MigrationRefusalRollback` (§1a's historical worked example).

- `internal/store/schema.go` — `ensureSchema`, `runMigrationChain`,
  `migrationSteps`/`migrationStep`, `migrationStepFrom`, `createSchema`,
  `schemaDDL`, `migrateV1toV2`, `migrateV2toV3`, `migrateV3toV4`,
  `migrateV4toV5`, `migrateV5toV6`, `migrateV6toV7` with its column list
  `v7Columns` and its phase 3 `v7ProveFinishedSQL`, `migrateV7toV8` with its
  column list `v8Columns` and its phase 3 `v8BackfillSQL`, `migrateV8toV9` with
  its renames `v9Renames`, its column list `v9Columns` (`agent`,
  `end_reported_at`, `subagent_tracking`), its subagent tables
  `v9SubagentTablesSQL` (the hop's own copy of the CREATE text for
  `subagents`, `subagent_launches` and the claim index, which `schemaDDL`
  repeats for a fresh store) and its backfill `v9BackfillSQL`,
  `buildMigrationRefusal`.
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
  `TestDowngradeRecipe_MatchesGuide` parses the five recipe SQL blocks of
  §5, "v12 → v11", "v8 → v7", "v7 → v6", "v6 → v5" and "v5 → v4", and fails
  when any differs from its test copy (`v12ToV11RecipeStatements`,
  `v8ToV7RecipeStatements`,
  `v7ToV6RecipeStatements`, `v6ToV5RecipeStatements`,
  `v5ToV4RecipeStatements`) or lacks the `.bail on` / `BEGIN;` / `COMMIT;`
  frame, so no recipe here can drift from the statements the tests run; it
  is also the test that pins the v12 → v11, v8 → v7, v7 → v6 and v6 → v5
  recipes to the guide.
  `TestDowngradeRecipe_KeepsRowsThenRemigratesToDefaults` takes a v8 store
  from `makeV8Fixture` back to v5 first (`v8ToV7RecipeStatements`, then
  `v7ToV6RecipeStatements`, then `v6ToV5RecipeStatements`), applies
  `v5ToV4RecipeStatements` to its reused and opted-out rows,
  checks every v4 column and history entry survives, then re-migrates and
  checks the v5 and v6 columns take their defaults (pre-trust opt-out and
  life numbers are lost; no launch owner).
- `internal/store/schema_v6_test.go` — v6's own cases (§3):
  `TestV6MigrationKeepsV5Rows` (`makeV5Fixture` migrated: every v5 value
  kept, no launch owner, the store id kept) and
  `TestDowngradeV6ToV5_KeepsRowsThenRemigrates`, which takes a v8 store from
  `makeV8Fixture` back to v6 (`v8ToV7RecipeStatements`, then
  `v7ToV6RecipeStatements`), applies `v6ToV5RecipeStatements` to
  it holding a `pending` row with a recorded owner, checks the v5 store keeps
  every other value and the store id and is refused with
  `ErrSchemaMigrationRequired`, then re-migrates and checks the row has no
  owner.
- `internal/store/schema_v7_test.go` — v7's own cases (§3):
  `TestV7MigrationKeepsV6Rows` (`makeV6Fixture` migrated: every v6 value of
  the live row and its requests kept, the v7 columns at their defaults through
  `assertV7Defaults`, the store id kept, and both requests judged as recorded
  before v7), `TestV7MigrationProvesFinishedRowsRequests` (phase 3: the
  `ended` and `missing` rows' requests proven `agent_gone` at one instant,
  the live row's unproven, a second run changing nothing) and
  `TestDowngradeV7ToV6_KeepsRowsThenRemigrates`, which takes a v8 store from
  `makeV8Fixture` back to v7 (`v8ToV7RecipeStatements`), then applies
  `v7ToV6RecipeStatements` to it, holding a request the relay hook
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
  its last column), `breakV7Backfill` (a trigger that fails every update of
  a permission request, so the hop fails at its phase 3, after every
  column), `makeV6Fixture` (the v6 store with a relay row in
  `check_permission`, an open request and a decided one, and an `ended`
  and a `missing` row with the same two requests, returned as a
  `v6Fixture`) and `v7ToV6RecipeStatements`, which must match the v7 → v6
  recipe statement for statement (checked by
  `TestDowngradeRecipe_MatchesGuide`). The v8 helper:
  `v8ToV7RecipeStatements`, which must match the v8 → v7 recipe statement
  for statement (checked by `TestDowngradeRecipe_MatchesGuide`).
- `internal/store/testdata/schema_v1.sql` — the v1 fixture `makeVersionedDB`
  builds every older version from.
- docs/engineering-guide.md §10 — sandboxed execution, the b.8dr incident.
