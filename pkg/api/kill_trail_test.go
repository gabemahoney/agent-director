package api_test

// kill_trail_test.go covers kill's trail (SR-6.4, SR-6.6, SR-14, SR-15):
// exactly one ad.kill.called per call on every return path, none from a
// closed Client, and ad.provenance.disagree at most once per reason per call.
// The fail-open case is in kill_trail_failopen_test.go.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ktrSentinels maps an ad.kill.called outcome to the sentinel the error wraps
// (ErrInternal has none).
var ktrSentinels = map[string]error{
	"ErrSpawnNotFound":       api.ErrSpawnNotFound,
	"ErrTmuxNotAvailable":    api.ErrTmuxNotAvailable,
	"ErrTmuxSessionConflict": api.ErrTmuxSessionConflict,
	"ErrTmuxUnresponsive":    api.ErrTmuxUnresponsive,
	"ErrTmuxKillFailed":      api.ErrTmuxKillFailed,
}

// ktrCallerFields are the four caller fields every kill trail record carries.
var ktrCallerFields = []string{"caller_process", "caller_pid", "caller_hostname", "caller_user"}

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

// TestKillTrailCalledPerReturnPath: every return path of Kill writes exactly
// one ad.kill.called for the id with the path's field values.
func TestKillTrailCalledPerReturnPath(t *testing.T) {
	cases := []struct {
		name  string
		noRow bool
		spec  killRowSpec
		setup func(*testing.T, *killEnv, *killRow)
		want  ktrCalled
	}{
		{name: "unknown id", noRow: true,
			want: ktrCalled{outcome: "ErrSpawnNotFound", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "finished row", spec: killRowSpec{State: store.StateEnded, NoSession: true},
			want: ktrCalled{outcome: "ok", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "unusable name", spec: killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName("kill.name")}},
			want: ktrCalled{outcome: "ErrInternal", lookup: "not_run", followup: "not_run", check: "not_run"}},
		{name: "ours success", setup: ktrDies,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: "not_run", check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "pending ours success", spec: killRowSpec{State: store.StatePending}, setup: ktrDies,
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
		{name: "gone, agent zombie", spec: killRowSpec{NoSession: true, Agent: agentZombie},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "gone", agentPID: true}},
		{name: "gone, none recorded", spec: killRowSpec{NoSession: true, Agent: agentNotRecorded},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "not_recorded"}},
		{name: "gone, unreadable", spec: killRowSpec{NoSession: true, Agent: agentUnreadable},
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: "not_run", check: "unreadable", agentPID: true}},
		{name: "gone, agent runs, no pane", spec: killRowSpec{NoSession: true},
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "gone", followup: "not_run", check: "alive", agentPID: true}},
		{name: "gone, agent pane in a viewer", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedViewer(t, *r)
				e.syncServers()
				ktrDies(t, e, r)
			},
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
		{name: "socket permission", setup: ktrScript(tmux.FailSocketDenied, tmux.CallLookup),
			want: ktrCalled{outcome: "ErrTmuxNotAvailable", lookup: "tmux_unavailable", followup: "not_run", check: "not_run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := killRow{ID: "kill-unknown-" + uuid.NewString()[:8]}
			if !tc.noRow {
				r = e.seedRow(t, tc.spec)
			}
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}

			res, err := e.kill(r.ID)

			recs := killCalled(t, r.ID)
			if len(recs) != 1 {
				t.Fatalf("ad.kill.called records = %d; want 1: %v", len(recs), recs)
			}
			ktrAssertCalled(t, recs[0], r, tc.want)
			ktrAssertOutcome(t, err, tc.want.outcome)
			if err == nil && res.KillSent != tc.want.killSent {
				t.Errorf("KillResult.KillSent = %t; want %t", res.KillSent, tc.want.killSent)
			}
		})
	}
}

// ktrAssertCalled checks one ad.kill.called record of r against want.
func ktrAssertCalled(t *testing.T, rec map[string]any, r killRow, want ktrCalled) {
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
		"include_finished": false, "source": "ad_kill",
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

// ktrAssertOutcome checks err against an ad.kill.called outcome name.
func ktrAssertOutcome(t *testing.T, err error, outcome string) {
	t.Helper()
	switch sentinel, named := ktrSentinels[outcome]; {
	case outcome == "ok" && err != nil:
		t.Errorf("err = %v; want nil", err)
	case outcome != "ok" && err == nil:
		t.Errorf("err = nil; want %s", outcome)
	case named && !errors.Is(err, sentinel):
		t.Errorf("err = %v; want %s", err, outcome)
	}
}

// ktrAssertCaller fails unless rec carries the four caller fields.
func ktrAssertCaller(t *testing.T, rec map[string]any) {
	t.Helper()
	for _, k := range ktrCallerFields {
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

// TestKillTrailClosedClient: Client.Kill writes one ad.kill.called; on a
// closed Client it returns ErrClientClosed and writes nothing.
func TestKillTrailClosedClient(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	ktrDies(t, e, &r)
	c, _ := e.client(t)

	if _, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if n := len(killCalled(t, r.ID)); n != 1 {
		t.Fatalf("ad.kill.called records after one Kill = %d; want 1", n)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID}); !errors.Is(err, api.ErrClientClosed) {
		t.Fatalf("Kill on a closed Client: err = %v; want ErrClientClosed", err)
	}
	if n := len(killCalled(t, r.ID)); n != 1 {
		t.Errorf("ad.kill.called records after a closed-Client Kill = %d; want still 1", n)
	}
	if n := len(killDisagrees(t, r.ID)); n != 0 {
		t.Errorf("ad.provenance.disagree records = %d; want 0", n)
	}
}

// ktrDisagree is one expected ad.provenance.disagree record; current is the
// current_session_name (null when "") and ours says tmux_session_id is the
// row's session id (else null).
type ktrDisagree struct {
	reason, server, verdict, action, current string
	ours                                     bool
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

// TestKillTrailProvenanceDisagree: each reason is written once per Kill call
// with its fields, never with a label value or another row's id; the normal
// Ours case writes none.
func TestKillTrailProvenanceDisagree(t *testing.T) {
	restarted := ktrDisagree{reason: "server_restarted", server: "restarted", verdict: "ours", action: "kill_sent", ours: true}
	adopted := ktrDisagree{reason: "adopted", server: "unknown", verdict: "ours", action: "kill_sent", ours: true}
	cases := []struct {
		name     string
		spec     killRowSpec
		setup    func(*testing.T, *killEnv, *killRow)
		want     []ktrDisagree
		followup string // the ad.kill.called followup_outcome, when checked
	}{
		{name: "normal ours writes none"},
		{name: "server_restarted", setup: ktrRestart, want: []ktrDisagree{restarted}},
		{name: "server_restarted on the lookup and the follow-up is written once",
			spec: killRowSpec{Agent: agentNotRecorded}, setup: ktrRestart, want: []ktrDisagree{restarted}, followup: "gone"},
		{name: "server_mismatch", setup: ktrRebind, want: []ktrDisagree{
			{reason: "server_mismatch", server: "differs", verdict: "different_server", action: "nothing_sent"}}},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true}, want: []ktrDisagree{adopted}},
		{name: "duplicate_label",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
			},
			want: []ktrDisagree{{reason: "duplicate_label", server: "match", verdict: "provenance_conflict", action: "nothing_sent"}}},
		{name: "scope_value",
			setup: func(_ *testing.T, e *killEnv, r *killRow) {
				e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
			},
			want: []ktrDisagree{{reason: "scope_value", server: "match", verdict: "provenance_conflict", action: "nothing_sent"}}},
		{name: "name_changed", spec: killRowSpec{NoSession: true}, setup: ktrRenamed, want: []ktrDisagree{
			{reason: "name_changed", server: "match", verdict: "ours", action: "kill_sent", current: "renamed-kill", ours: true}}},
		{name: "adopted and name_changed",
			spec: killRowSpec{NoServerIdentity: true, NoPane: true, NoSession: true}, setup: ktrRenamed,
			want: []ktrDisagree{adopted,
				{reason: "name_changed", server: "unknown", verdict: "ours", action: "kill_sent", current: "renamed-kill", ours: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			if tc.setup != nil {
				tc.setup(t, e, &r)
			}
			// Another row's label on the server: its id must appear in no record.
			other := "kill-other-" + uuid.NewString()[:8]
			e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8], Label: r.foreign(other)})
			if r.AgentPID > 0 {
				e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			}

			_, _ = e.kill(r.ID)

			recs := killDisagrees(t, r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			for i, want := range tc.want {
				ktrAssertDisagree(t, recs[i], r, want)
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

// ktrAssertDisagree checks one ad.provenance.disagree record of r against want.
func ktrAssertDisagree(t *testing.T, rec map[string]any, r killRow, want ktrDisagree) {
	t.Helper()
	var sessionID, current any
	if want.ours {
		sessionID = r.Session.ID
	}
	if want.current != "" {
		current = want.current
	}
	fields := map[string]any{
		"claude_instance_id": r.ID, "verb": "kill", "source": "ad_kill", "reason": want.reason,
		"tmux_socket": r.Socket, "tmux_session_name": r.Name, "tmux_session_id": sessionID,
		"current_session_name": current, "server": want.server, "verdict": want.verdict, "action": want.action,
	}
	for k, v := range fields {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%s: %s = %v (present %t); want %v", want.reason, k, got, ok, v)
		}
	}
	ktrAssertCaller(t, rec)
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
