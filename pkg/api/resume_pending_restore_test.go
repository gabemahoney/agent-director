package api_test

// resume_pending_restore_test.go covers resume after its move to pending
// (SR-8.5, SR-3.5, SR-5.8, SR-14; AC-RES-06, AC-RES-07, AC-RES-12, AC-RES-13):
// the restore after each launch failure, a restore that is not applied, store
// errors on the move and the restore, timeouts that leave the row pending, and
// the unlabelled-session path. The resumeEnv fixture is in resume_fixture_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rstSeedOther seeds a live row with no session, to serve as a parent id.
func rstSeedOther(t *testing.T, e *resumeEnv) string {
	t.Helper()
	id := "rst-parent-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(e.dbPath, id, store.StateWaiting, t.TempDir(), "off", "", false); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", id, err)
	}
	return id
}

// rstColumns reads id's row raw; present is false when the row is gone.
func rstColumns(t *testing.T, e *resumeEnv, id string) (cols apitest.SpawnColumns, present bool) {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return apitest.SpawnColumns{}, false
	}
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols, true
}

// rstRestored is r's row after a move and an applied restore: every column as
// seeded, the move's parent id, row_version two past the seed.
func rstRestored(r resumableRow, parent any) apitest.SpawnColumns {
	w := r.Before
	w.ParentID = parent
	w.RowVersion = r.Before.RowVersion.(int64) + 2
	return w
}

// rstMoved is r's row after the move alone: pending with the launch start,
// token and socket, the cleared columns NULL, no parent, row_version one past the seed.
func rstMoved(r resumableRow, launchStart int64, token, socket string) apitest.SpawnColumns {
	w := r.Before
	w.State, w.LaunchStartedAt, w.LaunchToken, w.TmuxSocket, w.ParentID = store.StatePending, launchStart, token, socket, nil
	w.PID, w.ProcStarttime, w.EndedAt, w.LivenessUnverifiedSince, w.LivenessNote = nil, nil, nil, nil, nil
	w.TmuxServerPID, w.TmuxServerStarted, w.TmuxServerStarttime, w.PaneID, w.PanePID, w.PaneStarttime = nil, nil, nil, nil, nil, nil
	w.RowVersion = r.Before.RowVersion.(int64) + 1
	return w
}

// rstAssertRow fails unless id's row equals want column for column.
func rstAssertRow(t *testing.T, e *resumeEnv, id string, want apitest.SpawnColumns) {
	t.Helper()
	if got := e.columns(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row %s =\n  %+v\nwant\n  %+v", id, got, want)
	}
}

// rstTrail returns id's ad.resume.* trail lines, in the order they were written.
func rstTrail(t *testing.T, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range readAPITrailLines(t) {
		if ev, _ := l["event"].(string); strings.HasPrefix(ev, "ad.resume.") && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// rstTrailOf returns id's trail lines of event.
func rstTrailOf(t *testing.T, id, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range rstTrail(t, id) {
		if l["event"] == event {
			out = append(out, l)
		}
	}
	return out
}

// rstAssertRestoredTrail checks id has exactly one ad.resume.restored with
// applied, launch_error and restore_error (nil: none).
func rstAssertRestoredTrail(t *testing.T, id string, applied bool, launchErr string, restoreErr any) {
	t.Helper()
	lines := rstTrailOf(t, id, "ad.resume.restored")
	if len(lines) != 1 {
		t.Fatalf("ad.resume.restored lines = %v; want exactly one", lines)
	}
	l := lines[0]
	if l["applied"] != applied || l["launch_error"] != launchErr || l["restore_error"] != restoreErr || l["source"] != "ad_resume" {
		t.Errorf("ad.resume.restored = %v; want applied %v, launch_error %s, restore_error %v, source ad_resume",
			l, applied, launchErr, restoreErr)
	}
}

// rstOneCreate returns the one recorded create, failing unless it is on e's socket.
func rstOneCreate(t *testing.T, e *resumeEnv) tmuxfix.SocketCall {
	t.Helper()
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(creates) != 1 || creates[0].Socket != e.socket {
		t.Fatalf("create calls = %+v; want exactly one on %s", creates, e.socket)
	}
	return creates[0]
}

// TestResumeRestoreAfterEachLaunchFailure: every non-timeout create failure
// restores the ended or missing row byte for byte, keeps the move's parent id,
// emits one applied ad.resume.restored, and a second resume then launches.
func TestResumeRestoreAfterEachLaunchFailure(t *testing.T) {
	createFailed := func(*resumeEnv, string) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{})
	}
	triggers := []struct {
		name     string
		fail     tmux.Failure
		want     error
		wantName string
		desc     func(e *resumeEnv, name string) apitest.DescCase
	}{
		{"unrecognised create failure", tmux.FailUnrecognized, tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate", createFailed},
		{"no-socket reply", tmux.FailNoSocket, tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate", createFailed},
		{"duplicate session", tmux.FailDuplicate, tmux.ErrTmuxSessionCreate, "ErrTmuxSessionCreate",
			func(_ *resumeEnv, name string) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: name, Duplicate: true})
			}},
		{"tmux binary not run", tmux.FailUnavailable, tmux.ErrTmuxNotAvailable, "ErrTmuxNotAvailable",
			func(*resumeEnv, string) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"socket permission denied", tmux.FailSocketDenied, tmux.ErrTmuxNotAvailable, "ErrTmuxNotAvailable",
			func(e *resumeEnv, _ string) apitest.DescCase { return apitest.DescSocketPermission(e.socket) }},
	}
	for _, tr := range triggers {
		for _, prior := range []string{store.StateEnded, store.StateMissing} {
			t.Run(tr.name+"/"+prior, func(t *testing.T) {
				e := newResumeEnv(t)
				parent := rstSeedOther(t, e)
				t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent)
				r := e.seedResumable(t, prior)
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tr.fail, ExitStatus: 1, Times: 1}, tmux.CallCreate)

				_, err := e.resume(r.ID)
				assertLaunchSentinel(t, err, tr.want)
				create := rstOneCreate(t, e)
				if got := callKinds(e.rec); !reflect.DeepEqual(got, []tmux.Call{tmux.CallCreate}) {
					t.Errorf("tmux calls = %v; want the one create", got)
				}
				apitest.AssertDescription(t, err.Error(), tr.desc(e, create.Target).AfterResumeRestore(
					apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: prior}),
					create.Token, e.storeID, r.Identity.Token)
				rstAssertRow(t, e, r.ID, rstRestored(r, parent))
				if moved := rstTrailOf(t, r.ID, "ad.resume.moved_to_pending"); len(moved) != 1 || moved[0]["prior_state"] != prior {
					t.Errorf("ad.resume.moved_to_pending = %v; want one with prior_state %s", moved, prior)
				}
				rstAssertRestoredTrail(t, r.ID, true, tr.wantName, nil)
				if e.logs.Len() != 0 {
					t.Errorf("client log = %q; want nothing", e.logs.String())
				}

				// AC-RES-12: with the cause gone, an immediate second resume launches.
				if _, err := e.resume(r.ID); err != nil {
					t.Fatalf("second resume: %v", err)
				}
				if cols := e.columns(t, r.ID); cols.State != store.StatePending || len(e.rec.SocketCallsOf(tmux.CallCreate)) != 2 {
					t.Errorf("after the second resume: state %v, creates %d; want pending, 2", cols.State,
						len(e.rec.SocketCallsOf(tmux.CallCreate)))
				}
			})
		}
	}
}

// TestResumeRestoreNotAppliedAfterAnotherWrite: a write that lands between the
// move and the restore stands; the restore writes nothing and resume still fails.
func TestResumeRestoreNotAppliedAfterAnotherWrite(t *testing.T) {
	cases := []struct {
		name      string
		onCreate  bool // run write from the create's after-call hook; else after the move
		write     func(e *resumeEnv, id, other string) error
		outcome   apitest.RestoreOutcome
		wantState any // the row's state after resume; nil: the row is gone
	}{
		// Hook-driven: Task rc's hook gate (S17) converts this case.
		{"SessionStart on the failing create", true, func(e *resumeEnv, id, _ string) error {
			return e.st.ApplyHookTransition(id, store.StateWaiting, false, "SessionStart")
		}, apitest.RestoreRowChanged, store.StateWaiting},
		{"parent id written after the move", false, func(e *resumeEnv, id, other string) error {
			return e.st.SetParentID(id, other)
		}, apitest.RestoreRowChanged, store.StatePending},
		{"row removed after the move", false, func(e *resumeEnv, id, _ string) error {
			return e.st.DeleteSpawn(id)
		}, apitest.RestoreRowRemoved, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			other := rstSeedOther(t, e)
			r := e.seedResumable(t, store.StateEnded)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
			var written apitest.SpawnColumns
			var present bool
			write := func() {
				if err := tc.write(e, r.ID, other); err != nil {
					t.Errorf("write: %v", err)
				}
				written, present = rstColumns(t, e, r.ID)
			}
			if tc.onCreate {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { write() })
			} else {
				e.store.afterMove(write)
			}

			_, err := e.resume(r.ID)
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			create := rstOneCreate(t, e)
			apitest.AssertDescription(t, err.Error(), apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).
				AfterResumeRestore(apitest.ResumeRestore{Outcome: tc.outcome}), create.Token, e.storeID)
			final, still := rstColumns(t, e, r.ID)
			if still != present || !reflect.DeepEqual(final, written) {
				t.Errorf("row after resume (present %v) =\n  %+v\nwant as the write left it (present %v)\n  %+v", still, final, present, written)
			}
			if tc.wantState != nil && final.State != tc.wantState {
				t.Errorf("state = %v; want %v", final.State, tc.wantState)
			}
			if tc.wantState == store.StatePending && (final.ParentID != other || final.LaunchStartedAt == nil) {
				t.Errorf("parent_id %v, launch_started_at %v; want %s and the launch start", final.ParentID, final.LaunchStartedAt, other)
			}
			rstAssertRestoredTrail(t, r.ID, false, "ErrTmuxSessionCreate", nil)
		})
	}
}

// TestResumeRestoreSkippedOnMoveStoreError: a failing move is ErrInternal with
// no create call, no trail line and the row unchanged.
func TestResumeRestoreSkippedOnMoveStoreError(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateMissing)
	e.store.failMove(nil)

	_, err := e.resume(r.ID)
	name, desc := errnames.Classify(err)
	if name != "ErrInternal" {
		t.Fatalf("resume err = %v (%s); want ErrInternal", err, name)
	}
	apitest.AssertDescription(t, desc, apitest.DescResumeMoveStoreError(), r.Identity.Token, e.storeID)
	if calls := e.rec.SocketCalls(); len(calls) != 0 {
		t.Errorf("socket calls = %+v; want none", calls)
	}
	rstAssertRow(t, e, r.ID, r.Before)
	if lines := rstTrail(t, r.ID); len(lines) != 0 {
		t.Errorf("trail = %v; want no ad.resume.* line", lines)
	}
}

// TestResumeRestoreStoreErrorLeavesPending: a failing restore leaves the row
// pending, logs one WARN, records restore_error and still returns the launch error.
func TestResumeRestoreStoreErrorLeavesPending(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateEnded)
	e.store.failRestore(nil)
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallCreate)
	start := e.clock.Now().UnixMilli()

	_, err := e.resume(r.ID)
	assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
	if name, _ := errnames.Classify(err); name != "ErrTmuxSessionCreate" {
		t.Errorf("classified as %s; want ErrTmuxSessionCreate (not ErrInternal)", name)
	}
	create := rstOneCreate(t, e)
	apitest.AssertDescription(t, err.Error(), apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{}).
		AfterResumeRestore(apitest.ResumeRestore{Outcome: apitest.RestoreStoreError}), create.Token, e.storeID)
	rstAssertRow(t, e, r.ID, rstMoved(r, start, create.Token, e.socket))
	lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "WARN") || !strings.Contains(lines[0], r.ID) ||
		strings.Contains(lines[0], create.Token) {
		t.Errorf("client log lines = %q; want one WARN line naming %s and no token", lines, r.ID)
	}
	rstAssertRestoredTrail(t, r.ID, false, "ErrTmuxSessionCreate", errInjectedStore.Error())
}

// rstResumeAsync runs e.resume in its own goroutine, so a hook may end it with
// runtime.Goexit; returned is false when it was ended that way.
func rstResumeAsync(e *resumeEnv, id string) (err error, returned bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = e.resume(id)
		returned = true
	}()
	<-done
	return err, returned
}

// TestResumeRestoreSkippedOnUnresponsiveCreate: a timed-out or unparseable
// create, or a resume stopped before its create, leaves the row pending with
// its launch start and new token, and attempts no restore.
func TestResumeRestoreSkippedOnUnresponsiveCreate(t *testing.T) {
	cases := []struct {
		name         string
		script       *tmuxfix.Script // nil: resume stops after its move, before the create
		unrecognised bool
	}{
		{"create times out", &tmuxfix.Script{Failure: tmux.FailTimeout}, false},
		{"non-zero exit, unparseable reply", &tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, HadStdout: true}, true},
		{"resume stops before its create", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			if tc.script != nil {
				e.rec.Script(tmuxfix.AnySocket, *tc.script, tmux.CallCreate)
			} else {
				e.store.afterMove(func() { runtime.Goexit() })
			}
			start := e.clock.Now()

			err, returned := rstResumeAsync(e, r.ID)
			token, _ := e.columns(t, r.ID).LaunchToken.(string)
			if tc.script == nil {
				if returned || len(e.rec.SocketCalls()) != 0 {
					t.Fatalf("resume returned %v (err %v) with calls %+v; want it stopped before any tmux call", returned, err, e.rec.SocketCalls())
				}
				if !spawnTokenRE.MatchString(token) || token == r.Identity.Token {
					t.Errorf("launch_token = %q; want a new 16-hex token", token)
				}
			} else {
				assertLaunchSentinel(t, err, tmux.ErrTmuxUnresponsive)
				token = rstOneCreate(t, e).Token
				apitest.AssertDescription(t, err.Error(), apitest.DescLaunchTimeout(apitest.LaunchTimeout{
					InstanceID: r.ID, Timeout: boundC, Unrecognised: tc.unrecognised}), token, e.storeID)
				if got := e.clock.Now().Sub(start); got != boundC {
					t.Errorf("virtual time charged = %v; want the default create timeout %v", got, boundC)
				}
			}
			rstAssertRow(t, e, r.ID, rstMoved(r, start.UnixMilli(), token, e.socket))
			if lines := rstTrailOf(t, r.ID, "ad.resume.restored"); len(lines) != 0 {
				t.Errorf("ad.resume.restored = %v; want none (no restore attempted)", lines)
			}
			if e.logs.Len() != 0 {
				t.Errorf("client log = %q; want nothing", e.logs.String())
			}
		})
	}
}

// TestResumeRestoreAfterUnlabelledSession: a failed chained label is relabelled
// by id with this store's id; a failed relabel kills by id, then restores.
func TestResumeRestoreAfterUnlabelledSession(t *testing.T) {
	create, label, kill := tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession
	cases := []struct {
		name      string
		labelFail bool
		killFail  bool
		wantCalls []tmux.Call
	}{
		{"relabel by id succeeds", false, false, []tmux.Call{create, label}},
		{"relabel fails, kill by id succeeds", true, false, []tmux.Call{create, label, kill}},
		{"relabel fails, kill by id fails", true, true, []tmux.Call{create, label, kill}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, create)
			if tc.labelFail {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, label)
			}
			if tc.killFail {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, kill)
			}
			var created tmuxfix.SeedSession
			e.rec.AfterCall(create, func(tmuxfix.SocketCall, error) { created = e.rec.Sessions(e.socket)[0] })
			start := e.clock.Now().UnixMilli()

			_, err := e.resume(r.ID)
			if got := callKinds(e.rec); !reflect.DeepEqual(got, tc.wantCalls) {
				t.Fatalf("tmux calls = %v; want %v", got, tc.wantCalls)
			}
			calls := e.rec.SocketCalls()
			c, l, tok := calls[0], calls[1], calls[0].Token
			if l.Socket != e.socket || l.Target != created.ID || l.PaneID != created.Panes[0].ID ||
				l.Token != tok || l.InstanceID != r.ID || l.StoreID != e.storeID {
				t.Errorf("label by id = %+v; want session %s pane %s on %s with token %s, id %s, store %s",
					l, created.ID, created.Panes[0].ID, e.socket, tok, r.ID, e.storeID)
			}
			remaining := e.rec.Sessions(e.socket)

			if !tc.labelFail {
				if err != nil {
					t.Fatalf("resume: %v", err)
				}
				if len(remaining) != 1 || remaining[0].Label != tmuxfix.Valid(tok, r.ID, e.storeID) || remaining[0].Panes[0].AdPane != tok {
					t.Errorf("sessions = %+v; want %s labelled ad1 %s <$N> %s <store id> with pane label", remaining, created.ID, tok, r.ID)
				}
				cols := e.columns(t, r.ID)
				if cols.State != store.StatePending || cols.LaunchStartedAt != start || cols.LaunchToken != tok ||
					cols.PaneID != created.Panes[0].ID || cols.RowVersion != r.Before.RowVersion.(int64)+2 {
					t.Errorf("row {state %v, launch start %v, token %v, pane %v, row_version %v}; want pending, %d, %s, %s, move + identity write",
						cols.State, cols.LaunchStartedAt, cols.LaunchToken, cols.PaneID, cols.RowVersion, start, tok, created.Panes[0].ID)
				}
				if lines := rstTrailOf(t, r.ID, "ad.resume.restored"); len(lines) != 0 {
					t.Errorf("ad.resume.restored = %v; want none", lines)
				}
				return
			}
			assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
			if calls[2].Target != created.ID {
				t.Errorf("kill targets %q; want %q", calls[2].Target, created.ID)
			}
			if gone := len(remaining) == 0; gone == tc.killFail {
				t.Errorf("sessions after resume = %+v; want the unlabelled session present only when its kill failed", remaining)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: c.Target, SessionID: created.ID, Ended: !tc.killFail,
				Restore: apitest.ResumeRestore{Outcome: apitest.RestoreApplied, PriorState: store.StateEnded},
			}), tok, e.storeID, tmuxfix.LabelValue(tok, created.ID, r.ID, e.storeID))
			rstAssertRow(t, e, r.ID, rstRestored(r, nil))
			rstAssertRestoredTrail(t, r.ID, true, "ErrTmuxSessionCreate", nil)
		})
	}
}

// TestResumeRestoreClientTrailOrder: Client.Resume with the default logger
// records ad.resume.moved_to_pending, then ad.resume.restored (SR-14, SR-20.6).
func TestResumeRestoreClientTrailOrder(t *testing.T) {
	e := newResumeEnv(t)
	r := e.seedResumable(t, store.StateMissing)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxClient: e.rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnavailable}, tmux.CallCreate)

	_, err = c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})
	assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)
	lines := rstTrail(t, r.ID)
	var events []any
	for _, l := range lines {
		events = append(events, l["event"])
	}
	if want := []any{"ad.resume.moved_to_pending", "ad.resume.restored"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("trail events = %v; want %v", events, want)
	}
	if m := lines[0]; m["prior_state"] != store.StateMissing || m["claude_session_id"] != r.SessionID || m["source"] != "ad_resume" {
		t.Errorf("ad.resume.moved_to_pending = %v; want prior_state missing, claude_session_id %s, source ad_resume", m, r.SessionID)
	}
	rstAssertRestoredTrail(t, r.ID, true, "ErrTmuxNotAvailable", nil)
	rstAssertRow(t, e, r.ID, rstRestored(r, nil))
}
