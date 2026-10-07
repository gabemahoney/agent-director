package mcp_test

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killMCPID is the row every kill and send-keys case seeds.
const killMCPID = "mcp-kill-row"

// seedOurs seeds the row's own labelled session plus an unrelated session, so
// the server keeps a session after the kill (FIXTURE NOTE, SR-3.3).
func seedOurs(t *testing.T, rec *tmuxfix.Recorder, dbPath string) {
	rec.SeedRowSession(t, dbPath, killMCPID)
	rec.SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "unrelated"})
}

// newKillEnv is an mcpEnv with killMCPID seeded in state and seed (if any)
// filling the Recorder.
func newKillEnv(t *testing.T, state string, seed func(*testing.T, *tmuxfix.Recorder, string)) *mcpEnv {
	t.Helper()
	e := newEnv(t)
	e.seed(t, killMCPID, state)
	if seed != nil {
		seed(t, e.rec, e.storePath)
	}
	return e
}

// TestKillMCP pins SR-6.6 and SR-6.1 on MCP: the kill tool result carries
// kill_sent, and a kill error reaches the caller with its err_name. The kill
// matrix itself is pkg/api's.
func TestKillMCP(t *testing.T) {
	seedLeftover := func(t *testing.T, rec *tmuxfix.Recorder, dbPath string) { // SR-6.1: an earlier launch's label, the row's id
		storeID, err := apitest.ReadStoreID(dbPath)
		if err != nil {
			t.Fatalf("ReadStoreID: %v", err)
		}
		rec.SeedRowSession(t, dbPath, killMCPID, tmuxfix.WithRowSessionLabel(tmuxfix.Valid(tmuxfix.OtherToken, killMCPID, storeID), true))
	}
	cases := []struct {
		name      string
		state     string
		seed      func(*testing.T, *tmuxfix.Recorder, string)
		wantSent  string // kill_sent; "" for an error
		wantErr   string
		wantKills int
	}{
		{name: "finished row", state: store.StateEnded, wantSent: "false"},
		// SeedSpawn's live-row pane pid reads gone, so the check passes at once.
		{name: "live row with its own session", state: store.StateWaiting, seed: seedOurs, wantSent: "true", wantKills: 2},
		{name: "live row beside a leftover", state: store.StateWaiting, seed: seedLeftover, wantErr: "ErrTmuxSessionConflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t, tc.state, tc.seed)

			resp := callTool(t, e.d, "kill", `{"claude_instance_id":"`+killMCPID+`"}`)

			if tc.wantErr != "" {
				if data := toolErrorData(t, resp); data.ErrName != tc.wantErr {
					t.Errorf("err_name = %q (%s); want %s", data.ErrName, data.ErrDescription, tc.wantErr)
				}
			} else if got := string(toolResult(t, resp)["kill_sent"]); got != tc.wantSent {
				t.Errorf("kill_sent = %s; want %s", got, tc.wantSent)
			}
			if kills := len(e.rec.SocketCallsOf(tmux.CallKillPane)) + len(e.rec.SocketCallsOf(tmux.CallKillSession)); kills != tc.wantKills {
				t.Errorf("pane and session kills = %d; want %d", kills, tc.wantKills)
			}
			if n, s := len(e.rec.Calls()), len(e.rec.SocketCalls()); tc.seed == nil && (n != 0 || s != 0) {
				t.Errorf("tmux calls = %d name-based, %d socket (%+v); want none", n, s, e.rec.SocketCalls())
			}
		})
	}
}
