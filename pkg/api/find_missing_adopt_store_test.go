package api_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's adoption and same-life guard on a real store (SR-3.6, SR-11.6; AC-FM-07/08/09): the adoption
// write advances row_version, so only a verdict guarded on its snapshot applies; a change in between stops both.

// seedLostReplyAt seeds the working row "r" recording no pane (a lost create reply) at dbPath with sessionID.
func seedLostReplyAt(t *testing.T, dbPath string, create bool, sessionID string) {
	t.Helper()
	if _, err := apitest.SeedSpawn(dbPath, "r", store.StateWorking, "/tmp", "off", sessionID, create, apitest.WithNoPane()); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
}

// rowSeed seeds the working row "r" at dbPath with sessionID ("" for none).
type rowSeed func(t *testing.T, dbPath string, create bool, sessionID string)

// seedPaneRowOnly is seedPaneRowAt with no further options.
func seedPaneRowOnly(t *testing.T, dbPath string, create bool, sessionID string) {
	t.Helper()
	seedPaneRowAt(t, dbPath, create, sessionID)
}

// openSeeded seeds row "r" with seed in a fresh store and returns the store open, with its path.
func openSeeded(t *testing.T, seed rowSeed) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seed(t, dbPath, true, "")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
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

			res, _, before := sweepRealTmux(t, s, adoptChecker(procfix.Unreadable()), rec)
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

// sweepRealTmux runs one sweep of s judged by pc against rec, returning the result, the log and the checkpoint.
func sweepRealTmux(t *testing.T, s api.FindMissingStore, pc *procfix.Checker, rec *tmuxfix.Recorder) (api.FindMissingResult, *recordingLogger, int) {
	t.Helper()
	before := trailLen(t)
	lg := &recordingLogger{}
	res, err := runFindMissing(s, pc, fmSweep{tmux: rec, lg: lg})
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	return res, lg, before
}

// ownRowRec returns a Recorder holding row "r"'s own session as the create left it (SeedRowSession).
func ownRowRec(t *testing.T, dbPath string) *tmuxfix.Recorder {
	t.Helper()
	rec := tmuxfix.NewRecorder()
	rec.SeedRowSession(t, dbPath, "r")
	return rec
}

// TestFindMissingTmuxPathChangedBeforeWrite (AC-FM-07/08/09): a row relaunched, reused (a new life, same second)
// or deleted between the sweep's read and its adoption, note or mark gets no write, tick or adopted record.
func TestFindMissingTmuxPathChangedBeforeWrite(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	rows := []struct {
		name  string
		after tmux.Call // the call after which the row changes
		seed  rowSeed
		rec   func(t *testing.T, dbPath string) *tmuxfix.Recorder
		hooks bool // the row records a pane, so its own agent's SessionStart applies
	}{
		{"lost reply adoption", tmux.CallListPanes, seedLostReplyAt, ownRowRec, false},
		{"ours note", tmux.CallLookup, seedPaneRowOnly, ownRowRec, true},
		{"gone mark", tmux.CallLookup, seedPaneRowOnly,
			func(*testing.T, string) *tmuxfix.Recorder { return tmuxfix.NewRecorder() }, true},
	}
	changes := []struct {
		name  string
		hook  bool
		apply func(t *testing.T, s *store.Store, dbPath string, reseed rowSeed)
	}{
		{"relaunch", true, func(t *testing.T, _ *store.Store, dbPath string, _ rowSeed) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "SessionStart", "sess-relaunch"); !a.Applied {
				t.Fatalf("SessionStart not applied: %+v", a)
			}
		}},
		{"reuse", false, func(t *testing.T, s *store.Store, dbPath string, reseed rowSeed) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
			reseed(t, dbPath, false, "sess-reuse")
		}},
		{"delete", false, func(t *testing.T, s *store.Store, _ string, _ rowSeed) {
			if err := s.DeleteSpawn("r"); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}},
	}
	for _, row := range rows {
		for _, ch := range changes {
			if ch.hook && !row.hooks {
				continue // a hook writes nothing on a row that records no pane (SR-22.9)
			}
			t.Run(row.name+" after "+ch.name, func(t *testing.T) {
				s, dbPath := openSeeded(t, row.seed)
				rec := row.rec(t, dbPath)
				var (
					changed    apitest.SpawnColumns
					changedErr error
				)
				rec.AfterCall(row.after, func(tmuxfix.SocketCall, error) {
					ch.apply(t, s, dbPath, row.seed)
					changed, changedErr = apitest.ReadSpawnColumns(dbPath, "r")
				})
				pc := paneChecker(procfix.Unreadable())

				res, lg, before := sweepRealTmux(t, s, pc, rec)
				assertLists(t, res, nil, nil)
				now, err := apitest.ReadSpawnColumns(dbPath, "r")
				if (err == nil) != (changedErr == nil) || !reflect.DeepEqual(now, changed) {
					t.Errorf("row after sweep = %+v (err %v); want as the change left it %+v (err %v)", now, err, changed, changedErr)
				}
				if ticks := ticksSince(t, before, "r"); len(ticks) != 0 {
					t.Errorf("ticks = %v; want none", ticks)
				}
				assertOneDisagree(t, before, "r", "adopted", "", 0)
				if len(lg.lines) != 0 {
					t.Errorf("log = %v; want none", lg.lines)
				}
			})
		}
	}
}
