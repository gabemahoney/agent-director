package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// The real dispatcher preserves the inspection outcome in JSON-RPC error data.
func TestMCPPauseTimeoutInspectionRequired(t *testing.T) {
	e := newEnv(t)
	const id = "mcp-pause-timeout"
	e.seed(t, id, store.StateWaiting)
	e.rec.SeedRowSession(t, e.storePath, id)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("[pause]\ntimeout_seconds = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := api.New(api.Options{StorePath: e.storePath, ConfigPath: configPath, TmuxClient: e.rec})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	data := toolErrorData(t, callTool(t, mcp.NewLiveDispatcher(client), "pause", paramJSON(t, map[string]any{"instance_id": id})))
	want := map[string]any{"inspection_required": true, "exit_keys_sent": true, "exit_confirmed": false, "row_state": "waiting"}
	if data.ErrName != "ErrPauseTimeout" || !reflect.DeepEqual(data.ErrDetails, want) {
		raw, _ := json.Marshal(data)
		t.Fatalf("pause error data = %s; want ErrPauseTimeout and %v", raw, want)
	}
}
