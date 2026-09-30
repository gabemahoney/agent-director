package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// killMCPID is the row every kill case seeds and kills.
const killMCPID = "mcp-kill-row"

// killMCPCase is one row shape: its state, the Recorder sessions seeded
// beside it, and the kill tool's expected result or err_name.
type killMCPCase struct {
	name      string
	state     string
	seed      func(t *testing.T, rec *tmuxfix.Recorder, dbPath string)
	wantSent  bool
	wantErr   string
	wantKills int
}

// seedOurs seeds the row's own labelled session plus an unrelated session, so
// the server keeps a session after the kill (FIXTURE NOTE, SR-3.3).
func seedOurs(t *testing.T, rec *tmuxfix.Recorder, dbPath string) {
	rec.SeedRowSession(t, dbPath, killMCPID)
	rec.SeedSessions(apitest.TestSocket, tmuxfix.SeedSession{Name: "unrelated"})
}

// seedLeftover seeds only a session carrying an earlier launch's label with
// the row's own id (SR-6.1 Leftover).
func seedLeftover(t *testing.T, rec *tmuxfix.Recorder, dbPath string) {
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	rec.SeedRowSession(t, dbPath, killMCPID,
		tmuxfix.WithRowSessionLabel(tmuxfix.Valid(tmuxfix.OtherToken, killMCPID, storeID), true))
}

var killMCPCases = []killMCPCase{
	{name: "finished row", state: store.StateEnded},
	// SeedSpawn's live-row pane pid reads gone, so the check passes at once.
	{name: "live row with its own session", state: store.StateWaiting, seed: seedOurs, wantSent: true, wantKills: 2},
	{name: "live row beside a leftover", state: store.StateWaiting, seed: seedLeftover, wantErr: "ErrTmuxSessionConflict"},
}

// newKillMCPServer seeds one row at state into an isolated store, lets seed
// fill the Recorder, and returns a live dispatcher over an api.New client.
func newKillMCPServer(t *testing.T, tc killMCPCase) (mcp.Dispatcher, *tmuxfix.Recorder) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	if _, err := apitest.SeedSpawn(storePath, killMCPID, tc.state, "/tmp", "off", "", true); err != nil {
		t.Fatalf("seed %s: %v", tc.state, err)
	}
	rec := tmuxfix.NewRecorder()
	if tc.seed != nil {
		tc.seed(t, rec, storePath)
	}
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, CreateIfMissing: true, TmuxClient: rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client), rec
}

// TestKillMCP pins SR-6.6 and SR-6.1 on MCP: the kill tool result carries
// kill_sent, and a kill error reaches the caller with its err_name.
func TestKillMCP(t *testing.T) {
	for _, tc := range killMCPCases {
		t.Run(tc.name, func(t *testing.T) {
			d, rec := newKillMCPServer(t, tc)
			resp := runOne(t, d, mcp.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage(`1`),
				Method:  "tools/call",
				Params:  json.RawMessage(`{"name":"kill","arguments":{"claude_instance_id":"` + killMCPID + `"}}`),
			})
			if resp == nil {
				t.Fatal("tools/call kill gave no response")
			}
			if tc.wantErr != "" {
				assertKillErrName(t, resp, tc.wantErr)
			} else {
				assertKillSent(t, resp, tc.wantSent)
			}
			kills := len(rec.SocketCallsOf(tmux.CallKillPane)) + len(rec.SocketCallsOf(tmux.CallKillSession))
			if kills != tc.wantKills {
				t.Errorf("pane and session kills = %d; want %d", kills, tc.wantKills)
			}
			if tc.seed == nil {
				if n, s := len(rec.Calls()), len(rec.SocketCalls()); n != 0 || s != 0 {
					t.Errorf("tmux calls = %d name-based, %d socket (%+v); want none", n, s, rec.SocketCalls())
				}
			}
		})
	}
}

// assertKillSent checks resp is a result whose text object carries kill_sent
// with the wanted value.
func assertKillSent(t *testing.T, resp *mcp.Response, want bool) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("tools/call kill failed: %+v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Content) != 1 {
		t.Fatalf("kill result = %s; want one text content part", body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Content[0].Text), &obj); err != nil {
		t.Fatalf("parse kill text %q: %v", env.Content[0].Text, err)
	}
	raw, ok := obj["kill_sent"]
	if !ok {
		t.Fatalf("kill text %s has no kill_sent", env.Content[0].Text)
	}
	if got, err := json.Marshal(want); err != nil || string(raw) != string(got) {
		t.Errorf("kill_sent = %s; want %v", raw, want)
	}
}

// assertKillErrName checks resp is a JSON-RPC error whose data carries want.
func assertKillErrName(t *testing.T, resp *mcp.Response, want string) {
	t.Helper()
	if resp.Error == nil {
		body, _ := json.Marshal(resp.Result)
		t.Fatalf("tools/call kill succeeded with %s; want %s", body, want)
	}
	dataBody, _ := json.Marshal(resp.Error.Data)
	var data mcp.ToolErrorData
	if err := json.Unmarshal(dataBody, &data); err != nil {
		t.Fatalf("parse error data: %v", err)
	}
	if data.ErrName != want {
		t.Errorf("err_name = %q (%s); want %s", data.ErrName, data.ErrDescription, want)
	}
}
