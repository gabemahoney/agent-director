package tmux_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Raw-level parse tests of the pane listing, the create reply and the capture
// (SR-2.1, SR-2.3, SR-2.4), and of data read only from an exit-0 call.

// parsePaneEntries returns the catalogue listings, one per pane label shape,
// and a silent reply.
func parsePaneEntries() []tmuxfix.Entry {
	entries := append(tmuxfix.PaneListings(), tmuxfix.Silent())
	for _, s := range tmuxfix.PaneLabelShapes() {
		entries = append(entries, s.Entry())
	}
	return entries
}

// TestParsePaneListings parses each catalogue listing into Panes, AdPane the
// token only for a value naming the line's own pane, or fails an unparseable
// one as FailUnrecognized carrying its first line cut before the label.
func TestParsePaneListings(t *testing.T) {
	for _, e := range parsePaneEntries() {
		t.Run(e.Name, func(t *testing.T) {
			c, _ := newReplay(t, e)
			panes, err := c.ListPanes(testSocket)
			if e.Want[tmux.CallListPanes] == 0 {
				if err != nil {
					t.Fatalf("ListPanes: %v", err)
				}
				if !reflect.DeepEqual(panes, e.Panes) {
					t.Errorf("panes = %+v, want %+v", panes, e.Panes)
				}
				return
			}
			ce := parseWantFailure(t, err, tmux.CallListPanes, tmux.FailUnrecognized)
			if ce.FirstLine != e.FirstLine || panes != nil {
				t.Errorf("FirstLine %q, panes %+v; want %q, nil", ce.FirstLine, panes, e.FirstLine)
			}
			parseNoLeak(t, ce, e.LabelValues)
		})
	}
}

// TestParsePaneListingNeverLeaksLabel: a malformed listing's FirstLine is its
// first line cut before the fifth tab, so no pane label value, nor any part
// of one, reaches FirstLine or Error() (SR-2.3, SR-15).
func TestParsePaneListingNeverLeaksLabel(t *testing.T) {
	own := tmuxfix.PaneLabelValue(tmuxfix.Token, "%0")
	good := tmuxfix.PaneLine("$0", 0, 0, "%0", 295, own)
	cases := []struct {
		name, stdout, firstLine string
	}{
		{"pid unparseable", "$0\t0\t0\t%0\tx\t" + own + "\n", "$0\t0\t0\t%0\tx"},
		{"session id unparseable", "@0\t0\t0\t%0\t295\t" + own + "\n", "@0\t0\t0\t%0\t295"},
		{"pane id unparseable", "$0\t0\t0\t0\t295\t" + own + "\n", "$0\t0\t0\t0\t295"},
		{"label holding tabs, pid unparseable", "$0\t0\t0\t%0\t-1\t" + tmuxfix.Token + "\t%0\t" + tmuxfix.Token + "\n",
			"$0\t0\t0\t%0\t-1"},
		{"label padded with spaces, second line bad", "$0\t0\t0\t%0\t295\t  " + own + "  \n$1\n", "$0\t0\t0\t%0\t295"},
		{"label holding a newline", "$0\t0\t0\t%0\t295\t" + tmuxfix.Token + "\n%0\n", "$0\t0\t0\t%0\t295"},
		{"good line then bad line", good + "\n" + "$1\t0\t0\t%1\n", "$0\t0\t0\t%0\t295"},
		{"long first line capped", "$0\t0\t0\t%0\t" + strings.Repeat("9", 300) + "x\t" + own + "\n",
			("$0\t0\t0\t%0\t" + strings.Repeat("9", 300))[:200]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newScripted(t, exitZero(tc.stdout))
			panes, err := c.ListPanes(testSocket)
			ce := parseWantFailure(t, err, tmux.CallListPanes, tmux.FailUnrecognized)
			if ce.FirstLine != tc.firstLine || panes != nil {
				t.Errorf("FirstLine %q, panes %+v; want %q, nil", ce.FirstLine, panes, tc.firstLine)
			}
			parseNoLeak(t, ce, []string{own, tmuxfix.Token})
		})
	}
}

// TestParsePaneLabelTab: a tab inside a label value stays in the label field
// (everything after the fifth tab), so the line and every other line still
// parse; the value just does not count.
func TestParsePaneLabelTab(t *testing.T) {
	pane := func(id string, pid int, token string) tmux.Pane {
		return tmux.Pane{SessionID: "$0", Window: 0, Index: 0, ID: id, PID: pid, AdPane: token}
	}
	own := func(id string, pid int) tmuxfix.PaneListing {
		return tmuxfix.PaneListing{Pane: pane(id, pid, tmuxfix.Token), Value: tmuxfix.PaneLabelValue(tmuxfix.Token, id)}
	}
	tabbed := func(id string, pid int, value string) tmuxfix.PaneListing {
		return tmuxfix.PaneListing{Pane: pane(id, pid, ""), Value: value}
	}
	cases := []tmuxfix.Entry{
		tmuxfix.Listing("tab after the pane id", "SR-2.1", tabbed("%0", 295, tmuxfix.PaneLabelValue(tmuxfix.Token, "%0")+"\t")),
		tmuxfix.Listing("tab then a field", "SR-2.1", tabbed("%0", 295, tmuxfix.PaneLabelValue(tmuxfix.Token, "%0")+"\t%0")),
		tmuxfix.Listing("only tabs", "SR-2.1", tabbed("%0", 295, "\t\t\t\t\t")),
		tmuxfix.Listing("a numeric-looking tabbed value", "SR-2.1", tabbed("%0", 295, "1\t2\t%0\t3")),
		tmuxfix.Listing("tabbed line before and after own-labelled lines", "SR-2.1",
			own("%1", 300), tabbed("%0", 295, tmuxfix.Token+"\t%0"), own("%2", 301)),
	}
	for _, e := range cases {
		t.Run(e.Name, func(t *testing.T) {
			c, _ := newReplay(t, e)
			panes, err := c.ListPanes(testSocket)
			if err != nil {
				t.Fatalf("ListPanes: %v", err)
			}
			if !reflect.DeepEqual(panes, e.Panes) {
				t.Errorf("panes = %+v, want %+v", panes, e.Panes)
			}
		})
	}
}

// TestParsePaneLabelBorrowed: one value listed on several panes, as tmux lists
// a window, session, global or server value on every pane it reaches, counts
// only on the pane it names, never on the others (SR-3.6 scope guard).
func TestParsePaneLabelBorrowed(t *testing.T) {
	type line struct {
		sessionID, id string
		window, index int
	}
	lines := []line{{"$0", "%0", 0, 0}, {"$0", "%2", 0, 1}, {"$0", "%3", 1, 0}, {"$1", "%1", 0, 0}, {"$1", "%10", 0, 1}}
	for _, named := range []string{"%0", "%1", "%10", "%4", ""} {
		t.Run("names "+strconv.Quote(named), func(t *testing.T) {
			value := tmuxfix.PaneLabelValue(tmuxfix.Token, named)
			var listed []tmuxfix.PaneListing
			for i, l := range lines {
				p := tmux.Pane{SessionID: l.sessionID, Window: l.window, Index: l.index, ID: l.id, PID: 400 + i}
				if l.id == named {
					p.AdPane = tmuxfix.Token
				}
				listed = append(listed, tmuxfix.PaneListing{Pane: p, Value: value})
			}
			e := tmuxfix.Listing("borrowed", "SR-3.6", listed...)
			c, _ := newReplay(t, e)
			panes, err := c.ListPanes(testSocket)
			if err != nil {
				t.Fatalf("ListPanes: %v", err)
			}
			if !reflect.DeepEqual(panes, e.Panes) {
				t.Errorf("panes = %+v, want %+v", panes, e.Panes)
			}
		})
	}
}

// TestParseCaptureUnchanged checks a capture returns stdout unchanged, with
// and without -e.
func TestParseCaptureUnchanged(t *testing.T) {
	for _, e := range tmuxfix.Captures() {
		for _, ansi := range []bool{false, true} {
			t.Run(e.Name+"/ansi="+strconv.FormatBool(ansi), func(t *testing.T) {
				c, _ := newReplay(t, e)
				got, err := c.CapturePaneID(testSocket, "%0", 50, ansi)
				if err != nil {
					t.Fatalf("CapturePaneID: %v", err)
				}
				if got != e.Stdout {
					t.Errorf("capture = %q, want %q", got, e.Stdout)
				}
			})
		}
	}
}

// TestParseDataOnlyFromExitZero checks stdout that would parse is ignored for
// a non-zero exit: the failure comes from stderr and no data is returned.
func TestParseDataOnlyFromExitZero(t *testing.T) {
	lookup := tmuxfix.Find(tmuxfix.LookupAnswers(), "lookup/F9b")
	calls := []struct {
		call   tmux.Call
		stdout string
		run    func(*tmux.Client) (empty bool, err error)
	}{
		{tmux.CallLookup, lookup.Stdout, func(c *tmux.Client) (bool, error) {
			a, err := c.Lookup(testSocket)
			return reflect.DeepEqual(a, tmux.LookupAnswer{}), err
		}},
		{tmux.CallListPanes, tmuxfix.Find(tmuxfix.PaneListings(), "panes/P1").Stdout, func(c *tmux.Client) (bool, error) {
			p, err := c.ListPanes(testSocket)
			return p == nil, err
		}},
		{tmux.CallCapture, tmuxfix.Find(tmuxfix.Captures(), "capture/P1").Stdout, func(c *tmux.Client) (bool, error) {
			s, err := c.CapturePaneID(testSocket, "%0", 50, false)
			return s == "", err
		}},
	}
	stderrs := []struct {
		name   string
		stderr string
		want   tmux.Failure
	}{
		{"empty-stderr", "", tmux.FailUnrecognized},
		{"no-server", tmuxfix.NoServer(testSocket).Stderr, tmux.FailNoServer},
	}
	for _, tc := range calls {
		for _, se := range stderrs {
			t.Run(string(tc.call)+"/"+se.name, func(t *testing.T) {
				c, _ := newScripted(t, exited(1, tc.stdout, se.stderr))
				empty, err := tc.run(c)
				ce := parseWantFailure(t, err, tc.call, se.want)
				if !empty {
					t.Error("data returned from a non-zero exit")
				}
				if ce.FirstLine != "" {
					t.Errorf("FirstLine = %q, want empty (stdout never read)", ce.FirstLine)
				}
				parseNoLeak(t, ce, lookup.LabelValues)
			})
		}
	}
}

// TestParseCreateReply checks each create outcome for a chained name and for
// names labelled by id ($, \): reply, FailLabel or FailUnrecognized.
func TestParseCreateReply(t *testing.T) {
	names := []struct {
		name string
		byID bool
	}{{"n1", false}, {`a$b`, true}, {`a\b`, true}}
	for _, e := range tmuxfix.CreateReplies() {
		if e.Name == "create/duplicate" {
			continue // the duplicate reply is the replay tests' case
		}
		for _, n := range names {
			t.Run(e.Name+"/"+strconv.Quote(n.name), func(t *testing.T) {
				c, _ := newReplay(t, e)
				reply, err := c.NewSession(testSocket, n.name, "/tmp", nil, nil, tmuxfix.Token, "agent-x", tmuxfix.StoreID)
				want := e.Want[tmux.CallCreate]
				if e.ChainOnly && n.byID {
					want = 0
				}
				switch want {
				case 0:
					if err != nil || reply != e.Create {
						t.Errorf("NewSession = %+v, %v; want %+v, nil", reply, err, e.Create)
					}
				case tmux.FailLabel:
					ce := parseWantFailure(t, err, tmux.CallCreate, tmux.FailLabel)
					if reply != e.Create || ce.ExitStatus != e.Exit || !ce.HadStdout {
						t.Errorf("reply %+v, ExitStatus %d, HadStdout %v; want %+v, %d, true",
							reply, ce.ExitStatus, ce.HadStdout, e.Create, e.Exit)
					}
				default:
					ce := parseWantFailure(t, err, tmux.CallCreate, want)
					if reply != (tmux.CreateReply{}) || ce.FirstLine != e.FirstLine ||
						ce.ExitStatus != e.Exit || ce.HadStdout != (e.Stdout != "") {
						t.Errorf("reply %+v, CallError %+v; want zero reply, FirstLine %q, ExitStatus %d",
							reply, *ce, e.FirstLine, e.Exit)
					}
				}
			})
		}
	}
}
