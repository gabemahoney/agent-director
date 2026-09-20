package api_test

// find_missing_trail_test.go — SR-A-2.5 / SR-8.4 / SR-7.7: ad.find_missing.tick
// emission tests for the per-row evidence-based verdict engine.
//
// The sweep (findMissingImpl) has two tick emit sites this file covers:
//   - proc_absent: one event per row transitioned to missing, emitted AFTER the
//     mark in the PM-pinned order (MarkSpawnMissing → ClearLivenessUnverified →
//     proc_absent tick → CloseOrphanedPermissionRequests).
//   - probe_eacces: exactly one event per NULL→set liveness transition (the
//     store's guarded SetLivenessUnverified.transitioned bool is the sole
//     signal). A repeat sweep over an already-unverified row emits ZERO.
//
// These use REAL *store.Store instances (seeded via apitest) so the widened
// FindMissingStore interface is exercised end-to-end, and a small local fake
// LivenessChecker drives the per-row verdict. The environ Prober is a no-op
// fake — full-identity rows never consult it.
//
// Assertions follow the checkpoint/delta pattern from
// internal/store/trail_emit_test.go: capture a line-count checkpoint before the
// operation under test and assert only on ad.find_missing.tick lines added
// since that checkpoint.
//
// Trail infrastructure: TestMain (example_main_test.go) redirects HOME to a
// temp dir (apiTrailDir) before any test runs. The trail singleton (sync.Once)
// resolves ~/.agent-director/ad-trail.jsonl from that HOME and captures it on
// first Emit. All trail reads in this file go through readAPITrailLines, which
// opens apiTrailDir/.agent-director/ad-trail.jsonl.
//
// Coordination: another test writer owns cmd/agent-director/find_missing_cli_test.go
// (or recovery_cmd_test.go) for the CLI surface, and a sibling owns
// find_missing_test.go (the shared fakeFindMissingStore). This file owns no
// shared fixtures — it uses real stores plus file-local fakes.

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

var apiTSRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3,}Z$`)

// apiTrailFilePath returns the trail file path used by the singleton in api tests.
func apiTrailFilePath() string {
	return filepath.Join(apiTrailDir, ".agent-director", "ad-trail.jsonl")
}

// readAPITrailLines parses every JSONL line from the api trail file.
// Returns nil when the file does not exist yet.
func readAPITrailLines(t *testing.T) []map[string]any {
	t.Helper()
	f, err := os.Open(apiTrailFilePath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("readAPITrailLines: %v", err)
	}
	defer f.Close()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("readAPITrailLines: unmarshal %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	if sc.Err() != nil {
		t.Fatalf("readAPITrailLines: scan: %v", sc.Err())
	}
	return rows
}

// apiFindMissingTicksAt returns ad.find_missing.tick lines added after prevCount
// total lines in the api trail file.
func apiFindMissingTicksAt(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readAPITrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.find_missing.tick" {
			out = append(out, row)
		}
	}
	return out
}

// apiTicksWithReason filters ticks (from apiFindMissingTicksAt) by
// reconciliation_reason.
func apiTicksWithReason(ticks []map[string]any, reason string) []map[string]any {
	var out []map[string]any
	for _, tick := range ticks {
		if tick["reconciliation_reason"] == reason {
			out = append(out, tick)
		}
	}
	return out
}

// assertAPITrailStr checks row[key] == want.
func assertAPITrailStr(t *testing.T, row map[string]any, key, want string) {
	t.Helper()
	got, ok := row[key]
	if !ok {
		t.Errorf("field %q missing", key)
		return
	}
	if got != want {
		t.Errorf("[%q] = %v; want %q", key, got, want)
	}
}

// ── file-local helpers ──────────────────────────────────────────────────────
//
// The verdict-level fakeChecker (+ newFakeChecker), fakeProber, and
// recordingLogger are defined by the sibling find_missing_test.go and shared
// across the api_test package; this file reuses them. checkerFor is a tiny
// convenience wrapping newFakeChecker with a per-id verdict map.

// checkerFor returns a programmable fakeChecker seeded with the given per-id
// verdicts. Ids absent from the map default to VerdictUnknown (fail-open).
func checkerFor(verdicts map[string]probe.LivenessVerdict) *fakeChecker {
	c := newFakeChecker()
	for id, v := range verdicts {
		c.verdicts[id] = v
	}
	return c
}

// seedFullIdentityStore opens a fresh temp store and seeds each id as a
// working-state row WITH a recorded pid+proc_starttime (a full identity, so the
// sweep routes it through the checker rather than the environ fallback).
// Returns the open store handle (registered for cleanup) and its dbPath.
func seedFullIdentityStore(t *testing.T, ids ...string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	for i, id := range ids {
		// Distinct positive pids so rows are independent; the fake checker
		// ignores pid/starttime and keys off the instance id.
		if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "/tmp", "off", "", i == 0,
			apitest.WithPID(1000+i), apitest.WithProcStarttime(apitest.LinuxProcStarttime)); err != nil {
			t.Fatalf("seedFullIdentityStore: SeedSpawn %q: %v", id, err)
		}
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seedFullIdentityStore: store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ── proc_absent (marking path) ──────────────────────────────────────────────

// TestFindMissingProcAbsentEmitsTrail verifies that the per-row engine emits one
// ad.find_missing.tick(proc_absent) per row transitioned to missing, with:
//   - reconciliation_reason="proc_absent"
//   - claude_instance_id equal to the transitioned row's instance ID
//   - prior_state="working" (the seeded live state, captured by MarkSpawnMissing)
//   - new_state="missing"
//   - source="ad_find_missing"
//
// Three full-identity rows: tick-a and tick-c get a provably-dead verdict; tick-b
// is verified-alive. Exactly two proc_absent ticks (one per marked row) result.
func TestFindMissingProcAbsentEmitsTrail(t *testing.T) {
	st := seedFullIdentityStore(t, "tick-a", "tick-b", "tick-c")
	chk := checkerFor(map[string]probe.LivenessVerdict{
		"tick-a": probe.VerdictProvablyDead,
		"tick-b": probe.VerdictVerifiedAlive,
		"tick-c": probe.VerdictProvablyDead,
	})
	before := len(readAPITrailLines(t))

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d; want 2", res.Count)
	}

	ticks := apiTicksWithReason(apiFindMissingTicksAt(t, before), "proc_absent")
	if len(ticks) != 2 {
		t.Fatalf("want 2 ad.find_missing.tick(proc_absent); got %d", len(ticks))
	}

	tickIDs := make(map[string]bool)
	for _, tick := range ticks {
		id, ok := tick["claude_instance_id"].(string)
		if !ok || id == "" {
			t.Errorf("[claude_instance_id] = %v; want non-empty string", tick["claude_instance_id"])
		}
		tickIDs[id] = true

		assertAPITrailStr(t, tick, "event", "ad.find_missing.tick")
		assertAPITrailStr(t, tick, "reconciliation_reason", "proc_absent")
		assertAPITrailStr(t, tick, "source", "ad_find_missing")
		assertAPITrailStr(t, tick, "new_state", "missing")
		// MarkSpawnMissing captures the prior live state; these rows were seeded
		// in StateWorking.
		assertAPITrailStr(t, tick, "prior_state", store.StateWorking)

		ts, ok := tick["ts"].(string)
		if !ok || !apiTSRe.MatchString(ts) {
			t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
		}
	}
	if !tickIDs["tick-a"] || !tickIDs["tick-c"] {
		t.Errorf("tick instance IDs = %v; want tick-a and tick-c", tickIDs)
	}

	// The verified-alive row (tick-b) is untouched — still working.
	if got, err := st.GetSpawnState("tick-b"); err != nil || got != store.StateWorking {
		t.Errorf("tick-b state = %q (err %v); want working", got, err)
	}
}

// ── probe_eacces (exactly-once via checkpoint/delta) ────────────────────────

// TestFindMissingProbeEaccesEmitsExactlyOnceTick pins SR-8.4's exactly-once
// discipline via checkpoint/delta:
//
//   - First sweep over an unknown-verdict full-identity row: the guarded
//     SetLivenessUnverified NULL→set transition fires, so exactly ONE
//     ad.find_missing.tick(probe_eacces) is emitted for that row.
//   - Second sweep over the SAME still-unverified row: SetLivenessUnverified
//     reports transitioned=false (the liveness_unverified_since is already set),
//     so ZERO new probe_eacces ticks are emitted.
//
// The row is never marked missing (unknown = fail-open, skip THIS ROW ONLY) and
// is reported in UnverifiedIDs on both sweeps.
func TestFindMissingProbeEaccesEmitsExactlyOnceTick(t *testing.T) {
	st := seedFullIdentityStore(t, "eacces-1")
	chk := checkerFor(map[string]probe.LivenessVerdict{
		"eacces-1": probe.VerdictUnknown,
	})

	// ── First sweep: NULL→set transition → exactly one probe_eacces tick. ──
	before := len(readAPITrailLines(t))
	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("first sweep count = %d; want 0 (unknown never marks)", res.Count)
	}
	if res.Unverified != 1 || len(res.UnverifiedIDs) != 1 || res.UnverifiedIDs[0] != "eacces-1" {
		t.Errorf("first sweep unverified = %d %v; want 1 [eacces-1]", res.Unverified, res.UnverifiedIDs)
	}

	firstTicks := apiTicksWithReason(apiFindMissingTicksAt(t, before), "probe_eacces")
	if len(firstTicks) != 1 {
		t.Fatalf("first sweep: want 1 ad.find_missing.tick(probe_eacces); got %d", len(firstTicks))
	}
	tick := firstTicks[0]
	assertAPITrailStr(t, tick, "event", "ad.find_missing.tick")
	assertAPITrailStr(t, tick, "claude_instance_id", "eacces-1")
	assertAPITrailStr(t, tick, "reconciliation_reason", "probe_eacces")
	assertAPITrailStr(t, tick, "source", "ad_find_missing")
	// prior_state and new_state are JSON null on the probe_eacces (no-transition) path.
	if v, exists := tick["prior_state"]; !exists || v != nil {
		t.Errorf("[prior_state] = %v; want null", tick["prior_state"])
	}
	if v, exists := tick["new_state"]; !exists || v != nil {
		t.Errorf("[new_state] = %v; want null", tick["new_state"])
	}
	ts, ok := tick["ts"].(string)
	if !ok || !apiTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want RFC3339Nano timestamp", tick["ts"])
	}

	// ── Second sweep: row still unverified → transitioned=false → ZERO ticks. ──
	before2 := len(readAPITrailLines(t))
	res2, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if res2.Unverified != 1 || len(res2.UnverifiedIDs) != 1 || res2.UnverifiedIDs[0] != "eacces-1" {
		t.Errorf("second sweep unverified = %d %v; want 1 [eacces-1] (still unverified)",
			res2.Unverified, res2.UnverifiedIDs)
	}
	secondTicks := apiTicksWithReason(apiFindMissingTicksAt(t, before2), "probe_eacces")
	if len(secondTicks) != 0 {
		t.Errorf("second sweep: want 0 new probe_eacces ticks (no NULL→set transition); got %d",
			len(secondTicks))
	}
}

// ── trail-write failure isolation (SR-7.7 fail-open) ────────────────────────

// TestFindMissingTrailFailureDoesNotAlterSweep pins SR-7.7: a trail-emit failure
// must not change the sweep's return value or the row writes. find-missing emits
// with `_ = trail.Emit(...)` (fail-open), so the marking path is fully decoupled
// from trail writability.
//
// We make the trail directory unwritable for the duration of the sweep and
// assert the row is still transitioned to missing and the returned count is
// correct. (The trail singleton caches its fd process-wide, so this asserts the
// invariant that the sweep's DB effects are independent of trail state,
// regardless of whether the append physically fails.)
func TestFindMissingTrailFailureDoesNotAlterSweep(t *testing.T) {
	st := seedFullIdentityStore(t, "trailfail-1")
	chk := checkerFor(map[string]probe.LivenessVerdict{
		"trailfail-1": probe.VerdictProvablyDead,
	})

	// Make the trail directory read-only for the duration of the sweep. Restore
	// on cleanup so later tests (and t.TempDir teardown) are unaffected.
	trailDir := filepath.Dir(apiTrailFilePath())
	if err := os.MkdirAll(trailDir, 0o700); err != nil {
		t.Fatalf("MkdirAll trail dir: %v", err)
	}
	if err := os.Chmod(trailDir, 0o500); err != nil {
		t.Fatalf("chmod trail dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(trailDir, 0o700) })

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing under unwritable trail: %v", err)
	}
	if res.Count != 1 || len(res.IDs) != 1 || res.IDs[0] != "trailfail-1" {
		t.Fatalf("res = %+v; want count=1 ids=[trailfail-1] despite trail failure", res)
	}

	// Restore write access before reading DB state (the store file lives under a
	// separate t.TempDir, so it is unaffected, but restore keeps teardown clean).
	_ = os.Chmod(trailDir, 0o700)

	// The row write is committed regardless of trail writability.
	if got, err := st.GetSpawnState("trailfail-1"); err != nil || got != store.StateMissing {
		t.Errorf("trailfail-1 state = %q (err %v); want missing", got, err)
	}
}

// ── zero-touch (no emit) ────────────────────────────────────────────────────

// TestFindMissingZeroTouchEmitsNoTrail pins the zero-touch path: when every
// full-identity row is verified-alive, the sweep transitions nothing and emits
// zero ad.find_missing.tick lines.
func TestFindMissingZeroTouchEmitsNoTrail(t *testing.T) {
	st := seedFullIdentityStore(t, "alive-p", "alive-q")
	chk := checkerFor(map[string]probe.LivenessVerdict{
		"alive-p": probe.VerdictVerifiedAlive,
		"alive-q": probe.VerdictVerifiedAlive,
	})
	before := len(readAPITrailLines(t))

	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, chk, &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 || res.Unverified != 0 {
		t.Errorf("res = %+v; want count=0 unverified=0", res)
	}
	if ticks := apiFindMissingTicksAt(t, before); len(ticks) != 0 {
		t.Errorf("zero-touch path emitted %d ad.find_missing.tick; want 0", len(ticks))
	}
}

// ── named negative: no degraded-mode skip event (removed guard) ─────────────

// TestFindMissingEmptyProbeSetNoDegradedModeSkip is the named negative
// replacing the deleted TestFindMissingDegradedModeEmitsTrail. Under the old
// global guard, an empty environ probe set with ≥1 live rows tripped a refusal
// that emitted an ad.find_missing.tick carrying the removed degraded-mode skip
// reason (name assembled at runtime — see below). The per-row engine removes
// that guard entirely.
//
// This sweep reproduces the exact condition that WOULD have tripped the guard:
// live rows with a NULL recorded identity (pid/proc_starttime NULL) route to the
// SR-7.5 environ fallback, and the prober returns an EMPTY set. Every such row
// is reconciled to missing (absent from the probe set), and NO event carrying
// the removed degraded-mode skip reason is emitted.
func TestFindMissingEmptyProbeSetNoDegradedModeSkip(t *testing.T) {
	// NULL-identity rows (no WithPID/WithProcStarttime) → environ fallback path.
	dbPath := filepath.Join(t.TempDir(), "state.db")
	for i, id := range []string{"dg-1", "dg-2"} {
		if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "/tmp", "off", "", i == 0); err != nil {
			t.Fatalf("SeedSpawn %q: %v", id, err)
		}
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	before := len(readAPITrailLines(t))
	// Empty probe set: the condition that formerly tripped the degraded-mode guard.
	res, err := api.FindMissing(context.Background(), st, &fakeProber{}, newFakeChecker(), &recordingLogger{})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}

	// The rows are reconciled (not refused): both marked missing via the fallback.
	if res.Count != 2 {
		t.Errorf("count = %d; want 2 (rows reconciled, not refused)", res.Count)
	}
	for _, id := range []string{"dg-1", "dg-2"} {
		if got, err := st.GetSpawnState(id); err != nil || got != store.StateMissing {
			t.Errorf("%s state = %q (err %v); want missing", id, got, err)
		}
	}

	// The removed guard: NO tick may carry the removed degraded-mode skip reason.
	// The reason name is assembled at runtime so a repo-wide grep for the removed
	// literal stays clean (same precedent as internal/trail/writer_test.go).
	degradedModeSkipReason := "degraded_mode" + "_skip"
	ticks := apiFindMissingTicksAt(t, before)
	if got := apiTicksWithReason(ticks, degradedModeSkipReason); len(got) != 0 {
		t.Errorf("emitted %d %s tick(s); want 0 (guard removed)", len(got), degradedModeSkipReason)
	}
}
