package tmux_test

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Raw-level parse tests of the one-call lookup (SR-3.4 with the LFR H5 and H6
// parse rule): the server identity line, session lines, scope values,
// malformed answers, label classes and stored names, each answer from the
// replay catalogue or its builders.

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

// parseNoLeak fails when ce's first line or message carries a label value or
// a catalogue store id (SR-15).
func parseNoLeak(t *testing.T, ce *tmux.CallError, values []string) {
	t.Helper()
	for _, v := range slices.Concat(values, []string{tmuxfix.StoreID, tmuxfix.OtherStoreID}) {
		if strings.Contains(ce.FirstLine, v) || strings.Contains(ce.Error(), v) {
			t.Errorf("CallError leaks label value or store id %q: FirstLine %q, Error %q", v, ce.FirstLine, ce.Error())
		}
	}
}

// parseScopeEmptyAnswer is a server with no sessions whose scope reads print a
// value: its identity line, and a scope value.
func parseScopeEmptyAnswer() tmuxfix.Entry {
	return tmuxfix.Answer("composed/zero-sessions-scope-value", "LFR H5, H6", 294, 1790549353, nil,
		tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-x", tmuxfix.StoreID))
}

// parseSessionAfterBlank is a session line after an empty line: the empty
// line starts the scope section, so the answer is malformed as
// lookup/session-after-scope is. Both lines carry five-field labels, the
// second another store's with a spaced id.
func parseSessionAfterBlank(answers []tmuxfix.Entry) tmuxfix.Entry {
	own := tmuxfix.LabelValue(tmuxfix.Token, "$0", "agent-x", tmuxfix.StoreID)
	other := tmuxfix.LabelValue(tmuxfix.OtherToken, "$1", "agent x y", tmuxfix.OtherStoreID)
	return tmuxfix.Entry{Name: "composed/session-after-blank", Source: "LFR H6",
		Stdout: tmuxfix.IdentityLine(294, 1790549353) + "\n" +
			tmuxfix.SessionLine("$0", 1790549353, 294, 1790549353, "a", own) + "\n\n" +
			tmuxfix.SessionLine("$1", 1790549353, 294, 1790549353, "b", other) + "\n",
		FirstLine:   tmuxfix.Find(answers, "lookup/session-after-scope").FirstLine,
		LabelValues: []string{own, other}}
}

// TestParseLookupRule checks every catalogue lookup answer against the LFR H5
// and H6 rule: parsed sessions and server fields, ScopeValue, or a malformed
// answer with the fixed FirstLine of the rule it breaks.
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
		{entry: find("lookup/I2-identity-shaped-scope-value"), scope: true},
		{entry: find("lookup/scope-blank-lines")},
		{entry: parseScopeEmptyAnswer(), scope: true},
		{entry: find("lookup/session-after-scope"), malformed: true},
		{entry: find("lookup/server-pid-disagrees"), malformed: true},
		{entry: find("lookup/server-start-disagrees"), malformed: true},
		{entry: find("lookup/identity-disagrees"), malformed: true},
		{entry: find("lookup/identity-missing"), malformed: true},
		{entry: find("lookup/identity-after-session"), malformed: true},
		{entry: find("lookup/identity-pid-unparseable"), malformed: true},
		{entry: find("lookup/identity-start-unparseable"), malformed: true},
		{entry: find("lookup/identity-missing-field"), malformed: true},
		{entry: find("lookup/identity-extra-field"), malformed: true},
		{entry: find("lookup/empty-output"), malformed: true},
		{entry: find("lookup/created-unparseable"), malformed: true},
		{entry: find("lookup/pid-unparseable"), malformed: true},
		{entry: find("lookup/start-unparseable"), malformed: true},
		{entry: find("lookup/missing-label-field"), malformed: true},
		{entry: parseSessionAfterBlank(answers), malformed: true},
	}
	for _, tc := range cases {
		t.Run(tc.entry.Name, func(t *testing.T) {
			ans, err := parseLookupOf(t, tc.entry)
			if tc.malformed {
				ce := parseWantFailure(t, err, tmux.CallLookup, tmux.FailUnrecognized)
				parseNoLeak(t, ce, tc.entry.LabelValues)
				if tc.entry.FirstLine == "" || ce.FirstLine != tc.entry.FirstLine {
					t.Errorf("FirstLine = %q, want %q", ce.FirstLine, tc.entry.FirstLine)
				}
				if !reflect.DeepEqual(ans, tmux.LookupAnswer{}) {
					t.Errorf("malformed answer returned data: %+v", ans)
				}
				if want := tc.entry.Stdout != ""; ce.HadStdout != want {
					t.Errorf("HadStdout = %v, want %v", ce.HadStdout, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if ans.ScopeValue != tc.scope {
				t.Errorf("ScopeValue = %v, want %v", ans.ScopeValue, tc.scope)
			}
			// b.47f: the identity line names the server with or without session lines.
			if ans.ServerPID == 0 || ans.ServerStart == 0 {
				t.Errorf("server = %d/%d, want the identity line's", ans.ServerPID, ans.ServerStart)
			}
			if !reflect.DeepEqual(ans, tc.entry.Lookup) {
				t.Errorf("answer = %+v\nwant     %+v", ans, tc.entry.Lookup)
			}
		})
	}
}

// parseExtraShapes are parse-rule edge cases built with the catalogue's
// builders: spaced ids, hex-word ids, control characters beside a space, and
// store ids that are not one 16-lowercase-hex last field.
func parseExtraShapes() []tmuxfix.LabelShape {
	value := func(id, store string) string {
		return tmuxfix.LabelValue(tmuxfix.Token, tmuxfix.LabelLineID, id, store)
	}
	none := func(name, id, store string) tmuxfix.LabelShape {
		return tmuxfix.LabelShape{Name: "extra/" + name, Source: "SR-3.4 (test-composed)", Value: value(id, store)}
	}
	valid := func(name, id, store string) tmuxfix.LabelShape {
		s := none(name, id, store)
		s.Want = tmuxfix.Valid(tmuxfix.Token, id, store)
		return s
	}
	return []tmuxfix.LabelShape{
		valid("id-double-space", "agent  x", tmuxfix.StoreID),
		valid("id-leading-space", " agent", tmuxfix.StoreID),
		valid("id-trailing-space", "agent ", tmuxfix.StoreID),
		valid("id-is-hex-word", tmuxfix.OtherStoreID, tmuxfix.StoreID),
		valid("id-two-hex-words", tmuxfix.OtherStoreID+" "+tmuxfix.OtherToken, tmuxfix.StoreID),
		valid("id-spaces-hash-other-store", "agent x#y", tmuxfix.OtherStoreID),
		none("id-space-then-nul", "agent \x00", tmuxfix.StoreID),
		none("id-esc-before-space", "agent\x1b x", tmuxfix.StoreID),
		none("id-del-last", "agent x\x7f", tmuxfix.StoreID),
		none("four-fields-spaced-id", "agent x", "y"),
		none("store-id-non-hex", "agent-x", tmuxfix.StoreID[:15]+"g"),
		none("store-id-control", "agent-x", tmuxfix.StoreID[:15]+"\x01"),
		none("store-id-inner-space", "agent-x", tmuxfix.StoreID[:8]+" "+tmuxfix.StoreID[8:]),
		none("store-id-trailing-space", "agent-x", tmuxfix.StoreID+" "),
	}
}

// TestParseLabelShapes classifies each catalogue shape and parseExtraShapes
// at the raw level; LabelNone carries no field.
func TestParseLabelShapes(t *testing.T) {
	for _, s := range slices.Concat(tmuxfix.LabelShapes(), parseExtraShapes()) {
		t.Run(s.Name, func(t *testing.T) {
			got, scope := parseShapeLabel(t, s)
			if got != s.Want {
				t.Errorf("label = %+v, want %+v", got, s.Want)
			}
			if got.Kind == tmux.LabelNone && got != (tmux.Label{}) {
				t.Errorf("LabelNone carries parts: %+v", got)
			}
			if scope != s.ScopeValue {
				t.Errorf("ScopeValue = %v, want %v", scope, s.ScopeValue)
			}
		})
	}
}

// parseShapeLabel looks up s's one-session answer and returns its label and
// ScopeValue.
func parseShapeLabel(t *testing.T, s tmuxfix.LabelShape) (tmux.Label, bool) {
	t.Helper()
	ans, err := parseLookupOf(t, s.Entry())
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(ans.Sessions) != 1 || ans.Sessions[0].ID != tmuxfix.LabelLineID {
		t.Fatalf("sessions = %+v, want one on %s", ans.Sessions, tmuxfix.LabelLineID)
	}
	return ans.Sessions[0].Label, ans.ScopeValue
}

// TestParseLabelFields pins the five-field split on named catalogue shapes,
// independent of their Want: the id runs to the last space, byte for byte,
// and the store id is the last field.
func TestParseLabelFields(t *testing.T) {
	valid := func(id, store string) tmux.Label {
		return tmux.Label{Kind: tmux.LabelValid, Token: tmuxfix.Token, InstanceID: id, StoreID: store}
	}
	want := map[string]tmux.Label{
		"valid-spaces":              valid("agent x y", tmuxfix.StoreID),
		"valid-id-ends-in-hex-word": valid("agent 0123456789abcdef", tmuxfix.StoreID),
		"valid-other-store":         valid("agent-x", tmuxfix.OtherStoreID),
		"four-fields":               {},
		"store-id-empty":            {},
	}
	for _, s := range tmuxfix.LabelShapes() {
		w, ok := want[s.Name]
		if !ok {
			continue
		}
		delete(want, s.Name)
		t.Run(s.Name, func(t *testing.T) {
			if got, _ := parseShapeLabel(t, s); got != w {
				t.Errorf("label = %+v, want %+v", got, w)
			}
		})
	}
	for name := range want {
		t.Errorf("catalogue has no label shape %q", name)
	}
}

// TestParseLabelBorrowedValue checks F9b: a value embedding $0 is valid on
// $0's line and LabelNone on $1's line, which borrows it.
func TestParseLabelBorrowedValue(t *testing.T) {
	ans, err := parseLookupOf(t, tmuxfix.Find(tmuxfix.LookupAnswers(), "lookup/F9b"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := []tmux.Label{tmuxfix.Valid(tmuxfix.Token, "agent-x", tmuxfix.StoreID), {}}
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
				Label: tmuxfix.LabelValue(tmuxfix.Token, "$0", f.Exact, tmuxfix.StoreID),
				Want:  tmuxfix.Valid(tmuxfix.Token, f.Exact, tmuxfix.StoreID),
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
