package tmuxfix_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestNewRecorderForReadPaneAndPause: either seeded table gives the lookup the
// verdict the label option asks for, its sessions created at the shared
// clock's second unless an option gives a time (and a name), and the checker
// answers the pane and server processes as alive. read-pane's pane (the row's
// by id for Ours, the lone leftover's by its @ad_pane for Leftover) captures
// its text; on Ours pause's row pane is the only one listed and takes /exit
// then Enter, the only calls after the lookup and the listing.
func TestNewRecorderForReadPaneAndPause(t *testing.T) {
	const id, text, loText, created, name = "agent-row", "row pane text", "leftover pane text", int64(1_700_000_000), `agent\$row`
	cases := []struct {
		name        string
		label       tmuxfix.RowLabel
		leftover    bool
		createdName bool
		want        tmux.Verdict
	}{
		{name: "ours", want: tmux.Ours},
		{name: "ours-created-and-name", createdName: true, want: tmux.Ours},
		{name: "old-label", label: tmuxfix.RowLabelOld, want: tmux.Leftover},
		{name: "other-store", label: tmuxfix.RowLabelOtherStore, want: tmux.Gone},
		{name: "leftover-option", label: tmuxfix.RowLabelNone, leftover: true, want: tmux.Leftover},
	}
	for _, tc := range cases {
		for _, pause := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/read-pane", true: "/pause"}[pause], func(t *testing.T) {
				dbPath, launch := readPaneRow(t, id)
				clock := tmuxfix.NewClock(start.Add(42 * time.Second))
				var r *tmuxfix.Recorder
				var pc *procfix.Checker
				if pause {
					opts := []tmuxfix.PauseOption{tmuxfix.WithPauseVirtualTime(clock, tmux.Timeouts{}), tmuxfix.WithPauseLabel(tc.label)}
					if tc.leftover {
						opts = append(opts, tmuxfix.WithPauseLeftover("left"))
					}
					if tc.createdName {
						opts = append(opts, tmuxfix.WithPauseCreated(created), tmuxfix.WithPauseName(name))
					}
					r, pc = tmuxfix.NewRecorderForPause(t, dbPath, id, opts...)
				} else {
					opts := []tmuxfix.ReadPaneOption{tmuxfix.WithReadPaneVirtualTime(clock, tmux.Timeouts{}), tmuxfix.WithReadPaneLabel(tc.label)}
					if tc.leftover {
						opts = append(opts, tmuxfix.WithReadPaneLeftover("left", loText))
					}
					if tc.createdName {
						opts = append(opts, tmuxfix.WithReadPaneCreated(created), tmuxfix.WithReadPaneName(name))
					}
					r, pc = tmuxfix.NewRecorderForReadPane(t, dbPath, id, text, opts...)
				}
				for i, s := range r.Sessions(launch.Socket) {
					wantCreated, wantName := clock.Now().Unix(), s.Name
					if i == 0 && tc.createdName {
						wantCreated, wantName = created, name
					}
					if s.Created != wantCreated || s.Name != wantName {
						t.Errorf("session %s created %d, name %q; want %d, %q", s.ID, s.Created, s.Name, wantCreated, wantName)
					}
				}

				res := tmux.Lookup(r, pc, launch, "")
				if wantLeftovers := map[bool]int{false: 0, true: 1}[tc.want == tmux.Leftover]; res.Verdict != tc.want || len(res.Leftovers) != wantLeftovers {
					t.Fatalf("verdict %v with %d leftovers, want %v with %d (result %+v)", res.Verdict, len(res.Leftovers), tc.want, wantLeftovers, res)
				}
				if tc.want == tmux.Gone {
					return
				}
				panes, err := r.ListPanes(launch.Socket)
				if err != nil {
					t.Fatal(err)
				}
				pane, wantText := tmux.Pane{ID: apitest.TestPaneID, PID: apitest.TestPanePID}, text
				if tc.want == tmux.Leftover {
					var match tmux.PaneMatch
					if pane, match = tmux.PaneByToken(panes, res.Leftovers[0].Label.Token); match != tmux.PaneOne {
						t.Fatalf("leftover's pane by @ad_pane = %v in %+v, want one", match, panes)
					}
					if tc.leftover {
						wantText = loText
					}
				}
				srv, _ := r.Server(launch.Socket)
				for _, pid := range []int{pane.PID, srv.PID} {
					if _, alive, known := pc.StartTime(pid); !alive || !known {
						t.Errorf("process checker for pid %d: alive %v known %v, want alive", pid, alive, known)
					}
				}
				if !pause {
					if got, err := r.CapturePaneID(launch.Socket, pane.ID, 25, false); err != nil || got != wantText {
						t.Errorf("CapturePaneID(%s) = %q, %v; want %q", pane.ID, got, err, wantText)
					}
					return
				}
				if tc.want != tmux.Ours {
					return
				}
				if len(panes) != 1 || panes[0].ID != apitest.TestPaneID || panes[0].PID != apitest.TestPanePID {
					t.Fatalf("panes = %+v, want the row's pane %s (pid %d)", panes, apitest.TestPaneID, apitest.TestPanePID)
				}
				if err := r.SendKeysPane(launch.Socket, apitest.TestPaneID, "/exit", true); err != nil {
					t.Fatalf("SendKeysPane: %v", err)
				}
				want := []tmuxfix.SocketCall{{Call: tmux.CallLookup, Socket: launch.Socket}, {Call: tmux.CallListPanes, Socket: launch.Socket},
					{Call: tmux.CallSendText, Socket: launch.Socket, Target: apitest.TestPaneID, Text: "/exit", PressEnter: true},
					{Call: tmux.CallSendEnter, Socket: launch.Socket, Target: apitest.TestPaneID}}
				if got := r.SocketCalls(); !reflect.DeepEqual(got, want) {
					t.Errorf("socket calls = %+v\nwant %+v", got, want)
				}
			})
		}
	}
}
