package tmux_test

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Raw-level parse tests of the one-call lookup (SR-3.4 with the LFR H6 parse
// rule): session lines, scope values, malformed answers, label classes and
// stored names, each answer from the replay catalogue or its builders.

// parseLookupOf runs one Lookup answered by e's recorded bytes and exit status.
func parseLookupOf(t *testing.T, e tmuxfix.Entry) (tmux.LookupAnswer, error) {
	t.Helper()
	c, _ := newReplay(t, e)
	return c.Lookup(testSocket)
}

// parseWantFailure returns err as a *tmux.CallError of call and failure f.
func parseWantFailure(t *testing.T, err error, call tmux.Call, f tmux.Failure) *tmux.CallError {
	t.Helper()
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v (%T), want *tmux.CallError", err, err)
	}
	if ce.Call != call || ce.Failure != f {
		t.Fatalf("CallError = {Call %q, Failure %v}, want {%q, %v}", ce.Call, ce.Failure, call, f)
	}
	return ce
}

// parseNoLeak fails when ce's first line or message carries a label value.
func parseNoLeak(t *testing.T, ce *tmux.CallError, values []string) {
	t.Helper()
	for _, v := range values {
		if strings.Contains(ce.FirstLine, v) || strings.Contains(ce.Error(), v) {
			t.Errorf("CallError leaks label value %q: FirstLine %q, Error %q", v, ce.FirstLine, ce.Error())
		}
	}
}

// parseScopeEmptyAnswer is a server with no sessions whose scope reads print a
// value: no server identity, and a scope value.
func parseScopeEmptyAnswer() tmuxfix.Entry {
	return tmuxfix.Answer("composed/zero-sessions-scope-value", "LFR H5, H6", 294, 1790549353, nil,
		tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-x"))
}

// parseSessionAfterBlank is a session line after an empty line: the empty
// line starts the scope section, so the answer is malformed.
func parseSessionAfterBlank() tmuxfix.Entry {
	own := tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-x")
	return tmuxfix.Entry{Name: "composed/session-after-blank", Source: "LFR H6",
		Stdout: tmuxfix.SessionLine("$0", 1790549353, 294, 1790549353, "a", own) + "\n\n" +
			tmuxfix.SessionLine("$1", 1790549353, 294, 1790549353, "b", "") + "\n",
		LabelValues: []string{own}}
}

// TestParseLookupRule checks every catalogue lookup answer against the LFR H6
// rule: parsed sessions and server fields, ScopeValue, or a malformed answer.
func TestParseLookupRule(t *testing.T) {
	answers := tmuxfix.LookupAnswers()
	find := func(name string) tmuxfix.Entry { return tmuxfix.Find(answers, name) }
	cases := []struct {
		entry     tmuxfix.Entry
		scope     bool
		malformed bool
	}{
		{entry: find("lookup/F9a")},
		{entry: find("lookup/F9b"), scope: true},
		{entry: find("lookup/F1")},
		{entry: find("lookup/F2-global"), scope: true},
		{entry: find("lookup/F2-server-or-global-window"), scope: true},
		{entry: find("lookup/F2-window-or-pane")},
		{entry: find("lookup/I2-zero-sessions")},
		{entry: find("lookup/scope-blank-lines")},
		{entry: parseScopeEmptyAnswer(), scope: true},
		{entry: find("lookup/session-after-scope"), malformed: true},
		{entry: find("lookup/server-pid-disagrees"), malformed: true},
		{entry: find("lookup/server-start-disagrees"), malformed: true},
		{entry: find("lookup/created-unparseable"), malformed: true},
		{entry: find("lookup/pid-unparseable"), malformed: true},
		{entry: find("lookup/start-unparseable"), malformed: true},
		{entry: find("lookup/missing-label-field"), malformed: true},
		{entry: parseSessionAfterBlank(), malformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.entry.Name, func(t *testing.T) {
			ans, err := parseLookupOf(t, tc.entry)
			if tc.malformed {
				ce := parseWantFailure(t, err, tmux.CallLookup, tmux.FailUnrecognized)
				parseNoLeak(t, ce, tc.entry.LabelValues)
				if !reflect.DeepEqual(ans, tmux.LookupAnswer{}) {
					t.Errorf("malformed answer returned data: %+v", ans)
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if ans.ScopeValue != tc.scope {
				t.Errorf("ScopeValue = %v, want %v", ans.ScopeValue, tc.scope)
			}
			if len(ans.Sessions) == 0 && (ans.ServerPID != 0 || ans.ServerStart != 0) {
				t.Errorf("no session lines but server = %d/%d, want zero", ans.ServerPID, ans.ServerStart)
			}
			if !reflect.DeepEqual(ans, tc.entry.Lookup) {
				t.Errorf("answer = %+v\nwant     %+v", ans, tc.entry.Lookup)
			}
		})
	}
}

// TestParseLabelShapes classifies each AC-LKP-05 shape, control characters in
// the id and valid labels at the raw level; the id-with-tab shape shows the
// value is everything after the fifth tab.
func TestParseLabelShapes(t *testing.T) {
	for _, s := range tmuxfix.LabelShapes() {
		t.Run(s.Name, func(t *testing.T) {
			ans, err := parseLookupOf(t, s.Entry())
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if len(ans.Sessions) != 1 || ans.Sessions[0].ID != tmuxfix.LabelLineID {
				t.Fatalf("sessions = %+v, want one on %s", ans.Sessions, tmuxfix.LabelLineID)
			}
			got := ans.Sessions[0].Label
			if got != s.Want {
				t.Errorf("label = %+v, want %+v", got, s.Want)
			}
			if got.Kind == tmux.LabelNone && (got.Token != "" || got.InstanceID != "") {
				t.Errorf("LabelNone carries parts: %+v", got)
			}
			if ans.ScopeValue != s.ScopeValue {
				t.Errorf("ScopeValue = %v, want %v", ans.ScopeValue, s.ScopeValue)
			}
		})
	}
}

// TestParseLabelBorrowedValue checks F9b: a value embedding $0 is valid on
// $0's line and LabelNone on $1's line, which borrows it.
func TestParseLabelBorrowedValue(t *testing.T) {
	ans, err := parseLookupOf(t, tmuxfix.Find(tmuxfix.LookupAnswers(), "lookup/F9b"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := []tmux.Label{tmuxfix.Valid(tmuxfix.Token, "agent-x"), {}}
	if len(ans.Sessions) != len(want) {
		t.Fatalf("sessions = %+v, want %d", ans.Sessions, len(want))
	}
	for i, s := range ans.Sessions {
		if s.Label != want[i] {
			t.Errorf("%s label = %+v, want %+v", s.ID, s.Label, want[i])
		}
	}
}

// TestParseStoredNames checks every catalogue stored name form is returned
// byte-exact in Session.Name.
func TestParseStoredNames(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		t.Run(n.Source+"/"+strconv.Quote(n.Raw), func(t *testing.T) {
			e := n.Entry()
			ans, err := parseLookupOf(t, e)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if len(ans.Sessions) != 1 || ans.Sessions[0].Name != n.Stored {
				t.Fatalf("sessions = %+v, want one named %q", ans.Sessions, n.Stored)
			}
			if !reflect.DeepEqual(ans, e.Lookup) {
				t.Errorf("answer = %+v, want %+v", ans, e.Lookup)
			}
		})
	}
}

// TestParseLocaleForms checks the U2/U3/U4 exact forms come back byte-exact,
// as a session name and as a label's instance id.
func TestParseLocaleForms(t *testing.T) {
	for _, f := range tmuxfix.LocaleForms() {
		t.Run(f.Source+"/"+strconv.Quote(f.Exact), func(t *testing.T) {
			e := tmuxfix.Answer("composed/locale", f.Source, 294, 1790549353, []tmuxfix.Listed{{
				ID: "$0", Created: 1790549353, Name: f.Exact,
				Label: tmuxfix.LabelValue(tmuxfix.Token, "$0", f.Exact),
				Want:  tmuxfix.Valid(tmuxfix.Token, f.Exact),
			}})
			ans, err := parseLookupOf(t, e)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if len(ans.Sessions) != 1 {
				t.Fatalf("sessions = %+v, want one", ans.Sessions)
			}
			if s := ans.Sessions[0]; s.Name != f.Exact || s.Label.InstanceID != f.Exact {
				t.Errorf("name %q, instance id %q, want both %q", s.Name, s.Label.InstanceID, f.Exact)
			}
		})
	}
}
