package api_test

// resume_pending_hook_test.go covers hooks around resume's move to pending
// under the hook gate (SR-22.9, SR-8.3, SR-8.5, SR-20.6; AC-RES-13): the move
// clears the row's pane, so a hook before the identity write or the restore is
// ignored, and after the identity write only the new pane's agent reports in.
// On the shared fixture in resume_fixture_test.go against a real store.

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// pendSessionStart delivers a SessionStart for id from its agent, the row's
// recorded pane process (SR-22.9: the new pane resume's identity write
// recorded), reporting transcript, whose basename is the session id and which
// exists on disk. It fails the test unless the hook applies.
func pendSessionStart(t *testing.T, e *resumeEnv, id, transcript string) {
	t.Helper()
	sessionID := strings.TrimSuffix(filepath.Base(transcript), ".jsonl")
	if got := apitest.ApplyAgentHook(t, e.dbPath, id, "SessionStart", sessionID,
		apitest.HookTranscript(transcript, true)); !got.Applied {
		t.Fatalf("SessionStart from the agent = %+v; want applied", got)
	}
}

// TestResumeSessionStartAfterMoveTurnsRowWaiting: after the move, a SessionStart
// from another process is ignored; the resumed agent's (the new pane's) makes
// the row waiting with no launch start, prior_state pending.
func TestResumeSessionStartAfterMoveTurnsRowWaiting(t *testing.T) {
	t.Parallel()
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateMissing)
	if _, err := e.resume(r.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	seen := len(pendTrail(t, "ad.spawn.state_transition", r.ID))

	// SR-22.9: a process other than the recorded (new) pane's, e.g. the old
	// agent carrying the id, changes nothing.
	moved := e.columns(t, r.ID)
	if got := apitest.ApplyForeignHook(t, e.dbPath, r.ID, "SessionStart", r.SessionID,
		apitest.HookTranscript(r.JSONLPath, true)); got.Applied || got.Reason != store.HookReasonPIDMismatch {
		t.Errorf("SessionStart from another process = %+v; want not applied, %s", got, store.HookReasonPIDMismatch)
	}
	if got := e.columns(t, r.ID); !reflect.DeepEqual(got, moved) {
		t.Errorf("row after the ignored SessionStart =\n  %+v\nwant unchanged\n  %+v", got, moved)
	}
	if n := len(pendTrail(t, "ad.spawn.state_transition", r.ID)) - seen; n != 0 {
		t.Errorf("ad.spawn.state_transition lines from the ignored SessionStart = %d; want 0", n)
	}

	pendSessionStart(t, e, r.ID, r.JSONLPath)

	cols := e.columns(t, r.ID)
	if cols.State != store.StateWaiting || cols.LaunchStartedAt != nil {
		t.Errorf("row {state %v, launch_started_at %#v}; want waiting, NULL", cols.State, cols.LaunchStartedAt)
	}
	lines := pendTrail(t, "ad.spawn.state_transition", r.ID)[seen:]
	if len(lines) != 1 {
		t.Fatalf("ad.spawn.state_transition lines from SessionStart = %d; want 1", len(lines))
	}
	assertAPITrailStr(t, lines[0], "prior_state", store.StatePending)
	assertAPITrailStr(t, lines[0], "new_state", store.StateWaiting)
	assertAPITrailStr(t, lines[0], "triggering_event_name", "SessionStart")
}

// TestResumeHooksBeforeRestoreIgnored (SR-22.9, SR-20.6, decision A7): the
// moved row records no pane and the failed create recorded none, so an ordinary
// hook and a SessionStart carrying the row's id before the restore are both
// ignored (no_pane_recorded) and the restore applies.
func TestResumeHooksBeforeRestoreIgnored(t *testing.T) {
	t.Parallel()
	for _, onCreate := range []bool{true, false} {
		t.Run(map[bool]string{true: "on the failing create", false: "after the move"}[onCreate], func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
			var got []store.HookApplied
			fire := func() {
				for _, ev := range []string{"Stop", "SessionStart"} {
					got = append(got, apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.SessionID,
						apitest.HookTranscript(r.JSONLPath, true)))
				}
			}
			if onCreate {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { fire() })
			} else {
				e.store.afterMove(fire)
			}

			_, err := e.resume(r.ID)
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			create := rstOneCreate(t, e)
			ignored := store.HookApplied{Reason: store.HookReasonNoPaneRecorded}
			if len(got) != 2 || got[0] != ignored || got[1] != ignored {
				t.Errorf("Stop, SessionStart = %+v; want both %+v", got, ignored)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).
				AfterResumeRestore(apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded}),
				create.Token, e.storeID, r.Identity.Token)
			rstAssertRow(t, e, r.ID, rstRestored(r, nil))
			rstAssertRestoredTrail(t, r.ID, true, "ErrTmuxSessionCreate", nil)
		})
	}
}
