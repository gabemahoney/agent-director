package tmuxfix_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// failures lists every tmux.Failure kind.
var failures = []tmux.Failure{
	tmux.FailTimeout, tmux.FailUnavailable, tmux.FailSocketDenied, tmux.FailNoServer,
	tmux.FailNoSocket, tmux.FailDuplicate, tmux.FailLabel, tmux.FailUnrecognized,
}

// classTimeout is call's class timeout in t (SR-13.1).
func classTimeout(t tmux.Timeouts, call tmux.Call) time.Duration {
	switch call {
	case tmux.CallLookup, tmux.CallListPanes:
		return t.Query
	case tmux.CallCreate:
		return t.Create
	}
	return t.Action
}

// defaultTimeouts are the internal/config defaults, read through a zero config.Tmux.
func defaultTimeouts() tmux.Timeouts {
	var c config.Tmux
	return tmux.Timeouts{Query: c.EffectiveQueryTimeout(), Action: c.EffectiveActionTimeout(), Create: c.EffectiveCreateTimeout()}
}

// TestRecorder_NewSession: the create adds a session labelled for its own
// instance id, and its failure modes leave the table as documented.
func TestRecorder_NewSession(t *testing.T) {
	cur := tmuxfix.Valid(tmuxfix.Token, agent, tmuxfix.StoreID)
	cases := []struct {
		name        string
		arrange     func(r *tmuxfix.Recorder)
		wantFailure tmux.Failure
		wantReply   bool
		wantNew     *tmux.Label // the "work" session's label; nil: none added
	}{
		{"chained-label", func(*tmuxfix.Recorder) {}, 0, true, &cur},
		{"no-server-starts-one", func(r *tmuxfix.Recorder) { r.StopServer(sockA) }, 0, true, &cur},
		{"label-step-fails", func(r *tmuxfix.Recorder) {
			r.Script(sockA, tmuxfix.Script{Failure: tmux.FailLabel}, tmux.CallCreate)
		}, tmux.FailLabel, true, &tmux.Label{}},
		{"duplicate", func(r *tmuxfix.Recorder) {
			r.SeedSessions(sockA, sess("", "work", tmux.Label{}, false))
		}, tmux.FailDuplicate, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seeded()
			tc.arrange(r)
			before := len(r.Sessions(sockA))
			reply, err := r.NewSession(sockA, "work", "/w", map[string]string{"K": "V"}, []string{"claude"}, tmuxfix.Token, agent, tmuxfix.StoreID)
			if tc.wantFailure != 0 {
				if ce := callErr(t, err); ce.Failure != tc.wantFailure || ce.Call != tmux.CallCreate {
					t.Errorf("CallError = %+v, want %v on the create", ce, tc.wantFailure)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if (reply.SessionID != "") != tc.wantReply {
				t.Fatalf("reply = %+v, want a reply %v", reply, tc.wantReply)
			}
			if tc.wantNew == nil {
				if n := len(r.Sessions(sockA)); n != before {
					t.Errorf("sessions %d -> %d, want nothing added", before, n)
				}
				return
			}
			got := findByName(t, r, "work")
			srv, _ := r.Server(sockA)
			want := tmux.CreateReply{SessionID: got.ID, ServerPID: srv.PID, ServerStart: srv.Start, PaneID: got.Panes[0].ID, PanePID: got.Panes[0].PID}
			if reply != want {
				t.Errorf("reply = %+v, want %+v", reply, want)
			}
			if got.Label != *tc.wantNew || got.LabelSet != (tc.wantNew.Kind == tmux.LabelValid) {
				t.Errorf("label = %+v (set %v), want %+v", got.Label, got.LabelSet, *tc.wantNew)
			}
			c := r.SocketCallsOf(tmux.CallCreate)[0]
			if c.Socket != sockA || c.Target != "work" || c.Cwd != "/w" || c.Token != tmuxfix.Token || c.InstanceID != agent ||
				c.StoreID != tmuxfix.StoreID || c.Envs["K"] != "V" {
				t.Errorf("recorded create = %+v", c)
			}
		})
	}
}

// TestRecorder_NewSessionStoredNames: a create lists the catalogue's stored
// name; a $ or \ name is unlabelled until SetLabel labels it by id. Both
// label with, and record, the caller's store id.
func TestRecorder_NewSessionStoredNames(t *testing.T) {
	want := tmuxfix.Valid(tmuxfix.Token, agent, tmuxfix.OtherStoreID)
	for _, n := range tmuxfix.StoredNames() {
		t.Run(n.Source+"/"+n.Stored, func(t *testing.T) {
			r := tmuxfix.NewRecorder()
			reply, err := r.NewSession(sockA, n.Raw, "/w", nil, nil, tmuxfix.Token, agent, tmuxfix.OtherStoreID)
			if err != nil {
				t.Fatal(err)
			}
			got := findByName(t, r, n.Stored)
			if labelled := got.Label == want; labelled == n.LabelByID || got.LabelSet != labelled {
				t.Fatalf("label = %+v (set %v); label-by-id name %v", got.Label, got.LabelSet, n.LabelByID)
			}
			if n.LabelByID {
				if err := r.SetLabel(sockA, reply.SessionID, reply.PaneID, tmuxfix.Token, agent, tmuxfix.OtherStoreID); err != nil {
					t.Fatal(err)
				}
				if a := lookup(t, r, sockA); a.Sessions[0].Label != want {
					t.Errorf("after SetLabel label = %+v", a.Sessions[0].Label)
				}
			}
			for _, c := range append(r.SocketCallsOf(tmux.CallCreate), r.SocketCallsOf(tmux.CallSetLabel)...) {
				if c.StoreID != tmuxfix.OtherStoreID {
					t.Errorf("recorded %s call StoreID = %q, want %q", c.Call, c.StoreID, tmuxfix.OtherStoreID)
				}
			}
		})
	}
}

// findByName returns sockA's session with stored name, failing the test when absent.
func findByName(t *testing.T, r *tmuxfix.Recorder, name string) tmuxfix.SeedSession {
	t.Helper()
	for _, s := range r.Sessions(sockA) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no session named %q in %+v", name, r.Sessions(sockA))
	return tmuxfix.SeedSession{}
}

// TestRecorder_ScriptedFailures: every failure kind on every call kind is a
// *tmux.CallError with that Failure, Call and documented fields, and changes
// no table (a create's FailLabel adds its session).
func TestRecorder_ScriptedFailures(t *testing.T) {
	const firstLine = "scripted first line"
	for _, inv := range invokers {
		for _, f := range failures {
			t.Run(string(inv.call)+"/"+f.String(), func(t *testing.T) {
				r := seeded().Script(sockA, tmuxfix.Script{Failure: f, FirstLine: firstLine}, inv.call)
				ce := callErr(t, inv.do(r, sockA))
				want := tmux.CallError{Call: inv.call, Failure: f, FirstLine: firstLine, ExitStatus: 1}
				switch f {
				case tmux.FailTimeout:
					want.Timeout, want.ExitStatus = classTimeout(defaultTimeouts(), inv.call), -1
				case tmux.FailUnavailable:
					want.ExitStatus = -1
				case tmux.FailSocketDenied, tmux.FailNoServer, tmux.FailNoSocket:
					want.Socket = sockA
				case tmux.FailLabel:
					want.HadStdout = true
				case tmux.FailUnrecognized:
					want.ExitStatus = 0
				}
				if *ce != want {
					t.Errorf("CallError = %+v, want %+v", *ce, want)
				}
				wantIDs := map[string]int{"$0": 1}
				if inv.call == tmux.CallCreate && f == tmux.FailLabel {
					wantIDs["$1"] = 1
				}
				if got := paneCounts(r, sockA); !reflect.DeepEqual(got, wantIDs) {
					t.Errorf("table = %v, want %v", got, wantIDs)
				}
			})
		}
	}
}

// TestRecorder_ScriptMatching: Times, socket matching, registration order,
// Applied and Reset decide which calls a script answers.
func TestRecorder_ScriptMatching(t *testing.T) {
	timeout := tmuxfix.Script{Failure: tmux.FailTimeout}
	kill := func(r *tmuxfix.Recorder) error { return r.KillSessionID(sockA, "$0") }
	lookupOn := func(sock string) func(r *tmuxfix.Recorder) error {
		return func(r *tmuxfix.Recorder) error { _, err := r.Lookup(sock); return err }
	}
	type step struct {
		call func(r *tmuxfix.Recorder) error
		want tmux.Failure
	}
	cases := []struct {
		name    string
		arrange func(r *tmuxfix.Recorder)
		steps   []step
		wantIDs map[string]int
	}{
		{"times-1", func(r *tmuxfix.Recorder) {
			r.Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallLookup)
		},
			[]step{{lookupOn(sockA), tmux.FailTimeout}, {lookupOn(sockA), 0}}, map[string]int{"$0": 1}},
		{"any-socket", func(r *tmuxfix.Recorder) { r.Script(tmuxfix.AnySocket, timeout, tmux.CallLookup) },
			[]step{{lookupOn(sockA), tmux.FailTimeout}, {lookupOn(sockB), tmux.FailTimeout}}, map[string]int{"$0": 1}},
		{"other-socket", func(r *tmuxfix.Recorder) { r.Script(sockB, timeout, tmux.CallLookup) },
			[]step{{lookupOn(sockA), 0}, {lookupOn(sockB), tmux.FailTimeout}}, map[string]int{"$0": 1}},
		{"first-registered-wins", func(r *tmuxfix.Recorder) {
			r.Script(sockA, timeout, tmux.CallLookup).Script(sockA, tmuxfix.Script{Failure: tmux.FailNoServer}, tmux.CallLookup)
		}, []step{{lookupOn(sockA), tmux.FailTimeout}}, map[string]int{"$0": 1}},
		{"zero-failure-uses-a-turn", func(r *tmuxfix.Recorder) {
			r.Script(sockA, tmuxfix.Script{Times: 1}, tmux.CallLookup).Script(sockA, timeout, tmux.CallLookup)
		}, []step{{lookupOn(sockA), 0}, {lookupOn(sockA), tmux.FailTimeout}}, map[string]int{"$0": 1}},
		{"not-applied", func(r *tmuxfix.Recorder) { r.Script(sockA, timeout, tmux.CallKillSession) },
			[]step{{kill, tmux.FailTimeout}}, map[string]int{"$0": 1}},
		{"applied", func(r *tmuxfix.Recorder) {
			r.Script(sockA, tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true}, tmux.CallKillSession)
		}, []step{{kill, tmux.FailTimeout}}, map[string]int{}},
		{"reset-clears-scripts", func(r *tmuxfix.Recorder) { r.Script(sockA, timeout, tmux.CallKillSession).Reset() },
			[]step{{kill, 0}}, map[string]int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seeded().SeedSessions(sockB, sess("$0", "b", tmux.Label{}, false))
			tc.arrange(r)
			for i, s := range tc.steps {
				err := s.call(r)
				if s.want == 0 && err != nil {
					t.Errorf("step %d: %v, want success", i, err)
				} else if s.want != 0 && callErr(t, err).Failure != s.want {
					t.Errorf("step %d: %v, want %v", i, err, s.want)
				}
			}
			if got := paneCounts(r, sockA); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Errorf("table = %v, want %v", got, tc.wantIDs)
			}
		})
	}
}
