package hook_test

// hook_parent_fakes_test.go — the hook's parent process for handler tests
// (SR-22.9). Handle applies a hook only when its parent (HandleConfig.ParentPID
// + HandleConfig.ParentProc) is the row's recorded pane process, so every
// handler test against a real store needs a row with a pane and a parent to
// fire from. Tests use these helpers rather than hand-building a gate:
//
//   - seedAgentRow: a temp store with one row whose pane is recorded.
//   - agentParent / foreignParent: the row's own agent, or another process
//     carrying the row's id (pid_mismatch; no_pane_recorded on a pane-less row).
//   - hookConfig: a HandleConfig whose ParentPID and ParentProc report a parent,
//     on a virtual clock (Clock, Now) with the default pending grace, so
//     SessionStart's wait for its launch's identity write never sleeps in real time.
//   - hookClock: that virtual clock, for its recorded sleeps.
//   - identityAtSleep: the launch's identity write, landing at the Nth sleep.
//   - fakeParentProc: the hook.ParentProc double (procfix.Checker + name and
//     parent-pid tables).
//   - hookIgnoredAfter: the ad.hook.ignored lines one row got after a checkpoint.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// hookParent is the process a hook runs under: its pid, start time ("" =
// unreadable, which never matches a row, decision A1) and command name ("" =
// unreadable; ad.hook.ignored's parent_command is then null).
type hookParent struct {
	PID   int
	Start string
	Name  string
}

// fakeParentProc is a hook.ParentProc double: StartTime comes from
// procfix.Checker's process table, CommandName from names, PPID from ppids.
type fakeParentProc struct {
	*procfix.Checker
	names map[int]string
	ppids map[int]int
}

// CommandName answers from the name table; an unlisted or empty name is unreadable.
func (p *fakeParentProc) CommandName(pid int) (string, bool) {
	n := p.names[pid]
	return n, n != ""
}

// PPID answers from the parent-pid table; an unlisted or non-positive entry is
// unreadable, so by default no hook reports a launcher.
func (p *fakeParentProc) PPID(pid int) (int, bool) {
	ppid := p.ppids[pid]
	return ppid, ppid > 0
}

// newParentProc returns a fakeParentProc holding each parent: alive with its
// start time, or unreadable when Start is "". Tests may Set more pids later.
func newParentProc(parents ...hookParent) *fakeParentProc {
	p := &fakeParentProc{Checker: procfix.New(), names: map[int]string{}, ppids: map[int]int{}}
	for _, hp := range parents {
		if hp.Start == "" {
			p.Set(hp.PID, procfix.Unreadable())
		} else {
			p.Set(hp.PID, procfix.Alive(hp.Start))
		}
		p.names[hp.PID] = hp.Name
	}
	return p
}

// hookConfig returns a HandleConfig with env for a hook whose parent is p, on a
// virtual clock starting now with the default pending grace. Callers may override Cfg/Clock.
func hookConfig(env func(string) string, p hookParent) hook.HandleConfig {
	now := time.Now()
	clock := &advancingClock{now: &now}
	return hook.HandleConfig{
		Env:          env,
		ParentPID:    func() int { return p.PID },
		ParentProc:   newParentProc(p),
		Clock:        clock,
		Now:          clock.Now,
		PendingGrace: config.Tmux{}.EffectivePendingGrace(),
	}
}

// hookClock returns hookConfig's virtual clock from hc (fails if Clock was replaced).
func hookClock(t *testing.T, hc hook.HandleConfig) *advancingClock {
	t.Helper()
	c, ok := hc.Clock.(*advancingClock)
	if !ok {
		t.Fatalf("hookClock: hc.Clock is %T, want hookConfig's *advancingClock", hc.Clock)
	}
	return c
}

// identityAtSleep runs the launch's identity write for id (RecordLaunchIdentity, recording
// agentParent's pane) at hc's clock's nth sleep; a write that does not apply fails the test.
func identityAtSleep(t *testing.T, hc hook.HandleConfig, st *store.Store, id string, n int) {
	t.Helper()
	pid, start, err := storefix.AgentHookParent(st, id)
	if err != nil {
		t.Fatalf("identityAtSleep(%q): %v", id, err)
	}
	c := hookClock(t, hc)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runAt, c.run = n, func() {
		sp, err := st.GetSpawn(id)
		if err != nil {
			t.Errorf("identityAtSleep(%q): read row: %v", id, err)
			return
		}
		res, err := st.RecordLaunchIdentity(id, sp.RowVersion, sp.Identity.Token,
			store.LaunchIdentity{PaneID: apitest.TestPaneID, PanePID: pid, PaneStarttime: start})
		if err != nil || res != store.CondApplied {
			t.Errorf("identityAtSleep(%q): RecordLaunchIdentity = %v, %v; want applied", id, res, err)
		}
	}
}

// agentParent is id's own agent: the row's recorded pane pid and start time
// (storefix.SeedPaneStarttime when none is recorded yet), named "claude".
func agentParent(t *testing.T, st *store.Store, id string) hookParent {
	t.Helper()
	pid, start, err := storefix.AgentHookParent(st, id)
	if err != nil {
		t.Fatalf("agentParent(%q): %v", id, err)
	}
	return hookParent{PID: pid, Start: start, Name: "claude"}
}

// foreignParent is another process carrying id (a leftover, a nested agent, a
// teammate): a pid that is not the row's pane pid, same start time, "claude".
func foreignParent(t *testing.T, st *store.Store, id string) hookParent {
	t.Helper()
	p := agentParent(t, st, id)
	p.PID = storefix.ForeignHookParentPID(p.PID)
	return p
}

// seedAgentRow opens a temp store and seeds id in state through
// apitest.SeedSpawn. A live state records the pane apitest.TestPanePID with no
// start time (the first applied hook records it); a terminal state records no
// pane unless opts say so (apitest.WithLaunchIdentity).
func seedAgentRow(t *testing.T, id, state string, opts ...apitest.SpawnOption) (*store.Store, string) {
	t.Helper()
	st, dbPath := storefix.OpenTempStore(t)
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", "", false, opts...); err != nil {
		t.Fatalf("seedAgentRow(%q, %q): %v", id, state, err)
	}
	return st, dbPath
}

// hookIgnoredAfter returns the ad.hook.ignored trail lines for id written
// after the first before lines (before = len(readTrailLines(t, trailFile()))).
func hookIgnoredAfter(t *testing.T, before int, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, row := range readTrailLines(t, trailFile())[before:] {
		if row["event"] == "ad.hook.ignored" && row["claude_instance_id"] == id {
			out = append(out, row)
		}
	}
	return out
}
