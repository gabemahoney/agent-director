package api_test

// expire_reasons_unusable_test.go pins expire's unusable recorded-name
// reasons (SR-3.2, SR-12.2, SR-12.5; AC-LKP-10, AC-LKP-15): each fixture of
// unusable_name_fixture_test.go keeps its row with the fixture's reason on
// every run, ahead of the process check, the socket's stop, the budget and
// every lookup answer, with no tmux call, no process read and no cost to the
// rows beside it. Rows and runs come from the expire fixture and
// expire_reasons_test.go's rsn helpers.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// xruSeed seeds spec's row as id, ended rsnAge ago with its agent in state a,
// recording *name (nil: the default name).
func (e *killEnv) xruSeed(t *testing.T, spec killRowSpec, id string, a agentState, name *string) killRow {
	t.Helper()
	spec = e.rsnFinish(spec, id)
	spec.Agent = a
	if name != nil {
		spec.Opts = append(spec.Opts, apitest.WithTmuxSessionName(*name))
	}
	return e.seedRow(t, spec)
}

// TestExpireUnusableName_KeptOnTwoRuns: alone, each fixture's old finished
// row, its agent gone, is kept with its reason on two runs, with no tmux call
// and the row unchanged; the usable control is looked up and deleted.
func TestExpireUnusableName_KeptOnTwoRuns(t *testing.T) {
	for _, f := range append(unusableNameFixtures(), usableNameFixture()) {
		t.Run(f.label, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.xruSeed(t, killRowSpec{NoSession: true}, rsnID("a"), agentGone, &f.raw)
			if f.kept == "" {
				e.rsnExpect(t, 0, map[string]string{r.ID: ""}, r.Socket)
				return
			}
			for range 2 {
				e.rsnExpect(t, 0, map[string]string{r.ID: f.kept})
			}
		})
	}
}

// xruCase is one world around the unusable-name row: seed seeds it, recording
// name, and the rows beside it, returning the row, the others' outcomes and
// the sockets looked up; spent runs with the budget spent.
type xruCase struct {
	name  string
	spent bool
	seed  func(t *testing.T, e *killEnv, name string) (killRow, map[string]string, []string)
}

// xruAlone seeds the row alone from spec with its agent in state a, then world.
func xruAlone(spec killRowSpec, a agentState, world func(*testing.T, *killEnv, *killRow)) func(*testing.T, *killEnv, string) (killRow, map[string]string, []string) {
	return func(t *testing.T, e *killEnv, name string) (killRow, map[string]string, []string) {
		r := e.xruSeed(t, spec, rsnID("a"), a, &name)
		if world != nil {
			world(t, e, &r)
		}
		return r, map[string]string{}, nil
	}
}

// xruCases are the checks an unusable name goes ahead of: a live agent
// (process_alive), a spent budget and a stopped socket (tmux_skipped), usable
// rows sharing the socket, the caller's socket refused for a later usable
// row that records none, and each call-table lookup world.
func xruCases() []xruCase {
	noSession := killRowSpec{NoSession: true}
	cases := []xruCase{
		{name: "agent alive, else process_alive", seed: xruAlone(noSession, agentAlive, nil)},
		{name: "budget spent, else tmux_skipped", spent: true, seed: xruAlone(noSession, agentGone, nil)},
		{name: "socket stopped by an earlier row, else tmux_skipped", seed: func(t *testing.T, e *killEnv, name string) (killRow, map[string]string, []string) {
			first := e.xruSeed(t, noSession, rsnID("a"), agentGone, nil)
			e.rec.Script(first.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
			r := e.xruSeed(t, noSession, rsnID("b"), agentGone, &name)
			return r, map[string]string{first.ID: "cant_tell"}, []string{first.Socket}
		}},
		{name: "usable rows on its socket, one lookup", seed: func(t *testing.T, e *killEnv, name string) (killRow, map[string]string, []string) {
			ours := e.xruSeed(t, killRowSpec{}, rsnID("a"), agentGone, nil)
			r := e.xruSeed(t, noSession, rsnID("b"), agentGone, &name)
			gone := e.xruSeed(t, noSession, rsnID("c"), agentGone, nil)
			return r, map[string]string{ours.ID: "ours", gone.ID: ""}, []string{apitest.TestSocket}
		}},
		{name: "no recorded socket, ahead of a usable one in an unusable socket directory", seed: func(t *testing.T, e *killEnv, name string) (killRow, map[string]string, []string) {
			if err := os.Chmod(filepath.Dir(e.defaultSocket), 0o755); err != nil {
				t.Fatalf("chmod socket directory: %v", err)
			}
			legacy := killRowSpec{NoSession: true, NoServerIdentity: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSocket("")}}
			r := e.xruSeed(t, legacy, rsnID("a"), agentGone, &name)
			usable := e.xruSeed(t, legacy, rsnID("b"), agentGone, nil)
			return r, map[string]string{usable.ID: "tmux_unavailable"}, nil
		}},
	}
	for _, col := range callTableLookupColumns() {
		if col.actionFailure == 0 {
			cases = append(cases, xruCase{name: "lookup world, " + string(col.outcome), seed: xruAlone(col.spec, agentGone, col.world)})
		}
	}
	return cases
}

// TestExpireUnusableName_AheadOfEveryCheck: in each world, each fixture's row
// is kept with its reason, never deleted, its agent never read and no tmux
// call made for it; the rows beside it get what they would alone.
func TestExpireUnusableName_AheadOfEveryCheck(t *testing.T) {
	for _, c := range xruCases() {
		t.Run(c.name, func(t *testing.T) {
			for _, f := range unusableNameFixtures() {
				t.Run(f.label, func(t *testing.T) {
					e := newKillEnv(t)
					r, want, sockets := c.seed(t, e, f.raw)
					want[r.ID] = f.kept
					budget := e.cfg.EffectiveSweepBudget()
					if c.spent {
						budget = fmBudgetSpent
					}
					e.rsnExpectWith(t, func() (api.ExpireResult, *recordingLogger, error) {
						lg := &recordingLogger{}
						res, err := api.Expire(e.st, e.rec, e.pc, config.Default().Defaults.ExpireRetentionDays,
							olderThan(0), budget, e.clock.Now, lg)
						return res, lg, err
					}, want, sockets...)
					if slices.Contains(e.pc.StartTimeCalls(), r.AgentPID) {
						t.Errorf("agent pid %d of the unusable-name row read; want no process check", r.AgentPID)
					}
				})
			}
		})
	}
}
