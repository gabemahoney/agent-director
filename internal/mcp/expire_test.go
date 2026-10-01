package mcp_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expireMCPID is the one finished row every expire case seeds.
const expireMCPID = "mcp-expire-row"

// expireMCPCase is one row shape: whether its own labelled session runs on
// the Recorder, and the expire tool's expected result fields as raw JSON.
type expireMCPCase struct {
	name        string
	ownSession  bool
	wantIDs     string
	wantKept    string
	wantKeptIDs string
}

var expireMCPCases = []expireMCPCase{
	{name: "no session is deleted", wantIDs: `["` + expireMCPID + `"]`, wantKept: `0`, wantKeptIDs: `[]`},
	{name: "own session is kept", ownSession: true, wantIDs: `[]`, wantKept: `1`, wantKeptIDs: `["` + expireMCPID + `"]`},
}

// newExpireMCPServer seeds one finished row past the default retention window
// into an isolated store and returns a live dispatcher, the Recorder and the store path.
func newExpireMCPServer(t *testing.T, ownSession bool) (mcp.Dispatcher, *tmuxfix.Recorder, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	retention := time.Duration(config.Default().Defaults.ExpireRetentionDays) * 24 * time.Hour
	old := time.Now().Add(-retention - 24*time.Hour)
	if _, err := apitest.SeedSpawn(storePath, expireMCPID, store.StateEnded, "/tmp", "off", "", true,
		apitest.WithEndedAt(old)); err != nil {
		t.Fatalf("seed ended row: %v", err)
	}
	rec := tmuxfix.NewRecorder()
	if ownSession {
		rec.SeedRowSession(t, storePath, expireMCPID)
	}
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, CreateIfMissing: true, TmuxClient: rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client), rec, storePath
}

// TestExpireMCP pins SR-12.2 on MCP: the expire tool deletes a row with no
// session, keeps one whose own session runs, and always carries kept and kept_ids.
func TestExpireMCP(t *testing.T) {
	for _, tc := range expireMCPCases {
		t.Run(tc.name, func(t *testing.T) {
			d, rec, storePath := newExpireMCPServer(t, tc.ownSession)
			resp := runOne(t, d, mcp.Request{
				JSONRPC: "2.0",
				ID:      json.RawMessage(`1`),
				Method:  "tools/call",
				Params:  json.RawMessage(`{"name":"expire","arguments":{}}`),
			})
			obj := expireToolResult(t, resp)
			for key, want := range map[string]string{"ids": tc.wantIDs, "kept": tc.wantKept, "kept_ids": tc.wantKeptIDs} {
				if got, ok := obj[key]; !ok || string(got) != want {
					t.Errorf("%s = %s (present %v); want %s", key, got, ok, want)
				}
			}

			_, err := apitest.ReadSpawnColumns(storePath, expireMCPID)
			if gone := errors.Is(err, store.ErrSpawnNotFound); gone == tc.ownSession {
				t.Errorf("row read after expire: err = %v; want row present = %v", err, tc.ownSession)
			}
			if n := len(rec.SocketCallsOf(tmux.CallLookup)); n != 1 {
				t.Errorf("lookups = %d; want 1", n)
			}
			for _, call := range []tmux.Call{tmux.CallListPanes, tmux.CallKillPane, tmux.CallKillSession} {
				if n := len(rec.SocketCallsOf(call)); n != 0 {
					t.Errorf("%s calls = %d; want none", call, n)
				}
			}
		})
	}
}

// expireToolResult checks resp is a successful tool result and returns its
// one text part's JSON object, each field left raw.
func expireToolResult(t *testing.T, resp *mcp.Response) map[string]json.RawMessage {
	t.Helper()
	if resp == nil {
		t.Fatal("tools/call expire gave no response")
	}
	if resp.Error != nil {
		t.Fatalf("tools/call expire failed: %+v", resp.Error)
	}
	body, _ := json.Marshal(resp.Result)
	var env struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Content) != 1 {
		t.Fatalf("expire result = %s; want one text content part", body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(env.Content[0].Text), &obj); err != nil {
		t.Fatalf("parse expire text %q: %v", env.Content[0].Text, err)
	}
	return obj
}
