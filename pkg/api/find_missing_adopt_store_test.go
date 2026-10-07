package api_test

import (
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's adoption on a real store (SR-3.6, SR-11.6): the adoption write advances row_version, so only a
// verdict guarded on its snapshot applies; a change in between stops both (TestFindMissingChangedBetweenReadAndWrite).

// seedLostReplyAt seeds the working row "r" recording no pane (a lost create reply) at dbPath with sessionID.
func seedLostReplyAt(t *testing.T, dbPath string, create bool, sessionID string) {
	t.Helper()
	if _, err := apitest.SeedSpawn(dbPath, "r", store.StateWorking, "/tmp", "off", sessionID, create, apitest.WithNoPane()); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
}

// lostReplyStoreRec returns a Recorder whose server (fmServer) holds row "r"'s labelled session with one pane,
// fmAdoptPane with pid fmAdoptPID, carrying the row's token when tokenPane.
func lostReplyStoreRec(t *testing.T, dbPath string, tokenPane bool) *tmuxfix.Recorder {
	t.Helper()
	cols := readRow(t, dbPath)
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	tok, _ := cols.LaunchToken.(string)
	pane := tmuxfix.SeedPane{ID: fmAdoptPane, PID: fmAdoptPID}
	if tokenPane {
		pane.AdPane = tok
	}
	return tmuxfix.NewRecorder().StartServer(apitest.TestSocket, fmServer).SeedSessions(apitest.TestSocket,
		tmuxfix.SeedSession{Name: cols.TmuxSessionName.(string), Label: tmuxfix.Valid(tok, "r", storeID),
			Panes: []tmuxfix.SeedPane{pane}})
}

// TestFindMissingAdoptRealStore: the adoption records the server identity (and the token pane) at +1 version, and
// the verdict guarded on that snapshot applies: the adopted pane's unreadable process notes, no token pane marks.
func TestFindMissingAdoptRealStore(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	cases := []struct {
		name      string
		tokenPane bool
		state     string
		note      any
		paneID    any
		panePID   any
	}{
		{"token pane unreadable noted", true, store.StateWorking, "probe_eacces", fmAdoptPane, int64(fmAdoptPID)},
		{"no token pane marked", false, store.StateMissing, nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := openSeeded(t, seedLostReplyAt)
			rec := lostReplyStoreRec(t, dbPath, tc.tokenPane)
			was := readRow(t, dbPath)

			res, _, before := sweepFrom(t, s, adoptChecker(procfix.Unreadable()), fmSweep{tmux: rec})
			now := readRow(t, dbPath)
			got := []any{now.State, now.LivenessNote, now.TmuxServerPID, now.TmuxServerStarted, now.TmuxServerStarttime,
				now.PaneID, now.PanePID, now.PaneStarttime}
			want := []any{tc.state, tc.note, int64(fmServer.PID), fmServer.Start, fmServer.ProcStart,
				tc.paneID, tc.panePID, nil}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("state, note, server and pane columns = %v; want %v", got, want)
			}
			if tc.state == store.StateWorking {
				assertLists(t, res, nil, []string{"r"})
				if d := now.RowVersion.(int64) - was.RowVersion.(int64); d != 2 {
					t.Errorf("row_version delta = %d; want 2 (adoption, then the note on its snapshot)", d)
				}
				return
			}
			assertLists(t, res, []string{"r"}, nil)
			assertMarkReason(t, before, "r", "tmux_absent")
		})
	}
}
