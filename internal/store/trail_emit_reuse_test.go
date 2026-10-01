package store

// ad.spawn.state_transition around reuse's reset and restore (SR-14,
// SR-18.13): neither write emits one, and the reused agent's SessionStart
// records prior_state pending. Uses trail_emit_test.go's helpers.

import (
	"reflect"
	"testing"
	"time"
)

// trailReuseToken is a well-formed launch token (SR-3.5) for the reset.
const trailReuseToken = "fedcba9876543210"

// trailReuseFresh is the reset's new life; the store reads no clock, so the
// start times are fixed.
func trailReuseFresh(id string) Spawn {
	return Spawn{CWD: "/tmp", TmuxSessionName: "cd-trail-reused-" + id, RelayMode: "off",
		StartedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), LaunchStartedAtMillis: trailMoveStart,
		Identity: LaunchIdentity{Token: trailReuseToken, Socket: "/tmp/trail-reuse-sock"}}
}

// trailSeedEndedWithSession seeds an ended row that records a session through
// the gated writes (a SessionStart, then the agent's ended transition).
func trailSeedEndedWithSession(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := insertAgentRow(s, Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: "cd-trail-" + id, RelayMode: "off"}); err != nil {
		t.Fatalf("insertAgentRow(%s): %v", id, err)
	}
	if err := agentSessionStart(s, id, "sess-old", "", false); err != nil {
		t.Fatalf("seed SessionStart: %v", err)
	}
	if err := agentHook(s, id, StateEnded, false, "test_seed"); err != nil {
		t.Fatalf("seed ended: %v", err)
	}
}

// trailReuseSeeds are the finished rows a reuse starts from, by name.
var trailReuseSeeds = map[string]func(*testing.T, *Store, string){
	StateEnded:           func(t *testing.T, s *Store, id string) { trailSeedFinished(t, s, id, StateEnded) },
	StateMissing:         func(t *testing.T, s *Store, id string) { trailSeedFinished(t, s, id, StateMissing) },
	"ended_with_session": trailSeedEndedWithSession,
}

// trailReset reads id as reuse does and resets it, failing unless the reset
// applied; it returns the prior life and the reset version.
func trailReset(t *testing.T, s *Store, id string) (RawLife, int64) {
	t.Helper()
	row, found, err := s.ReadForReuse(id)
	if err != nil || !found {
		t.Fatalf("ReadForReuse(%s) = found %v, %v; want found", id, found, err)
	}
	res, _, resetVersion, err := s.ResetForReuse(id, row.Snapshot, trailReuseFresh(id))
	if err != nil || res != CondApplied {
		t.Fatalf("ResetForReuse(%s) = %v, %d, %v; want CondApplied", id, res, resetVersion, err)
	}
	return row.Life, resetVersion
}

// TestTrailReuseWritesEmitNoStateTransition pins SR-14 and SR-18.13: reuse's
// applied reset (its archive included) and restore add no
// ad.spawn.state_transition, and no store trail line names the row.
func TestTrailReuseWritesEmitNoStateTransition(t *testing.T) {
	for _, write := range []string{"reset", "restore"} {
		for seedName, seed := range trailReuseSeeds {
			write, seed := write, seed
			t.Run(write+"_"+seedName, func(t *testing.T) {
				s := openTestStore(t)
				id := "trail-reuse-" + write + "-" + seedName
				seed(t, s, id)
				finished, err := s.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}

				var before int
				switch write {
				case "reset":
					before = len(readStoreTrailLines(t))
					trailReset(t, s, id)
				case "restore":
					prior, resetVersion := trailReset(t, s, id)
					before = len(readStoreTrailLines(t))
					res, err := s.RestoreAfterFailedReuse(id, resetVersion, prior, time.Now())
					if err != nil || res != CondApplied {
						t.Fatalf("RestoreAfterFailedReuse = %v, %v; want CondApplied", res, err)
					}
				}

				if got := spawnStateTransitionLines(t, before); len(got) != 0 {
					t.Errorf("applied %s emitted %d ad.spawn.state_transition; want 0: %v", write, len(got), got)
				}
				for _, row := range readStoreTrailLines(t)[before:] {
					if row["claude_instance_id"] == id {
						t.Errorf("applied %s emitted trail event %v naming the row; want none", write, row["event"])
					}
				}

				// The write did apply: the row is where the write put it.
				got, err := s.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}
				wantState := StatePending
				if write == "restore" {
					wantState = finished.State
				}
				if got.State != wantState {
					t.Errorf("state after %s = %q; want %q", write, got.State, wantState)
				}
			})
		}
	}
}

// TestTrailHookAfterReuseReset pins SR-14's SessionStart part and SR-22.9 on
// the reset row: once the identity write records the new pane, SessionStart
// emits one transition with prior_state pending; before it, neither a
// SessionStart nor an ordinary transition applies, and neither emits one.
func TestTrailHookAfterReuseReset(t *testing.T) {
	sessionStart := func(s *Store, id string) (HookApplied, error) {
		return fireSessionStart(s, id, agentGate("SessionStart", "sess-reused"), "", false)
	}
	ordinary := func(s *Store, id string) (HookApplied, error) {
		return s.ApplyHookTransition(id, agentGate("UserPromptSubmit", ""), StateWorking, false, "UserPromptSubmit", "", false)
	}
	cases := []struct {
		name         string
		identity     bool // the launch's identity write runs first
		hook         func(*Store, string) (HookApplied, error)
		want         HookApplied
		wantNewState string // the applied transition's new state; "" when not applied
	}{
		{"session start after the identity write", true, sessionStart, HookApplied{Applied: true}, StateWaiting},
		{"session start before the identity write", false, sessionStart, HookApplied{Reason: HookReasonNoPaneRecorded}, ""},
		{"ordinary transition before the identity write", false, ordinary, HookApplied{Reason: HookReasonNoPaneRecorded}, ""},
	}
	for _, tc := range cases {
		for _, seedName := range []string{StateEnded, StateMissing} {
			tc, seed := tc, trailReuseSeeds[seedName]
			t.Run(tc.name+"/"+seedName, func(t *testing.T) {
				s := openTestStore(t)
				id := "trail-reuse-hook-" + seedName
				seed(t, s, id)
				_, resetVersion := trailReset(t, s, id)
				if tc.identity {
					if err := recordPaneAt(s, id, resetVersion, trailReuseToken); err != nil {
						t.Fatalf("identity write after the reset: %v", err)
					}
				}
				pending, err := s.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}

				before := len(readStoreTrailLines(t))
				applied, err := tc.hook(s, id)
				if err != nil || applied != tc.want {
					t.Fatalf("hook = %+v, %v; want %+v, nil", applied, err, tc.want)
				}
				lines := spawnStateTransitionLines(t, before)
				got, err := s.GetSpawn(id)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}
				if tc.wantNewState == "" {
					if len(lines) != 0 {
						t.Errorf("ignored hook emitted %d ad.spawn.state_transition; want 0: %v", len(lines), lines)
					}
					if !reflect.DeepEqual(got, pending) {
						t.Errorf("row changed by an ignored hook:\n before %+v\n after  %+v", pending, got)
					}
					return
				}
				if len(lines) != 1 {
					t.Fatalf("want 1 ad.spawn.state_transition; got %d: %v", len(lines), lines)
				}
				assertSpawnStateTransitionFields(t, lines[0], id, StatePending, tc.wantNewState, "SessionStart", false)
				if got.State != tc.wantNewState {
					t.Errorf("state = %q; want %q", got.State, tc.wantNewState)
				}
			})
		}
	}
}
