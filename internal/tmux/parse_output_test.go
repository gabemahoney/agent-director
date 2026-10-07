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
	cases := []struct {
		name, stdout, firstLine string
	}{
		{"session id unparseable", "@0\t0\t0\t%0\t295\t" + own + "\n", "@0\t0\t0\t%0\t295"},
		{"pane id unparseable", "$0\t0\t0\t0\t295\t" + own + "\n", "$0\t0\t0\t0\t295"},
		{"label holding tabs, pid unparseable", "$0\t0\t0\t%0\t-1\t" + tmuxfix.Token + "\t%0\t" + tmuxfix.Token + "\n",
			"$0\t0\t0\t%0\t-1"},
		{"label holding a newline", "$0\t0\t0\t%0\t295\t" + tmuxfix.Token + "\n%0\n", "$0\t0\t0\t%0\t295"},
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
// a non-zero exit: the failure comes from stderr (empty here; exec_mechanics'
// TestExecStreamsSeparate has a recognised reply) and no data is returned.
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
	for _, tc := range calls {
		t.Run(string(tc.call), func(t *testing.T) {
			c, _ := newScripted(t, exited(1, tc.stdout, ""))
			empty, err := tc.run(c)
			ce := parseWantFailure(t, err, tc.call, tmux.FailUnrecognized)
			if !empty || ce.FirstLine != "" {
				t.Errorf("data returned %v, FirstLine %q; want no data and an empty FirstLine (stdout never read)", !empty, ce.FirstLine)
			}
			parseNoLeak(t, ce, lookup.LabelValues)
		})
	}
}
