package tmuxfix_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestNewRecorderForPause: the seeded table gives the lookup the verdict the
// label option asks for; on Ours the row's pane is listed by its id and takes
// /exit then Enter, the only calls after the lookup and the listing.
func TestNewRecorderForPause(t *testing.T) {
	const id, created, name = "agent-row", int64(1_700_000_000), `agent\$row`
	cases := []struct {
		name          string
		opts          []tmuxfix.PauseOption
		want          tmux.Verdict
		wantLeftovers int
		wantCreated   int64  // 0: the clock's second
		wantName      string // "": the row's session name
	}{
		{"ours", nil, tmux.Ours, 0, 0, ""},
		{"ours-created-and-name", []tmuxfix.PauseOption{tmuxfix.WithPauseCreated(created), tmuxfix.WithPauseName(name)},
			tmux.Ours, 0, created, name},
		{"old-label", []tmuxfix.PauseOption{tmuxfix.WithPauseLabel(tmuxfix.RowLabelOld)}, tmux.Leftover, 1, 0, ""},
		{"other-store", []tmuxfix.PauseOption{tmuxfix.WithPauseLabel(tmuxfix.RowLabelOtherStore)}, tmux.Gone, 0, 0, ""},
		{"leftover-option", []tmuxfix.PauseOption{tmuxfix.WithPauseLabel(tmuxfix.RowLabelNone), tmuxfix.WithPauseLeftover("left")},
			tmux.Leftover, 1, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath, launch := readPaneRow(t, id)
			clock := tmuxfix.NewClock(start.Add(42 * time.Second))
			opts := append([]tmuxfix.PauseOption{tmuxfix.WithPauseVirtualTime(clock, tmux.Timeouts{})}, tc.opts...)
			r, pc := tmuxfix.NewRecorderForPause(t, dbPath, id, opts...)
			wantCreated := tc.wantCreated
			if wantCreated == 0 {
				wantCreated = clock.Now().Unix()
			}
			own := r.Sessions(launch.Socket)[0]
			if own.Created != wantCreated || (tc.wantName != "" && own.Name != tc.wantName) {
				t.Errorf("row's session created %d, name %q; want %d, %q", own.Created, own.Name, wantCreated, tc.wantName)
			}

			res := tmux.Lookup(r, pc, launch, "")
			if res.Verdict != tc.want || len(res.Leftovers) != tc.wantLeftovers {
				t.Fatalf("verdict %v with %d leftovers, want %v with %d (result %+v)",
					res.Verdict, len(res.Leftovers), tc.want, tc.wantLeftovers, res)
			}
			if tc.want != tmux.Ours {
				return
			}
			panes, err := r.ListPanes(launch.Socket)
			if err != nil {
				t.Fatal(err)
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
			srv, _ := r.Server(launch.Socket)
			for _, pid := range []int{apitest.TestPanePID, srv.PID} {
				if _, alive, known := pc.StartTime(pid); !alive || !known {
					t.Errorf("process checker for pid %d: alive %v known %v, want alive", pid, alive, known)
				}
			}
		})
	}
}
