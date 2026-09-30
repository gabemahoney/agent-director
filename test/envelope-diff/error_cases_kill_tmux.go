// error_cases_kill_tmux.go holds the kill error rows whose error comes from
// tmux (SR-20.5): each seeds a live row recording a private socket whose
// fake-tmux table the row writes (or leaves empty), so both runners see the
// same tmux and no kill is sent.
package envelope_diff

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// killFailedID is the row whose agent still runs with no session or
	// pane of its launch on the server.
	killFailedID = "id-err-tkf-1"
	// killRowName is the tmux session name each kill row records.
	killRowName = "err-kill-row"
	// killLeftoverID is the row whose socket holds only a session labelled
	// for it under another launch's token; killLeftoverSessionID,
	// killLeftoverName and killLeftoverCreated describe that session.
	killLeftoverID        = "id-err-ksc-1"
	killLeftoverSessionID = "$5"
	killLeftoverName      = "err-ksc-leftover"
	killLeftoverCreated   = 1790549183
	// ctxAgentPID is the ctx key under which the killFailed row's seed
	// passes its live agent's pid on to its desc.
	ctxAgentPID = "agent_pid"
)

// startLiveAgent starts a child process standing in for a row's agent and
// returns its pid and real start time; cleanup kills and reaps it.
func startLiveAgent(t *testing.T) (int, string) {
	t.Helper()
	cmd := exec.Command("sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("startLiveAgent: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd.Process.Pid, procstat.ReadStarttime(t, cmd.Process.Pid)
}

// seedKillRow seeds a waiting row id named killRowName, launched on socket
// with token tmuxfix.Token, whose agent is its pane process (panePID,
// paneStart), and returns the store's directory and store id.
func seedKillRow(t *testing.T, id, socket string, panePID int, paneStart string) (string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWaiting, "", "", "", true,
		apitest.WithTmuxSessionName(killRowName),
		apitest.WithLaunchIdentity(store.LaunchIdentity{
			Token: tmuxfix.Token, Socket: socket,
			PaneID: apitest.TestPaneID, PanePID: panePID, PaneStarttime: paneStart,
		})); err != nil {
		t.Fatalf("seedKillRow: %v", err)
	}
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("seedKillRow: read store id: %v", err)
	}
	return filepath.Dir(dbPath), storeID
}

// killParams and killArgv are the kill call on id for each runner.
func killParams(id string) map[string]any { return map[string]any{"claude_instance_id": id} }
func killArgv(id string) []string         { return []string{"kill", "--claude-instance-id", id} }

// killTmuxErrorCases are appended to errorCases.
var killTmuxErrorCases = []errorCase{

	// ── kill / ErrTmuxKillFailed ──────────────────────────────────────────
	// The row's socket has no server (Gone) while its recorded agent, a live
	// child of the test, still runs: no kill is sent (SR-6.1).
	{
		verb:    "kill",
		errName: "ErrTmuxKillFailed",
		skip: func(t *testing.T) {
			if runtime.GOOS != "linux" {
				t.Skipf("the live agent's start time is read from /proc; not on %s", runtime.GOOS)
			}
		},
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, _ := usePrivateFakeTmux(t)
			pid, start := startLiveAgent(t)
			dir, storeID := seedKillRow(t, killFailedID, socket, pid, start)
			return dir, map[string]any{ctxAgentPID: pid, ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return killParams(killFailedID) },
		cliArgv: func(_ map[string]any) []string { return killArgv(killFailedID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			pid, _ := ctx[ctxAgentPID].(int)
			storeID, _ := ctx[ctxStoreID].(string)
			return apitest.DescKillNoPane(killFailedID, killRowName, pid), []string{storeID, tmuxfix.Token}
		},
	},

	// ── kill / ErrTmuxSessionConflict ─────────────────────────────────────
	// Leftover: the row's socket holds only a session labelled by this store
	// for the row's id under another launch's token; no kill is sent.
	{
		verb:    "kill",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, tables := usePrivateFakeTmux(t)
			dir, storeID := seedKillRow(t, killLeftoverID, socket, apitest.TestPanePID, "")
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: killLeftoverCreated},
				Sessions: []faketmuxfix.Session{{
					ID: killLeftoverSessionID, Created: killLeftoverCreated, Name: killLeftoverName,
					Label: tmuxfix.LabelValue(tmuxfix.OtherToken, killLeftoverSessionID, killLeftoverID, storeID),
					Panes: []faketmuxfix.Pane{{ID: "%5", PID: os.Getpid()}},
				}},
			})
			return dir, map[string]any{ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return killParams(killLeftoverID) },
		cliArgv: func(_ map[string]any) []string { return killArgv(killLeftoverID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			c := apitest.DescKillLeftover([]apitest.DescSession{{Name: killLeftoverName, ID: killLeftoverSessionID}})
			label := tmuxfix.LabelValue(tmuxfix.OtherToken, killLeftoverSessionID, killLeftoverID, storeID)
			return c, []string{storeID, tmuxfix.Token, tmuxfix.OtherToken, label}
		},
	},
}
