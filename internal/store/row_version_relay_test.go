package store_test

// SR-5.2 versioning cases for the relay's spawns writes of b.146 step 2: the
// relay hook's first write (rule 1), a move to working held by a request that
// still awaits an answer, which clears idle_since, and the idle-prompt
// Notification's move of a relay-on check_permission row whose request is
// acked (problem 3), appended to row_version_test.go's applied table.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rvRelayToken is the token of the relay request the cases record.
const rvRelayToken = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"

// rvIdleSince is a seeded idle_since, as the idle-prompt Notification stores it.
const rvIdleSince = "2026-01-02 03:04:05"

// rvRelayOn seeds the row with the relay on.
var rvRelayOn = apitest.WithRelayMode("on")

// rvInsertRelay is the relay hook's first write of a request on id, from the
// row's own agent; it must apply.
func rvInsertRelay(t *testing.T, f *v5Store, id string) {
	t.Helper()
	req := store.RelayRequest{RequestToken: rvRelayToken, ToolName: "Bash", ToolInput: `{}`,
		Hook:      store.ProcessIdentity{PID: 4321, Starttime: apitest.LinuxProcStarttime, PIDNamespace: "pid:[4026531836]"},
		SettledAt: time.UnixMilli(1790000000000)}
	if out, applied, err := f.s.InsertRelayRequest(id, rvAgentGate(t, f, id), req, 0, store.DefaultLockWait); err != nil ||
		!applied.Applied || out != store.UpsertInserted {
		t.Fatalf("InsertRelayRequest = %q, %+v, %v; want inserted, applied", out, applied, err)
	}
}

// rvAckedRelayRequest records a relay request on id, decide's allow on it and
// its hook's ack: the request no longer awaits an answer, and the row is in
// check_permission with no launch start.
func rvAckedRelayRequest(t *testing.T, f *v5Store, id string) {
	t.Helper()
	rvInsertRelay(t, f, id)
	ok, err := f.s.DecideRelayRequest(id, rvRelayToken, "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait)
	wantBool(t, "DecideRelayRequest", ok, err, true)
	if _, _, acked, err := f.s.AckRelayDecision(id, rvRelayToken, time.Now(), store.DefaultLockWait, nil); err != nil || !acked {
		t.Fatalf("AckRelayDecision = %v, %v; want acked", acked, err)
	}
}

// idleCleared fails unless idle_since went from set to NULL.
func idleCleared(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if before.IdleSince == nil || after.IdleSince != nil {
		t.Errorf("idle_since %#v -> %#v; want set, then NULL", before.IdleSince, after.IdleSince)
	}
}

// idleSet fails unless idle_since went from NULL to set.
func idleSet(t *testing.T, before, after apitest.SpawnColumns) {
	t.Helper()
	if before.IdleSince != nil || after.IdleSince == nil {
		t.Errorf("idle_since %#v -> %#v; want NULL, then set", before.IdleSince, after.IdleSince)
	}
}

// rvRelayWrites are the relay's applied spawns writes: the first write moves
// a working row to check_permission and clears idle_since and the launch
// start; a held move to working writes only idle_since's clear; the idle
// Notification moves a check_permission row whose request is acked to
// waiting and sets idle_since.
func rvRelayWrites() []rowVersionCase {
	return []rowVersionCase{
		{name: "InsertRelayRequest/applied, working row with idle_since", state: "working",
			opts: []apitest.SpawnOption{rvRelayOn, apitest.WithIdleSince(rvIdleSince)}, clears: true, wantState: "check_permission",
			write: rvInsertRelay, check: idleCleared},
		{name: "ApplyHookTransitionResult/move to working held by a request awaiting an answer, idle_since set", state: "check_permission",
			opts:  []apitest.SpawnOption{rvRelayOn, apitest.WithIdleSince(rvIdleSince)},
			setup: func(t *testing.T, f *v5Store, id string) { seedRequest(t, f, id) }, wantState: "check_permission",
			write: func(t *testing.T, f *v5Store, id string) {
				out, applied, err := f.s.ApplyHookTransitionResult(id, rvAgentGate(t, f, id), "working", false, "row_version_test", "", false)
				if err != nil || !applied.Applied || out != store.UpsertNoChange {
					t.Fatalf("held move to working = %q, %+v, %v; want %q, applied", out, applied, err, store.UpsertNoChange)
				}
			}, check: idleCleared},
		{name: "ApplyHookWaitingIfWorking/check_permission to waiting, its relay request acked", state: "working",
			opts: []apitest.SpawnOption{rvRelayOn}, setup: rvAckedRelayRequest, noLaunchStart: true, clears: true, wantState: "waiting",
			write: waitingIfWorking, check: idleSet},
	}
}
