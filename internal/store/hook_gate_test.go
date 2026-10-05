package store_test

// Store-level proof of the hook gate (SR-22.9, SR-5.2, Appendix F.4): a hook
// write applies only when the hook's parent process, with its start time, is
// the row's recorded pane process, and one not applied changes nothing. Rows
// are seeded through apitest (SR-20.2) and read raw; hook parents come from
// storefix.AgentHookParent and ForeignHookParentPID. SessionStart, the races
// with a new launch and the write-failure triggers are in
// hook_gate_session_start_test.go.

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// hgHookSession and hgHookPath are the session id and transcript path the
// tests' hooks report.
const (
	hgHookSession = "sess-hook"
	hgHookPath    = "/tmp/hg/hook.jsonl"
)

// hgRow describes a row the gate tests seed: its state, its session id, and
// the pane it records (the test pane with its start time unless told otherwise).
type hgRow struct {
	state       string
	session     string
	noPane      bool // no pane recorded (a lost create reply, SR-3.6)
	noPaneStart bool // pane recorded, start time unreadable at the create (NULL)
	opts        []apitest.SpawnOption
}

// hgSeed seeds r with liveness notes (so a clear is observable) and returns its id.
func hgSeed(t *testing.T, f *v5Store, r hgRow) string {
	t.Helper()
	pane := store.LaunchIdentity{Token: goodToken, Socket: apitest.TestSocket,
		PaneID: apitest.TestPaneID, PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}
	if r.noPane {
		pane.PaneID, pane.PanePID, pane.PaneStarttime = "", 0, ""
	}
	if r.noPaneStart {
		pane.PaneStarttime = ""
	}
	opts := append([]apitest.SpawnOption{apitest.WithLaunchIdentity(pane)}, liveness...)
	id, err := apitest.SeedSpawn(f.path, "", r.state, "/tmp", "", r.session, false, append(opts, r.opts...)...)
	if err != nil {
		t.Fatalf("SeedSpawn(%s): %v", r.state, err)
	}
	return id
}

// hgColumns reads id's row raw.
func hgColumns(t *testing.T, f *v5Store, id string) apitest.SpawnColumns {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(f.path, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return c
}

// hgParent is a hook's parent process: its pid and start time ("" when unreadable).
type hgParent struct {
	pid   int
	start string
}

// hgAgentParent is id's own agent: the row's recorded pane process.
func hgAgentParent(t *testing.T, f *v5Store, id string) hgParent {
	t.Helper()
	pid, start, err := storefix.AgentHookParent(f.s, id)
	if err != nil {
		t.Fatalf("AgentHookParent(%s): %v", id, err)
	}
	return hgParent{pid, start}
}

// hgGate is the gate of event from parent p carrying sessionID.
func hgGate(event string, p hgParent, sessionID string) store.HookGate {
	return store.HookGate{Event: event, ParentPID: p.pid, ParentStart: p.start, SessionID: sessionID}
}

// hgParents are the gate cases: which parent a hook has, relative to the
// row's own agent, and the reason when the gate refuses it ("" = applies).
var hgParents = []struct {
	name   string
	noPane bool // the row records no pane
	parent func(agent hgParent) hgParent
	reason string
}{
	{"own pane process", false, func(a hgParent) hgParent { return a }, ""},
	{"other pid", false, func(a hgParent) hgParent { return hgParent{storefix.ForeignHookParentPID(a.pid), a.start} }, store.HookReasonPIDMismatch},
	{"same pid other start", false, func(a hgParent) hgParent { return hgParent{a.pid, apitest.DarwinProcStarttime} }, store.HookReasonPIDMismatch},
	{"parent start unreadable", false, func(a hgParent) hgParent { return hgParent{a.pid, ""} }, store.HookReasonPIDMismatch},
	{"row with no pane", true, func(a hgParent) hgParent { return a }, store.HookReasonNoPaneRecorded},
}

// hgOpenRequests counts id's open permission requests.
func hgOpenRequests(t *testing.T, f *v5Store, id string) int {
	t.Helper()
	rows, err := f.s.OpenPermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("OpenPermissionRequestsForSpawn(%s): %v", id, err)
	}
	return len(rows)
}

// hgAssertIgnored checks a hook write that did not apply: want's reason, the
// row's every column as before, and no ad.spawn.state_transition since mark.
func hgAssertIgnored(t *testing.T, f *v5Store, id string, mark int, got, want store.HookApplied, before apitest.SpawnColumns) {
	t.Helper()
	if got != want {
		t.Errorf("result = %+v; want %+v", got, want)
	}
	if after := hgColumns(t, f, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed by a hook not applied:\n before %+v\n after  %+v", before, after)
	}
	if n := len(store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)); n != 0 {
		t.Errorf("%d ad.spawn.state_transition lines; want none", n)
	}
}

// TestHookGateOrdinaryTable is the gate table (SR-22.9, SR-5.2): every
// ordinary hook branch, from every gate case, on a pending, a live and a
// missing-before-report row. The own pane process applies whatever the state;
// any other parent changes nothing and gets its reason.
func TestHookGateOrdinaryTable(t *testing.T) {
	branches := []struct {
		name, event, to string
		soft, held      bool // held: an open request holds the working transition (SR-5.1)
	}{
		{"soft refresh", "Notification", "", true, false},
		{"ended", "SessionEnd", store.StateEnded, false, false},
		{"working", "UserPromptSubmit", store.StateWorking, false, false},
		{"working with open request", "UserPromptSubmit", store.StateWorking, false, true},
		{"waiting", "Stop", store.StateWaiting, false, false},
		{"ask_user", "PreToolUse", store.StateAskUser, false, false},
		{"check_permission", "PermissionRequest", store.StateCheckPermission, false, false},
	}
	f := newV5Store(t)
	for _, b := range branches {
		for _, state := range []string{store.StatePending, store.StateWaiting, store.StateMissing} {
			for _, g := range hgParents {
				t.Run(b.name+"/"+state+"/"+g.name, func(t *testing.T) {
					id := hgSeed(t, f, hgRow{state: state, noPane: g.noPane})
					if b.held {
						if _, err := apitest.SeedPermissionRequest(f.path, id, "Bash"); err != nil {
							t.Fatalf("SeedPermissionRequest: %v", err)
						}
					}
					before, requests, mark := hgColumns(t, f, id), hgOpenRequests(t, f, id), store.TrailMark(t)
					gate := hgGate(b.event, g.parent(hgAgentParent(t, f, id)), hgHookSession)
					out, got, err := f.s.ApplyHookTransitionResult(id, gate, b.to, b.soft, b.event, hgHookPath, true)
					if err != nil {
						t.Fatalf("ApplyHookTransitionResult: %v", err)
					}
					if n := hgOpenRequests(t, f, id); n != requests {
						t.Errorf("open requests %d -> %d; want unchanged", requests, n)
					}
					if g.reason != "" || b.held {
						if out != store.UpsertNoChange {
							t.Errorf("outcome = %q; want %q", out, store.UpsertNoChange)
						}
					}
					if g.reason != "" {
						hgAssertIgnored(t, f, id, mark, got, store.HookApplied{Reason: g.reason}, before)
						return
					}
					if !got.Applied {
						t.Fatalf("result = %+v; want applied", got)
					}
					lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
					if b.held {
						// Held: nothing written, one no-op transition line (SR-A-2.2).
						if after := hgColumns(t, f, id); !reflect.DeepEqual(after, before) {
							t.Errorf("held transition changed the row:\n before %+v\n after  %+v", before, after)
						}
						if len(lines) != 1 || lines[0]["prior_state"] != state || lines[0]["new_state"] != state {
							t.Errorf("transition lines = %v; want one %s -> %s", lines, state, state)
						}
						return
					}
					if out != store.UpsertUpdated {
						t.Errorf("outcome = %q; want %q", out, store.UpsertUpdated)
					}
					hgAssertApplied(t, before, hgColumns(t, f, id), b.to, b.soft)
					want := b.to
					if b.soft {
						want = state
					}
					if len(lines) != 1 || lines[0]["prior_state"] != state || lines[0]["new_state"] != want {
						t.Errorf("transition lines = %v; want one %s -> %s", lines, state, want)
					}
				})
			}
		}
	}
}

// hgAssertApplied checks an applied ordinary hook write on a row with no
// session id: the transition's columns, one version advance, the liveness
// notes cleared and the hook's session id and path recorded.
func hgAssertApplied(t *testing.T, before, after apitest.SpawnColumns, to string, soft bool) {
	t.Helper()
	wantState, wantLaunch, wantEnded := any(to), any(nil), any(nil)
	if soft {
		wantState, wantLaunch, wantEnded = before.State, before.LaunchStartedAt, before.EndedAt
	}
	got := map[string]any{
		"state": after.State, "row_version": after.RowVersion, "launch_started_at": after.LaunchStartedAt,
		"liveness_unverified_since": after.LivenessUnverifiedSince, "liveness_note": after.LivenessNote,
		"claude_session_id": after.ClaudeSessionID, "jsonl_path": after.JSONLPath,
		"pane_starttime": after.PaneStarttime, "pid": after.PID,
	}
	want := map[string]any{
		"state": wantState, "row_version": before.RowVersion.(int64) + 1, "launch_started_at": wantLaunch,
		"liveness_unverified_since": nil, "liveness_note": nil,
		"claude_session_id": hgHookSession, "jsonl_path": hgHookPath,
		"pane_starttime": before.PaneStarttime, "pid": before.PID,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applied write columns:\n got  %v\n want %v", got, want)
	}
	if to == store.StateEnded {
		if after.EndedAt == nil {
			t.Error("ended_at is NULL after the ended transition")
		}
	} else if !reflect.DeepEqual(after.EndedAt, wantEnded) {
		t.Errorf("ended_at = %v; want %v", after.EndedAt, wantEnded)
	}
}

// TestHookGateWaitingIfWorking pins b.svb's idle-prompt write: from the own pane
// process a working row returns to waiting and any other row is soft refreshed
// (an open request held or not); on a working row any other parent changes
// nothing and gets its reason.
func TestHookGateWaitingIfWorking(t *testing.T) {
	rows := []struct {
		name, state string
		openRequest bool
	}{
		{"working", store.StateWorking, false},
		{"pending", store.StatePending, false},
		{"waiting", store.StateWaiting, false},
		{"ask_user", store.StateAskUser, false},
		{"check_permission", store.StateCheckPermission, false},
		{"check_permission with open request", store.StateCheckPermission, true},
		{"ended", store.StateEnded, false},
		{"missing", store.StateMissing, false},
	}
	f := newV5Store(t)
	for _, r := range rows {
		// Other states only need the own pane process: the refusals there are
		// TestHookGateOrdinaryTable's soft-refresh cases.
		parents := hgParents[:1]
		if r.state == store.StateWorking {
			parents = hgParents
		}
		for _, g := range parents {
			t.Run(r.name+"/"+g.name, func(t *testing.T) {
				id := hgSeed(t, f, hgRow{state: r.state, noPane: g.noPane})
				if r.openRequest {
					if _, err := apitest.SeedPermissionRequest(f.path, id, "Bash"); err != nil {
						t.Fatalf("SeedPermissionRequest: %v", err)
					}
				}
				before, requests, mark := hgColumns(t, f, id), hgOpenRequests(t, f, id), store.TrailMark(t)
				gate := hgGate("Notification", g.parent(hgAgentParent(t, f, id)), hgHookSession)
				out, got, err := f.s.ApplyHookWaitingIfWorking(id, gate, "Notification", hgHookPath, true)
				if err != nil {
					t.Fatalf("ApplyHookWaitingIfWorking: %v", err)
				}
				if n := hgOpenRequests(t, f, id); n != requests {
					t.Errorf("open requests %d -> %d; want unchanged", requests, n)
				}
				if g.reason != "" {
					if out != store.UpsertNoChange {
						t.Errorf("outcome = %q; want %q", out, store.UpsertNoChange)
					}
					hgAssertIgnored(t, f, id, mark, got, store.HookApplied{Reason: g.reason}, before)
					return
				}
				if out != store.UpsertUpdated || !got.Applied {
					t.Fatalf("result = %q, %+v; want %q, applied", out, got, store.UpsertUpdated)
				}
				moved := r.state == store.StateWorking
				to, want := "", r.state
				if moved {
					to, want = store.StateWaiting, store.StateWaiting
				}
				hgAssertApplied(t, before, hgColumns(t, f, id), to, !moved)
				lines := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", id)
				if len(lines) != 1 || lines[0]["prior_state"] != r.state || lines[0]["new_state"] != want ||
					lines[0]["soft_refresh"] != !moved || lines[0]["triggering_event_name"] != "Notification" {
					t.Errorf("transition lines = %v; want one %s -> %s, soft_refresh %v, trigger Notification", lines, r.state, want, !moved)
				}
			})
		}
	}
}

// TestHookGateNullPaneStarttime pins SR-22.9's NULL pane_starttime: the pid
// alone decides and the first applied hook records its parent's start time;
// from then a parent with another start time is refused.
func TestHookGateNullPaneStarttime(t *testing.T) {
	f := newV5Store(t)
	for _, first := range []string{"Stop", "SessionStart"} {
		t.Run(first, func(t *testing.T) {
			id := hgSeed(t, f, hgRow{state: store.StateWaiting, session: "sess-row", noPaneStart: true})
			recorded := hgParent{apitest.TestPanePID, apitest.LinuxProcStarttime}
			var got store.HookApplied
			var err error
			if first == "SessionStart" {
				got, _, err = hgSessionStart(t, f, id, recorded, "sess-row")
			} else {
				got, err = f.s.ApplyHookTransition(id, hgGate(first, recorded, ""), store.StateWaiting, false, first, "", false)
			}
			if err != nil || !got.Applied {
				t.Fatalf("%s with pane_starttime NULL = %+v, %v; want applied", first, got, err)
			}
			if c := hgColumns(t, f, id); c.PaneStarttime != apitest.LinuxProcStarttime {
				t.Fatalf("pane_starttime = %v; want the parent's %q recorded", c.PaneStarttime, apitest.LinuxProcStarttime)
			}

			other := hgParent{apitest.TestPanePID, apitest.DarwinProcStarttime}
			before, mark := hgColumns(t, f, id), store.TrailMark(t)
			got, err = f.s.ApplyHookTransition(id, hgGate("Stop", other, ""), store.StateWaiting, false, "Stop", "", false)
			if err != nil {
				t.Fatalf("ApplyHookTransition: %v", err)
			}
			hgAssertIgnored(t, f, id, mark, got, store.HookApplied{Reason: store.HookReasonPIDMismatch}, before)
		})
	}
}

// TestHookGateSessionIDNotAGate pins SR-22.9: the session id is recorded, never
// compared. An applied ordinary hook records its session id and path only on a
// row with none (the path by the presence rule); a row with one keeps both.
func TestHookGateSessionIDNotAGate(t *testing.T) {
	const rowPath = "/tmp/hg/row.jsonl"
	cases := []struct {
		name                  string
		rowSession            string
		hookSession           string
		present               bool
		wantSession, wantPath any
	}{
		{"mismatching id applies, row keeps its id", "sess-row", "sess-other", true, "sess-row", rowPath},
		{"row with no id records the hook's", "", hgHookSession, true, hgHookSession, hgHookPath},
		{"row with no id, transcript not on disk", "", hgHookSession, false, hgHookSession, nil},
		{"hook with no id records nothing", "", "", true, nil, rowPath},
	}
	f := newV5Store(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := hgSeed(t, f, hgRow{state: store.StateWorking, session: c.rowSession,
				opts: []apitest.SpawnOption{apitest.WithJsonlPath(rowPath)}})
			gate := hgGate("Stop", hgAgentParent(t, f, id), c.hookSession)
			got, err := f.s.ApplyHookTransition(id, gate, store.StateWaiting, false, "Stop", hgHookPath, c.present)
			if err != nil || !got.Applied {
				t.Fatalf("Stop = %+v, %v; want applied", got, err)
			}
			after := hgColumns(t, f, id)
			if after.State != store.StateWaiting || after.ClaudeSessionID != c.wantSession || after.JSONLPath != c.wantPath {
				t.Errorf("state, session, path = %v, %v, %v; want waiting, %v, %v",
					after.State, after.ClaudeSessionID, after.JSONLPath, c.wantSession, c.wantPath)
			}
		})
	}
}

// TestHookGateReasonOrder pins decision A1's one read after the statement: no
// row gives no reason; a row with no pane gives no_pane_recorded even when the
// parent's start time is unreadable; a row with a pane and an unreadable parent
// start time gives pid_mismatch. Ordinary, SessionStart and idle-prompt
// (b.svb) writes alike.
func TestHookGateReasonOrder(t *testing.T) {
	f := newV5Store(t)
	unreadable := hgParent{apitest.TestPanePID, ""}
	cases := []struct {
		name string
		seed func(t *testing.T) string
		want store.HookApplied
	}{
		{"no row", func(*testing.T) string { return "hg-no-such-row" }, store.HookApplied{}},
		{"no pane, parent start unreadable", func(t *testing.T) string {
			return hgSeed(t, f, hgRow{state: store.StatePending, noPane: true})
		}, store.HookApplied{Reason: store.HookReasonNoPaneRecorded}},
		{"pane recorded, parent start unreadable", func(t *testing.T) string {
			return hgSeed(t, f, hgRow{state: store.StatePending})
		}, store.HookApplied{Reason: store.HookReasonPIDMismatch}},
	}
	for _, c := range cases {
		t.Run(c.name+"/ordinary", func(t *testing.T) {
			id := c.seed(t)
			got, err := f.s.ApplyHookTransition(id, hgGate("Stop", unreadable, ""), store.StateWaiting, false, "Stop", "", false)
			if err != nil || got != c.want {
				t.Errorf("ApplyHookTransition = %+v, %v; want %+v, nil", got, err, c.want)
			}
		})
		t.Run(c.name+"/SessionStart", func(t *testing.T) {
			id := c.seed(t)
			got, changed, err := hgSessionStart(t, f, id, unreadable, hgHookSession)
			if err != nil || changed || got != c.want {
				t.Errorf("RecordSessionStartIdentity = %+v, changed %v, %v; want %+v, false, nil", got, changed, err, c.want)
			}
		})
		t.Run(c.name+"/idle prompt", func(t *testing.T) {
			id := c.seed(t)
			out, got, err := f.s.ApplyHookWaitingIfWorking(id, hgGate("Notification", unreadable, ""), "Notification", "", false)
			if err != nil || out != store.UpsertNoChange || got != c.want {
				t.Errorf("ApplyHookWaitingIfWorking = %q, %+v, %v; want %q, %+v, nil", out, got, err, store.UpsertNoChange, c.want)
			}
		})
	}
}

// TestHookGatePermissionRequestInsert pins decision A8: the relay's
// permission-request INSERT carries the gate in its own statement, so a hook
// not from the row's pane process records no request and changes no row.
func TestHookGatePermissionRequestInsert(t *testing.T) {
	f := newV5Store(t)
	for _, g := range hgParents {
		t.Run(g.name, func(t *testing.T) {
			id := hgSeed(t, f, hgRow{state: store.StateCheckPermission, noPane: g.noPane})
			before, mark := hgColumns(t, f, id), store.TrailMark(t)
			gate := hgGate("PermissionRequest", g.parent(hgAgentParent(t, f, id)), "")
			out, got, err := f.s.UpsertOpenPermissionRequestResult(id, gate, "hg-req-1", "Bash", `{"command":"ls"}`, 0, store.WriterProcessHook)
			if err != nil {
				t.Fatalf("UpsertOpenPermissionRequestResult: %v", err)
			}
			wantOut, wantRequests := store.UpsertInserted, 1
			if g.reason != "" {
				wantOut, wantRequests = store.UpsertNoChange, 0
				if n := len(store.TrailEventsSince(t, mark, "ad.row_mutation.committed", id)); n != 0 {
					t.Errorf("%d ad.row_mutation.committed lines; want none", n)
				}
			}
			if want := (store.HookApplied{Applied: g.reason == "", Reason: g.reason}); out != wantOut || got != want {
				t.Errorf("result = %q, %+v; want %q, %+v", out, got, wantOut, want)
			}
			if n := hgOpenRequests(t, f, id); n != wantRequests {
				t.Errorf("open requests = %d; want %d", n, wantRequests)
			}
			if after := hgColumns(t, f, id); !reflect.DeepEqual(after, before) {
				t.Errorf("the request INSERT changed the spawns row:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}
