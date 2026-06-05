package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

// spawnStateTransitionLines returns ad.spawn.state_transition trail lines
// added after prevCount total lines in the store trail file.
func spawnStateTransitionLines(t *testing.T, prevCount int) []map[string]any {
	t.Helper()
	all := readStoreTrailLines(t)
	var out []map[string]any
	for _, row := range all[prevCount:] {
		if row["event"] == "ad.spawn.state_transition" {
			out = append(out, row)
		}
	}
	return out
}

// assertSpawnStateTransitionFields checks the required top-level fields of
// an ad.spawn.state_transition event (SR-A-2.1).
func assertSpawnStateTransitionFields(t *testing.T, row map[string]any, instanceID, priorState, newState, triggeringEventName string, softRefresh bool) {
	t.Helper()
	ts, ok := row["ts"].(string)
	if !ok || !storeTSRe.MatchString(ts) {
		t.Errorf("[ts] = %v; want RFC3339Nano timestamp", row["ts"])
	}
	assertTrailStr(t, row, "event", "ad.spawn.state_transition")
	assertTrailStr(t, row, "source", "ad_spawn_store")
	assertTrailStr(t, row, "claude_instance_id", instanceID)
	assertTrailStr(t, row, "prior_state", priorState)
	assertTrailStr(t, row, "new_state", newState)
	assertTrailStr(t, row, "triggering_event_name", triggeringEventName)
	if v, ok := row["soft_refresh"].(bool); !ok || v != softRefresh {
		t.Errorf("[soft_refresh] = %v (%T); want %v", row["soft_refresh"], row["soft_refresh"], softRefresh)
	}
}

func TestInsertPendingThenGet(t *testing.T) {
	s, _ := openTempStore(t)
	want := Spawn{
		ClaudeInstanceID: "11111111-aaaa-4bbb-8ccc-000000000001",
		ParentID:         "",
		CWD:              "/tmp/x",
		TmuxSessionName:  "cd-x-11111111",
		ClaudeArgs:       []string{"--model", "opus"},
		RelayMode:        "off",
		Labels:           map[string]string{"role": "researcher", "owner": "alice"},
	}
	if err := s.InsertPending(want); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	got, err := s.GetSpawn(want.ClaudeInstanceID)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got.State != StatePending {
		t.Errorf("State = %q; want pending", got.State)
	}
	if got.CWD != want.CWD {
		t.Errorf("CWD = %q; want %q", got.CWD, want.CWD)
	}
	if got.TmuxSessionName != want.TmuxSessionName {
		t.Errorf("TmuxSessionName = %q; want %q", got.TmuxSessionName, want.TmuxSessionName)
	}
	if got.RelayMode != want.RelayMode {
		t.Errorf("RelayMode = %q; want %q", got.RelayMode, want.RelayMode)
	}
	if len(got.ClaudeArgs) != 2 || got.ClaudeArgs[0] != "--model" || got.ClaudeArgs[1] != "opus" {
		t.Errorf("ClaudeArgs = %v; want [--model opus]", got.ClaudeArgs)
	}
	if got.Labels["role"] != "researcher" || got.Labels["owner"] != "alice" {
		t.Errorf("Labels = %v; want role=researcher owner=alice", got.Labels)
	}
	if got.StartedAt.IsZero() {
		t.Error("StartedAt zero")
	}
	if got.EndedAt != nil {
		t.Errorf("EndedAt = %v; want nil for pending row", got.EndedAt)
	}
}

func TestGetSpawnNotFound(t *testing.T) {
	s, _ := openTempStore(t)
	_, err := s.GetSpawn("absent")
	if !errors.Is(err, ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
}

func TestGetSpawnStateNotFound(t *testing.T) {
	s, _ := openTempStore(t)
	_, err := s.GetSpawnState("absent")
	if !errors.Is(err, ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
}

func TestLiveSpawnExistsDetectsLiveRows(t *testing.T) {
	s, _ := openTempStore(t)
	id := "22222222-aaaa-4bbb-8ccc-000000000002"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-tmp",
		RelayMode:        "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	exists, err := s.LiveSpawnExists(id)
	if err != nil {
		t.Fatalf("LiveSpawnExists: %v", err)
	}
	if !exists {
		t.Fatalf("LiveSpawnExists = false; want true (pending is a live state)")
	}
}

func TestLiveSpawnExistsIgnoresTerminalRows(t *testing.T) {
	s, _ := openTempStore(t)
	id := "33333333-aaaa-4bbb-8ccc-000000000003"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("transition to ended: %v", err)
	}
	trailLines := spawnStateTransitionLines(t, beforeTrail)
	if len(trailLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after ended transition; got %d", len(trailLines))
	}
	assertSpawnStateTransitionFields(t, trailLines[0], id, StatePending, StateEnded, "test_seed", false)
	exists, err := s.LiveSpawnExists(id)
	if err != nil {
		t.Fatalf("LiveSpawnExists: %v", err)
	}
	if exists {
		t.Fatalf("LiveSpawnExists = true for ended row; want false")
	}
}

func TestApplyHookTransitionStateChange(t *testing.T) {
	s, _ := openTempStore(t)
	id := "44444444-aaaa-4bbb-8ccc-000000000004"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateWaiting, false, "test_seed"); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
	trailLines := spawnStateTransitionLines(t, beforeTrail)
	if len(trailLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after state-change; got %d", len(trailLines))
	}
	assertSpawnStateTransitionFields(t, trailLines[0], id, StatePending, StateWaiting, "test_seed", false)
	got, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got.State != StateWaiting {
		t.Fatalf("state = %q; want waiting", got.State)
	}
	if got.EndedAt != nil {
		t.Fatalf("ended_at set on non-terminal transition: %v", got.EndedAt)
	}
}

func TestApplyHookTransitionEndedSetsEndedAt(t *testing.T) {
	s, _ := openTempStore(t)
	id := "55555555-aaaa-4bbb-8ccc-000000000005"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
	trailLines := spawnStateTransitionLines(t, beforeTrail)
	if len(trailLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after ended transition; got %d", len(trailLines))
	}
	assertSpawnStateTransitionFields(t, trailLines[0], id, StatePending, StateEnded, "test_seed", false)
	got, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got.State != StateEnded {
		t.Fatalf("state = %q; want ended", got.State)
	}
	if got.EndedAt == nil {
		t.Fatal("ended_at not set after ended transition")
	}
	if time.Since(*got.EndedAt) > 5*time.Second {
		t.Errorf("ended_at too far from now: %v", got.EndedAt)
	}
}

// TestApplyHookTransitionResurrectionClearsEndedAt pins SRD §8.1's
// resurrection behavior: when SessionStart on a previously-ended (or
// missing) Spawn fires, the row's state moves back to `waiting` and
// `ended_at` is cleared so the row's metadata reflects the active
// life. This is the hook-side half of the resume contract — resume
// itself doesn't touch state; only SessionStart does.
func TestApplyHookTransitionResurrectionClearsEndedAt(t *testing.T) {
	s, _ := openTempStore(t)
	id := "55555555-aaaa-4bbb-8ccc-000000000099"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// Move the row to ended so ended_at gets stamped.
	beforeEnded := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("transition to ended: %v", err)
	}
	endedLines := spawnStateTransitionLines(t, beforeEnded)
	if len(endedLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after ended; got %d", len(endedLines))
	}
	assertSpawnStateTransitionFields(t, endedLines[0], id, StatePending, StateEnded, "test_seed", false)
	got, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn after ended: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatal("precondition: ended_at not set after ended transition")
	}

	// Simulate the SessionStart hook firing on the resurrected Claude.
	beforeResurrect := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateWaiting, false, "test_seed"); err != nil {
		t.Fatalf("resurrection transition: %v", err)
	}
	resurrectLines := spawnStateTransitionLines(t, beforeResurrect)
	if len(resurrectLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after resurrection; got %d", len(resurrectLines))
	}
	assertSpawnStateTransitionFields(t, resurrectLines[0], id, StateEnded, StateWaiting, "test_seed", false)
	got, err = s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn after resurrection: %v", err)
	}
	if got.State != StateWaiting {
		t.Errorf("state = %q; want waiting", got.State)
	}
	if got.EndedAt != nil {
		t.Errorf("ended_at = %v; want nil after resurrection", got.EndedAt)
	}
}

// TestApplyHookTransitionFreshSpawnLeavesEndedAtNil is the regression
// guard for the cleared-on-non-terminal behavior: a fresh-spawn
// pending→waiting transition must not break — ended_at was already
// NULL, the column stays NULL.
func TestApplyHookTransitionFreshSpawnLeavesEndedAtNil(t *testing.T) {
	s, _ := openTempStore(t)
	id := "55555555-aaaa-4bbb-8ccc-000000000098"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateWaiting, false, "test_seed"); err != nil {
		t.Fatalf("transition: %v", err)
	}
	trailLines := spawnStateTransitionLines(t, beforeTrail)
	if len(trailLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after fresh spawn transition; got %d", len(trailLines))
	}
	assertSpawnStateTransitionFields(t, trailLines[0], id, StatePending, StateWaiting, "test_seed", false)
	got, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if got.EndedAt != nil {
		t.Errorf("fresh spawn ended_at = %v; want nil", got.EndedAt)
	}
}

func TestApplyHookTransitionSoftRefreshLeavesState(t *testing.T) {
	s, _ := openTempStore(t)
	id := "66666666-aaaa-4bbb-8ccc-000000000006"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	beforeRow, _ := s.GetSpawn(id)
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, "", true, "test_seed"); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
	trailLines := spawnStateTransitionLines(t, beforeTrail)
	if len(trailLines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition on soft refresh; got %d", len(trailLines))
	}
	assertSpawnStateTransitionFields(t, trailLines[0], id, StatePending, StatePending, "test_seed", true)
	afterRow, _ := s.GetSpawn(id)
	if afterRow.State != beforeRow.State {
		t.Fatalf("state changed on soft refresh: %q -> %q", beforeRow.State, afterRow.State)
	}
}

func TestSetSessionID(t *testing.T) {
	s, _ := openTempStore(t)
	id := "77777777-aaaa-4bbb-8ccc-000000000007"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := s.SetSessionID(id, "session-abc"); err != nil {
		t.Fatalf("SetSessionID: %v", err)
	}
	got, _ := s.GetSpawn(id)
	if got.ClaudeSessionID != "session-abc" {
		t.Fatalf("ClaudeSessionID = %q; want session-abc", got.ClaudeSessionID)
	}
}

func TestApplyHookTransitionMissingRowIsNoop(t *testing.T) {
	s, _ := openTempStore(t)
	// No row inserted; the UPDATE finds nothing. UPDATE on a missing row
	// is a no-op in SQL — neither InsertPending nor ApplyHookTransition
	// should error. (SRD §3.2 fail-open invariant.)
	beforeTrail := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition("ghost", StateWaiting, false, "test_seed"); err != nil {
		t.Fatalf("transition on missing row should be no-op: %v", err)
	}
	if got := spawnStateTransitionLines(t, beforeTrail); len(got) != 0 {
		t.Errorf("missing-row transition emitted %d ad.spawn.state_transition; want 0 (fail-open per SRD §3.2)", len(got))
	}
	if err := s.SetSessionID("ghost", "session-x"); err != nil {
		t.Fatalf("session-id on missing row should be no-op: %v", err)
	}
}

func TestInsertPendingCollisionSurfacesAsDriverError(t *testing.T) {
	// Two inserts with the same id must fail at the second one. spawn.Launch
	// maps this back to ErrInstanceIdCollision for the surface.
	s, _ := openTempStore(t)
	id := "88888888-aaaa-4bbb-8ccc-000000000008"
	sp := Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off"}
	if err := s.InsertPending(sp); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := s.InsertPending(sp); err == nil {
		t.Fatalf("second insert with same id should fail")
	}
}

// TestParentFKEnforced asserts the foreign key on parent_id is honored —
// a parent_id pointing to a non-existent row must be rejected.
func TestParentFKEnforced(t *testing.T) {
	s, _ := openTempStore(t)
	sp := Spawn{
		ClaudeInstanceID: "99999999-aaaa-4bbb-8ccc-000000000009",
		ParentID:         "no-such-parent",
		CWD:              "/tmp", TmuxSessionName: "cd-tmp", RelayMode: "off",
	}
	if err := s.InsertPending(sp); err == nil {
		t.Fatalf("expected FK violation for non-existent parent")
	}
}

// TestSetParentID pins the three SetParentID behaviors used by Resume:
// non-empty parent writes the value, empty parent writes NULL (matches the
// original spawn path's "no caller env var" semantics), and a missing
// target row returns ErrSpawnNotFound (distinguishing "asked to update a
// nonexistent row" from "the update silently no-op'd").
// TestStateMachineMultiRowRetention pins SR-5.1 / SR-5.2: a Spawn with multiple
// open permission_requests rows must remain in check_permission until the LAST
// row is decided. Each ApplyHookTransition(working) call is held at
// check_permission while any open rows remain; only the call after the final
// decide actually advances the state to working.
func TestStateMachineMultiRowRetention(t *testing.T) {
	t.Run("two_rows_held_until_last_decided", func(t *testing.T) {
		s, _ := openTempStore(t)
		const id = "sm-multi-row-1"

		// Seed a Spawn in check_permission with two open rows.
		if err := s.InsertPending(Spawn{
			ClaudeInstanceID: id,
			CWD:              "/tmp",
			TmuxSessionName:  "cd-sm-1",
			RelayMode:        "on",
		}); err != nil {
			t.Fatalf("InsertPending: %v", err)
		}
		if err := s.ApplyHookTransition(id, StateCheckPermission, false, "test_seed"); err != nil {
			t.Fatalf("transition to check_permission: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"cmd":"ls"}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{"file":"/etc"}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}

		// Decide row A; try to advance to working — tokenB still open, must be held.
		if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", ""); err != nil {
			t.Fatalf("decide tokenA: %v", err)
		}
		beforeHeld := len(readStoreTrailLines(t))
		if err := s.ApplyHookTransition(id, StateWorking, false, "test_seed"); err != nil {
			t.Fatalf("ApplyHookTransition(working) after deciding tokenA: %v", err)
		}
		// Multi-row retention hold: must emit a no-op line (prior==new==check_permission).
		heldLines := spawnStateTransitionLines(t, beforeHeld)
		if len(heldLines) != 1 {
			t.Fatalf("multi-row hold: want 1 ad.spawn.state_transition no-op; got %d", len(heldLines))
		}
		assertSpawnStateTransitionFields(t, heldLines[0], id, StateCheckPermission, StateCheckPermission, "test_seed", false)
		state, err := s.GetSpawnState(id)
		if err != nil {
			t.Fatalf("GetSpawnState: %v", err)
		}
		if state != StateCheckPermission {
			t.Errorf("state = %q after deciding 1 of 2 rows; want check_permission (held)", state)
		}

		// Decide row B; advance to working — no open rows remain.
		if _, err := s.DecidePermissionRequest(id, tokenB, "deny", "no", ""); err != nil {
			t.Fatalf("decide tokenB: %v", err)
		}
		beforeAdvance := len(readStoreTrailLines(t))
		if err := s.ApplyHookTransition(id, StateWorking, false, "test_seed"); err != nil {
			t.Fatalf("ApplyHookTransition(working) after deciding tokenB: %v", err)
		}
		advanceLines := spawnStateTransitionLines(t, beforeAdvance)
		if len(advanceLines) != 1 {
			t.Fatalf("working advance: want 1 ad.spawn.state_transition; got %d", len(advanceLines))
		}
		assertSpawnStateTransitionFields(t, advanceLines[0], id, StateCheckPermission, StateWorking, "test_seed", false)
		state, err = s.GetSpawnState(id)
		if err != nil {
			t.Fatalf("GetSpawnState after last decide: %v", err)
		}
		if state != StateWorking {
			t.Errorf("state = %q after all rows decided; want working", state)
		}
	})

	t.Run("single_row_transitions_directly", func(t *testing.T) {
		s, _ := openTempStore(t)
		const id = "sm-single-row-1"

		if err := s.InsertPending(Spawn{
			ClaudeInstanceID: id,
			CWD:              "/tmp",
			TmuxSessionName:  "cd-sm-2",
			RelayMode:        "on",
		}); err != nil {
			t.Fatalf("InsertPending: %v", err)
		}
		if err := s.ApplyHookTransition(id, StateCheckPermission, false, "test_seed"); err != nil {
			t.Fatalf("transition to check_permission: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"cmd":"pwd"}`, 0, ""); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}

		// Decide the single row; advance must succeed immediately.
		if _, err := s.DecidePermissionRequest(id, tokenA, "allow", "", ""); err != nil {
			t.Fatalf("decide tokenA: %v", err)
		}
		beforeWorking := len(readStoreTrailLines(t))
		if err := s.ApplyHookTransition(id, StateWorking, false, "test_seed"); err != nil {
			t.Fatalf("ApplyHookTransition(working): %v", err)
		}
		workingLines := spawnStateTransitionLines(t, beforeWorking)
		if len(workingLines) != 1 {
			t.Fatalf("want 1 ad.spawn.state_transition after single-row advance; got %d", len(workingLines))
		}
		assertSpawnStateTransitionFields(t, workingLines[0], id, StateCheckPermission, StateWorking, "test_seed", false)
		state, err := s.GetSpawnState(id)
		if err != nil {
			t.Fatalf("GetSpawnState: %v", err)
		}
		if state != StateWorking {
			t.Errorf("state = %q after single row decided; want working", state)
		}
	})
}

func TestSetParentID(t *testing.T) {
	s, _ := openTempStore(t)

	// Bootstrap a parent + child row.
	parent := Spawn{
		ClaudeInstanceID: "parent-1",
		CWD:              "/tmp", TmuxSessionName: "cd-parent", RelayMode: "off",
	}
	if err := s.InsertPending(parent); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	child := Spawn{
		ClaudeInstanceID: "child-1",
		CWD:              "/tmp", TmuxSessionName: "cd-child", RelayMode: "off",
	}
	if err := s.InsertPending(child); err != nil {
		t.Fatalf("insert child: %v", err)
	}

	t.Run("sets_non_empty_parent", func(t *testing.T) {
		if err := s.SetParentID("child-1", "parent-1"); err != nil {
			t.Fatalf("SetParentID: %v", err)
		}
		row, err := s.GetSpawn("child-1")
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if row.ParentID != "parent-1" {
			t.Errorf("ParentID = %q, want %q", row.ParentID, "parent-1")
		}
	})

	t.Run("empty_writes_null", func(t *testing.T) {
		if err := s.SetParentID("child-1", ""); err != nil {
			t.Fatalf("SetParentID: %v", err)
		}
		row, err := s.GetSpawn("child-1")
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		// GetSpawn applies COALESCE(parent_id, '') so the Go field is
		// empty whether the column is NULL or an empty string. Verify
		// the underlying column directly via raw SQL.
		var got sql.NullString
		if err := s.db.QueryRow(
			"SELECT parent_id FROM spawns WHERE claude_instance_id = ?", "child-1",
		).Scan(&got); err != nil {
			t.Fatalf("raw select: %v", err)
		}
		if got.Valid {
			t.Errorf("parent_id column = %q (Valid=true); want NULL", got.String)
		}
		if row.ParentID != "" {
			t.Errorf("ParentID = %q, want empty", row.ParentID)
		}
	})

	t.Run("missing_row_returns_err_spawn_not_found", func(t *testing.T) {
		err := s.SetParentID("does-not-exist", "parent-1")
		if !errors.Is(err, ErrSpawnNotFound) {
			t.Fatalf("err = %v; want ErrSpawnNotFound", err)
		}
	})
}

// TestSpawnStateTransitionSameStateTwice pins SR-A-2.2: a no-op UPDATE
// (prior state == new state, but last_seen_at changes) still emits an
// ad.spawn.state_transition line. Two consecutive writes to the same target
// state both emit; the second line has prior_state == new_state.
func TestSpawnStateTransitionSameStateTwice(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "trail-noop-twice-001"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-noop", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// Seed: pending → waiting (not under test; captures setup emit).
	if err := s.ApplyHookTransition(id, StateWaiting, false, "SessionStart"); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	// First same-state write: waiting → waiting.
	before1 := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateWaiting, false, "SessionStart"); err != nil {
		t.Fatalf("first same-state write: %v", err)
	}
	lines1 := spawnStateTransitionLines(t, before1)
	if len(lines1) != 1 {
		t.Fatalf("first same-state write: want 1 ad.spawn.state_transition; got %d", len(lines1))
	}
	assertSpawnStateTransitionFields(t, lines1[0], id, StateWaiting, StateWaiting, "SessionStart", false)

	// Second same-state write: still waiting → waiting.
	before2 := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateWaiting, false, "SessionStart"); err != nil {
		t.Fatalf("second same-state write: %v", err)
	}
	lines2 := spawnStateTransitionLines(t, before2)
	if len(lines2) != 1 {
		t.Fatalf("second same-state write: want 1 ad.spawn.state_transition; got %d", len(lines2))
	}
	assertSpawnStateTransitionFields(t, lines2[0], id, StateWaiting, StateWaiting, "SessionStart", false)
}

// TestSpawnStateTransitionSoftRefreshField pins that a soft-refresh write
// emits an ad.spawn.state_transition line with soft_refresh=true and
// prior_state == new_state (state column unchanged, only last_seen_at moves).
func TestSpawnStateTransitionSoftRefreshField(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "trail-softrefresh-field-001"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-sr", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := s.ApplyHookTransition(id, StateWorking, false, "SessionStart"); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	before := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, "", true, "PreToolUse"); err != nil {
		t.Fatalf("soft refresh: %v", err)
	}
	lines := spawnStateTransitionLines(t, before)
	if len(lines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition on soft refresh; got %d", len(lines))
	}
	// soft_refresh=true; prior_state and new_state are both the current state.
	assertSpawnStateTransitionFields(t, lines[0], id, StateWorking, StateWorking, "PreToolUse", true)
}

// TestSpawnStateTransitionNonExistentEmitsZero pins the fail-open contract
// (SRD §3.2): ApplyHookTransition against a non-existent instance_id (no
// row matched) must emit ZERO ad.spawn.state_transition lines.
func TestSpawnStateTransitionNonExistentEmitsZero(t *testing.T) {
	s, _ := openTempStore(t)

	before := len(readStoreTrailLines(t))
	// "ghost-trail-no-row" was never inserted.
	if err := s.ApplyHookTransition("ghost-trail-no-row", StateWaiting, false, "SessionStart"); err != nil {
		t.Fatalf("non-existent id should be no-op: %v", err)
	}
	if got := spawnStateTransitionLines(t, before); len(got) != 0 {
		t.Errorf("non-existent instance_id emitted %d ad.spawn.state_transition; want 0", len(got))
	}
}

// TestSpawnStateTransitionEndedNewState pins the ended-transition code path:
// the emitted ad.spawn.state_transition line must carry new_state="ended".
func TestSpawnStateTransitionEndedNewState(t *testing.T) {
	s, _ := openTempStore(t)
	const id = "trail-ended-newstate-001"
	if err := s.InsertPending(Spawn{
		ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-ended", RelayMode: "off",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	before := len(readStoreTrailLines(t))
	if err := s.ApplyHookTransition(id, StateEnded, false, "SessionEnd"); err != nil {
		t.Fatalf("ended transition: %v", err)
	}
	lines := spawnStateTransitionLines(t, before)
	if len(lines) != 1 {
		t.Fatalf("want 1 ad.spawn.state_transition after ended transition; got %d", len(lines))
	}
	assertSpawnStateTransitionFields(t, lines[0], id, StatePending, StateEnded, "SessionEnd", false)
}

