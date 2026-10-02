package api_test

// kill_test.go holds kill's SR-6.1 rows that are not lookup outcomes (unknown
// id, finished rows, an unusable recorded name, an unusable socket
// directory), the four SR-20.6 rewrites (their names kept for the sprint
// demo's -run Kill), the no-log-line check, Client.Kill, and the finished-row
// opt-in's SessionStart race and its companion (SR-20.6). The lookup
// outcomes are lookup_calltable_test.go's; the sequence, the check, the
// ceilings, pending rows and the trail have their own kill_*_test.go files.
// The fixture is kill_fixture_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killOursCalls is the Ours kill sequence whose agent can be checked.
var killOursCalls = []tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession}

// killAssertOutcome fails unless id's one new ad.kill.called record (after
// the before already written) carries outcome.
func killAssertOutcome(t *testing.T, id string, before int, outcome string) {
	t.Helper()
	recs := killCalled(t, id)
	if len(recs) != before+1 {
		t.Fatalf("ad.kill.called records for %s = %d; want %d", id, len(recs), before+1)
	}
	if got := recs[before]["outcome"]; got != outcome {
		t.Errorf("ad.kill.called outcome = %v; want %s", got, outcome)
	}
}

// TestKillNonLookupRows covers SR-6.1's rows decided before any tmux call: an
// unknown id, finished rows, and a live row whose recorded name is unusable.
func TestKillNonLookupRows(t *testing.T) {
	dotted := apitest.RewrittenChars{Dot: true}
	cases := []struct {
		name string
		spec killRowSpec
		id   string // killed id; "" is the seeded row's
		want string // error name; "" is success
		desc *apitest.DescCase
	}{
		{name: "unknown id", id: "kill-absent", want: "ErrSpawnNotFound",
			desc: &apitest.DescCase{Name: "ErrSpawnNotFound"}},
		{name: "ended row", spec: killRowSpec{State: store.StateEnded}},
		{name: "missing row", spec: killRowSpec{State: store.StateMissing}},
		{name: "ended row with an unusable name", spec: killRowSpec{State: store.StateEnded, NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("")}}},
		{name: "empty name", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameEmpty())},
		{name: "control character", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a\tb")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameControlChar("a\tb"))},
		{name: "dot", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a.b")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameRewritten("a.b", dotted))},
		{name: "colon on a pending row", spec: killRowSpec{State: store.StatePending, NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a:b")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameRewritten("a:b", apitest.RewrittenChars{Colon: true}))},
		{name: "invalid UTF-8", spec: killRowSpec{State: store.StateWorking, NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a\xffb")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameRewritten("a\xffb", apitest.RewrittenChars{InvalidUTF8: true}))},
		{name: "every rewritten character", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a.b:c\xff")}},
			want: "ErrInternal", desc: killDescPtr(apitest.DescUnusableNameRewritten("a.b:c\xff",
				apitest.RewrittenChars{Dot: true, Colon: true, InvalidUTF8: true}))},
		{name: "control character wins over a rewritten one", spec: killRowSpec{NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("a.b\x01")}},
			want: "ErrInternal", desc: killDescPtr(killControlOnly("a.b\x01"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			id := tc.id
			var before apitest.SpawnColumns
			if id == "" {
				r := e.seedRow(t, tc.spec)
				id, before = r.ID, e.columns(t, r.ID)
			}
			res, err := e.kill(id)
			if tc.want == "" && err != nil {
				t.Fatalf("Kill: %v; want success", err)
			}
			if tc.want != "" {
				assertOneName(t, err, tc.want)
				apitest.AssertDescription(t, err.Error(), *tc.desc)
			}
			if res.KillSent {
				t.Error("kill_sent = true; want false")
			}
			e.assertKillCalls(t)
			if tc.id == "" {
				e.assertRowUnchanged(t, id, before)
			}
		})
	}
}

// killDescPtr returns a pointer to c, for a table's optional description case.
func killDescPtr(c apitest.DescCase) *apitest.DescCase { return &c }

// killControlOnly is the control-character case for name that also must not
// name any rewritten character: the first fault alone is described (SR-3.2).
func killControlOnly(name string) apitest.DescCase {
	c := apitest.DescUnusableNameControlChar(name)
	c.MustNot = append(c.MustNot, apitest.DescUnusableNameRewritten(name, apitest.RewrittenChars{}).MustNot...)
	return c
}

// TestKillUnusableSocketDirectory: a row with no recorded socket whose
// resolved socket directory is unusable gives ErrTmuxNotAvailable, no tmux call.
func TestKillUnusableSocketDirectory(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}})
	before := e.columns(t, r.ID)
	dir := filepath.Dir(e.defaultSocket)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
	want := &tmux.SocketDirError{Socket: e.defaultSocket, Dir: dir, Reason: tmux.SocketDirUnsafePermissions}

	res, err := e.kill(r.ID)
	assertOneName(t, err, "ErrTmuxNotAvailable")
	apitest.AssertDescription(t, err.Error(), apitest.DescSocketDirNothingDone(want.Socket, want.Dir, want.Error()))
	if res.KillSent {
		t.Error("kill_sent = true; want false")
	}
	e.assertKillCalls(t)
	e.assertRowUnchanged(t, r.ID, before)
}

// TestKillSwallowsTmuxFailure (SR-20.6, inverted): failed kills whose
// follow-up lookup still finds the session give ErrTmuxKillFailed; Gone is success.
func TestKillSwallowsTmuxFailure(t *testing.T) {
	cases := []struct {
		name    string
		failure tmux.Failure
		applied bool // the kills take effect although they report failure
	}{
		{"timed-out kills, session still there", tmux.FailTimeout, false},
		{"unrecognised kill replies, session still there", tmux.FailUnrecognized, false},
		{"timed-out kills, session gone", tmux.FailTimeout, true},
		{"unrecognised kill replies, session gone", tmux.FailUnrecognized, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{Agent: agentUnreadable})
			e.seedBystander(t, r.Socket)
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tc.failure, ExitStatus: 1, Applied: tc.applied},
				tmux.CallKillPane, tmux.CallKillSession)
			before := e.columns(t, r.ID)

			res, err := e.kill(r.ID)
			if tc.applied {
				if err != nil {
					t.Fatalf("Kill: %v; want success (follow-up finds no session)", err)
				}
				if !res.KillSent {
					t.Error("kill_sent = false; want true")
				}
			} else {
				assertOneName(t, err, "ErrTmuxKillFailed")
				apitest.AssertDescription(t, err.Error(),
					apitest.DescKillUncheckable(r.ID, r.Name, apitest.KillSent{Pane: true, Session: true}))
			}
			e.assertKillCalls(t, append(killOursCalls, tmux.CallLookup)...)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestKillSwallowedTmuxFailureLogsAtWARN (SR-20.6, inverted): Client.Kill
// writes no log line on success, a refusal or ErrTmuxKillFailed; the trail records each.
func TestKillSwallowedTmuxFailureLogsAtWARN(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, e *killEnv) (killRow, *apitest.DescCase)
		want  string // error name; "" is success
	}{
		{"success", func(t *testing.T, e *killEnv) (killRow, *apitest.DescCase) {
			r := e.seedRow(t, killRowSpec{})
			e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
			return r, nil
		}, ""},
		{"Leftover refusal", func(t *testing.T, e *killEnv) (killRow, *apitest.DescCase) {
			r := e.seedRow(t, killRowSpec{NoSession: true})
			e.seedSession(t, &r, tmuxfix.WithRowSessionLabel(r.old(), true))
			return r, killDescPtr(apitest.DescKillLeftover([]apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}}))
		}, "ErrTmuxSessionConflict"},
		{"no pane while the agent runs", func(t *testing.T, e *killEnv) (killRow, *apitest.DescCase) {
			r := e.seedRow(t, killRowSpec{NoSession: true})
			e.ensureServer(&r)
			e.seedBystander(t, r.Socket)
			e.syncServers()
			return r, killDescPtr(apitest.DescKillNoPane(r.ID, r.Name, r.AgentPID))
		}, "ErrTmuxKillFailed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r, desc := tc.setup(t, e)
			c, logs := e.client(t)
			trailed := len(killCalled(t, r.ID))

			_, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID})
			if tc.want == "" && err != nil {
				t.Fatalf("Kill: %v; want success", err)
			}
			outcome := "ok"
			if tc.want != "" {
				assertOneName(t, err, tc.want)
				apitest.AssertDescription(t, err.Error(), desc.PointsToOperatorActions())
				outcome = tc.want
			}
			if logs.Len() != 0 {
				t.Errorf("Client.Kill logged %q; want no log line", logs.String())
			}
			killAssertOutcome(t, r.ID, trailed, outcome)
		})
	}
}

// TestKillIsIdempotentAcrossRepeatedCalls (SR-20.6): once the session is
// gone, repeated kills of the live row succeed with no kill sent.
func TestKillIsIdempotentAcrossRepeatedCalls(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	e.seedBystander(t, r.Socket)
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	before := e.columns(t, r.ID)

	want := []bool{true, false, false}
	for i, sent := range want {
		res, err := e.kill(r.ID)
		if err != nil {
			t.Fatalf("Kill %d: %v", i+1, err)
		}
		if res.KillSent != sent {
			t.Errorf("Kill %d: kill_sent = %v; want %v", i+1, res.KillSent, sent)
		}
		e.assertRowUnchanged(t, r.ID, before)
	}
	e.assertKillCalls(t, append(killOursCalls, tmux.CallLookup, tmux.CallLookup)...)
}

// TestKillLiveRowInvokesTmux (SR-20.6): a live Ours row's kills target the
// agent's pane id and the labelled session's id on the row's socket.
func TestKillLiveRowInvokesTmux(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
	before := e.columns(t, r.ID)

	res, err := e.kill(r.ID)
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !res.KillSent {
		t.Error("kill_sent = false; want true")
	}
	e.assertKillCalls(t, killOursCalls...)
	for _, want := range []tmuxfix.SocketCall{
		{Call: tmux.CallKillPane, Socket: r.Socket, Target: r.Spawn.Identity.PaneID},
		{Call: tmux.CallKillSession, Socket: r.Socket, Target: r.Session.ID},
	} {
		got := e.rec.SocketCallsOf(want.Call)
		if len(got) != 1 || got[0].Socket != want.Socket || got[0].Target != want.Target {
			t.Errorf("%v calls = %+v; want one on %s targeting %s", want.Call, got, want.Socket, want.Target)
		}
	}
	e.assertRowUnchanged(t, r.ID, before)
}

// TestKillClient: Client.Kill with the Recorder as Options.TmuxClient kills a
// live Ours row (kill_sent true); a closed Client returns ErrClientClosed.
func TestKillClient(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
	c, logs := e.client(t)

	res, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !res.KillSent {
		t.Error("kill_sent = false; want true")
	}
	e.assertKillCalls(t, killOursCalls...)
	if logs.Len() != 0 {
		t.Errorf("Client.Kill logged %q; want no log line", logs.String())
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID}); !errors.Is(err, api.ErrClientClosed) {
		t.Errorf("Kill after Close: %v; want ErrClientClosed", err)
	}
}

// killRaceRow seeds a missing row whose resumed agent never reported in: its
// pane recorded, no pid, its session id kept, ended the window ago, and its own
// session older than ended_at and the bound.
func killRaceRow(t *testing.T, e *killEnv) resumeRow {
	t.Helper()
	r := e.seedStarting(t, startingRow{state: store.StateMissing, endedAgo: defWindow, noPID: true, age: defWindow + defBound})
	if c := e.columns(t, r.ID); c.PID != nil || c.ClaudeSessionID == nil || c.PanePID == nil {
		t.Fatalf("precondition: pid %v, session id %v, pane pid %v; want NULL, set, set", c.PID, c.ClaudeSessionID, c.PanePID)
	}
	return r
}

// killAssertAgentRuns fails unless r's agent process still runs in the fake.
func killAssertAgentRuns(t *testing.T, e *killEnv, r killRow) {
	t.Helper()
	if _, alive, _ := e.pc.StartTime(r.AgentPID); !alive {
		t.Errorf("agent process %d gone; want it still running", r.AgentPID)
	}
}

// TestKillIncludeFinishedSessionStartAfterLookupSendsNoKill (SR-20.6,
// AC-KILL-15): the agent's own SessionStart landing after kill's row read
// changes nothing: "never reported in", no listing and no kill.
func TestKillIncludeFinishedSessionStartAfterLookupSendsNoKill(t *testing.T) {
	e := newKillEnv(t)
	r := killRaceRow(t, e)
	e.sessionStartAfter(t, tmux.CallLookup, r.killRow, r.Spawn.ClaudeSessionID)

	res, err := e.killOptIn(r.ID)
	assertOneName(t, err, "ErrTmuxSessionConflict")
	apitest.AssertDescription(t, err.Error(), apitest.DescKillOptInNeverReportedIn(r.ID, r.Name, defBound), r.Token, r.StoreID)
	if res.KillSent {
		t.Error("kill_sent = true; want false")
	}
	e.assertKillCalls(t, tmux.CallLookup)
	if !seqHas(e, r.Socket, r.Session.ID) {
		t.Errorf("session %s gone; want it still running", r.Session.ID)
	}
	killAssertAgentRuns(t, e, r.killRow)
	if c := e.columns(t, r.ID); c.State != store.StateWaiting || c.PID != int64(r.Spawn.Identity.PanePID) {
		t.Errorf("row after the SessionStart: state %v, pid %v; want waiting, %d", c.State, c.PID, r.Spawn.Identity.PanePID)
	}
	kolAssertCalled(t, r.ID, map[string]any{"include_finished": true, "lookup_outcome": "ours",
		"outcome": "ErrTmuxSessionConflict", "kill_sent": false})
}

// TestKillIncludeFinishedSessionStartBeforeReadRefusesLiveRow (AC-KILL-16):
// the agent's own SessionStart before kill's read makes the row live, so the
// opt-in gets the live-row refusal with no tmux call; the agent runs on.
func TestKillIncludeFinishedSessionStartBeforeReadRefusesLiveRow(t *testing.T) {
	e := newKillEnv(t)
	r := killRaceRow(t, e)
	if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "SessionStart", r.Spawn.ClaudeSessionID); !a.Applied {
		t.Fatalf("SessionStart from the agent = %+v; want applied", a)
	}
	started, sessions := e.columns(t, r.ID), e.rec.Sessions(r.Socket)
	if started.State != store.StateWaiting {
		t.Fatalf("state after the SessionStart = %v; want waiting", started.State)
	}

	res, err := e.killOptIn(r.ID)
	kolAssertRefused(t, e, r.killRow, store.StateWaiting, res, err, started, sessions)
	killAssertAgentRuns(t, e, r.killRow)
}
