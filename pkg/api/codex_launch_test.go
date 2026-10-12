package api_test

// codex_launch_test.go holds the helpers the pkg/api tests of a Codex launch
// share (spawn, reuse and resume of a codex agent, and the advice follows that
// end in one). It holds no tests.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// codexProgram is the program a Codex launch runs when [launch] codex_path is not set.
const codexProgram = "codex"

// assertCodexLaunch fails unless creates holds exactly one create, running
// program (first, one element) as a Codex launch command, and instance id is a
// pending row recording agent codex that registered no subagent tracking
// (subagent_tracking 0, until Codex rows are tracked). It returns the create's
// command split into parts for the caller's own checks of what stands in it.
func assertCodexLaunch(t *testing.T, creates []tmuxfix.SocketCall, dbPath, id, program string) codexfix.Launch {
	t.Helper()
	if len(creates) != 1 {
		t.Fatalf("creates = %+v; want one, launching %s", creates, program)
	}
	l := codexfix.Split(t, creates[0].Command)
	if l.Program != program {
		t.Errorf("create runs %q; want %q", l.Program, program)
	}
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	if cols.State != store.StatePending || cols.Agent != "codex" || cols.SubagentTracking != int64(0) {
		t.Errorf("row %s: state %v, agent %v, subagent_tracking %v; want pending, codex, 0", id, cols.State, cols.Agent, cols.SubagentTracking)
	}
	return l
}

// codexRowOpts is the seed options of a codex row as a Codex launch stores it
// (agent codex, subagent_tracking 0; apitest seeds 1 unless told otherwise),
// then extra.
func codexRowOpts(extra ...apitest.SpawnOption) []apitest.SpawnOption {
	return append([]apitest.SpawnOption{apitest.WithAgent("codex"), apitest.WithNoSubagentTracking()}, extra...)
}

// agentRowOpts is the seed options of a row of agent as that agent's launch
// stores it (codexRowOpts for codex, else agent alone), then extra.
func agentRowOpts(agent string, extra ...apitest.SpawnOption) []apitest.SpawnOption {
	if agent == "codex" {
		return codexRowOpts(extra...)
	}
	return append([]apitest.SpawnOption{apitest.WithAgent(agent)}, extra...)
}
