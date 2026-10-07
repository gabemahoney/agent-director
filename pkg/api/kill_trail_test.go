package api_test

// kill_trail_test.go covers kill's trail, with and without the finished-row
// opt-in (SR-6.4, SR-6.6, SR-14, SR-15): exactly one ad.kill.called per call,
// every field checked, on every return path of kill and on the opt-in's
// unknown id, unusable name, Gone (agent gone, agent pane in a viewer, agent
// running with no pane), Leftover, still stopping, still starting, never
// reported in and reported-in paths, with include_finished and the lookup's
// token; ad.provenance.disagree at most once per reason per call; and, for
// kill, send-keys and pause, the fail-open trail. The opt-in's Can't tell,
// tmux-unavailable and name-held Gone rows are TestKillIncludeFinishedTable's,
// which checks only their include_finished, outcome, kill_sent and
// lookup_outcome. A closed Client is TestPaneVerbsUnknownIDAndClosedClient's.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ktrCalled is one expected ad.kill.called; agentPID and survivors say whether
// the row's agent pid and teammate pids are recorded (else null and []).
type ktrCalled struct {
	outcome, lookup, followup, check string
	killSent, paneKilled             bool
	agentPID, survivors              bool
}

// ktrDies makes the agent's pane kill end r's agent process.
func ktrDies(_ *testing.T, e *killEnv, r *killRow) {
	e.setAfterCall(tmux.CallKillPane, procfix.Gone(), r.AgentPID)
}

// ktrScript scripts the typed failure f on every call of kind call on r's socket.
func ktrScript(f tmux.Failure, call tmux.Call) func(*testing.T, *killEnv, *killRow) {
	return func(_ *testing.T, e *killEnv, r *killRow) { e.rec.Script(r.Socket, tmuxfix.Script{Failure: f}, call) }
}

// ktrBystander keeps a session on r's server after the row's is killed.
func ktrBystander(t *testing.T, e *killEnv, r *killRow) { e.seedBystander(t, r.Socket) }

// ktrRebind binds a new server on r's socket while the recorded one still runs.
func ktrRebind(_ *testing.T, e *killEnv, r *killRow) {
	e.rec.RebindServer(r.Socket, tmuxfix.Server{})
	e.syncServers()
}

// ktrRestart restarts r's server and seeds r's session on the new one.
func ktrRestart(t *testing.T, e *killEnv, r *killRow) {
	e.rec.RestartServer(r.Socket, tmuxfix.Server{})
	e.syncServers()
	e.seedSession(t, r)
}

// ktrRenamed seeds r's session under another stored name.
func ktrRenamed(t *testing.T, e *killEnv, r *killRow) {
	e.seedSession(t, r, tmuxfix.WithRowSessionName("renamed-kill"))
}

// TestKillTrailCalledPerReturnPath: every return path of Kill, and the
// finished-row opt-in's paths the file header lists (include_finished true;
// an ended row, as it never branches on ended against missing), writes
// exactly one ad.kill.called for the id with the path's field values and
// leaves the row as it was; a path that makes no lookup writes no
// ad.provenance.disagree, and only the opt-in's "never reported in" has its
// field combination.
func TestKillTrailCalledPerReturnPath(t *testing.T) {
	t.Parallel()
	ended := func(s startingRow) *startingRow { s.state = store.StateEnded; return &s }
	past := ended(kosReportedIn("", agentAlive))
	goneRunning := ended(startingRow{endedAgo: defWindow, noSession: true})
	viewer := func(t *testing.T, e *killEnv, r *killRow) {
		e.seedViewer(t, *r)
		e.syncServers()
		ktrDies(t, e, r)
	}
	notRun := tmux.TokenNotRun
	cases := []struct {
		name  string
		noRow bool
		spec  killRowSpec
		row   *startingRow // seeded with seedStarting instead of spec
		optIn bool
		setup func(*testing.T, *killEnv, *killRow)
		want  ktrCalled
		calls []tmux.Call // the tmux calls, when checked, and nothing else: no wait
	}{
		{name: "unknown id", noRow: true,
			want: ktrCalled{outcome: "ErrSpawnNotFound", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "finished row", spec: killRowSpec{State: store.StateEnded, NoSession: true},
			want: ktrCalled{outcome: "ok", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "unusable name", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("kill.name")}},
			want: ktrCalled{outcome: "ErrInternal", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "ours success", setup: ktrDies,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: "not_run", check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "ours wait expired, agent runs",
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "ours", followup: "not_run", check: "alive", killSent: true, paneKilled: true, agentPID: true}},
		{name: "ours wait expired, teammate survives", spec: killRowSpec{Teammates: 2}, setup: ktrDies,
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "ours", followup: "not_run", check: "gone", killSent: true, paneKilled: true, agentPID: true, survivors: true}},
		{name: "follow-up gone, none recorded", spec: killRowSpec{Agent: agentNotRecorded}, setup: ktrBystander,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: "gone", check: "not_recorded", killSent: true}},
		{name: "follow-up gone, unreadable", spec: killRowSpec{Agent: agentUnreadable}, setup: ktrBystander,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: "gone", check: "unreadable", killSent: true, paneKilled: true, agentPID: true}},
		{name: "follow-up ours", spec: killRowSpec{Agent: agentNotRecorded}, setup: ktrScript(tmux.FailTimeout, tmux.CallKillSession),
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "ours", followup: "ours", check: "not_recorded", killSent: true}},
		{name: "follow-up unreadable", spec: killRowSpec{Agent: agentNotRecorded},
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Times: 1}, tmux.CallLookup).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
			},
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: "cant_tell", check: "not_recorded", killSent: true}},
		{name: "leftover", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			},
			want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "leftover", followup: "not_run", check: "not_run"}},
		{name: "gone, agent gone", spec: killRowSpec{NoSession: true, Agent: agentGone},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "gone", agentPID: true}},
		{name: "gone, none recorded", spec: killRowSpec{NoSession: true, Agent: agentNotRecorded},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "not_recorded"}},
		{name: "gone, unreadable: nothing sent, no listing, no follow-up", spec: killRowSpec{NoSession: true, Agent: agentUnreadable},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "unreadable", agentPID: true}, calls: paneLookupCalls},
		{name: "gone, agent runs, no pane", spec: killRowSpec{NoSession: true},
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "gone", followup: "not_run", check: "alive", agentPID: true}},
		{name: "gone, agent pane in a viewer", spec: killRowSpec{NoSession: true}, setup: viewer,
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "different server", setup: ktrRebind,
			want: ktrCalled{outcome: "ErrTmuxNotAvailable", lookup: "different_server", followup: "not_run", check: "not_run"}},
		{name: "conflicting labels",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
			},
			want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "provenance_conflict", followup: "not_run", check: "not_run"}},
		{name: "unreadable lookup", setup: ktrScript(tmux.FailTimeout, tmux.CallLookup),
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "cant_tell", followup: "not_run", check: "not_run"}},
		{name: "tmux unavailable", setup: ktrScript(tmux.FailUnavailable, tmux.CallLookup),
			want: ktrCalled{outcome: "ErrTmuxNotAvailable", lookup: "tmux_unavailable", followup: "not_run", check: "not_run"}},
		{name: "opt-in, unknown id", optIn: true, noRow: true, calls: []tmux.Call{},
			want: ktrCalled{outcome: "ErrSpawnNotFound", lookup: notRun, followup: notRun, check: notRun}},
		{name: "opt-in, unusable name", optIn: true, spec: killRowSpec{State: store.StateEnded, NoSession: true,
			Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(preGqeDefaultName)}},
			want: ktrCalled{outcome: "ErrInternal", lookup: notRun, followup: notRun, check: notRun}},
		{name: "opt-in, gone, agent gone", optIn: true, row: ended(startingRow{endedAgo: defWindow, agent: agentGone, noSession: true}),
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: notRun, check: "gone", agentPID: true}},
		{name: "opt-in, gone, agent pane in a viewer", optIn: true, row: goneRunning, setup: viewer,
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: notRun, check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "opt-in, gone, agent runs, no pane", optIn: true, row: goneRunning,
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "gone", followup: notRun, check: "alive", agentPID: true}},
		{name: "opt-in, leftover", optIn: true, row: ended(startingRow{endedAgo: defWindow, noSession: true}),
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true), e.createdBefore(defWindow+defBound))
			}, want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "leftover", followup: notRun, check: notRun}},
		{name: "opt-in, still stopping", optIn: true, row: ended(startingRow{endedAgo: defWindow - time.Second, age: defWindow + defBound}),
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: notRun, check: notRun}},
		{name: "opt-in, still starting", optIn: true, row: ended(startingRow{endedAgo: defWindow, age: defBound - time.Second}),
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: notRun, check: notRun}},
		{name: "opt-in, never reported in", optIn: true, row: ended(startingRow{endedAgo: defWindow, noPID: true, age: defWindow + defBound}),
			want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "ours", followup: notRun, check: notRun}},
		{name: "opt-in, reported in, killed", optIn: true, row: past, setup: ktrDies,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: notRun, check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "opt-in, reported in, agent outlives the wait", optIn: true, row: past,
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "ours", followup: notRun, check: "alive", killSent: true, paneKilled: true, agentPID: true}},
		{name: "opt-in, reported in, follow-up unreadable", optIn: true, row: ended(kosReportedIn("", agentUnreadable)),
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.Script(r.Socket, tmuxfix.Script{Times: 1}, tmux.CallLookup).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
			}, want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: "cant_tell", check: "unreadable", killSent: true, paneKilled: true, agentPID: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := killRow{ID: "kill-unknown-" + uuid.NewString()[:8]}
			switch {
			case tc.row != nil:
				r = e.seedStarting(t, *tc.row).killRow
			case !tc.noRow:
				r = e.seedRow(t, tc.spec)
			}
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			kill := e.kill
			if tc.optIn {
				kill = e.killOptIn
			}
			var before apitest.SpawnColumns
			if !tc.noRow {
				before = e.columns(t, r.ID)
			}

			start := e.clock.Now()
			res, err := kill(r.ID)

			if elapsed := e.clock.Now().Sub(start); tc.calls != nil && elapsed != seqCharged(e, tc.calls) {
				t.Errorf("virtual time = %v; want the calls alone, no wait", elapsed)
			}
			if !tc.noRow {
				e.assertRowUnchanged(t, r.ID, before)
			}
			if tc.calls != nil {
				e.assertKillCalls(t, tc.calls...)
			}

			recs := killCalled(t, r.ID)
			if len(recs) != 1 {
				t.Fatalf("ad.kill.called records = %d; want 1: %v", len(recs), recs)
			}
			ktrAssertCalled(t, recs[0], r, tc.want, tc.optIn)
			ktrAssertOutcome(t, err, tc.want.outcome)
			if err == nil && res.KillSent != tc.want.killSent {
				t.Errorf("KillResult.KillSent = %t; want %t", res.KillSent, tc.want.killSent)
			}
			rec := recs[0]
			never := rec["include_finished"] == true && rec["lookup_outcome"] == "ours" &&
				rec["outcome"] == "ErrTmuxSessionConflict" && rec["kill_sent"] == false
			if want := tc.name == "opt-in, never reported in"; never != want {
				t.Errorf("never-reported-in fields (include_finished, ours, ErrTmuxSessionConflict, no kill) = %t; want %t", never, want)
			}
			if d := killDisagrees(t, r.ID); tc.want.lookup == notRun && len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}
		})
	}
}

// ktrAssertCalled checks one ad.kill.called record of r against want, with
// include_finished optIn.
func ktrAssertCalled(t *testing.T, rec map[string]any, r killRow, want ktrCalled, optIn bool) {
	t.Helper()
	var agent any
	if want.agentPID {
		agent = float64(r.AgentPID)
	}
	survivors := []any{}
	if want.survivors {
		for _, pid := range r.TeammatePIDs {
			survivors = append(survivors, float64(pid))
		}
	}
	fields := map[string]any{
		"claude_instance_id": r.ID, "tmux_session_name": r.Name, "outcome": want.outcome,
		"lookup_outcome": want.lookup, "followup_outcome": want.followup, "process_check": want.check,
		"kill_sent": want.killSent, "pane_killed": want.paneKilled, "agent_pid": agent,
		"include_finished": optIn, "source": "ad_kill",
	}
	for k, v := range fields {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%s = %v (present %t); want %v", k, got, ok, v)
		}
	}
	if got, _ := json.Marshal(rec["survivor_pids"]); string(got) != ktrJSON(t, survivors) {
		t.Errorf("survivor_pids = %s; want %s", got, ktrJSON(t, survivors))
	}
	ktrAssertCaller(t, rec)
}

// ktrAssertOutcome checks err against an ad.kill.called outcome: nil for ok,
// else that one catalogued name (ErrInternal: none).
func ktrAssertOutcome(t *testing.T, err error, outcome string) {
	t.Helper()
	if outcome == "ok" {
		if err != nil {
			t.Errorf("err = %v; want nil", err)
		}
		return
	}
	assertOneName(t, err, outcome)
}

// ktrAssertCaller fails unless rec carries the four caller fields.
func ktrAssertCaller(t *testing.T, rec map[string]any) {
	t.Helper()
	for _, k := range []string{"caller_process", "caller_pid", "caller_hostname", "caller_user"} {
		if _, ok := rec[k]; !ok {
			t.Errorf("record lacks %s: %v", k, rec)
		}
	}
}

// ktrJSON is v's JSON text.
func ktrJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return string(b)
}

// TestKillTrailProvenanceDisagree: each reason is written once per kill call
// (with the opt-in too, its row never written) with its fields, never with a
// label value or another row's id; the normal Ours case writes none.
func TestKillTrailProvenanceDisagree(t *testing.T) {
	t.Parallel()
	restarted := disagreeWant{reason: "server_restarted", server: "restarted", verdict: "ours", action: "kill_sent", ours: true}
	adopted := disagreeWant{reason: "adopted", server: "unknown", verdict: "ours", action: "kill_sent", ours: true}
	reported := kosReportedIn("", agentAlive)
	never := reported
	never.noPID = true
	renamed := func(action string) disagreeWant {
		return disagreeWant{reason: "name_changed", server: "match", verdict: "ours", action: action, current: "renamed-kill", ours: true}
	}
	cases := []struct {
		name     string
		spec     killRowSpec
		setup    func(*testing.T, *killEnv, *killRow)
		optIn    func(*testing.T, *killEnv) killRow // the opt-in on the ended row it seeds, in place of spec and setup
		want     []disagreeWant
		followup string // the ad.kill.called followup_outcome, when checked
	}{
		{name: "normal ours writes none"},
		{name: "server_restarted", setup: ktrRestart, want: []disagreeWant{restarted}},
		{name: "server_restarted on the lookup and the follow-up is written once",
			spec: killRowSpec{Agent: agentNotRecorded}, setup: ktrRestart, want: []disagreeWant{restarted}, followup: "gone"},
		{name: "server_mismatch", setup: ktrRebind, want: []disagreeWant{
			{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "nothing_sent"}}},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true}, want: []disagreeWant{adopted}},
		{name: "duplicate_label",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
			},
			want: []disagreeWant{{reason: "duplicate_label", server: "match", verdict: "provenance_conflict", action: "nothing_sent"}}},
		{name: "scope_value",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
			},
			want: []disagreeWant{{reason: "scope_value", server: "match", verdict: "provenance_conflict", action: "nothing_sent"}}},
		{name: "name_changed", spec: killRowSpec{NoSession: true}, setup: ktrRenamed, want: []disagreeWant{
			{reason: "name_changed", server: "match", verdict: "ours", action: "kill_sent", current: "renamed-kill", ours: true}}},
		{name: "adopted and name_changed",
			spec: killRowSpec{NoServerIdentity: true, NoPane: true, NoSession: true}, setup: ktrRenamed,
			want: []disagreeWant{adopted,
				{reason: "name_changed", server: "unknown", verdict: "ours", action: "kill_sent", current: "renamed-kill", ours: true}}},
		// The opt-in's lookup writes its reasons too, name_changed on a refusal,
		// but never adopted: a finished row is never written.
		{name: "opt-in, normal ours writes none", optIn: func(t *testing.T, e *killEnv) killRow { return kotFinished(t, e, reported) }},
		{name: "opt-in, server_restarted", optIn: func(t *testing.T, e *killEnv) killRow {
			r := e.seedStarting(t, startingRow{state: store.StateEnded, endedAgo: defWindow, noSession: true}).killRow
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedSession(t, &r, e.createdBefore(kotOldSession))
			return r
		}, want: []disagreeWant{restarted}},
		{name: "opt-in, adoption due, no adopted written", optIn: func(t *testing.T, e *killEnv) killRow {
			spec := e.resumableSpec(defWindow, agentAlive)
			spec.NoServerIdentity = true
			r := e.seedResumableRow(t, spec).killRow
			e.seedSession(t, &r, e.createdBefore(kotOldSession))
			return r
		}},
		{name: "opt-in, name_changed, killed", optIn: func(t *testing.T, e *killEnv) killRow {
			return kotFinished(t, e, reported, tmuxfix.WithRowSessionName("renamed-kill"))
		}, want: []disagreeWant{renamed("kill_sent")}},
		{name: "opt-in, name_changed, never reported in", optIn: func(t *testing.T, e *killEnv) killRow {
			return kotFinished(t, e, never, tmuxfix.WithRowSessionName("renamed-kill"))
		}, want: []disagreeWant{renamed("nothing_sent")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			var r killRow
			kill := e.kill
			if tc.optIn != nil {
				r, kill = tc.optIn(t, e), e.killOptIn
			} else {
				r = e.seedRow(t, tc.spec)
			}
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			// Another row's label on the server: its id must appear in no record.
			other := "kill-other-" + uuid.NewString()[:8]
			e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
			if r.AgentPID > 0 {
				e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			}
			before := e.columns(t, r.ID)

			_, _ = kill(r.ID)

			if tc.optIn != nil {
				e.assertRowUnchanged(t, r.ID, before)
				if e.store.adoptTries != 0 {
					t.Errorf("adoption writes attempted = %d; want none on a finished row", e.store.adoptTries)
				}
			}

			recs := killDisagrees(t, r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			for i, want := range tc.want {
				assertDisagreeRecord(t, recs[i], r, "kill", "ad_kill", want)
				ktrAssertNoForeignContent(t, recs[i], r.Token, e.storeID, other)
			}
			called := killCalled(t, r.ID)
			if len(called) != 1 {
				t.Fatalf("ad.kill.called records = %d; want 1", len(called))
			}
			ktrAssertNoForeignContent(t, called[0], r.Token, e.storeID, other)
			if tc.followup != "" && called[0]["followup_outcome"] != tc.followup {
				t.Errorf("followup_outcome = %v; want %s", called[0]["followup_outcome"], tc.followup)
			}
		})
	}
}

// ktrAssertNoForeignContent fails when rec's text holds any of the values
// (a launch token, the store id, another row's id).
func ktrAssertNoForeignContent(t *testing.T, rec map[string]any, values ...string) {
	t.Helper()
	text := ktrJSON(t, rec)
	for _, v := range values {
		if strings.Contains(text, v) {
			t.Errorf("record %s contains %q", text, v)
		}
	}
}

// kotOldSession is the age of a reported-in row's own session: past the
// bound and older than an ended_at the window ago.
var kotOldSession = defWindow + defBound

// kotFinished seeds s's ended row with no session, then its own session aged
// kotOldSession with opts, and makes the agent exit at the session kill.
func kotFinished(t *testing.T, e *killEnv, s startingRow, opts ...tmuxfix.RowSessionOption) killRow {
	t.Helper()
	s.state, s.noSession = store.StateEnded, true
	r := e.seedStarting(t, s).killRow
	e.seedSession(t, &r, append([]tmuxfix.RowSessionOption{e.createdBefore(kotOldSession)}, opts...)...)
	e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
	return r
}

// kotFailOpenRuns runs one reported-in and one never-reported-in opt-in kill
// and returns one line each. With a working trail each wrote its ad.kill.called.
func kotFailOpenRuns(t *testing.T, working bool) []string {
	t.Helper()
	never := kosReportedIn(store.StateEnded, agentAlive)
	never.noPID = true
	var lines []string
	for i, s := range []startingRow{kosReportedIn(store.StateMissing, agentAlive), never} {
		e := newKillEnv(t)
		r := e.seedStarting(t, s).killRow
		ktrDies(t, e, &r)
		res, err := e.killOptIn(r.ID)
		if n := len(killCalled(t, r.ID)); working && n != 1 {
			t.Fatalf("working trail: ad.kill.called records for %s = %d; want 1", r.ID, n)
		}
		name := fmt.Sprintf("opt-in %s kill_sent=%t ended_at=%v", []string{"reported-in", "never-reported-in"}[i],
			res.KillSent, e.columns(t, r.ID).EndedAt)
		lines = append(lines, failOpenLine(t, e, name, r.ID, err))
	}
	return lines
}

// ktrFailOpenRuns kills one row per return-path shape, ids prefix-<name>, and
// returns one line per kill. With a working trail each wrote its ad.kill.called.
func ktrFailOpenRuns(t *testing.T, prefix string, working bool) []string {
	t.Helper()
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(*testing.T, *killEnv, *killRow)
	}{
		{name: "ours", setup: ktrDies},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true}, setup: ktrDies},
		{name: "leftover", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			}},
		{name: "no-pane", spec: killRowSpec{NoSession: true}},
		{name: "rebound", setup: ktrRebind},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		tc.spec.ID = prefix + "-" + tc.name
		r := e.seedRow(t, tc.spec)
		if tc.setup != nil {
			tc.setup(t, e, &r)
		}
		res, err := e.kill(r.ID)
		if n := len(killCalled(t, r.ID)); working && n != 1 {
			t.Fatalf("working trail: ad.kill.called records for %s = %d; want 1", r.ID, n)
		}
		lines = append(lines, failOpenLine(t, e, fmt.Sprintf("%s kill_sent=%t", tc.name, res.KillSent), r.ID, err))
	}
	return lines
}

// failOpenLine is one fail-open call's line: name, its error, the tmux calls
// and the row's state, row_version, pid and identity columns, with id elided
// (ended_at is not compared: an agent's own SessionEnd stamps the wall clock).
func failOpenLine(t *testing.T, e *killEnv, name, id string, err error) string {
	t.Helper()
	var calls []tmux.Call
	for _, c := range e.rec.SocketCalls() {
		calls = append(calls, recordedCall(c))
	}
	c := e.columns(t, id)
	return strings.ReplaceAll(fmt.Sprintf("%s err=%v calls=%v state=%v row_version=%v pid=%v server=%v/%v/%v pane=%v/%v/%v",
		name, err, calls, c.State, c.RowVersion, c.PID, c.TmuxServerPID, c.TmuxServerStarted,
		c.TmuxServerStarttime, c.PaneID, c.PanePID, c.PaneStarttime), id, "<id>")
}

// trailFailOpenChildEnv gates TestTrailFailOpenChild and carries the id prefix.
const trailFailOpenChildEnv = "AD_TRAIL_FAIL_OPEN_CHILD"

// trailFailOpenLinePrefix marks the child's result lines in its output.
const trailFailOpenLinePrefix = "TFO|"

// trailFailOpenRuns runs kill's, the opt-in's, send-keys' and pause's
// fail-open calls, ids under prefix, checking their records when working.
func trailFailOpenRuns(t *testing.T, prefix string, working bool) []string {
	t.Helper()
	return slices.Concat(ktrFailOpenRuns(t, prefix+"-kill", working), kotFailOpenRuns(t, working),
		sktFailOpenRuns(t, prefix+"-sk", working), ptrFailOpenRuns(t, prefix+"-pause", working))
}

// TestTrailFailOpen (SR-6.4, SR-7.4, SR-14): with the trail unwritable, kill
// (with and without the finished-row opt-in), send-keys and pause give the
// results, errors, tmux calls and rows of a run with a working trail. The
// unwritable run is a child of this test binary whose HOME's .agent-director
// is mode 0500 before the trail opens.
func TestTrailFailOpen(t *testing.T) {
	t.Parallel()
	prefix := "failopen-" + uuid.NewString()[:8]
	want := trailFailOpenRuns(t, prefix, true)

	cmd := exec.Command(os.Args[0], "-test.run=^TestTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), trailFailOpenChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, trailFailOpenLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestTrailFailOpenChild is TestTrailFailOpen's child: it runs the calls with
// an unwritable trail, prints their lines and checks no trail file appeared.
func TestTrailFailOpenChild(t *testing.T) {
	t.Parallel()
	prefix := os.Getenv(trailFailOpenChildEnv)
	if prefix == "" {
		t.Skip("run only as TestTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.fail_open_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range trailFailOpenRuns(t, prefix, false) {
		fmt.Println(trailFailOpenLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
