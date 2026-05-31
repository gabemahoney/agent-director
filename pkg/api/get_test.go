package api_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
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
// row → response carries a fully populated PermissionRequests slice
// with one element.
//
// Also pins req-review m2: tool_input is the byte-for-byte raw JSON
// string seeded into the DB — no parse / re-emit round trip.
func TestGetCheckPermissionWithOpenRow(t *testing.T) {
	s := openGetFixture(t, "id-g-1", store.StateCheckPermission)
	const rawInput = `{"file":"/tmp/x","mode":"rw"}`
	const tok = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	if err := s.UpsertOpenPermissionRequest("id-g-1", tok, "Read", rawInput); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}

	got, err := api.Get(s, "id-g-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.PermissionRequests) != 1 {
		t.Fatalf("len(PermissionRequests) = %d; want 1", len(got.PermissionRequests))
	}
	pr := got.PermissionRequests[0]
	if pr.RequestID == 0 {
		t.Errorf("RequestID = 0; want non-zero autoincrement id")
	}
	if pr.RequestToken != tok {
		t.Errorf("RequestToken = %q; want %q", pr.RequestToken, tok)
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
// check_permission AND no open rows → PermissionRequests is an empty
// non-nil slice. No error surfaces.
func TestGetCheckPermissionNoRow(t *testing.T) {
	s := openGetFixture(t, "id-g-2", store.StateCheckPermission)

	got, err := api.Get(s, "id-g-2")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PermissionRequests == nil {
		t.Errorf("PermissionRequests is nil; want empty non-nil slice")
	}
	if len(got.PermissionRequests) != 0 {
		t.Errorf("len(PermissionRequests) = %d; want 0 (no open rows)", len(got.PermissionRequests))
	}
}

// TestGetCheckPermissionWithDecidedRow pins req-review MAJOR M1: the
// permission-fetch branch MUST NOT surface decided rows. A row whose
// Decision is non-empty (decided in a prior cycle) MUST be absent from
// PermissionRequests. OpenPermissionRequestsForSpawn enforces this at
// the SQL layer (decision IS NULL predicate), so this test pins the
// end-to-end contract.
func TestGetCheckPermissionWithDecidedRow(t *testing.T) {
	s := openGetFixture(t, "id-g-3", store.StateCheckPermission)
	const tok = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	if err := s.UpsertOpenPermissionRequest("id-g-3", tok, "Bash", `{"cmd":"ls"}`); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}
	updated, err := s.DecidePermissionRequest("id-g-3", tok, "allow", "trusted")
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
	if len(got.PermissionRequests) != 0 {
		t.Errorf("len(PermissionRequests) = %d; want 0 — decided rows MUST be absent (M1)", len(got.PermissionRequests))
	}
}

// TestGetNonCheckPermissionStateSkipsFetch pins SR-3.1 branch 4: when
// state != check_permission, OpenPermissionRequestsForSpawn MUST NOT be
// called. Uses a recording fake store so the call-count assertion is
// direct — the equivalent CLI-level test (5th SR-8.3 case) covers the
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
	if len(got.PermissionRequests) != 0 {
		t.Errorf("len(PermissionRequests) = %d; want 0 (state != check_permission)", len(got.PermissionRequests))
	}
	if fake.permCalls != 0 {
		t.Errorf("OpenPermissionRequestsForSpawn called %d time(s); want 0 — Get must short-circuit on non-check_permission states", fake.permCalls)
	}
}

// TestGetPropagatesPermissionFetchError pins SR-3.1: a non-nil error
// from OpenPermissionRequestsForSpawn must propagate to the caller (no
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
	permRows  []store.PermissionRow
	permErr   error
	permCalls int
}

func (r *recordingGetStore) GetSpawn(id string) (store.Spawn, error) {
	if r.spawn.ClaudeInstanceID == id {
		return r.spawn, nil
	}
	return store.Spawn{}, store.ErrSpawnNotFound
}

func (r *recordingGetStore) OpenPermissionRequestsForSpawn(_ string) ([]store.PermissionRow, error) {
	r.permCalls++
	if r.permErr != nil {
		return nil, r.permErr
	}
	return r.permRows, nil
}

// TestGetVerbPluralShape pins the plural PermissionRequests contract
// introduced in Task F:
//
//   - two_open_rows:   two distinct tokens → slice of two, both projected
//   - zero_open_rows:  no open rows → non-nil empty slice, JSON encodes as []
//   - one_closed_row:  one decided row → non-nil empty slice (decided rows invisible)
func TestGetVerbPluralShape(t *testing.T) {
	const tokA = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	const tokB = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"

	t.Run("two_open_rows", func(t *testing.T) {
		s := openGetFixture(t, "id-plural-1", store.StateCheckPermission)
		if err := s.UpsertOpenPermissionRequest("id-plural-1", tokA, "Read", `{"file":"/a"}`); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest A: %v", err)
		}
		if err := s.UpsertOpenPermissionRequest("id-plural-1", tokB, "Bash", `{"cmd":"ls"}`); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest B: %v", err)
		}

		got, err := api.Get(s, "id-plural-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.PermissionRequests) != 2 {
			t.Fatalf("len(PermissionRequests) = %d; want 2", len(got.PermissionRequests))
		}
		// Build a token→row index for order-independent assertions.
		byToken := map[string]api.PermissionRequestInfo{}
		for _, pr := range got.PermissionRequests {
			byToken[pr.RequestToken] = pr
		}
		prA, okA := byToken[tokA]
		prB, okB := byToken[tokB]
		if !okA || !okB {
			t.Fatalf("want both tokens %q and %q in result", tokA, tokB)
		}
		if prA.RequestID == 0 {
			t.Errorf("prA.RequestID = 0; want non-zero")
		}
		if prA.ToolName != "Read" {
			t.Errorf("prA.ToolName = %q; want Read", prA.ToolName)
		}
		if prA.ToolInput != `{"file":"/a"}` {
			t.Errorf("prA.ToolInput = %q; want raw JSON (no parse/re-emit)", prA.ToolInput)
		}
		if prA.RequestedAt.IsZero() {
			t.Errorf("prA.RequestedAt is zero; want populated")
		}
		if prB.ToolName != "Bash" {
			t.Errorf("prB.ToolName = %q; want Bash", prB.ToolName)
		}
	})

	t.Run("zero_open_rows", func(t *testing.T) {
		s := openGetFixture(t, "id-plural-2", store.StateCheckPermission)

		got, err := api.Get(s, "id-plural-2")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.PermissionRequests == nil {
			t.Errorf("PermissionRequests is nil; want non-nil empty slice")
		}
		if len(got.PermissionRequests) != 0 {
			t.Errorf("len(PermissionRequests) = %d; want 0", len(got.PermissionRequests))
		}
		// JSON must encode as [] not null.
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		if !strings.Contains(string(raw), `"permission_requests":[]`) {
			t.Errorf("JSON does not contain permission_requests:[]; got %s", raw)
		}
	})

	t.Run("one_closed_row", func(t *testing.T) {
		s := openGetFixture(t, "id-plural-3", store.StateCheckPermission)
		if err := s.UpsertOpenPermissionRequest("id-plural-3", tokA, "Write", `{"path":"/x"}`); err != nil {
			t.Fatalf("UpsertOpenPermissionRequest: %v", err)
		}
		updated, err := s.DecidePermissionRequest("id-plural-3", tokA, "allow", "")
		if err != nil {
			t.Fatalf("DecidePermissionRequest: %v", err)
		}
		if !updated {
			t.Fatalf("DecidePermissionRequest updated=false; want true")
		}

		got, err := api.Get(s, "id-plural-3")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.PermissionRequests == nil {
			t.Errorf("PermissionRequests is nil; want non-nil empty slice")
		}
		if len(got.PermissionRequests) != 0 {
			t.Errorf("len(PermissionRequests) = %d; want 0 — decided row must be invisible", len(got.PermissionRequests))
		}
	})
}
