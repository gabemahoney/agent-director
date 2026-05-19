package store

import (
	"errors"
	"path/filepath"
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

// TestStoreUsesSingleDB makes sure tests that swap DB files via t.TempDir
// don't accidentally share state. The check is a one-liner sanity test
// guarding against subtle filesystem reuse.
func TestStoreUsesSingleDB(t *testing.T) {
	t1 := t.TempDir()
	t2 := t.TempDir()
	if filepath.Clean(t1) == filepath.Clean(t2) {
		t.Fatalf("t.TempDir returned the same dir twice")
	}
}
