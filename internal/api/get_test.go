package api_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// openGetFixture seeds a Spawn at the given state with an explicit
// session name and relay_mode. Returns the store so each subtest can
// drive its own permission-row state via the real store helpers.
func openGetFixture(t *testing.T, instanceID, state string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: instanceID,
		CWD:              "/tmp",
		TmuxSessionName:  "cd-" + instanceID,
		RelayMode:        "on",
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(instanceID, state, false); err != nil {
			t.Fatalf("ApplyHookTransition(%s): %v", state, err)
		}
	}
	return s
}

// TestGetCheckPermissionWithOpenRow pins SR-3.1 branch 1: state ==
// check_permission AND an open (Decision == "") permission_requests
// row → response carries a fully populated PermissionRequest sub-object.
//
// Also pins req-review m2: tool_input is the byte-for-byte raw JSON
// string seeded into the DB — no parse / re-emit round trip.
func TestGetCheckPermissionWithOpenRow(t *testing.T) {
	s := openGetFixture(t, "id-g-1", store.StateCheckPermission)
	const rawInput = `{"file":"/tmp/x","mode":"rw"}`
	if err := s.UpsertOpenPermissionRequest("id-g-1", "Read", rawInput); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	got, err := api.Get(s, "id-g-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PermissionRequest == nil {
		t.Fatalf("PermissionRequest is nil; want populated sub-object")
	}
	pr := got.PermissionRequest
	if pr.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if pr.ToolName != "Read" {
		t.Errorf("ToolName = %q; want Read", pr.ToolName)
	}
	if pr.ToolInput != rawInput {
		t.Errorf("ToolInput = %q; want %q (raw JSON string, no parse/re-emit)", pr.ToolInput, rawInput)
	}
	if pr.RequestedAt.IsZero() {
		t.Errorf("RequestedAt is zero; want populated created_at")
	}
}

// TestGetCheckPermissionNoRow pins SR-3.1 branch 2: state ==
// check_permission AND sql.ErrNoRows from the store → PermissionRequest
// stays nil. No error surfaces.
func TestGetCheckPermissionNoRow(t *testing.T) {
	s := openGetFixture(t, "id-g-2", store.StateCheckPermission)

	got, err := api.Get(s, "id-g-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PermissionRequest != nil {
		t.Errorf("PermissionRequest = %+v; want nil (sql.ErrNoRows branch)", got.PermissionRequest)
	}
}

// TestGetCheckPermissionWithDecidedRow pins req-review MAJOR M1: the
// permission-fetch branch MUST gate on pr.Decision == "". A row whose
// Decision is non-empty (decided in a prior cycle) MUST be treated as
// absent. If the implementation were to only check sql.ErrNoRows and
// surface any returned row, this test would fail.
func TestGetCheckPermissionWithDecidedRow(t *testing.T) {
	s := openGetFixture(t, "id-g-3", store.StateCheckPermission)
	if err := s.UpsertOpenPermissionRequest("id-g-3", "Bash", `{"cmd":"ls"}`); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}
	updated, err := s.DecidePermissionRequest("id-g-3", "allow", "trusted")
	if err != nil {
		t.Fatalf("DecidePermissionRequest: %v", err)
	}
	if !updated {
		t.Fatalf("DecidePermissionRequest reported updated=false; want true (seed flow)")
	}

	got, err := api.Get(s, "id-g-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PermissionRequest != nil {
		t.Errorf("PermissionRequest = %+v; want nil — decided rows MUST be treated as absent (M1)", got.PermissionRequest)
	}
}

// TestGetNonCheckPermissionStateSkipsFetch pins SR-3.1 branch 4: when
// state != check_permission, GetPermissionRequest MUST NOT be called.
// Uses a recording fake store so the call-count assertion is direct —
// the equivalent CLI-level test (5th SR-8.3 case) covers the
// behavior via a real DB row.
func TestGetNonCheckPermissionStateSkipsFetch(t *testing.T) {
	fake := &recordingGetStore{
		spawn: store.Spawn{
			ClaudeInstanceID: "id-g-4",
			State:            store.StateWaiting,
			CWD:              "/tmp",
			TmuxSessionName:  "cd-id-g-4",
			RelayMode:        "on",
		},
	}
	got, err := api.Get(fake, "id-g-4")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PermissionRequest != nil {
		t.Errorf("PermissionRequest = %+v; want nil (state != check_permission)", got.PermissionRequest)
	}
	if fake.permCalls != 0 {
		t.Errorf("GetPermissionRequest called %d time(s); want 0 — Get must short-circuit on non-check_permission states", fake.permCalls)
	}
}

// TestGetPropagatesPermissionFetchError pins SR-3.1: a non-ErrNoRows
// error from GetPermissionRequest must propagate to the caller (no
// silent swallow). This is the "any other error" branch of SR-3.1.
func TestGetPropagatesPermissionFetchError(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &recordingGetStore{
		spawn: store.Spawn{
			ClaudeInstanceID: "id-g-5",
			State:            store.StateCheckPermission,
			CWD:              "/tmp",
			TmuxSessionName:  "cd-id-g-5",
			RelayMode:        "on",
		},
		permErr: wantErr,
	}
	_, err := api.Get(fake, "id-g-5")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v; want propagation of %v", err, wantErr)
	}
}

// recordingGetStore is a minimal GetStore double used by the two
// branches that benefit from a behavioral assertion (call-count;
// arbitrary error propagation) rather than a real DB fixture.
type recordingGetStore struct {
	spawn     store.Spawn
	permRow   store.PermissionRow
	permErr   error
	permCalls int
}

func (r *recordingGetStore) GetSpawn(id string) (store.Spawn, error) {
	if r.spawn.ClaudeInstanceID == id {
		return r.spawn, nil
	}
	return store.Spawn{}, store.ErrSpawnNotFound
}

func (r *recordingGetStore) GetPermissionRequest(id string) (store.PermissionRow, error) {
	r.permCalls++
	if r.permErr != nil {
		return store.PermissionRow{}, r.permErr
	}
	if r.permRow.ClaudeInstanceID == "" {
		return store.PermissionRow{}, sql.ErrNoRows
	}
	return r.permRow, nil
}
