package api_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/store"
)

// seedDecideFixture opens a real store and inserts a Spawn row with a
// configurable relay_mode. The caller separately seeds the
// permission_requests row via raw SQL — the only way to drive the
// "row absent" branch without going through the hook handler.
func seedDecideFixture(t *testing.T, relayMode string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: "id-d-1",
		CWD:              "/tmp",
		TmuxSessionName:  "cd-d-1",
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	// Transition into check_permission so it looks realistic.
	if err := s.ApplyHookTransition("id-d-1", store.StateCheckPermission, false); err != nil {
		t.Fatalf("transition: %v", err)
	}
	return s
}

func seedPermissionRow(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if err := s.UpsertOpenPermissionRequest(id, "Bash", `{"cmd":"echo"}`); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest: %v", err)
	}
}

func TestDecideRelayOffRejected(t *testing.T) {
	s := seedDecideFixture(t, "off")
	seedPermissionRow(t, s, "id-d-1")
	_, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "allow",
	})
	if !errors.Is(err, api.ErrRelayModeOff) {
		t.Fatalf("err = %v; want ErrRelayModeOff", err)
	}
}

func TestDecideUnknownSpawn(t *testing.T) {
	s := seedDecideFixture(t, "on")
	_, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "absent", Decision: "allow",
	})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
}

func TestDecideInvalidDecision(t *testing.T) {
	s := seedDecideFixture(t, "on")
	seedPermissionRow(t, s, "id-d-1")
	_, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "perhaps",
	})
	if !errors.Is(err, api.ErrInvalidDecision) {
		t.Fatalf("err = %v; want ErrInvalidDecision", err)
	}
}

func TestDecideFirstCallWins(t *testing.T) {
	// Two consecutive decides on the same open row. The first writes
	// allow; the second sees the populated decision column and the
	// `decision IS NULL` guard short-circuits the UPDATE.
	s := seedDecideFixture(t, "on")
	seedPermissionRow(t, s, "id-d-1")

	if _, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "allow", Reason: "ok",
	}); err != nil {
		t.Fatalf("first Decide: %v", err)
	}

	// Read directly to verify the write landed.
	row, err := s.GetPermissionRequest("id-d-1")
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if row.Decision != "allow" || row.DecisionReason != "ok" {
		t.Errorf("row after first decide: %+v", row)
	}

	// Second call → ErrAlreadyDecided.
	_, err = api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "deny", Reason: "no",
	})
	if !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("second Decide err = %v; want ErrAlreadyDecided", err)
	}

	// Reason from the first decide must not have been clobbered.
	row, _ = s.GetPermissionRequest("id-d-1")
	if row.DecisionReason != "ok" {
		t.Errorf("reason clobbered by second decide: %q", row.DecisionReason)
	}
}

// TestDecideConcurrentFirstCallWins drives N parallel Decide calls
// against the same open row. The SQL `decision IS NULL` guard makes the
// update first-call-wins; exactly one goroutine must succeed and the
// rest must observe ErrAlreadyDecided. Contention surface is the SQL
// boundary, not the Go-level test.
func TestDecideConcurrentFirstCallWins(t *testing.T) {
	const workers = 8
	s := seedDecideFixture(t, "on")
	seedPermissionRow(t, s, "id-d-1")

	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		decision := "allow"
		if i%2 == 1 {
			decision = "deny"
		}
		reason := fmt.Sprintf("w%d", i)
		go func() {
			defer wg.Done()
			<-start
			_, err := api.Decide(s, api.DecideParams{
				ClaudeInstanceID: "id-d-1", Decision: decision, Reason: reason,
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var winners, losers, other int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, store.ErrAlreadyDecided):
			losers++
		default:
			other++
			t.Errorf("unexpected err from concurrent Decide: %v", err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d; want exactly 1", winners)
	}
	if losers != workers-1 {
		t.Errorf("losers = %d; want %d", losers, workers-1)
	}
	if other != 0 {
		t.Errorf("unexpected non-ErrAlreadyDecided errors: %d", other)
	}
}

func TestDecideNoOpenPermissionRequest(t *testing.T) {
	// Spawn exists, relay_mode=on, but no row in permission_requests.
	// The verb surfaces ErrNoOpenPermissionRequest after the UPDATE
	// no-ops and the follow-up SELECT returns sql.ErrNoRows.
	s := seedDecideFixture(t, "on")
	_, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "allow",
	})
	if !errors.Is(err, store.ErrNoOpenPermissionRequest) {
		t.Fatalf("err = %v; want ErrNoOpenPermissionRequest", err)
	}
}

func TestDecideDenyDefaultEnvelopeReasonNotWritten(t *testing.T) {
	// The store records the reason verbatim (NULL when empty); the
	// envelope-level "Denied by orchestrator" default lives in
	// hook.EncodeDecision, NOT in the DB. This test pins that
	// boundary: an empty reason on the verb produces a NULL reason
	// column.
	s := seedDecideFixture(t, "on")
	seedPermissionRow(t, s, "id-d-1")
	if _, err := api.Decide(s, api.DecideParams{
		ClaudeInstanceID: "id-d-1", Decision: "deny", Reason: "",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	row, _ := s.GetPermissionRequest("id-d-1")
	if row.Decision != "deny" {
		t.Errorf("Decision = %q; want deny", row.Decision)
	}
	// PermissionRow uses COALESCE → "" when NULL. The point: the
	// envelope default is NOT pre-written into the column.
	if row.DecisionReason != "" {
		t.Errorf("DecisionReason = %q; want empty (default applied at envelope time, not DB time)", row.DecisionReason)
	}
}

