package api_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing's SR-11.6 same-life guard and SR-5.8 store errors: the fake store (find_missing_test.go) scripts
// Changed/Absent/error on the process path; a real store shows a change between read and write on both paths.

// guardedWrites are the three guarded writes, each with a row and checker that lead the sweep to it.
var guardedWrites = []struct {
	op  string
	row func(id string) store.LiveSpawnIdentity
}{
	{"mark", func(id string) store.LiveSpawnIdentity { return liveRow(id, withSessionStart(51, fmStart)) }},
	{"note", func(id string) store.LiveSpawnIdentity { return liveRow(id, withSessionStart(52, fmStart)) }},
	{"clear", func(id string) store.LiveSpawnIdentity {
		return liveRow(id, withSessionStart(53, fmStart), withNote("tmux_server_changed"))
	}},
}

// guardedWritesChecker answers 51 gone (mark), 52 unreadable (note) and 53 alive (clear).
func guardedWritesChecker() *procfix.Checker {
	pc := procfix.New()
	pc.Set(52, procfix.Unreadable())
	pc.Set(53, procfix.Alive(fmStart))
	return pc
}

// TestFindMissingRefusedWriteNotListed: a mark, note write or clear that finds the row changed or absent, or
// fails in the store, gets no tick or permission-request close and leaves the row in neither list; only a store
// error is logged (naming the row and the error), and a later dead row is still marked.
func TestFindMissingRefusedWriteNotListed(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	storeErr := errors.New("disk I/O error")
	answers := []struct {
		name string
		a    fmAnswer
	}{{"changed", fmAnswer{res: store.CondChanged}}, {"absent", fmAnswer{res: store.CondAbsent}}, {"store error", fmAnswer{err: storeErr}}}
	for _, w := range guardedWrites {
		for _, ans := range answers {
			t.Run(w.op+" "+ans.name, func(t *testing.T) {
				st := &fakeFindMissingStore{
					rows:    []store.LiveSpawnIdentity{w.row("bad"), liveRow("next", withSessionStart(59, fmStart))},
					answers: map[string]map[string]fmAnswer{w.op: {"bad": ans.a}},
				}

				res, lg, before := sweepFrom(t, st, guardedWritesChecker(), fmSweep{budget: fmBudgetSpent})
				assertLists(t, res, []string{"next"}, nil)
				if ops := st.ops("bad"); !equalStrings(ops, []string{w.op}) {
					t.Errorf("writes on bad = %v; want [%s] only", ops, w.op)
				}
				if ticks := ticksSince(t, before, "bad"); len(ticks) != 0 {
					t.Errorf("ticks on bad = %v; want none", ticks)
				}
				assertMarkReason(t, before, "next", "proc_absent")
				logged := len(lg.lines) == 1 && strings.Contains(lg.lines[0], "bad") && strings.Contains(lg.lines[0], storeErr.Error())
				if logged != (ans.a.err != nil) || (ans.a.err == nil && len(lg.lines) != 0) {
					t.Errorf("log = %v; want one line naming bad and the store error, only on a store error", lg.lines)
				}
			})
		}
	}
}

// interleavedStore is a real store that runs between once after its live-row read returns, before any write.
type interleavedStore struct {
	*store.Store
	between func()
}

func (s interleavedStore) ListLiveSpawnIdentities() ([]store.LiveSpawnIdentity, error) {
	rows, err := s.Store.ListLiveSpawnIdentities()
	if err == nil {
		s.between()
	}
	return rows, err
}

// TestFindMissingChangedBetweenReadAndWrite (AC-FM-03, AC-FM-07/08/09): a row relaunched, written by its own
// agent's hook, reused (a new life, same second) or deleted between the sweep's read and its guarded write (on the
// process path a mark, note, unusable-name note or clear; on the tmux path an adoption, note, provenance_conflict
// note or mark) is left as the change left it: no write, tick, adopted record or log line, in neither list.
func TestFindMissingChangedBetweenReadAndWrite(t *testing.T) {
	// Serial: it checks the shared trail by literal row ids other find-missing tests reuse.
	paned := func(opts ...apitest.SpawnOption) rowSeed {
		return func(t *testing.T, dbPath string, create bool, sid string) {
			seedPaneRowAt(t, dbPath, create, sid, opts...)
		}
	}
	own := func(t *testing.T, dbPath string) *tmuxfix.Recorder {
		rec := tmuxfix.NewRecorder()
		rec.SeedRowSession(t, dbPath, "r")
		return rec
	}
	writes := []struct {
		name   string
		seed   rowSeed
		proc   procfix.Process
		rec    func(t *testing.T, dbPath string) *tmuxfix.Recorder // nil: no tmux call; the row changes after the read
		after  tmux.Call                                           // with rec: the call after which the row changes
		noPane bool                                                // a lost reply: no hook applies (SR-22.9)
	}{
		{name: "mark", seed: paned(), proc: procfix.Gone()},
		{name: "note", seed: paned(apitest.WithLivenessNote("tmux_server_changed"), apitest.WithLivenessUnverifiedSince("2026-09-01 10:00:00")),
			proc: procfix.Unreadable()},
		{name: "clear", seed: paned(apitest.WithLivenessNote("probe_eacces")), proc: procfix.Alive(fmStart)},
		{name: "unusable-name note", seed: paned(apitest.WithTmuxSessionName(fmuReps()[0].raw)), proc: procfix.Unreadable()},
		{name: "lost reply adoption", seed: seedLostReplyAt, proc: procfix.Unreadable(), rec: own, after: tmux.CallListPanes, noPane: true},
		{name: "ours note", seed: paned(), proc: procfix.Unreadable(), rec: own, after: tmux.CallLookup},
		{name: "gone mark", seed: paned(), proc: procfix.Unreadable(), after: tmux.CallLookup,
			rec: func(*testing.T, string) *tmuxfix.Recorder { return tmuxfix.NewRecorder() }},
		{name: "conflict note", seed: paned(), proc: procfix.Unreadable(), after: tmux.CallLookup,
			rec: func(t *testing.T, dbPath string) *tmuxfix.Recorder {
				rec := tmuxfix.NewRecorder()
				s := rec.SeedRowSession(t, dbPath, "r")
				return rec.SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: s.Name + "-b", Label: s.Label})
			}},
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
		{"own agent hook", true, func(t *testing.T, _ *store.Store, dbPath string, _ rowSeed) {
			if a := apitest.ApplyAgentHook(t, dbPath, "r", "Stop", ""); !a.Applied {
				t.Fatalf("Stop not applied: %+v", a)
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
	for _, w := range writes {
		for _, ch := range changes {
			if ch.hook && w.noPane {
				continue
			}
			t.Run(w.name+" after "+ch.name, func(t *testing.T) {
				s, dbPath := openSeeded(t, w.seed)
				var (
					changed    apitest.SpawnColumns
					changedErr error
				)
				change := func() {
					ch.apply(t, s, dbPath, w.seed)
					changed, changedErr = apitest.ReadSpawnColumns(dbPath, "r")
				}
				var fs api.FindMissingStore = interleavedStore{Store: s, between: change}
				o := fmSweep{budget: fmBudgetSpent}
				if w.rec != nil {
					fs, o.budget, o.tmux = s, 0, w.rec(t, dbPath)
					o.tmux.AfterCall(w.after, func(tmuxfix.SocketCall, error) { change() })
				}

				res, lg, before := sweepFrom(t, fs, paneChecker(w.proc), o)
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
