package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
)

// seedWriteFailureRow inserts a pending row for id, its agent's pane recorded,
// and, for any other state, moves it there with the agent's hook transition.
func seedWriteFailureRow(t *testing.T, s *Store, id, state string) {
	t.Helper()
	seedSpawnForPerm(t, s, id, "off")
	if state == StatePending {
		return
	}
	if err := agentHook(s, id, state, false, "test_seed"); err != nil {
		t.Fatalf("seed %s as %s: %v", id, state, err)
	}
}

// getWriteFailureRow reads id's row, failing the test on error.
func getWriteFailureRow(t *testing.T, s *Store, id string) Spawn {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return sp
}

// moveToPendingWrite is resume's move to pending of id's row as examined now;
// a move that does not apply is an error.
func moveToPendingWrite(s *Store, id string) error {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	res, _, err := s.MoveToPending(id, sp.Snapshot, 1767225600000, "0123456789abcdef", "/tmp/wf-sock", "")
	if err == nil && res != CondApplied {
		err = fmt.Errorf("MoveToPending(%s) = %v; want CondApplied", id, res)
	}
	return err
}

// TestInjectWriteFailure_WhiteBox proves the white-box installer fails the
// matching store write for its id only, and that its cleanup removes it.
func TestInjectWriteFailure_WhiteBox(t *testing.T) {
	cases := []struct {
		kind               writefailfix.Kind
		fromState, toState string
		write              func(s *Store, id string) error
	}{
		// SR-22.9: no hook moves a finished row to pending; resume's move does.
		{writefailfix.ReuseReset, StateEnded, StatePending, moveToPendingWrite},
		{writefailfix.ReuseRestore, StatePending, StateEnded, func(s *Store, id string) error {
			return agentHook(s, id, StateEnded, false, "SessionEnd")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.kind.String(), func(t *testing.T) {
			s := openTestStore(t)
			const target, other = "wf-target", "wf-other"
			seedWriteFailureRow(t, s, target, tc.fromState)
			seedWriteFailureRow(t, s, other, tc.fromState)
			write := func(id string) error { return tc.write(s, id) }

			t.Run("installed", func(t *testing.T) {
				injectWriteFailure(t, s, tc.kind, target)
				before := getWriteFailureRow(t, s, target)

				err := write(target)
				if err == nil || !strings.Contains(err.Error(), "injected write failure: "+tc.kind.String()) {
					t.Fatalf("write on %s = %v, want the injected %v failure", target, err, tc.kind)
				}
				if after := getWriteFailureRow(t, s, target); !reflect.DeepEqual(after, before) {
					t.Errorf("row changed by failed write:\n before %+v\n after  %+v", before, after)
				}

				if err := write(other); err != nil {
					t.Fatalf("write on other id %s: %v", other, err)
				}
				if got := getWriteFailureRow(t, s, other).State; got != tc.toState {
					t.Errorf("other id state = %q, want %q", got, tc.toState)
				}
			})

			if err := write(target); err != nil {
				t.Fatalf("write on %s after cleanup: %v", target, err)
			}
			if got := getWriteFailureRow(t, s, target).State; got != tc.toState {
				t.Errorf("state after cleanup = %q, want %q", got, tc.toState)
			}
		})
	}
}
