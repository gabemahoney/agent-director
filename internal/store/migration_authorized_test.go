package store

// migration_authorized_test.go — the SR-1 SUCCESS path: an exact-match
// administrator authorization sentinel authorizes a chained migration in a
// single open, the sentinel is consumed (deleted) as part of the same logical
// operation, and an `ad.schema.migrated` audit line is emitted with the exact
// `migrated <from>→<to>, consumed authorization` phrasing. A reopen after
// consumption is a clean no-op (no sentinel recreated, no further migration, no
// duplicate audit line).
//
// Audit assertions use the checkpoint/delta pattern established in
// trail_emit_test.go: capture len(readStoreTrailLines) before the open under
// test and assert only on lines added since. TestMain (store_test.go) pins
// AGENT_DIRECTOR_STATE_DIR so the trail singleton writes to storeTrailDir.
//
// Shared fixtures (makeV1DB / makeVersionedDB / stampUserVersion /
// readUserVersion / writeSentinel / assertSentinelPresent / assertSentinelAbsent
// / sentinelExists) live in migration_fixtures_test.go. The refusal-path
// siblings live in migration_gate_refusal_test.go. This file modifies neither.
//
// JOINT OWNERSHIP NOTE (PM Q6 / t2.93m.i5.ck AC): the real end-to-end
// {"from":1,"to":3} v1→v3 chain cannot execute until Epic t1.93m.fn lands
// schemaVersion=3 and appends the v2→v3 migrationStep. That end-to-end bullet is
// JOINTLY OWNED with t1.93m.fn. At today's schemaVersion=2 this file proves the
// chain-loop mechanics two ways: (1) the real v1→v2 transition driven THROUGH
// runMigrationChain (not a direct migrateV1toV2 call), and (2) a white-box
// synthetic multi-step test that temporarily extends the package-level
// migrationSteps table to prove ≥2 chained steps run in ONE authorized open with
// exactly ONE sentinel consume. The synthetic test never touches fresh-create
// DDL and never adds a fake step to the production table beyond its own
// save/restore window.

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
)

// schemaMigratedAt reads trail lines added after prevCount and returns the
// ad.schema.migrated lines among them (the audit line consumeAuthorization
// emits on a successful consume).
func schemaMigratedAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.schema.migrated" {
			out = append(out, row)
		}
	}
	return out
}

// schemaDeleteFailedAt reads trail lines added after prevCount and returns the
// ad.schema.authorization_delete_failed lines among them (the loud, fail-open
// event consumeAuthorization emits when the post-commit sentinel delete fails).
func schemaDeleteFailedAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.schema.authorization_delete_failed" {
			out = append(out, row)
		}
	}
	return out
}

// TestAuthorizedMigration_ExactMatchConsumesAndAudits covers AC bullet 1: a v1
// DB beside an exact-match {"from":1,"to":schemaVersion} sentinel opens
// successfully, lands at schemaVersion, the sentinel is deleted, and exactly one
// ad.schema.migrated audit line with the required message is emitted.
//
// Not parallel: it asserts on the shared trail file via checkpoint/delta, and
// keeping it serial keeps the delta window tight and unambiguous.
func TestAuthorizedMigration_ExactMatchConsumesAndAudits(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV1DB(t, dir)
	writeSentinel(t, dir, 1, schemaVersion)
	assertSentinelPresent(t, dir)

	before := len(readStoreTrailLines(t))

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(authorized v1): %v", err)
	}
	defer s.Close()

	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("post-migration user_version = %d; want %d", got, schemaVersion)
	}
	assertSentinelAbsent(t, dir)

	migrated := schemaMigratedAt(t, before)
	if len(migrated) != 1 {
		t.Fatalf("want 1 ad.schema.migrated after checkpoint %d; got %d", before, len(migrated))
	}
	row := migrated[0]
	wantMsg := fmt.Sprintf("migrated %d→%d, consumed authorization", 1, schemaVersion)
	assertTrailStr(t, row, "event", "ad.schema.migrated")
	assertTrailStr(t, row, "source", "ad_store_schema")
	assertTrailStr(t, row, "message", wantMsg)
	if got, ok := row["from"].(float64); !ok || int(got) != 1 {
		t.Errorf("[from] = %v; want 1", row["from"])
	}
	if got, ok := row["to"].(float64); !ok || int(got) != schemaVersion {
		t.Errorf("[to] = %v; want %d", row["to"], schemaVersion)
	}
}

// TestChainMechanics_CurrentVersionThroughLoop covers AC bullet 2 (real-version
// half): the v1→v2 transition is driven THROUGH runMigrationChain (the authorized
// open path), not a direct migrateV1toV2 call, and lands the DB at schemaVersion
// with the sentinel consumed.
//
// JOINT OWNERSHIP: the full v1→v3 chain is owned with Epic t1.93m.fn (schema v3);
// see the file header. This asserts loop mechanics at today's schemaVersion=2.
func TestChainMechanics_CurrentVersionThroughLoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV1DB(t, dir)
	writeSentinel(t, dir, 1, schemaVersion)

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(authorized v1 through chain): %v", err)
	}
	defer s.Close()

	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("chain landed user_version = %d; want %d", got, schemaVersion)
	}
	assertSentinelAbsent(t, dir)
}

// TestChainMechanics_SyntheticMultiStep covers AC bullet 2 (white-box half): it
// proves the engine is a true multi-step loop by TEMPORARILY replacing the
// package-level migrationSteps table with a synthetic two-step chain and driving
// the REAL runMigrationChain through ≥2 iterations in one call. This is the only
// place in the suite where runMigrationChain's own loop executes more than one
// hop, so it is what would catch the SR-1.2 return-after-one-hop hazard.
//
// Mechanics: runMigrationChain loops `while version < schemaVersion` (a const =
// 2). We therefore start the DB at user_version=0 and register synthetic
// stamp-only steps from:0 (0→1) and from:1 (1→2); a single runMigrationChain(db,
// 0) must loop TWICE through the real engine to reach schemaVersion=2. Using
// from:0 in the chain is safe: the fresh-create (version==0) branch lives in
// ensureSchema, not in runMigrationChain, so we never touch fresh-create DDL.
// The synthetic steps only stamp user_version — no DDL, no schema shape — and
// the production step table is saved/restored around the window.
//
// NOT parallel and NOT run under t.Parallel siblings: it mutates the package-
// level migrationSteps slice; a save/restore via t.Cleanup keeps the production
// table pristine for every other test in the package.
func TestChainMechanics_SyntheticMultiStep(t *testing.T) {
	// Save and restore the production step table around the mutation window.
	savedSteps := migrationSteps
	t.Cleanup(func() { migrationSteps = savedSteps })

	// A DB stamped to user_version=0 that must climb to schemaVersion (2) via two
	// synthetic steps (0→1, 1→2). Each step only stamps user_version so we avoid
	// any DDL and keep the test about loop mechanics, not schema shape.
	dir := t.TempDir()
	dbPath := makeVersionedDB(t, dir, schemaVersion)
	stampUserVersion(t, dbPath, 0)

	var stepCalls []int
	mkStep := func(from int) migrationStep {
		return migrationStep{
			from: from,
			apply: func(db *sql.DB) error {
				stepCalls = append(stepCalls, from)
				if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", from+1)); err != nil {
					return err
				}
				return nil
			},
		}
	}
	migrationSteps = []migrationStep{mkStep(0), mkStep(1)}

	// Write a matching sentinel and drive the same authorized-open sequence
	// ensureSchema runs: runMigrationChain(from) then consumeAuthorization.
	writeSentinel(t, dir, 0, schemaVersion)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()

	before := len(readStoreTrailLines(t))

	// Drive the REAL engine loop: runMigrationChain must iterate twice (0→1→2),
	// invoking each registered step in ascending order via migrationStepFrom. A
	// return-after-one-hop regression would leave the DB at 1 and stepCalls at
	// [0], failing the assertions below.
	if err := runMigrationChain(db, 0); err != nil {
		t.Fatalf("runMigrationChain(db, 0): %v", err)
	}
	// Consume exactly once, as ensureSchema does after the chain commits.
	consumeAuthorization(dbPath, 0, schemaVersion)

	// Two chained steps ran, in order — proving the loop iterated twice.
	if len(stepCalls) != 2 || stepCalls[0] != 0 || stepCalls[1] != 1 {
		t.Fatalf("synthetic chain step order = %v; want [0 1]", stepCalls)
	}
	// DB climbed the full two versions in the one call.
	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("synthetic chain user_version = %d; want %d", got, schemaVersion)
	}
	// Exactly ONE sentinel consume: sentinel gone, exactly one audit line.
	assertSentinelAbsent(t, dir)
	migrated := schemaMigratedAt(t, before)
	if len(migrated) != 1 {
		t.Fatalf("synthetic chain: want 1 ad.schema.migrated; got %d", len(migrated))
	}
	assertTrailStr(t, migrated[0], "message",
		fmt.Sprintf("migrated %d→%d, consumed authorization", 0, schemaVersion))
}

// TestPostConsumptionReopen_CleanNoOp covers AC bullet 3: after an authorized
// migration consumes its sentinel, a second Open of the same (now-current) DB
// succeeds, does not recreate the sentinel, does not migrate again, and emits NO
// additional ad.schema.migrated line (checkpoint/delta shows zero new events).
func TestPostConsumptionReopen_CleanNoOp(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV1DB(t, dir)
	writeSentinel(t, dir, 1, schemaVersion)

	// First open: authorized migration + consume.
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Fatalf("after first open user_version = %d; want %d", got, schemaVersion)
	}
	assertSentinelAbsent(t, dir)

	// Checkpoint AFTER consumption so we count only the reopen's events.
	before := len(readStoreTrailLines(t))

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after consumption: %v", err)
	}
	defer s2.Close()

	// No further migration: still at schemaVersion.
	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("reopen user_version = %d; want %d", got, schemaVersion)
	}
	// Sentinel not recreated.
	assertSentinelAbsent(t, dir)
	// No duplicate audit line.
	if got := schemaMigratedAt(t, before); len(got) != 0 {
		t.Errorf("reopen emitted %d ad.schema.migrated; want 0 (clean no-op)", len(got))
	}
}

// TestConsumeDeleteFailure_LoudButOpenSucceeds covers AC bullet 4, DELETE-FAILURE
// branch (chosen because it IS cleanly provokable in-sandbox — see below).
//
// Branch taken: PROVOKED DELETE FAILURE. The sandbox runs tests as UID 1000
// (non-root — verified: `id -u` == 1000 and a chmod-0500 parent dir blocks
// os.Remove of a child with "permission denied"). So we make the DB's parent
// directory read-only AFTER the migration commits but BEFORE the delete, forcing
// consumeAuthorization down its os.Remove-failure path: it must emit a loud
// ad.schema.authorization_delete_failed trail event and NOT fail the (already-
// committed) open.
//
// We drive runMigrationChain + consumeAuthorization directly (not through Open)
// so we can flip the directory mode precisely between commit and delete — Open
// runs them back-to-back with no seam. The chain here is the real v1→schemaVersion
// migration, so the committed state is genuine.
func TestConsumeDeleteFailure_LoudButOpenSucceeds(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV1DB(t, dir)
	writeSentinel(t, dir, 1, schemaVersion)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()

	// Real migration chain to current — this genuinely commits.
	if err := runMigrationChain(db, 1); err != nil {
		t.Fatalf("runMigrationChain: %v", err)
	}
	// Close the pool so SQLite releases the file before we lock the directory
	// (a lingering handle is irrelevant to os.Remove of the sentinel, but this
	// keeps the fixture clean).
	if err := db.Close(); err != nil {
		t.Fatalf("close after chain: %v", err)
	}

	before := len(readStoreTrailLines(t))

	// Make the parent dir read-only so os.Remove(sentinel) fails. Restore in a
	// cleanup so t.TempDir can remove the tree.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Sanity: confirm the environment actually blocks the delete (root ignores
	// the mode bits, so the read-only chmod would not stop os.Remove and the
	// test would be vacuous). Skip loudly under root rather than assert a false
	// negative. os.Geteuid()==0 is the reliable signal here — a probe os.Remove
	// on a nonexistent file returns ENOENT for root and non-root alike, so it
	// cannot distinguish the two.
	if !sentinelExists(t, dir) {
		t.Fatalf("precondition: sentinel missing before delete-failure test")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod cannot block delete, failure branch not provokable")
	}

	// Now run the consume against the locked directory. It must NOT panic/fail;
	// it emits the loud delete-failed event and returns.
	consumeAuthorization(dbPath, 1, schemaVersion)

	// Sentinel is still present (delete failed) — admin evidence preserved.
	assertSentinelPresent(t, dir)

	// Exactly one loud delete-failed event, and NO success (migrated) event.
	failed := schemaDeleteFailedAt(t, before)
	if len(failed) != 1 {
		t.Fatalf("want 1 ad.schema.authorization_delete_failed; got %d", len(failed))
	}
	assertTrailStr(t, failed[0], "event", "ad.schema.authorization_delete_failed")
	assertTrailStr(t, failed[0], "source", "ad_store_schema")
	if _, ok := failed[0]["error"].(string); !ok {
		t.Errorf("delete-failed event missing string [error] field: %v", failed[0]["error"])
	}
	if got := schemaMigratedAt(t, before); len(got) != 0 {
		t.Errorf("delete-failure path emitted %d ad.schema.migrated; want 0", len(got))
	}
}

// TestStaleSentinelInert_MatchingVersionReopen covers AC bullet 4's stale-
// sentinel-inert property directly (the fallback the ticket names): a DB already
// AT schemaVersion with a leftover consumed-shape sentinel beside it re-opens as
// a clean no-op — ensureSchema short-circuits on the version match BEFORE
// authorizeMigration is ever consulted, so the stale sentinel is provably inert
// (not honored, not deleted, no audit line). This complements the provoked
// delete-failure test above by proving the "next open is safe" invariant that
// makes the fail-open consume acceptable.
func TestStaleSentinelInert_MatchingVersionReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := makeV2DB(t, dir) // already at schemaVersion
	// Leftover consumed-shape sentinel from a prior migration.
	writeSentinel(t, dir, 1, schemaVersion)

	before := len(readStoreTrailLines(t))

	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen at matching version with stale sentinel: %v", err)
	}
	defer s.Close()

	// Version unchanged.
	if got := readUserVersion(t, dbPath); got != schemaVersion {
		t.Errorf("user_version = %d; want %d", got, schemaVersion)
	}
	// Stale sentinel neither honored nor deleted — left as-is (inert).
	assertSentinelPresent(t, dir)
	// No migration audit line, no delete-failed line.
	if got := schemaMigratedAt(t, before); len(got) != 0 {
		t.Errorf("stale-sentinel reopen emitted %d ad.schema.migrated; want 0", len(got))
	}
	if got := schemaDeleteFailedAt(t, before); len(got) != 0 {
		t.Errorf("stale-sentinel reopen emitted %d ad.schema.authorization_delete_failed; want 0", len(got))
	}
}
