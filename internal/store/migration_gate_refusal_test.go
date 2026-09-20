package store

// migration_gate_refusal_test.go — SR-1 refusal-side tests for the schema
// migration gate. An older-than-binary DB opened without a valid administrator
// authorization sentinel must:
//
//   - refuse with a typed ErrSchemaMigrationRequired (errors.Is),
//   - leave the DB file byte-identical (main + WAL/SHM sidecars),
//   - leave user_version unchanged,
//   - preserve the sentinel state exactly (missing stays missing; a bad
//     sentinel is left in place as admin evidence, never consumed),
//   - emit the distinct ad.schema.authorization_mismatch trail line (reporting
//     BOTH ends) for malformed/mismatched sentinels, and emit NO such line for
//     a plain missing-sentinel refusal.
//
// The authorized/success chaining + audit-line tests live in the sibling file
// owned by t3.93m.i5.ck.vc — this file stays strictly on the refusal side (the
// one exception is the rewritten TestSchemaV2Migration in schema_test.go, which
// is owned by this subtask).
//
// Trail assertions follow the checkpoint/delta pattern from trail_emit_test.go:
// capture len(readStoreTrailLines) before the refused Open, then inspect only
// the lines added since. The trail singleton writes to storeTrailDir (set by
// TestMain in store_test.go).

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// authMismatchesAt reads trail lines added after prevCount and returns the
// ad.schema.authorization_mismatch lines among them. Mirrors rowMutationsAt
// from trail_emit_test.go.
func authMismatchesAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.schema.authorization_mismatch" {
			out = append(out, row)
		}
	}
	return out
}

// assertTrailInt asserts row[key] equals want. Trail lines round-trip through
// JSON, so integer fields decode as float64.
func assertTrailInt(t *testing.T, row map[string]any, key string, want int) {
	t.Helper()
	got, ok := row[key].(float64)
	if !ok {
		t.Errorf("[%q] = %v (%T); want float64 %d", key, row[key], row[key], want)
		return
	}
	if int(got) != want {
		t.Errorf("[%q] = %d; want %d", key, int(got), want)
	}
}

// assertMismatchLine asserts a single authorization_mismatch line reports BOTH
// ends (expected_from/expected_to always the true DB/binary versions; found_*
// as supplied, -1 meaning "unknown" for malformed/unreadable sentinels) plus
// the expected reason and source.
func assertMismatchLine(t *testing.T, row map[string]any, wantReason string, expectFrom, expectTo, foundFrom, foundTo int) {
	t.Helper()
	assertTrailStr(t, row, "event", "ad.schema.authorization_mismatch")
	assertTrailStr(t, row, "source", "ad_store_schema")
	assertTrailStr(t, row, "reason", wantReason)
	assertTrailInt(t, row, "expected_from", expectFrom)
	assertTrailInt(t, row, "expected_to", expectTo)
	assertTrailInt(t, row, "found_from", foundFrom)
	assertTrailInt(t, row, "found_to", foundTo)
}

// TestGateRefusesMissingSentinel covers the plain dead-end: an older-than-binary
// DB with NO sentinel refuses with ErrSchemaMigrationRequired, touches no DB
// bytes, leaves user_version unchanged, creates no sentinel, and emits NO
// authorization_mismatch line (that line is reserved for malformed/mismatched
// sentinels).
func TestGateRefusesMissingSentinel(t *testing.T) {
	dir := t.TempDir()
	path := makeV1DB(t, dir)
	before := snapshotDBBytes(t, path)
	beforeVersion := readUserVersion(t, path)

	checkpoint := len(readStoreTrailLines(t))

	_, err := Open(path)
	if !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open(v1, no sentinel) err = %v; want errors.Is ErrSchemaMigrationRequired", err)
	}

	assertDBBytesUnchanged(t, path, before)
	if v := readUserVersion(t, path); v != beforeVersion {
		t.Errorf("user_version = %d after refused open; want %d (unchanged)", v, beforeVersion)
	}
	assertSentinelAbsent(t, dir)

	if lines := authMismatchesAt(t, checkpoint); len(lines) != 0 {
		t.Errorf("missing-sentinel refusal emitted %d authorization_mismatch line(s); want 0", len(lines))
	}
}

// TestGateRefusesBadSentinel covers malformed-JSON, wrong-from, and wrong-to
// sentinels. Each refuses with ErrSchemaMigrationRequired, leaves the DB
// byte-identical, leaves user_version unchanged, leaves the sentinel in place
// (NOT consumed — it is admin evidence), and emits exactly one distinct
// authorization_mismatch line reporting BOTH ends.
func TestGateRefusesBadSentinel(t *testing.T) {
	// schemaVersion is the binary's current version; v1 is the on-disk DB.
	const dbVersion = 1

	cases := []struct {
		name      string
		write     func(t *testing.T, dir string) string
		reason    string
		foundFrom int
		foundTo   int
	}{
		{
			name:      "malformed_json",
			write:     func(t *testing.T, dir string) string { return writeSentinelMalformed(t, dir) },
			reason:    "malformed_json",
			foundFrom: -1, // ends unknown for unparseable sentinel
			foundTo:   -1,
		},
		{
			name:      "wrong_from",
			write:     func(t *testing.T, dir string) string { return writeSentinelWrongFrom(t, dir, dbVersion, schemaVersion) },
			reason:    "version_mismatch",
			foundFrom: dbVersion + 1, // writeSentinelWrongFrom uses wantFrom+1
			foundTo:   schemaVersion,
		},
		{
			name:      "wrong_to",
			write:     func(t *testing.T, dir string) string { return writeSentinelWrongTo(t, dir, dbVersion, schemaVersion) },
			reason:    "version_mismatch",
			foundFrom: dbVersion,
			foundTo:   schemaVersion + 1, // writeSentinelWrongTo uses wantTo+1
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := makeV1DB(t, dir)
			tc.write(t, dir)

			before := snapshotDBBytes(t, path)
			beforeVersion := readUserVersion(t, path)
			sentinelBefore := readSentinel(t, dir)

			checkpoint := len(readStoreTrailLines(t))

			_, err := Open(path)
			if !errors.Is(err, ErrSchemaMigrationRequired) {
				t.Fatalf("Open(v1, %s sentinel) err = %v; want errors.Is ErrSchemaMigrationRequired", tc.name, err)
			}

			assertDBBytesUnchanged(t, path, before)
			if v := readUserVersion(t, path); v != beforeVersion {
				t.Errorf("user_version = %d after refused open; want %d (unchanged)", v, beforeVersion)
			}

			// Sentinel left in place, byte-for-byte (not consumed).
			assertSentinelPresent(t, dir)
			if got := readSentinel(t, dir); string(got) != string(sentinelBefore) {
				t.Errorf("sentinel mutated after refused open:\n  before %q\n  after  %q", sentinelBefore, got)
			}

			lines := authMismatchesAt(t, checkpoint)
			if len(lines) != 1 {
				t.Fatalf("%s refusal emitted %d authorization_mismatch line(s); want exactly 1", tc.name, len(lines))
			}
			assertMismatchLine(t, lines[0], tc.reason, dbVersion, schemaVersion, tc.foundFrom, tc.foundTo)
		})
	}
}

// TestGateRefusalErrorText enforces the exact SR-1.4 dead-end pattern with
// correct N/M substitution and the absence of any self-service breadcrumb —
// no command names, flags, file paths, or environment variable names that
// would let a non-admin route around the administrator.
func TestGateRefusalErrorText(t *testing.T) {
	dir := t.TempDir()
	path := makeV1DB(t, dir) // v1 DB, no sentinel → dead-end refusal

	_, err := Open(path)
	if !errors.Is(err, ErrSchemaMigrationRequired) {
		t.Fatalf("Open err = %v; want errors.Is ErrSchemaMigrationRequired", err)
	}

	msg := err.Error()

	// Exact dead-end phrasing with N=1 (on-disk) and M=schemaVersion (binary).
	wantSubstr := fmt.Sprintf(
		"state.db is schema v1; this binary requires v%d. "+
			"Migration must be performed by an administrator via the agent-director install process.",
		schemaVersion)
	if !strings.Contains(msg, wantSubstr) {
		t.Errorf("error text missing dead-end pattern.\n  got:  %q\n  want substr: %q", msg, wantSubstr)
	}

	// No self-service breadcrumbs: no command names, flags, file paths, env
	// vars, or the internal sentinel filename. The message routes to an
	// administrator and nowhere else.
	forbidden := []string{
		"migrate",            // command/flag verb
		"--",                 // any flag
		"AGENT_DIRECTOR",     // env-var prefix
		"~/.agent-director",  // store path
		"/",                  // any file path separator
		sentinelFilename,     // internal sentinel name must never leak
		"migrate-authorized", // ditto, literal
	}
	lower := strings.ToLower(msg)
	for _, bad := range forbidden {
		if strings.Contains(lower, strings.ToLower(bad)) {
			t.Errorf("error text leaks self-service breadcrumb %q:\n  %q", bad, msg)
		}
	}
}

// TestGateFreshCreateStillInitializes is a regression guard: creating a store
// on a nonexistent path still auto-initializes a fresh schema stamped at the
// current schemaVersion — the gate only intercepts older-than-binary opens.
func TestGateFreshCreateStillInitializes(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/state.db"

	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("OpenOrInit(fresh path) err = %v; want nil", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if v := readUserVersion(t, path); v != schemaVersion {
		t.Errorf("fresh DB user_version = %d; want %d", v, schemaVersion)
	}
	// A fresh create must never leave an authorization sentinel behind.
	assertSentinelAbsent(t, dir)
}

// TestGateNewerThanBinaryStillMismatch is a regression guard: a DB whose
// user_version is NEWER than the binary still yields ErrSchemaMismatch, not the
// new ErrSchemaMigrationRequired — the two dispositions must not cross-wire.
func TestGateNewerThanBinaryStillMismatch(t *testing.T) {
	dir := t.TempDir()
	// Build a genuine current-version fixture, then stamp its header one past
	// the binary. A stamp is a pure header write and leaves physical shape
	// untouched — exactly what the newer-than-binary disposition needs.
	path := makeVersionedDB(t, dir, schemaVersion)
	stampUserVersion(t, path, schemaVersion+1)

	_, err := Open(path)
	if !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("Open(newer-than-binary) err = %v; want errors.Is ErrSchemaMismatch", err)
	}
	if errors.Is(err, ErrSchemaMigrationRequired) {
		t.Errorf("newer-than-binary open wrongly matched ErrSchemaMigrationRequired: %v", err)
	}
}
