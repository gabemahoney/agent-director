package realtmux_test

import (
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The per-pane label @ad_pane on real tmux (SRD SR-20.7, SR-2.1, SR-3.5;
// WD 2026-09-29c). The tests' own failure messages name pane and session ids
// only, but a failing assertTriple prints the got and want stdout and
// stderr through the shared harness redact, which hides only `ad1 …`
// values, so @ad_pane values can appear in its output.

const paneLabelOption = "@ad_pane"

// ownPaneLabels reads every pane's own @ad_pane (no inherited value), by
// pane id; "" when unset.
func (r *realTmux) ownPaneLabels(t testing.TB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range strings.Fields(r.must(t, "list-panes", "-a", "-F", "#{pane_id}")) {
		out[id] = strings.TrimSuffix(r.must(t, "show-options", "-p", "-qv", "-t", id, paneLabelOption), "\n")
	}
	return out
}

// listedPaneLabels reads every pane's #{@ad_pane} as list-panes -a formats
// it (scope values included), by pane id.
func (r *realTmux) listedPaneLabels(t testing.TB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(r.must(t, "list-panes", "-a", "-F", "#{pane_id}\t#{@ad_pane}"), "\n"), "\n") {
		id, v, _ := strings.Cut(line, "\t")
		out[id] = v
	}
	return out
}

// assertPaneLabels checks got holds exactly want's panes with want's values.
func assertPaneLabels(t testing.TB, what string, got, want map[string]string) {
	t.Helper()
	for id, w := range want {
		if g, ok := got[id]; !ok || g != w {
			t.Errorf("pane %s: %s @ad_pane is not the expected value (present %v, set %v, want set %v)", id, what, ok, g != "", w != "")
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected pane %s", id)
		}
	}
}

// wantListing builds the listing entry the production ListPanes must give on
// the live server: each pane's fields as tmux reports them, the raw value
// values[id] and the token tokens[id] ("" when absent).
func (r *realTmux) wantListing(t testing.TB, name string, values, tokens map[string]string) tmuxfix.Entry {
	t.Helper()
	out := r.must(t, "list-panes", "-a", "-F", "#{session_id}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_pid}")
	var lines []tmuxfix.PaneListing
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			t.Fatalf("pane line has %d fields, want 5: %q", len(f), line)
		}
		w, err1 := strconv.Atoi(f[1])
		i, err2 := strconv.Atoi(f[2])
		pid, err3 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil || err3 != nil {
			t.Fatalf("pane line numbers: %q", line)
		}
		p := tmux.Pane{SessionID: f[0], Window: w, Index: i, ID: f[3], PID: pid, AdPane: tokens[f[3]]}
		lines = append(lines, tmuxfix.PaneListing{Pane: p, Value: values[f[3]]})
	}
	return tmuxfix.Listing(name, "live tmux", lines...)
}

// assertPaneListing runs the production ListPanes and checks its outcome,
// streams and answer against want.
func (r *realTmux) assertPaneListing(t testing.TB, want tmuxfix.Entry) {
	t.Helper()
	cl, log := newRecordingClient(t)
	got, err := cl.ListPanes(r.Socket)
	assertCallError(t, err, tmux.CallListPanes, want)
	assertTriple(t, log.last(t).rawResult, want)
	if len(got) != len(want.Panes) {
		t.Fatalf("pane listing has %d panes, want %d", len(got), len(want.Panes))
	}
	for i, p := range got {
		w := want.Panes[i]
		if p != w {
			t.Errorf("pane line %d: got %s %s %d.%d pid %d token set %v, want %s %s %d.%d pid %d token set %v (tokens equal %v)",
				i, p.SessionID, p.ID, p.Window, p.Index, p.PID, p.AdPane != "",
				w.SessionID, w.ID, w.Window, w.Index, w.PID, w.AdPane != "", p.AdPane == w.AdPane)
		}
	}
}

// paneShape is a listing line's form: its indices, whether its @ad_pane is
// unset, names the line's own pane or another, and whether a token is found.
type paneShape struct {
	Window, Index int
	Value         string
	Found         bool
}

// listingShape returns an entry's line forms.
func listingShape(t testing.TB, e tmuxfix.Entry) []paneShape {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(e.Stdout, "\n"), "\n")
	if len(lines) != len(e.Panes) {
		t.Fatalf("%s: %d lines for %d panes", e.Name, len(lines), len(e.Panes))
	}
	out := make([]paneShape, len(lines))
	for i, line := range lines {
		f := strings.SplitN(line, "\t", 6)
		if len(f) != 6 {
			t.Fatalf("%s: line %d has %d fields, want 6", e.Name, i, len(f))
		}
		kind := "other pane"
		switch {
		case f[5] == "":
			kind = "unset"
		case strings.HasSuffix(f[5], " "+f[3]):
			kind = "own pane"
		}
		p := e.Panes[i]
		out[i] = paneShape{Window: p.Window, Index: p.Index, Value: kind, Found: p.AdPane != ""}
	}
	return out
}

// assertCatalogueShape checks a live listing has, line by line, the form of
// the catalogue entry of the same name.
func assertCatalogueShape(t testing.TB, live tmuxfix.Entry) {
	t.Helper()
	cat := tmuxfix.Find(tmuxfix.PaneListings(), live.Name)
	if got, want := listingShape(t, live), listingShape(t, cat); !reflect.DeepEqual(got, want) {
		t.Errorf("live listing is %+v, catalogue %s is %+v", got, cat.Name, want)
	}
}

// unlabelledCreate runs a production create with a $-bearing name, which
// gets no chain, and checks neither label is set.
func (r *realTmux) unlabelledCreate(t testing.TB) created {
	t.Helper()
	c := r.mustCreate(t, createSpec{Name: uniqueName() + "$p"})
	if r.label(t, c.Reply.SessionID) != "" || r.ownPaneLabels(t)[c.Reply.PaneID] != "" {
		t.Fatalf("session %s: a label-by-id name was labelled by the create", c.Reply.SessionID)
	}
	return c
}

// TestPaneLabelCreate: the chained create sets @ad_pane '<token> <own %N>' on
// its new pane only, detached or from inside another labelled pane.
func TestPaneLabelCreate(t *testing.T) {
	for _, inside := range []bool{false, true} {
		where := "detached"
		if inside {
			where = "inside-pane"
		}
		t.Run(where, func(t *testing.T) {
			rt := newRealTmux(t)
			holder := rt.mustCreate(t, createSpec{})
			rt.startSession(t, "") // an unlabelled bystander
			if inside {
				rt.enterPane(t, holder)
			}
			before := rt.ownPaneLabels(t)
			if before[holder.Reply.PaneID] != tmuxfix.PaneLabelValue(holder.Token, holder.Reply.PaneID) {
				t.Fatalf("holder pane %s does not carry its pane label before the create", holder.Reply.PaneID)
			}

			c := rt.mustCreate(t, createSpec{})
			if _, clash := before[c.Reply.PaneID]; clash {
				t.Fatalf("create reply names existing pane %s", c.Reply.PaneID)
			}
			want := maps.Clone(before)
			want[c.Reply.PaneID] = tmuxfix.PaneLabelValue(c.Token, c.Reply.PaneID)
			assertPaneLabels(t, "own", rt.ownPaneLabels(t), want)
			assertPaneLabels(t, "listed", rt.listedPaneLabels(t), want)
			rt.assertPaneListing(t, rt.wantListing(t, "live/create", want,
				map[string]string{holder.Reply.PaneID: holder.Token, c.Reply.PaneID: c.Token}))
		})
	}
}

// TestPaneLabelSplitPaneReadsEmpty: a pane split from the created pane has no
// @ad_pane, own or listed, and no token (catalogue panes/split-pane-unlabelled).
func TestPaneLabelSplitPaneReadsEmpty(t *testing.T) {
	rt := newRealTmux(t)
	c := rt.mustCreate(t, createSpec{})
	split, _ := idtSplit(t, rt, c.Reply.SessionID)

	want := map[string]string{c.Reply.PaneID: tmuxfix.PaneLabelValue(c.Token, c.Reply.PaneID), split: ""}
	assertPaneLabels(t, "own", rt.ownPaneLabels(t), want)
	assertPaneLabels(t, "listed", rt.listedPaneLabels(t), want)
	live := rt.wantListing(t, "panes/split-pane-unlabelled", want, map[string]string{c.Reply.PaneID: c.Token})
	rt.assertPaneListing(t, live)
	assertCatalogueShape(t, live)
}

// TestPaneLabelByID: SetLabel sets @ad_owner and, on the pane named by id
// (not the session's active pane), @ad_pane, in one silent invocation.
func TestPaneLabelByID(t *testing.T) {
	rt := newRealTmux(t)
	holder := rt.mustCreate(t, createSpec{})
	c := rt.unlabelledCreate(t)
	id, pane := c.Reply.SessionID, c.Reply.PaneID
	split, _ := idtSplit(t, rt, id)
	rt.must(t, "select-pane", "-t", split)
	if rt.format(t, split, "#{pane_active}") != "1" {
		t.Fatalf("split pane %s is not the active pane of %s", split, id)
	}
	before := rt.ownPaneLabels(t)

	cl, log := newRecordingClient(t)
	err := cl.SetLabel(rt.Socket, id, pane, c.Token, c.InstanceID, c.StoreID)
	calls := log.calls(t)
	if len(calls) != 1 {
		t.Fatalf("SetLabel made %d tmux calls, want 1", len(calls))
	}
	silent := tmuxfix.Silent()
	assertCallError(t, err, tmux.CallSetLabel, silent)
	assertTriple(t, calls[0].rawResult, silent)

	want := maps.Clone(before)
	want[pane] = tmuxfix.PaneLabelValue(c.Token, pane)
	assertPaneLabels(t, "own", rt.ownPaneLabels(t), want)
	assertPaneLabels(t, "listed", rt.listedPaneLabels(t), want)
	if rt.label(t, id) != tmuxfix.LabelValue(c.Token, id, c.InstanceID, c.StoreID) {
		t.Errorf("session %s does not carry the label set by id", id)
	}
	rt.assertPaneListing(t, rt.wantListing(t, "live/label-by-id", want,
		map[string]string{holder.Reply.PaneID: holder.Token, pane: c.Token}))
}

// TestPaneLabelByIDFailures pins tmux's step order: an unknown pane id fails
// after the session is relabelled; an unknown session id fails before the
// pane step runs.
func TestPaneLabelByIDFailures(t *testing.T) {
	cases := []struct {
		name        string
		unknownPane bool
		entry       string
	}{
		{"unknown pane id", true, "reply/no-such-pane"},
		{"unknown session id", false, "reply/no-such-session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := newRealTmux(t)
			c := rt.unlabelledCreate(t)
			want := tmuxfix.Find(tmuxfix.Replies(rt.Socket), tc.entry)
			words := strings.Fields(want.FirstLine)
			unknown := words[len(words)-1] // the id the catalogue wording names
			before := rt.ownPaneLabels(t)
			sessionID, paneID := c.Reply.SessionID, c.Reply.PaneID
			if tc.unknownPane {
				paneID = unknown
				if _, ok := before[unknown]; ok {
					t.Fatalf("pane %s exists on the server", unknown)
				}
			} else {
				sessionID = unknown
				if rt.run(t, "has-session", "-t", unknown).Exit == 0 {
					t.Fatalf("session %s exists on the server", unknown)
				}
			}

			cl, log := newRecordingClient(t)
			err := cl.SetLabel(rt.Socket, sessionID, paneID, c.Token, c.InstanceID, c.StoreID)
			calls := log.calls(t)
			if len(calls) != 1 {
				t.Fatalf("SetLabel made %d tmux calls, want 1", len(calls))
			}
			assertCallError(t, err, tmux.CallSetLabel, want)
			assertTriple(t, calls[0].rawResult, want)

			assertPaneLabels(t, "own", rt.ownPaneLabels(t), before)
			wantLabel := ""
			if tc.unknownPane {
				wantLabel = tmuxfix.LabelValue(c.Token, c.Reply.SessionID, c.InstanceID, c.StoreID)
			}
			if got := rt.label(t, c.Reply.SessionID); got != wantLabel {
				t.Errorf("session %s: label set %v, want set %v (as the value set by id: %v)",
					c.Reply.SessionID, got != "", wantLabel != "", got == wantLabel)
			}
		})
	}
}

// TestPaneLabelScopeValuesNeverCount: a window, session, global or
// global-window @ad_pane naming the created pane is listed on the panes
// without their own value, and none of them gets a token.
func TestPaneLabelScopeValuesNeverCount(t *testing.T) {
	scopes := []struct {
		name string
		// flags are set-option's scope flags and target for the created c.
		flags func(c created) []string
		// everySession: the value reaches other sessions' panes too.
		everySession bool
	}{
		{"window", func(c created) []string { return []string{"-w", "-t", c.Reply.PaneID} }, false},
		{"session", func(c created) []string { return []string{"-t", c.Reply.SessionID} }, false},
		{"global", func(created) []string { return []string{"-g"} }, true},
		{"global-window", func(created) []string { return []string{"-gw"} }, true},
	}
	for _, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			rt := newRealTmux(t)
			c := rt.mustCreate(t, createSpec{})
			split, _ := idtSplit(t, rt, c.Reply.SessionID)
			other := rt.startSession(t, "")
			v := tmuxfix.PaneLabelValue(c.Token, c.Reply.PaneID)
			rt.must(t, append(append([]string{"set-option"}, sc.flags(c)...), paneLabelOption, v)...)

			own := map[string]string{c.Reply.PaneID: v, split: "", other.PaneID: ""}
			listed := map[string]string{c.Reply.PaneID: v, split: v, other.PaneID: ""}
			if sc.everySession {
				listed[other.PaneID] = v
			}
			assertPaneLabels(t, "own", rt.ownPaneLabels(t), own)
			assertPaneLabels(t, "listed", rt.listedPaneLabels(t), listed)
			rt.assertPaneListing(t, rt.wantListing(t, "live/scope-"+sc.name, listed,
				map[string]string{c.Reply.PaneID: c.Token}))
		})
	}
}

// TestPaneLabelServerValueHidesOwnValues pins tmux 3.3a: a server-scope
// @ad_pane is listed on every pane in place of its own value, so only a pane
// it names is found (none when it names none); unsetting it restores them.
func TestPaneLabelServerValueHidesOwnValues(t *testing.T) {
	for _, namesFirst := range []bool{true, false} {
		name := "names no pane"
		if namesFirst {
			name = "names the first pane"
		}
		t.Run(name, func(t *testing.T) {
			rt := newRealTmux(t)
			// Names sort a before b, so the listing order is the catalogue's.
			a := rt.mustCreate(t, createSpec{Name: "a-" + uniqueName()})
			b := rt.mustCreate(t, createSpec{Name: "b-" + uniqueName()})
			split, _ := idtSplit(t, rt, a.Reply.SessionID)
			own := map[string]string{
				a.Reply.PaneID: tmuxfix.PaneLabelValue(a.Token, a.Reply.PaneID),
				b.Reply.PaneID: tmuxfix.PaneLabelValue(b.Token, b.Reply.PaneID),
				split:          "",
			}
			v, found := tmuxfix.PaneLabelValue(newToken(t), "%99"), map[string]string{}
			if namesFirst {
				v, found = own[a.Reply.PaneID], map[string]string{a.Reply.PaneID: a.Token}
			} else if _, ok := own["%99"]; ok {
				t.Fatalf("pane %%99 exists on the server")
			}
			rt.must(t, "set-option", "-s", paneLabelOption, v)

			assertPaneLabels(t, "own", rt.ownPaneLabels(t), own)
			listed := map[string]string{a.Reply.PaneID: v, b.Reply.PaneID: v, split: v}
			assertPaneLabels(t, "listed", rt.listedPaneLabels(t), listed)
			live := rt.wantListing(t, "panes/server-value-borrowed", listed, found)
			rt.assertPaneListing(t, live)
			if namesFirst {
				assertCatalogueShape(t, live)
			}

			rt.must(t, "set-option", "-su", paneLabelOption)
			assertPaneLabels(t, "listed after unset", rt.listedPaneLabels(t), own)
			rt.assertPaneListing(t, rt.wantListing(t, "live/server-value-unset", own,
				map[string]string{a.Reply.PaneID: a.Token, b.Reply.PaneID: b.Token}))
		})
	}
}

// TestPaneLabelBaseIndexOne: with base-index and pane-base-index 1 the
// created pane is window 1, pane 1 and still found by its token (catalogue
// panes/P2-base-index-1).
func TestPaneLabelBaseIndexOne(t *testing.T) {
	rt := newRealTmux(t)
	a := rt.mustCreate(t, createSpec{Name: "a-" + uniqueName()})
	rt.must(t, "set-option", "-g", "base-index", "1")
	rt.must(t, "set-option", "-gw", "pane-base-index", "1")
	b := rt.mustCreate(t, createSpec{Name: "b-" + uniqueName()})
	if w, i := rt.formatInt(t, b.Reply.PaneID, "#{window_index}"), rt.formatInt(t, b.Reply.PaneID, "#{pane_index}"); w != 1 || i != 1 {
		t.Fatalf("created pane %s is window %d pane %d, want 1.1", b.Reply.PaneID, w, i)
	}

	values := map[string]string{
		a.Reply.PaneID: tmuxfix.PaneLabelValue(a.Token, a.Reply.PaneID),
		b.Reply.PaneID: tmuxfix.PaneLabelValue(b.Token, b.Reply.PaneID),
	}
	assertPaneLabels(t, "own", rt.ownPaneLabels(t), values)
	live := rt.wantListing(t, "panes/P2-base-index-1", values,
		map[string]string{a.Reply.PaneID: a.Token, b.Reply.PaneID: b.Token})
	rt.assertPaneListing(t, live)
	assertCatalogueShape(t, live)
}
