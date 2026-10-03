package api_test

// spawn_held_row_test.go covers the new row around plain spawn's held-name
// path (SR-9.4, SR-5.8, SR-20.6; AC-SPN-07, AC-SPN-09): an end write that a
// competing write or a delete-and-reinsert keeps from applying, a store
// error that leaves the row pending with one WARN line, an ended row that
// the leftover's hooks and find-missing leave as it is, what the id answers
// afterwards, the retry guidance of an ErrTmuxUnresponsive or a vanished
// holder's ErrTmuxSessionCreate when the end write did not apply or failed,
// and no end write on a path without
// "duplicate session". It
// uses the held-name fixture of spawn_held_test.go.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// heldRowName is the requested name every test here finds held.
const heldRowName = "held-row"

// heldPaneText is the capture text of the leftover's pane.
const heldPaneText = "leftover pane text"

// heldLeftoverPane is the pane id of the leftover's one pane.
const heldLeftoverPane = "%4"

// readRow reads every column of id's row, failing the test on a read error.
func (e heldEnv) readRow(t *testing.T, id string) apitest.SpawnColumns {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return cols
}

// assertRowIs checks id's row reads exactly as want.
func (e heldEnv) assertRowIs(t *testing.T, id, what string, want apitest.SpawnColumns) {
	t.Helper()
	if got := e.readRow(t, id); !reflect.DeepEqual(got, want) {
		t.Errorf("row %s:\ngot  %+v\nwant %+v", what, got, want)
	}
}

// assertHeldDesc checks err's description is DescHeldNoValidID for a
// no-label holder $4 of heldRowName with row result row.
func (e heldEnv) assertHeldDesc(t *testing.T, err error, id string, row apitest.HeldRow, forbid ...string) {
	t.Helper()
	assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	if err == nil {
		return
	}
	_, desc := errnames.Classify(err)
	p := apitest.HeldName{Name: heldRowName, SessionID: "$4", Row: row}
	apitest.AssertDescription(t, desc, apitest.DescHeldNoValidID(p), append(e.forbid(id, nil), forbid...)...)
}

// TestSpawnHeldEndNotApplied: a write that changes the new row between the
// insert and the end write keeps the end write from applying; the row reads
// as that write left it and the error says "left as it is".
func TestSpawnHeldEndNotApplied(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, e heldEnv, id string) // the competing write, as the create returns
		check func(t *testing.T, cols apitest.SpawnColumns, e heldEnv)
	}{
		{"another versioned write", func(t *testing.T, e heldEnv, id string) {
			apitest.SeedSessionID(t, e.dbPath, id, uuid.NewString())
		}, func(t *testing.T, cols apitest.SpawnColumns, _ heldEnv) {
			if cols.State != store.StatePending || cols.RowVersion != int64(1) || cols.ClaudeSessionID == nil {
				t.Errorf("after the competing write: state %v, row_version %v, session %v; want pending, 1, set",
					cols.State, cols.RowVersion, cols.ClaudeSessionID)
			}
		}},
		{"deleted and inserted again", func(t *testing.T, e heldEnv, id string) {
			if res, _ := e.c.Delete([]string{id}); res.Results[id] != "ok" {
				t.Fatalf("Delete(%s) = %v; want ok", id, res.Results)
			}
			launch := e.start.Add(-time.Hour).UnixMilli()
			if _, err := apitest.SeedSpawn(e.dbPath, id, store.StatePending, "", "", "", false,
				apitest.WithLaunchStartedAt(launch)); err != nil {
				t.Fatalf("SeedSpawn(%s): %v", id, err)
			}
		}, func(t *testing.T, cols apitest.SpawnColumns, e heldEnv) {
			if cols.State != store.StatePending || cols.RowVersion != int64(0) ||
				cols.LaunchStartedAt != e.start.Add(-time.Hour).UnixMilli() {
				t.Errorf("re-seeded row: state %v, row_version %v, launch_started_at %v; want pending, 0, an hour before the spawn",
					cols.State, cols.RowVersion, cols.LaunchStartedAt)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			e.rec.SeedSessions(e.socket, heldSession(heldRowName, "$4", tmux.Label{}, false))
			var written apitest.SpawnColumns
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				tc.write(t, e, id)
				written = e.readRow(t, id)
			})

			run := e.spawnHeld(t, id, heldRowName, nil)

			tc.check(t, written, e)
			e.assertRowIs(t, id, "after the spawn (the competing write stands)", written)
			if run.stateAtRelookup != store.StatePending {
				t.Errorf("row state at the re-lookup = %v; want pending (the end write wrote nothing)", run.stateAtRelookup)
			}
			token, _ := written.LaunchToken.(string)
			e.assertHeldDesc(t, run.err, id, apitest.HeldRowLeftAsIs, token)
			if e.logs.Len() != 0 {
				t.Errorf("client log = %q; want nothing", e.logs.String())
			}
		})
	}
}

// TestSpawnHeldEndStoreError: a failed end write leaves the row pending with
// its launch start; the error says so and the client log has one WARN line.
func TestSpawnHeldEndStoreError(t *testing.T) {
	e := newHeldEnv(t)
	id := heldID()
	holder := heldSession(heldRowName, "$4", tmux.Label{}, false)
	e.rec.SeedSessions(e.socket, holder)
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
	var insertedAt time.Time
	run := e.spawnHeld(t, id, heldRowName, func() { insertedAt = e.clock.Now() })

	cols := e.readRow(t, id)
	token, _ := cols.LaunchToken.(string)
	if cols.State != store.StatePending || cols.RowVersion != int64(0) || cols.EndedAt != nil ||
		cols.LaunchStartedAt != insertedAt.UnixMilli() || token == "" {
		t.Errorf("row: state %v, row_version %v, ended_at %v, launch_started_at %v, token %v; want pending, 0, NULL, %d, the insert's",
			cols.State, cols.RowVersion, cols.EndedAt, cols.LaunchStartedAt, cols.LaunchToken, insertedAt.UnixMilli())
	}
	e.assertHeldDesc(t, run.err, id, apitest.HeldRowStoreError, token)

	lines := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("client log lines = %q; want exactly one", lines)
	}
	warn := apitest.DescCase{Name: "held-name end-write WARN line", Require: []string{"WARN", id}}
	apitest.AssertDescription(t, lines[0], warn, append(e.forbid(id, []tmuxfix.SeedSession{holder}),
		token, heldRowName, holder.ID)...)
}

// TestSpawnHeldUnansweredRow: when the re-lookup cannot answer (a timeout, or
// more than one session matching the name) or the holder vanished, and the
// end write did not apply or failed, the error says the row's sentence and,
// in place of "retry later", not to retry until get shows the row ended or
// missing, then the opted-in retry once the name is free (SR-1.4; WD
// 2026-09-30d (a); b.1qq).
func TestSpawnHeldUnansweredRow(t *testing.T) {
	relookups := []struct {
		name    string
		holders []tmuxfix.SeedSession
		timeout bool // the re-lookup times out
		vanish  bool // the holder is gone by the re-lookup: ErrTmuxSessionCreate
		forbid  []string
		desc    func(p apitest.HeldName) apitest.DescCase
	}{
		{name: "re-lookup timeout", holders: []tmuxfix.SeedSession{heldSession(heldRowName, "$4", tmux.Label{}, false)},
			timeout: true, desc: func(p apitest.HeldName) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallLookup, boundQ).AfterHeldName(p)
			}},
		// Plain spawn names cannot hold $ or \, so two entries with one stored name stand in.
		{name: "ambiguous holder", holders: []tmuxfix.SeedSession{
			heldSession(heldRowName, "$4", tmux.Label{}, false), heldSession(heldRowName, "$5", tmux.Label{}, false)},
			forbid: []string{"$4", "$5"}, desc: apitest.DescHeldAmbiguous},
		{name: "holder vanished", holders: []tmuxfix.SeedSession{heldSession(heldRowName, "$4", tmux.Label{}, false)},
			vanish: true, desc: func(p apitest.HeldName) apitest.DescCase {
				return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: heldRowName, Duplicate: true}).AfterHeldName(p)
			}},
	}
	rows := []struct {
		name  string
		row   apitest.HeldRow
		warns int
	}{
		{"left as it is", apitest.HeldRowLeftAsIs, 0},
		{"store error", apitest.HeldRowStoreError, 1},
	}
	for _, rl := range relookups {
		for _, r := range rows {
			t.Run(rl.name+"/"+r.name, func(t *testing.T) {
				e := newHeldEnv(t)
				id := heldID()
				e.rec.SeedSessions(e.socket, rl.holders...)
				switch r.row {
				case apitest.HeldRowLeftAsIs:
					e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
						apitest.SeedSessionID(t, e.dbPath, id, uuid.NewString())
					})
				case apitest.HeldRowStoreError:
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
				}
				var onScan func()
				if rl.timeout {
					onScan = func() {
						e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
					}
				}
				var want error = api.ErrTmuxUnresponsive
				if rl.vanish {
					e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, "$4")
					want = api.ErrTmuxSessionCreate
				}

				run := e.spawnHeld(t, id, heldRowName, onScan)

				cols := e.readRow(t, id)
				if cols.State != store.StatePending || cols.EndedAt != nil {
					t.Errorf("row: state %v, ended_at %v; want pending, NULL (the end write wrote nothing)",
						cols.State, cols.EndedAt)
				}
				assertOneSentinel(t, run.err, want)
				if run.err != nil {
					_, desc := errnames.Classify(run.err)
					token, _ := cols.LaunchToken.(string)
					p := apitest.HeldName{Name: heldRowName, Row: r.row, InstanceID: id}
					forbid := append(e.forbid(id, rl.holders), append(rl.forbid, token)...)
					apitest.AssertDescription(t, desc, rl.desc(p), forbid...)
				}
				if n := strings.Count(e.logs.String(), "WARN"); n != r.warns {
					t.Errorf("client log WARN lines = %d (%q); want %d", n, e.logs.String(), r.warns)
				}
			})
		}
	}
}

// TestSpawnHeldEndedSticks: after the end write applies, the leftover's hooks
// (as itself or as the row's agent) and find-missing, inside and past the
// grace period, leave the ended row exactly as it is.
func TestSpawnHeldEndedSticks(t *testing.T) {
	e := newHeldEnv(t)
	id := heldID()
	run := e.spawnHeld(t, id, heldRowName, func() {
		e.rec.SeedSessions(e.socket, e.leftover(heldRowName, "$4", id, 0))
	})
	assertOneSentinel(t, run.err, api.ErrTmuxSessionConflict)
	e.assertEndedRow(t, id, run.createdAt)
	ended := e.readRow(t, id)
	leftoverSession := uuid.NewString()

	hooks := []struct {
		name  string
		apply func(t *testing.T, dbPath, id, event, sessionID string, opts ...apitest.HookOption) store.HookApplied
	}{
		{"from the leftover", apitest.ApplyForeignHook},
		{"as the row's agent", apitest.ApplyAgentHook},
	}
	for _, h := range hooks {
		for _, event := range []string{"SessionEnd", "Stop", "SessionStart"} {
			t.Run(h.name+"/"+event, func(t *testing.T) {
				got := h.apply(t, e.dbPath, id, event, leftoverSession)
				if want := (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}); got != want {
					t.Errorf("%s = %+v; want %+v", event, got, want)
				}
				e.assertRowIs(t, id, "after "+event, ended)
			})
		}
	}

	grace := time.Duration(config.DefaultPendingGraceSeconds) * time.Second
	for _, step := range []struct {
		name    string
		advance time.Duration
	}{{"find-missing inside the grace period", 0}, {"find-missing past the grace period", grace + time.Second}} {
		t.Run(step.name, func(t *testing.T) {
			e.clock.Advance(step.advance)
			res, err := e.c.FindMissing(context.Background())
			if err != nil || res.Count != 0 {
				t.Errorf("FindMissing = %+v, %v; want no row marked", res, err)
			}
			e.assertRowIs(t, id, "after "+step.name, ended)
		})
	}
}

// TestSpawnHeldIdAfterwards: once the end write applied, the id is a
// finished row: spawn collides, resume has no session id, and nothing
// touches the holder; for the leftover, read-pane makes one lookup (the
// lone leftover), one pane listing and a capture of the leftover's pane by
// its pane id, while send-keys and kill make no tmux call.
func TestSpawnHeldIdAfterwards(t *testing.T) {
	cases := []struct {
		name     string
		leftover bool // placed after the scan (SR-20.9); otherwise no label, seeded before
	}{
		{"leftover", true},
		{"no label", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			var onScan func()
			if tc.leftover {
				onScan = func() {
					lo := e.leftover(heldRowName, "$4", id, 0)
					lo.Panes = []tmuxfix.SeedPane{{ID: heldLeftoverPane, AdPane: tmuxfix.OtherToken}}
					e.rec.SeedSessions(e.socket, lo).SetCapture(e.socket, heldLeftoverPane, heldPaneText)
				}
			} else {
				e.rec.SeedSessions(e.socket, heldSession(heldRowName, "$4", tmux.Label{}, false))
			}
			run := e.spawnHeld(t, id, heldRowName, onScan)
			assertOneSentinel(t, run.err, api.ErrTmuxSessionConflict)
			e.assertEndedRow(t, id, run.createdAt)
			ended := e.readRow(t, id)
			sessions, socketCalls := e.rec.Sessions(e.socket), len(e.rec.SocketCalls())

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})
			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			_, err = e.c.Resume(api.ResumeParams{ClaudeInstanceID: id})
			assertOneSentinel(t, err, api.ErrNoSessionId)

			if tc.leftover {
				pane, err := e.c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: id})
				if err != nil || pane.Pane != heldPaneText {
					t.Errorf("ReadPane = %q, %v; want %q", pane.Pane, err, heldPaneText)
				}
				var got []tmuxfix.SocketCall
				for _, c := range e.rec.SocketCalls()[socketCalls:] {
					got = append(got, tmuxfix.SocketCall{Call: c.Call, Socket: c.Socket, Target: c.Target})
				}
				want := []tmuxfix.SocketCall{{Call: tmux.CallLookup, Socket: e.socket}, {Call: tmux.CallListPanes, Socket: e.socket},
					{Call: tmux.CallCapture, Socket: e.socket, Target: heldLeftoverPane}}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("read-pane socket calls = %+v; want %+v", got, want)
				}
				socketCalls = len(e.rec.SocketCalls())
				for _, allow := range []bool{false, true} {
					_, err := e.c.SendKeys(api.SendKeysParams{ClaudeInstanceID: id, Text: "hello", AllowPending: allow})
					if !errors.Is(err, api.ErrSpawnNotInteractive) {
						t.Errorf("SendKeys(allow_pending %t) err = %v; want ErrSpawnNotInteractive", allow, err)
					}
				}
				if res, err := e.c.Kill(api.KillParams{ClaudeInstanceID: id}); err != nil || res.KillSent {
					t.Errorf("Kill = %+v, %v; want kill_sent false, nil", res, err)
				}
			}

			e.assertRowIs(t, id, "after the later verbs", ended)
			if n := len(e.rec.SocketCalls()); n != socketCalls {
				t.Errorf("socket calls after the held spawn (and read-pane) = %+v; want none", e.rec.SocketCalls()[socketCalls:])
			}
			if after := e.rec.Sessions(e.socket); !reflect.DeepEqual(after, sessions) {
				t.Errorf("sessions changed:\nbefore %+v\nafter  %+v", sessions, after)
			}
		})
	}
}

// TestSpawnHeldNoEndWithoutDuplicate: a create that succeeds, times out or
// fails otherwise makes no end write and no re-lookup; the row stays pending.
func TestSpawnHeldNoEndWithoutDuplicate(t *testing.T) {
	cases := []struct {
		name string
		fail tmux.Failure // 0: the create succeeds
		want error        // nil: no error
	}{
		{"success", 0, nil},
		{"create timeout", tmux.FailTimeout, api.ErrTmuxUnresponsive},
		{"other create failure", tmux.FailNoServer, api.ErrTmuxSessionCreate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			if tc.fail != 0 {
				e.rec.Script(e.socket, tmuxfix.Script{Failure: tc.fail, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			}

			run := e.spawnHeld(t, id, heldRowName, nil)

			if tc.want == nil && run.err != nil {
				t.Errorf("Spawn err = %v; want nil", run.err)
			} else if tc.want != nil {
				assertOneSentinel(t, run.err, tc.want)
			}
			cols := e.readRow(t, id)
			if cols.State != store.StatePending || cols.EndedAt != nil || cols.LaunchStartedAt == nil {
				t.Errorf("row: state %v, ended_at %v, launch_started_at %v; want pending, NULL, the insert's",
					cols.State, cols.EndedAt, cols.LaunchStartedAt)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallLookup)); n != 1 {
				t.Errorf("lookups = %d; want only the scan's", n)
			}
			if strings.Contains(e.logs.String(), "WARN") {
				t.Errorf("client log = %q; want no WARN", e.logs.String())
			}
		})
	}
}
