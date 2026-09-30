package tmux_test

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Raw-level parse tests of the pane listing, the create reply and the capture
// (SR-2.1, SR-2.3, SR-2.4), and of data read only from an exit-0 call.

// TestParsePaneListings parses each catalogue listing into Panes, or fails an
// unparseable one as FailUnrecognized carrying its first line.
func TestParsePaneListings(t *testing.T) {
	for _, e := range append(tmuxfix.PaneListings(), tmuxfix.Silent()) {
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
