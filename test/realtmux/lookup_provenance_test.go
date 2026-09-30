package realtmux_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Paths that must never make a session Ours, on real tmux 3.3a (SRD SR-3.4,
// SR-3.12, SR-3.13, SR-20.7; provenance-fresh F2, F9b; LFR M1; AC-LKP-04).
// Label values are built with tmuxfix.LabelValue and never printed.

// ownLabel is the label sessionID would carry for row in this store.
func ownLabel(row tmux.Launch, sessionID string) string {
	return tmuxfix.LabelValue(row.Token, sessionID, row.InstanceID, tmuxfix.StoreID)
}

// scopeConflict is the result of a lookup on the recorded server while a
// scope value is set: provenance_conflict with scope_value, never Ours.
var scopeConflict = wantResult{
	Verdict: tmux.CantTell, CantTell: tmux.CantTellProvenanceConflict, Token: "provenance_conflict",
	Server: tmux.ServerMatch, Disagree: []string{tmux.ReasonScopeValue},
}

// TestLookupScopeValue: @ad_owner at -g, -s or -gw alone is provenance_conflict
// for an own-labelled session and an unlabelled one; unsetting it restores the verdict.
func TestLookupScopeValue(t *testing.T) {
	shapes := []struct {
		name string
		// setup returns the row and the session its scope value names.
		setup func(t *testing.T, f *lookupFix) (tmux.Launch, string)
		// unset is the verdict once the scope value is removed.
		unset func(sessionID string) wantResult
	}{
		{
			name: "session carries its own label",
			setup: func(t *testing.T, f *lookupFix) (tmux.Launch, string) {
				a := f.agent(t, createSpec{})
				return a.row(), a.Reply.SessionID
			},
			unset: func(id string) wantResult {
				return wantResult{Verdict: tmux.Ours, Token: "ours", Session: id, Server: tmux.ServerMatch}
			},
		},
		{
			name: "unlabelled session borrows the row's label",
			setup: func(t *testing.T, f *lookupFix) (tmux.Launch, string) {
				s := f.startSession(t, "")
				return rowFor(newInstanceID("agent"), newToken(t), f.Socket, f.serverOf(t, f.realTmux, s.ID)), s.ID
			},
			unset: func(string) wantResult {
				return wantResult{Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch}
			},
		},
	}
	for _, scope := range []string{"-g", "-s", "-gw"} {
		for _, sh := range shapes {
			t.Run(scope+"/"+sh.name, func(t *testing.T) {
				f := newLookupFix(t)
				row, sessionID := sh.setup(t, f)
				f.must(t, "set-option", scope, "@ad_owner", ownLabel(row, sessionID))
				assertResult(t, "scope value set", f.lookup(row, ""), scopeConflict)

				f.must(t, "set-option", scope+"u", "@ad_owner")
				assertResult(t, "scope value unset", f.lookup(row, ""), sh.unset(sessionID))
			})
		}
	}
}

// newlineTail is the extra line of the raw-newline label: not session-shaped.
const newlineTail = "not a session line"

// TestLookupNewlineLabel: a session label with a raw newline is a scope value when
// it lists last (agent "a-", it "z-") and malformed when it lists first (agent "z-", it "a-").
func TestLookupNewlineLabel(t *testing.T) {
	cases := []struct {
		name                     string
		agentPrefix, otherPrefix string
		want                     wantResult
	}{
		{
			name: "newline session lists last", agentPrefix: "a-", otherPrefix: "z-",
			want: scopeConflict,
		},
		{
			name: "newline session lists first", agentPrefix: "z-", otherPrefix: "a-",
			want: wantResult{Verdict: tmux.CantTell, CantTell: tmux.CantTellUnreadable, Token: "cant_tell"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFix(t)
			a := f.agent(t, createSpec{Name: tc.agentPrefix + uniqueName()})
			other := f.startSession(t, tc.otherPrefix+uniqueName())
			foreign := rowFor(newInstanceID("other"), newToken(t), f.Socket, a.Server)
			f.must(t, "set-option", "-t", other.ID, "@ad_owner", ownLabel(foreign, other.ID)+"\n"+newlineTail)

			got := f.lookup(a.row(), "")
			assertResult(t, "lookup of the agent's row", got, tc.want)
			if tc.want.CantTell != tmux.CantTellUnreadable {
				return
			}
			if got.Cause == nil || got.Cause.Failure != tmux.FailUnrecognized {
				t.Fatalf("Cause is nil or not FailUnrecognized; want a FailUnrecognized call error")
			}
			for _, part := range []string{"ad1", a.InstanceID, foreign.InstanceID, newlineTail} {
				if strings.Contains(got.Cause.FirstLine+got.Cause.Error(), part) {
					t.Errorf("the malformed answer's call error quotes a label line")
				}
			}
		})
	}
}

// TestLookupEnvironmentNotEvidence: an unlabelled session never becomes Ours or
// Leftover because a session or global environment carries the row's id (AC-LKP-04).
func TestLookupEnvironmentNotEvidence(t *testing.T) {
	cases := []struct {
		name string
		// sessionEnv and globalEnv pick the id in the session's -e and in
		// set-environment -g: "row", "other" or "" for none.
		sessionEnv, globalEnv string
	}{
		{name: "session created with -e carrying the row's id", sessionEnv: "row"},
		{name: "global environment carries the row's id, -e another row's", sessionEnv: "other", globalEnv: "row"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLookupFix(t)
			ids := map[string]string{"row": newInstanceID("agent"), "other": newInstanceID("agent"), "": ""}
			name := uniqueName()
			s := f.startSessionWithID(t, name, ids[tc.sessionEnv])
			if tc.globalEnv != "" {
				f.must(t, "set-environment", "-g", "AGENT_DIRECTOR_INSTANCE_ID", ids[tc.globalEnv])
			}
			assertEnv(t, f, ids[tc.sessionEnv], "-t", s.ID)
			assertEnv(t, f, ids[tc.globalEnv], "-g")
			if got := f.label(t, s.ID); got != "" {
				t.Fatalf("session %s carries an @ad_owner value; want none", s.ID)
			}

			srv := f.serverOf(t, f.realTmux, s.ID)
			for _, key := range []string{"row", "other"} {
				row := rowFor(ids[key], newToken(t), f.Socket, srv)
				assertResult(t, "row with the "+key+" id", f.lookup(row, name), wantResult{
					Verdict: tmux.Gone, Token: "gone", Server: tmux.ServerMatch,
					Holder: s.ID, HolderClass: tmux.ClassNone,
				})
			}
		})
	}
}

// assertEnv fails unless show-environment with scope reports
// AGENT_DIRECTOR_INSTANCE_ID as id ("" = not set there).
func assertEnv(t *testing.T, f *lookupFix, id string, scope ...string) {
	t.Helper()
	res := f.run(t, append(append([]string{"show-environment"}, scope...), "AGENT_DIRECTOR_INSTANCE_ID")...)
	got := ""
	if res.Exit == 0 {
		got = strings.TrimPrefix(strings.TrimSuffix(res.Stdout, "\n"), "AGENT_DIRECTOR_INSTANCE_ID=")
	}
	if got != id {
		t.Fatalf("show-environment %v: AGENT_DIRECTOR_INSTANCE_ID = %q, want %q", scope, got, id)
	}
}
