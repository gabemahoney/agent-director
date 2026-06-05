package hook_test

// SR-9.2: TestParallelHookOrdering and TestPerRowTimeoutIsolation.
//
// Both tests use a real *store.Store (via storefix.OpenTempStore) to exercise
// actual SQLite write-lock interleaving rather than a mock. This lets us verify
// that distinct requestToken values key distinct rows and that timeouts on one
// row do not affect another row for the same Spawn.

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// seedWorkingSpawn inserts a Spawn in StateWorking with relay_mode=on for the
// given instanceID. Used as the precondition for parallel-hook tests that drive
// PermissionRequest events via Handle.
func seedWorkingSpawn(t *testing.T, st *store.Store, instanceID string) {
	t.Helper()
	sp := store.Spawn{
		ClaudeInstanceID: instanceID,
		State:            store.StatePending,
		CWD:              "/tmp",
		TmuxSessionName:  "t-" + instanceID,
		RelayMode:        "on",
	}
	if err := st.InsertPending(sp); err != nil {
		t.Fatalf("seedWorkingSpawn: InsertPending(%q): %v", instanceID, err)
	}
	if err := st.ApplyHookTransition(instanceID, store.StateWorking, false, "test_seed"); err != nil {
		t.Fatalf("seedWorkingSpawn: transition to working (%q): %v", instanceID, err)
	}
}

// waitForOpenRows polls OpenPermissionRequestsForSpawn until at least n open
// rows appear or the deadline expires. Returns the rows found; fails the test
// if fewer than n rows arrive before the deadline.
func waitForOpenRows(t *testing.T, st *store.Store, instanceID string, n int) []store.PermissionRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := st.OpenPermissionRequestsForSpawn(instanceID)
		if err == nil && len(rows) >= n {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := st.OpenPermissionRequestsForSpawn(instanceID)
	t.Fatalf("waitForOpenRows: want %d rows for %q; got %d after 5s", n, instanceID, len(rows))
	return nil
}

// TestParallelHookOrdering drives two concurrent hook.Handle calls for the same
// Spawn and asserts that:
//  1. Two distinct request_token values are written to permission_requests.
//  2. Deciding row A (allow) does not cause row B's poll loop to emit any
//     envelope — row B stays undecided until we decide it explicitly.
//  3. Each goroutine receives its own verdict (A→allow, B→deny); no
//     cross-row verdict leakage.
//  4. The Spawn remains in check_permission state until the last row is decided.
func TestParallelHookOrdering(t *testing.T) {
	st, _ := storefix.OpenTempStore(t)
	const instanceID = "parallel-hook-ord"

	seedWorkingSpawn(t, st, instanceID)

	env := envWith(instanceID)
	var bufA, bufB bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_ = hook.Handle(context.Background(),
			strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`),
			&bufA, st,
			hook.HandleConfig{
				Env: env,
				Cfg: config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
			},
			nil)
	}()
	go func() {
		defer wg.Done()
		_ = hook.Handle(context.Background(),
			strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Read","tool_input":{}}`),
			&bufB, st,
			hook.HandleConfig{
				Env: env,
				Cfg: config.Relay{TimeoutSeconds: 30, PollBaseMs: 0, PollJitterMs: 0},
			},
			nil)
	}()

	// Wait for both open rows to appear.
	rows := waitForOpenRows(t, st, instanceID, 2)

	// Map tokens by tool_name to avoid depending on SQLite's insertion order.
	// SQLite TIMESTAMP is second-precision; concurrent inserts tie and the
	// ORDER BY created_at ASC ordering is undefined when timestamps collide.
	var tokenA, tokenB string
	for _, r := range rows {
		switch r.ToolName {
		case "Bash":
			tokenA = r.RequestToken
		case "Read":
			tokenB = r.RequestToken
		}
	}
	if tokenA == "" || tokenB == "" {
		t.Fatalf("could not identify tokens by tool_name: Bash=%q Read=%q", tokenA, tokenB)
	}
	if tokenA == tokenB {
		t.Fatalf("tokens not distinct: both = %q", tokenA)
	}

	// Spawn must be in check_permission before any row is decided.
	state, err := st.GetSpawnState(instanceID)
	if err != nil {
		t.Fatalf("GetSpawnState: %v", err)
	}
	if state != store.StateCheckPermission {
		t.Errorf("state = %q before last row decided; want check_permission", state)
	}

	// Decide row A (allow). Row B must remain undecided — no cross-row leakage.
	if _, err := st.DecidePermissionRequest(instanceID, tokenA, "allow", "", ""); err != nil {
		t.Fatalf("DecidePermissionRequest(A): %v", err)
	}

	// Give goroutine A's poll loop time to observe and exit.
	time.Sleep(150 * time.Millisecond)

	rowB, err := st.GetPermissionRequest(instanceID, tokenB)
	if err != nil {
		t.Fatalf("GetPermissionRequest(B before decide): %v", err)
	}
	if rowB.Decision != "" {
		t.Errorf("row B prematurely decided = %q after deciding A only", rowB.Decision)
	}

	// Decide row B (deny) — last row, unblocks goroutine B.
	if _, err := st.DecidePermissionRequest(instanceID, tokenB, "deny", "not allowed", ""); err != nil {
		t.Fatalf("DecidePermissionRequest(B): %v", err)
	}

	wg.Wait()

	// Each goroutine must carry its own verdict.
	if !strings.Contains(bufA.String(), `"behavior":"allow"`) {
		t.Errorf("goroutine A: want allow envelope; got %q", bufA.String())
	}
	if !strings.Contains(bufB.String(), `"behavior":"deny"`) {
		t.Errorf("goroutine B: want deny envelope; got %q", bufB.String())
	}
	// Cross-contamination guard: A must not carry deny, B must not carry allow.
	if strings.Contains(bufA.String(), `"behavior":"deny"`) {
		t.Errorf("goroutine A received deny (row B verdict leaked): %q", bufA.String())
	}
	if strings.Contains(bufB.String(), `"behavior":"allow"`) {
		t.Errorf("goroutine B received allow (row A verdict leaked): %q", bufB.String())
	}
}

// TestPerRowTimeoutIsolation verifies that a polling timeout on row A writes
// decision='deny' and decision_reason='timeout' to row A without affecting row
// B's verdict. Specifically:
//   - Row A: decision='deny', decision_reason == store.DecisionReasonTimeout
//   - Row B: decision='allow', decision_reason IS NULL (empty string via COALESCE)
//   - Row B's raw decision_reason column is confirmed NULL via a direct SQL query.
//
// Goroutine A uses a 1-second real timeout so the test takes ~1s wall-clock.
func TestPerRowTimeoutIsolation(t *testing.T) {
	st, dbPath := storefix.OpenTempStore(t)
	const instanceID = "per-row-timeout-iso"

	seedWorkingSpawn(t, st, instanceID)

	env := envWith(instanceID)
	var bufA, bufB bytes.Buffer

	// Goroutine A: 1-second timeout with the real poll clock (50ms floor per
	// iteration ≈ 20 polls before the deadline expires).
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		_ = hook.Handle(context.Background(),
			strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{}}`),
			&bufA, st,
			hook.HandleConfig{
				Env: env,
				Cfg: config.Relay{TimeoutSeconds: 1, PollBaseMs: 0, PollJitterMs: 0},
			},
			nil)
	}()

	// Goroutine B: long timeout; will be decided externally before it expires.
	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		_ = hook.Handle(context.Background(),
			strings.NewReader(`{"hook_event_name":"PermissionRequest","tool_name":"Read","tool_input":{}}`),
			&bufB, st,
			hook.HandleConfig{
				Env: env,
				Cfg: config.Relay{TimeoutSeconds: 60, PollBaseMs: 0, PollJitterMs: 0},
			},
			nil)
	}()

	// Wait for both open rows to appear.
	rows := waitForOpenRows(t, st, instanceID, 2)

	// Identify tokens by tool_name — the rows are ordered by created_at ASC.
	var tokenA, tokenB string
	for _, r := range rows {
		switch r.ToolName {
		case "Bash":
			tokenA = r.RequestToken
		case "Read":
			tokenB = r.RequestToken
		}
	}
	if tokenA == "" || tokenB == "" {
		t.Fatalf("could not identify tokens: Bash=%q Read=%q", tokenA, tokenB)
	}

	// Wait for goroutine A to time out (≤ 2s).
	select {
	case <-doneA:
	case <-time.After(4 * time.Second):
		t.Fatal("goroutine A did not complete within 4s; expected 1s timeout")
	}

	// Row A: runRelay timeout path must have written deny + DecisionReasonTimeout.
	rowA, err := st.GetPermissionRequest(instanceID, tokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest(A): %v", err)
	}
	if rowA.Decision != "deny" {
		t.Errorf("row A decision = %q; want deny", rowA.Decision)
	}
	if rowA.DecisionReason != store.DecisionReasonTimeout {
		t.Errorf("row A decision_reason = %q; want %q", rowA.DecisionReason, store.DecisionReasonTimeout)
	}

	// Row B must still be undecided — row A's timeout must not touch row B.
	rowBOpen, err := st.GetPermissionRequest(instanceID, tokenB)
	if err != nil {
		t.Fatalf("GetPermissionRequest(B before decide): %v", err)
	}
	if rowBOpen.Decision != "" {
		t.Errorf("row B unexpectedly decided = %q after row A timeout", rowBOpen.Decision)
	}

	// Operator decides row B (allow, no reason).
	if _, err := st.DecidePermissionRequest(instanceID, tokenB, "allow", "", ""); err != nil {
		t.Fatalf("DecidePermissionRequest(B): %v", err)
	}

	// Wait for goroutine B to observe the decision and exit.
	select {
	case <-doneB:
	case <-time.After(4 * time.Second):
		t.Fatal("goroutine B did not complete within 4s after decision")
	}

	// Row B: decision=allow, decision_reason empty (NULL).
	rowB, err := st.GetPermissionRequest(instanceID, tokenB)
	if err != nil {
		t.Fatalf("GetPermissionRequest(B after decide): %v", err)
	}
	if rowB.Decision != "allow" {
		t.Errorf("row B decision = %q; want allow", rowB.Decision)
	}
	if rowB.DecisionReason != "" {
		t.Errorf("row B decision_reason = %q; want empty (NULL column)", rowB.DecisionReason)
	}

	// Verify row B's decision_reason IS NULL in the raw DB column — COALESCE('')
	// in GetPermissionRequest maps NULL to ""; confirm the underlying NULL here.
	rawDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = rawDB.Close() }()

	var rawReasonB sql.NullString
	if err := rawDB.QueryRow(
		`SELECT decision_reason FROM permission_requests WHERE claude_instance_id = ? AND request_token = ?`,
		instanceID, tokenB,
	).Scan(&rawReasonB); err != nil {
		t.Fatalf("raw query row B: %v", err)
	}
	if rawReasonB.Valid {
		t.Errorf("row B raw decision_reason = %q; want NULL (operator allow has no reason)", rawReasonB.String)
	}

	// Assert envelopes.
	assertDenyEnvelope(t, &bufA) // row A timed out → deny
	if !strings.Contains(bufB.String(), `"behavior":"allow"`) {
		t.Errorf("goroutine B: want allow envelope; got %q", bufB.String())
	}
}
