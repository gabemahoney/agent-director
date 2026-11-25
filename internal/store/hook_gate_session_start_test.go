package store_test

// Store-level proof of the gate on SessionStart (SR-22.9, SR-5.3, SR-5.9), of
// every hook write racing a new launch, and that writefailfix's launch identity
// trigger never fires on a hook write. Fixtures are in hook_gate_test.go.

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// hgExamine reads id's snapshot as the hook handler does before SessionStart;
// no row gives the zero snapshot.
func hgExamine(t *testing.T, f *v5Store, id string) store.RowSnapshot {
	t.Helper()
	sp, err := f.s.GetSpawn(id)
	if errors.Is(err, store.ErrSpawnNotFound) {
		return store.RowSnapshot{}
	}
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	return sp.Snapshot
}

// hgSessionStartAt is a SessionStart from p reporting sessionID and the hook
// transcript (present), written against examined.
func hgSessionStartAt(f *v5Store, id string, examined store.RowSnapshot, p hgParent, sessionID string) (store.HookApplied, bool, error) {
	gate := hgGate("SessionStart", p, sessionID)
	gate.SessionStart, gate.Examined = true, examined
	return f.s.RecordSessionStartIdentity(id, gate, hgHookPath, true)
}

// hgSessionStart is hgSessionStartAt against the row as it is now.
func hgSessionStart(t *testing.T, f *v5Store, id string, p hgParent, sessionID string) (store.HookApplied, bool, error) {
	t.Helper()
	return hgSessionStartAt(f, id, hgExamine(t, f, id), p, sessionID)
}

// hgHistory reads id's session history across every life.
func hgHistory(t *testing.T, f *v5Store, id string) []apitest.HistoryEntry {
	t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(f.path, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	return h
}

// TestHookGateSessionStartOneWrite pins SR-22.9's applied SessionStart: one
// statement (row_version + 1) that reports non-active/new-session rows waiting,
// records session, path and parent identity (including a NULL pane start),
// and clears launch_started_at, ended_at and liveness.
func TestHookGateSessionStartOneWrite(t *testing.T) {
	cases := []hgRow{
		{state: store.StatePending},
		{state: store.StatePending, noPaneStart: true},
		{state: store.StateWaiting, session: "sess-old"},
		{state: store.StateMissing},
		{state: store.StateEnded, session: "sess-old", opts: []apitest.SpawnOption{apitest.WithEndedAt("2026-09-29 01:02:03")}},
	}
	f := newV5Store(t)
	for _, r := range cases {
		name := r.state
		if r.noPaneStart {
			name += "/pane start NULL"
		}
		t.Run(name, func(t *testing.T) {
			id := hgSeed(t, f, r)
			p := hgAgentParent(t, f, id)
			before, mark := hgColumns(t, f, id), store.TrailMark(t)
			got, changed, err := hgSessionStart(t, f, id, p, "sess-new")
			if err != nil || changed || !got.Applied {
				t.Fatalf("SessionStart = %+v, changed %v, %v; want applied", got, changed, err)
			}
			after := hgColumns(t, f, id)
			gotCols := map[string]any{
				"state": after.State, "row_version": after.RowVersion, "session_id": after.SessionID,
				"jsonl_path": after.JSONLPath, "pid": after.PID, "proc_starttime": after.ProcStarttime,
				"pane_starttime": after.PaneStarttime, "launch_started_at": after.LaunchStartedAt,
				"ended_at": after.EndedAt, "liveness_unverified_since": after.LivenessUnverifiedSince,
				"liveness_note": after.LivenessNote,
			}
			wantCols := map[string]any{
				"state": store.StateWaiting, "row_version": before.RowVersion.(int64) + 1, "session_id": "sess-new",
				"jsonl_path": hgHookPath, "pid": int64(p.pid), "proc_starttime": p.start,
				"pane_starttime": apitest.LinuxProcStarttime, "launch_started_at": nil,
				"ended_at": nil, "liveness_unverified_since": nil, "liveness_note": nil,
			}
			if !reflect.DeepEqual(gotCols, wantCols) {
				t.Errorf("SessionStart columns:\n got  %v\n want %v", gotCols, wantCols)
			}
			lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
			if len(lines) != 1 || lines[0]["prior_state"] != r.state || lines[0]["new_state"] != store.StateWaiting {
				t.Errorf("transition lines = %v; want one %s -> waiting", lines, r.state)
			}
		})
	}
}

// b.hib: the store preserves active state and idle evidence for both refresh
// routes, including compact across a session-id change.
func TestHookGateSessionStartKeepsActiveRecords(t *testing.T) {
	for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
		for _, state := range []string{store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
			for _, compact := range []bool{false, true} {
				name, priorSession := "same-session", hgHookSession
				if compact {
					name, priorSession = "compact", "sess-before-compact"
				}
				t.Run(agent+"/"+state+"/"+name, func(t *testing.T) {
					f := newV5Store(t)
					id := hgSeed(t, f, hgRow{state: state, session: priorSession, noPaneStart: true,
						opts: []apitest.SpawnOption{apitest.WithAgent(agent), apitest.WithPermissionEpoch(7),
							apitest.WithDisplayObservation(storedIdleScreen), apitest.WithLaunchStartedAt(123),
							apitest.WithIdleSince(rvIdleSince)}})
					// A distinct base and clock catch a false settle stamp even if
					// the implementation restores the raw state after writing waiting.
					setSettle(t, f, id, store.StateWaiting, 456)
					before := rvExamine(t, f, id)
					p := hgAgentParent(t, f, id)
					gate := hgGate("SessionStart", p, hgHookSession)
					gate.SessionStart, gate.Examined, gate.SessionStartSoftRefresh = true, before.Snapshot, compact
					got, changed, err := f.s.RecordSessionStartIdentity(id, gate, hgHookPath, true)
					if err != nil || changed || !got.Applied {
						t.Fatalf("SessionStart = %+v, changed %v, %v; want applied", got, changed, err)
					}
					after := rvExamine(t, f, id)
					settled, stamp := settleOf(t, f, id)
					if after.State != state || settled != store.StateWaiting || stamp != 456 {
						t.Errorf("raw/settled/stamp = %s/%s/%d; want %s/waiting/456", after.State, settled, stamp, state)
					}
					if after.PermissionEpoch != before.PermissionEpoch || after.StateEvidence != before.StateEvidence ||
						after.SettledStateEvidence != before.SettledStateEvidence || after.DisplayObservation != before.DisplayObservation ||
						after.ObservationVersion != before.ObservationVersion {
						t.Errorf("SessionStart changed active records:\n before %+v\n after %+v", before, after)
					}
					if after.IdleSince != rvIdleSince {
						t.Errorf("idle_since = %q; want kept %q", after.IdleSince, rvIdleSince)
					}
					columns := hgColumns(t, f, id)
					if columns.RowVersion != before.RowVersion+1 || columns.PID != int64(p.pid) || columns.ProcStarttime != p.start ||
						columns.PaneStarttime != p.start || columns.JSONLPath != hgHookPath || columns.LaunchStartedAt != nil ||
						columns.LivenessNote != nil || columns.LivenessUnverifiedSince != nil {
						t.Errorf("preserved state did not record SessionStart identity/clear launch markers: %+v", columns)
					}
				})
			}
		}
	}
}

// b.hib: refreshing a held, idle Claude row must retain the observation that
// makes permission recovery choose waiting once an outside answer releases it.
func TestHookGateSessionStartKeepsIdlePermissionRecovery(t *testing.T) {
	for _, compact := range []bool{false, true} {
		name := "same-session"
		if compact {
			name = "compact"
		}
		t.Run(name, func(t *testing.T) {
			f := newV5Store(t)
			id := hgSeed(t, f, hgRow{state: store.StateCheckPermission, session: hgHookSession,
				opts: []apitest.SpawnOption{apitest.WithRelayMode("on")}})
			request, err := apitest.SeedPermissionRequest(f.path, id, "Bash")
			if err != nil {
				t.Fatal(err)
			}
			p := hgAgentParent(t, f, id)
			_, applied, err := f.s.ApplyHookWaitingIfWorking(id, hgGate("Notification", p, hgHookSession), "Notification", "", false)
			if err != nil || !applied.Applied {
				t.Fatalf("idle Notification = %+v, %v; want applied", applied, err)
			}
			idle := rvExamine(t, f, id)
			if idle.State != store.StateCheckPermission || idle.IdleSince == "" {
				t.Fatalf("held idle setup = state %s, idle %q; want check_permission, set", idle.State, idle.IdleSince)
			}
			gate := hgGate("SessionStart", p, hgHookSession)
			gate.SessionStart, gate.Examined, gate.SessionStartSoftRefresh = true, idle.Snapshot, compact
			got, changed, err := f.s.RecordSessionStartIdentity(id, gate, hgHookPath, true)
			if err != nil || changed || !got.Applied {
				t.Fatalf("SessionStart = %+v, changed %v, %v; want applied", got, changed, err)
			}
			refreshed := rvExamine(t, f, id)
			if refreshed.State != store.StateCheckPermission || refreshed.IdleSince != idle.IdleSince {
				t.Errorf("refreshed state/idle = %s/%q; want check_permission/%q", refreshed.State, refreshed.IdleSince, idle.IdleSince)
			}
			if ok, err := f.s.RecordPaneOutside(id, request.RequestToken, "allow", store.DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("outside answer = %v, %v; want recorded", ok, err)
			}
			row := rvExamine(t, f, id)
			requests, err := f.s.PermissionRequestsForSpawn(id)
			if err != nil || len(requests) != 1 || requests[0].AwaitsAnswer() || row.State != store.StateCheckPermission {
				t.Fatalf("outside answer did not release hold for repair: state %s, requests %+v, %v", row.State, requests, err)
			}
			state, result, err := f.s.RepairCheckPermissionSample(row, requests)
			after := rvExamine(t, f, id)
			if err != nil || result != store.CondApplied || state != store.StateWaiting || after.State != store.StateWaiting || after.IdleSince != idle.IdleSince {
				t.Errorf("repair = %s/%v/%v, raw %s, idle %q; want applied waiting with idle %q", state, result, err, after.State, after.IdleSince, idle.IdleSince)
			}
		})
	}
}

// b.hib leaves Cursor's SessionStart idle boundary unchanged.
func TestHookGateSessionStartCursorBoundary(t *testing.T) {
	e, row, gate := rotationStore(t)
	if row.State != store.StateWorking || row.SessionID != gate.SessionID {
		t.Fatalf("Cursor setup state/session = %s/%s; want working/%s", row.State, row.SessionID, gate.SessionID)
	}
	gate.SessionStart, gate.Examined = true, row.Snapshot
	got, changed, err := e.f.s.RecordSessionStartIdentity(row.InstanceID, gate, hgHookPath, true)
	if err != nil || changed || !got.Applied {
		t.Fatalf("SessionStart = %+v, changed %v, %v; want applied", got, changed, err)
	}
	if after := hgColumns(t, e.f, row.InstanceID); after.State != store.StateWaiting {
		t.Errorf("Cursor same-session startup state = %v; want waiting", after.State)
	}
}

// TestHookGateSessionStartNotApplied pins SR-22.9: a SessionStart from any
// parent but the row's pane process writes nothing, archives nothing and emits
// nothing, and reports the gate's reason (not a changed snapshot).
func TestHookGateSessionStartNotApplied(t *testing.T) {
	f := newV5Store(t)
	for _, g := range hgParents[1:] {
		t.Run(g.name, func(t *testing.T) {
			id := hgSeed(t, f, hgRow{state: store.StateWaiting, session: "sess-old", noPane: g.noPane,
				opts: []apitest.SpawnOption{apitest.WithJsonlPath("/tmp/hg/old.jsonl")}})
			before, history, mark := hgColumns(t, f, id), hgHistory(t, f, id), store.TrailMark(t)
			got, changed, err := hgSessionStart(t, f, id, g.parent(hgAgentParent(t, f, id)), "sess-new")
			if err != nil || changed {
				t.Fatalf("SessionStart: changed %v, %v; want false, nil", changed, err)
			}
			hgAssertIgnored(t, f, id, mark, got, store.HookApplied{Reason: g.reason}, before)
			if h := hgHistory(t, f, id); !reflect.DeepEqual(h, history) {
				t.Errorf("history changed: %+v -> %+v", history, h)
			}
			if n := len(store.TrailEventsSince(t, mark, "ad.session.archived", id)); n != 0 {
				t.Errorf("%d ad.session.archived lines; want none", n)
			}
		})
	}
}

// TestHookGateSessionStartStaleSnapshot pins SR-5.3: the agent's SessionStart
// against a snapshot the row no longer holds writes nothing and archives
// nothing; the store reports the changed snapshot (no reason) so the caller
// may re-read.
func TestHookGateSessionStartStaleSnapshot(t *testing.T) {
	f := newV5Store(t)
	id := hgSeed(t, f, hgRow{state: store.StateWorking, session: "sess-old"})
	p := hgAgentParent(t, f, id)
	examined := hgExamine(t, f, id)
	if got, err := f.s.ApplyHookTransition(id, hgGate("Notification", p, ""), "", true, "Notification", "", false); err != nil || !got.Applied {
		t.Fatalf("soft refresh between the read and the write = %+v, %v; want applied", got, err)
	}
	before, history, mark := hgColumns(t, f, id), hgHistory(t, f, id), store.TrailMark(t)
	got, changed, err := hgSessionStartAt(f, id, examined, p, "sess-new")
	if err != nil || !changed {
		t.Fatalf("stale SessionStart: changed %v, %v; want true, nil", changed, err)
	}
	hgAssertIgnored(t, f, id, mark, got, store.HookApplied{}, before)
	if h := hgHistory(t, f, id); !reflect.DeepEqual(h, history) {
		t.Errorf("history changed: %+v -> %+v", history, h)
	}
}

// TestHookGateSessionStartRotation pins SR-5.9 inside the gated write: a new
// session id archives the outgoing pair in the life the write read, moving an
// entry of another life; the same id archives nothing and keeps working
// (b.hib). With writefailfix's ReuseArchive the archive fails open and the
// identity still commits.
func TestHookGateSessionStartRotation(t *testing.T) {
	const (
		oldPath = "/tmp/hg/old.jsonl"
		life    = int64(3)
	)
	archived := []apitest.HistoryEntry{{SessionID: "sess-old", JSONLPath: sql.NullString{String: oldPath, Valid: true}, LifeNumber: life}}
	cases := []struct {
		name         string
		hookSession  string
		priorEntry   bool // an entry for sess-old in life 1
		failArchive  bool
		wantHistory  []apitest.HistoryEntry
		wantArchived int
		wantFailed   int
	}{
		{"new session archives the pair", "sess-new", false, false, archived, 1, 0},
		{"entry of another life moves", "sess-new", true, false, archived, 1, 0},
		{"same session archives nothing", "sess-old", false, false, nil, 0, 0},
		{"archive fails open", "sess-new", false, true, nil, 0, 1},
	}
	f := newV5Store(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := []apitest.SpawnOption{apitest.WithJsonlPath(oldPath), apitest.WithLifeNumber(life), apitest.WithIdleSince(rvIdleSince)}
			if c.priorEntry {
				opts = append(opts, apitest.WithSessionHistory(apitest.SessionHistorySeed{
					SessionID: "sess-old", JSONLPath: "/tmp/hg/life1.jsonl", Life: 1}))
			}
			id := hgSeed(t, f, hgRow{state: store.StateWorking, session: "sess-old", opts: opts})
			if c.failArchive {
				storefix.InjectWriteFailure(t, f.path, storefix.WriteFailReuseArchive, id)
			}
			before, mark := hgColumns(t, f, id), store.TrailMark(t)
			got, changed, err := hgSessionStart(t, f, id, hgAgentParent(t, f, id), c.hookSession)
			if err != nil || changed || !got.Applied {
				t.Fatalf("SessionStart = %+v, changed %v, %v; want applied", got, changed, err)
			}
			after := hgColumns(t, f, id)
			wantState := store.StateWaiting
			if c.hookSession == "sess-old" {
				wantState = store.StateWorking
			}
			var wantIdle any
			if wantState == store.StateWorking {
				wantIdle = rvIdleSince
			}
			if after.IdleSince != wantIdle {
				t.Errorf("idle_since = %v; want %v (state %s)", after.IdleSince, wantIdle, wantState)
			}
			if after.State != wantState || after.SessionID != c.hookSession ||
				after.RowVersion != before.RowVersion.(int64)+1 || after.PID != int64(apitest.TestPanePID) {
				t.Errorf("state, session, version, pid = %v, %v, %v, %v; want %s, %s, %v+1, %d",
					after.State, after.SessionID, after.RowVersion, after.PID, wantState, c.hookSession, before.RowVersion, apitest.TestPanePID)
			}
			var h []apitest.HistoryEntry
			for _, e := range hgHistory(t, f, id) {
				e.RecordedAt = ""
				h = append(h, e)
			}
			if !reflect.DeepEqual(h, c.wantHistory) {
				t.Errorf("history = %+v; want %+v", h, c.wantHistory)
			}
			if n := len(store.TrailEventsSince(t, mark, "ad.session.archived", id)); n != c.wantArchived {
				t.Errorf("%d ad.session.archived lines; want %d", n, c.wantArchived)
			}
			if n := len(store.TrailEventsSince(t, mark, "ad.session.archive_failed", id)); n != c.wantFailed {
				t.Errorf("%d ad.session.archive_failed lines; want %d", n, c.wantFailed)
			}
		})
	}
}

// TestHookGateRaceWithNewLaunch pins SR-22.9's statement-level gate: a hook
// whose parent was the row's pane process when it read the row, written after
// resume's move to pending (and after the new launch's identity write), does
// not apply and leaves the new launch untouched; a row deleted in between
// gives today's silent no-op.
func TestHookGateRaceWithNewLaunch(t *testing.T) {
	newPane := store.LaunchIdentity{PaneID: "%9", PanePID: 444, PaneStarttime: apitest.DarwinProcStarttime}
	races := []struct {
		name string
		race func(t *testing.T, f *v5Store, id string, examined store.RowSnapshot)
		want store.HookApplied
	}{
		{"move to pending", hgMove, store.HookApplied{Reason: store.HookReasonNoPaneRecorded}},
		{"identity write", func(t *testing.T, f *v5Store, id string, examined store.RowSnapshot) {
			hgMove(t, f, id, examined)
			if res, err := f.s.RecordLaunchIdentity(id, examined.RowVersion+1, moveToken, newPane); err != nil || res != store.CondApplied {
				t.Fatalf("RecordLaunchIdentity = %v, %v; want CondApplied", res, err)
			}
		}, store.HookApplied{Reason: store.HookReasonPIDMismatch}},
		{"row deleted", func(t *testing.T, f *v5Store, id string, _ store.RowSnapshot) {
			if err := storefix.DeleteWithExit(t, f.s, id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}, store.HookApplied{}},
	}
	f := newV5Store(t)
	for _, r := range races {
		for _, write := range []string{"SessionEnd", "SessionStart"} {
			t.Run(r.name+"/"+write, func(t *testing.T) {
				id := hgSeed(t, f, hgRow{state: store.StateEnded, session: "sess-old"})
				p, examined := hgAgentParent(t, f, id), hgExamine(t, f, id) // the hook's reads
				r.race(t, f, id, examined)
				var before apitest.SpawnColumns
				if r.want != (store.HookApplied{}) {
					before = hgColumns(t, f, id)
				}
				mark := store.TrailMark(t)
				var got store.HookApplied
				var changed bool
				var err error
				if write == "SessionStart" {
					got, changed, err = hgSessionStartAt(f, id, examined, p, "sess-new")
				} else {
					got, err = f.s.ApplyHookTransition(id, hgGate(write, p, ""), store.StateEnded, false, write, "", false)
				}
				if err != nil || changed {
					t.Fatalf("%s after the race: changed %v, %v; want false, nil", write, changed, err)
				}
				if r.want == (store.HookApplied{}) {
					if got != r.want {
						t.Errorf("result = %+v; want %+v", got, r.want)
					}
					return
				}
				hgAssertIgnored(t, f, id, mark, got, r.want, before)
			})
		}
	}
}

// hgMove is resume's move of id's finished row to pending on examined.
func hgMove(t *testing.T, f *v5Store, id string, examined store.RowSnapshot) {
	t.Helper()
	if res, _, err := f.s.MoveToPendingWithExit(id, examined, moveLaunchMillis, moveToken, moveSocket, "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, f.s, id)); err != nil || res != store.CondApplied {
		t.Fatalf("MoveToPending = %v, %v; want CondApplied", res, err)
	}
}

// TestHookGateLaunchIdentityTriggerSkipsHookWrites pins writefailfix's
// LaunchIdentityWrite scope: no hook write fires it, not a soft refresh, a
// same-state transition or a same-state SessionStart, whether or not the write
// records pane_starttime (SR-22.9).
func TestHookGateLaunchIdentityTriggerSkipsHookWrites(t *testing.T) {
	writes := []struct {
		name  string
		write func(f *v5Store, id string, p hgParent) (store.HookApplied, error)
	}{
		{"soft refresh", func(f *v5Store, id string, p hgParent) (store.HookApplied, error) {
			return f.s.ApplyHookTransition(id, hgGate("Notification", p, ""), "", true, "Notification", "", false)
		}},
		{"same-state transition", func(f *v5Store, id string, p hgParent) (store.HookApplied, error) {
			return f.s.ApplyHookTransition(id, hgGate("Stop", p, ""), store.StateWaiting, false, "Stop", "", false)
		}},
		{"same-state SessionStart", func(f *v5Store, id string, p hgParent) (store.HookApplied, error) {
			sp, err := f.s.GetSpawn(id)
			if err != nil {
				return store.HookApplied{}, err
			}
			got, _, err := hgSessionStartAt(f, id, sp.Snapshot, p, "sess-a")
			return got, err
		}},
	}
	f := newV5Store(t)
	for _, w := range writes {
		for _, nullStart := range []bool{true, false} {
			name := w.name + "/pane start recorded"
			if nullStart {
				name = w.name + "/pane start NULL"
			}
			t.Run(name, func(t *testing.T) {
				id := hgSeed(t, f, hgRow{state: store.StateWaiting, session: "sess-a", noPaneStart: nullStart})
				storefix.InjectWriteFailure(t, f.path, storefix.WriteFailLaunchIdentity, id)
				got, err := w.write(f, id, hgAgentParent(t, f, id))
				if err != nil || !got.Applied {
					t.Fatalf("%s with the launch identity failure installed = %+v, %v; want applied", w.name, got, err)
				}
				if c := hgColumns(t, f, id); c.PaneStarttime != apitest.LinuxProcStarttime {
					t.Errorf("pane_starttime = %v; want %q", c.PaneStarttime, apitest.LinuxProcStarttime)
				}
			})
		}
	}
}
