package store

// working_hold_test.go — the working hold against a request recorded between
// the hold's read and the move to working's UPDATE (b.36h). The race is forced
// with request_proof_test.go's beforeEachExec; seeds are relay_writes_test.go's
// relay rows.

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestStateWriteRacingARequestRecordedAfterItsRead (b.36h): the relay hook
// records request A on another connection after a hook's reads, just before
// its state write. A move to working is then held (check_permission kept,
// UpsertNoChange, applied, a no-op trail line, idle_since cleared), unless its
// gate does not hold, its row is gone or A no longer holds the row; a move to
// waiting carries no hold.
func TestStateWriteRacingARequestRecordedAfterItsRead(t *testing.T) {
	recordA := func(t *testing.T, other *Store, _ string) { insertRelay(t, other, mainReq(tokenA)) }
	cases := []struct {
		name        string
		gate        HookGate
		target      string
		race        func(t *testing.T, other *Store, path string)
		wantState   string // "": the row is gone
		wantOutcome UpsertOutcome
		wantApplied HookApplied
		wantTrail   []string // the one state_transition's prior and new state; prior "": not checked; nil: none
		wantBumps   int64    // row_version advance, the race's included
	}{
		{name: "a move to working is held", gate: agentGate("PreToolUse", ""), target: StateWorking, race: recordA,
			wantState: StateCheckPermission, wantOutcome: UpsertNoChange, wantApplied: HookApplied{Applied: true},
			wantTrail: []string{StateCheckPermission, StateCheckPermission}, wantBumps: 1},
		{name: "a held move to working clears idle_since set after the request", gate: agentGate("PreToolUse", ""), target: StateWorking,
			race: func(t *testing.T, other *Store, path string) {
				recordA(t, other, path)
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `UPDATE spawns SET idle_since = '2026-10-01 00:00:00'`) })
			},
			wantState: StateCheckPermission, wantOutcome: UpsertNoChange, wantApplied: HookApplied{Applied: true},
			wantTrail: []string{StateCheckPermission, StateCheckPermission}, wantBumps: 2},
		{name: "a move to working whose gate does not hold", gate: foreignGate("PreToolUse", ""), target: StateWorking, race: recordA,
			wantState: StateCheckPermission, wantOutcome: UpsertNoChange, wantApplied: HookApplied{Reason: HookReasonPIDMismatch}, wantBumps: 1},
		{name: "a move to working whose row is deleted", gate: agentGate("PreToolUse", ""), target: StateWorking,
			race: func(t *testing.T, _ *Store, path string) {
				withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `DELETE FROM spawns WHERE claude_instance_id = ?`, relayID) })
			},
			wantOutcome: UpsertNoChange, wantApplied: HookApplied{}},
		{name: "a move to working after the request is proven gone", gate: agentGate("PreToolUse", ""), target: StateWorking,
			race: func(t *testing.T, other *Store, path string) {
				recordA(t, other, path)
				proof := RequestProof{How: ProvenGoneToolRan, ToolUseID: "toolu_" + tokenA[:8], At: proofAt}
				if n, err := other.ProveRequestsGone(relayID, agentGate("PostToolUse", ""), proof); err != nil || n != 1 {
					t.Fatalf("ProveRequestsGone = %d, %v; want 1", n, err)
				}
			},
			wantState: StateWorking, wantOutcome: UpsertUpdated, wantApplied: HookApplied{Applied: true},
			wantTrail: []string{"", StateWorking}, wantBumps: 2},
		{name: "a move to waiting", gate: agentGate("Stop", ""), target: StateWaiting, race: recordA,
			wantState: StateWaiting, wantOutcome: UpsertUpdated, wantApplied: HookApplied{Applied: true},
			wantTrail: []string{"", StateWaiting}, wantBumps: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			other, err := Open(path)
			if err != nil {
				t.Fatalf("open a second store: %v", err)
			}
			t.Cleanup(func() { _ = other.Close() })
			before := mustGetSpawn(t, s, relayID)
			landed, mark := false, 0
			beforeEachExec(t, s, path, func(query string) {
				// The hook's state write: hookStateSet leads its SET (b.3ie).
				if landed || !strings.Contains(query, "SET settled_state = CASE") {
					return
				}
				landed = true
				tc.race(t, other, path)
				mark = TrailMark(t) // after the race's own trail lines
			})

			outcome, applied, err := s.ApplyHookTransitionResult(relayID, tc.gate, tc.target, false, tc.gate.Event, "", false)

			if !landed || err != nil {
				t.Fatalf("ApplyHookTransitionResult err = %v, raced %v; want nil, raced", err, landed)
			}
			if outcome != tc.wantOutcome || applied != tc.wantApplied {
				t.Errorf("ApplyHookTransitionResult = %v, %+v; want %v, %+v", outcome, applied, tc.wantOutcome, tc.wantApplied)
			}
			lines := TrailEventsSince(t, mark, "ad.spawn.state_transition", relayID)
			switch {
			case tc.wantTrail == nil && len(lines) != 0:
				t.Errorf("state_transition lines = %d; want none", len(lines))
			case tc.wantTrail != nil && len(lines) != 1:
				t.Errorf("state_transition lines = %d; want 1", len(lines))
			case tc.wantTrail != nil:
				// A held line's prior is the gate read's state. An applied write's
				// prior is read before the race, outside b.36h, so it is not checked.
				prior := tc.wantTrail[0]
				if prior == "" {
					prior, _ = lines[0]["prior_state"].(string)
				}
				assertSpawnStateTransitionFields(t, lines[0], relayID, prior, tc.wantTrail[1], tc.gate.Event, false)
			}
			if tc.wantState == "" {
				if _, err := s.GetSpawn(relayID); !errors.Is(err, ErrSpawnNotFound) {
					t.Errorf("GetSpawn err = %v; want ErrSpawnNotFound", err)
				}
				return
			}
			after := mustGetSpawn(t, s, relayID)
			if after.State != tc.wantState || after.RowVersion != before.RowVersion+tc.wantBumps || after.IdleSince != "" {
				t.Errorf("row = state %q version %d idle_since %q; want %q, %d, cleared",
					after.State, after.RowVersion, after.IdleSince, tc.wantState, before.RowVersion+tc.wantBumps)
			}
			if got := openTokens(t, s, relayID); !slices.Equal(got, []string{tokenA}) {
				t.Errorf("open requests = %v; want A", got)
			}
		})
	}
}
