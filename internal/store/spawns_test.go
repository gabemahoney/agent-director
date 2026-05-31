package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

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
	if err := s.ApplyHookTransition(id, StateEnded, false); err != nil {
		t.Fatalf("transition to ended: %v", err)
	}
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
	if err := s.ApplyHookTransition(id, StateWaiting, false); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
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
	if err := s.ApplyHookTransition(id, StateEnded, false); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
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
	if err := s.ApplyHookTransition(id, StateEnded, false); err != nil {
		t.Fatalf("transition to ended: %v", err)
	}
	got, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn after ended: %v", err)
	}
	if got.EndedAt == nil {
		t.Fatal("precondition: ended_at not set after ended transition")
	}

	// Simulate the SessionStart hook firing on the resurrected Claude.
	if err := s.ApplyHookTransition(id, StateWaiting, false); err != nil {
		t.Fatalf("resurrection transition: %v", err)
	}
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
	if err := s.ApplyHookTransition(id, StateWaiting, false); err != nil {
		t.Fatalf("transition: %v", err)
	}
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
	if err := s.ApplyHookTransition(id, "", true); err != nil {
		t.Fatalf("ApplyHookTransition: %v", err)
	}
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
	if err := s.ApplyHookTransition("ghost", StateWaiting, false); err != nil {
		t.Fatalf("transition on missing row should be no-op: %v", err)
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
		if err := s.ApplyHookTransition(id, StateCheckPermission, false); err != nil {
			t.Fatalf("transition to check_permission: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"cmd":"ls"}`, 0); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenB, "Read", `{"file":"/etc"}`, 0); err != nil {
			t.Fatalf("upsert tokenB: %v", err)
		}

		// Decide row A; try to advance to working — tokenB still open, must be held.
		if _, err := s.DecidePermissionRequest(id, tokenA, "allow", ""); err != nil {
			t.Fatalf("decide tokenA: %v", err)
		}
		if err := s.ApplyHookTransition(id, StateWorking, false); err != nil {
			t.Fatalf("ApplyHookTransition(working) after deciding tokenA: %v", err)
		}
		state, err := s.GetSpawnState(id)
		if err != nil {
			t.Fatalf("GetSpawnState: %v", err)
		}
		if state != StateCheckPermission {
			t.Errorf("state = %q after deciding 1 of 2 rows; want check_permission (held)", state)
		}

		// Decide row B; advance to working — no open rows remain.
		if _, err := s.DecidePermissionRequest(id, tokenB, "deny", "no"); err != nil {
			t.Fatalf("decide tokenB: %v", err)
		}
		if err := s.ApplyHookTransition(id, StateWorking, false); err != nil {
			t.Fatalf("ApplyHookTransition(working) after deciding tokenB: %v", err)
		}
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
		if err := s.ApplyHookTransition(id, StateCheckPermission, false); err != nil {
			t.Fatalf("transition to check_permission: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest(id, tokenA, "Bash", `{"cmd":"pwd"}`, 0); err != nil {
			t.Fatalf("upsert tokenA: %v", err)
		}

		// Decide the single row; advance must succeed immediately.
		if _, err := s.DecidePermissionRequest(id, tokenA, "allow", ""); err != nil {
			t.Fatalf("decide tokenA: %v", err)
		}
		if err := s.ApplyHookTransition(id, StateWorking, false); err != nil {
			t.Fatalf("ApplyHookTransition(working): %v", err)
		}
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

