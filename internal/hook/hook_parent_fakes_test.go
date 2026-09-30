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
//   - hookConfig: a HandleConfig whose ParentPID and ParentProc report a parent.
//   - fakeParentProc: the hook.ParentProc double (procfix.Checker + name table).
//   - hookIgnoredAfter: the ad.hook.ignored lines one row got after a checkpoint.

import (
	"testing"

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
// procfix.Checker's process table, CommandName from names.
type fakeParentProc struct {
	*procfix.Checker
	names map[int]string
}

// CommandName answers from the name table; an unlisted or empty name is unreadable.
func (p *fakeParentProc) CommandName(pid int) (string, bool) {
	n := p.names[pid]
	return n, n != ""
}

// newParentProc returns a fakeParentProc holding each parent: alive with its
// start time, or unreadable when Start is "". Tests may Set more pids later.
func newParentProc(parents ...hookParent) *fakeParentProc {
	p := &fakeParentProc{Checker: procfix.New(), names: map[int]string{}}
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

// hookConfig returns a HandleConfig with env for a hook whose parent is p
// (getppid() = p.PID). Callers set Cfg and Clock on the result as needed.
func hookConfig(env func(string) string, p hookParent) hook.HandleConfig {
	return hook.HandleConfig{
		Env:        env,
		ParentPID:  func() int { return p.PID },
		ParentProc: newParentProc(p),
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
