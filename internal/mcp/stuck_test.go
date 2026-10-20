package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckMCPWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedPermissionSurface(path); err != nil {
		t.Fatal(err)
	}
	d := settleDispatcher(t, path, nil)
	for _, agent := range []string{"claude", "codex", "cursor"} {
		for _, verb := range []string{"status", "get"} {
			raw := toolText(t, callTool(t, d, verb, paramJSON(t, map[string]any{"instance_id": "stuck-" + agent})))
			apitest.CheckPermissionWire(t, []byte(raw), agent, verb)
		}
	}
	list := callToolText(t, d, "list", paramJSON(t, map[string]any{"state": []string{"stuck"}, "limit": 3}))
	var rows []json.RawMessage
	if err := json.Unmarshal(list["spawns"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("list=%s", list["spawns"])
	}
	for _, raw := range rows {
		var row struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		apitest.CheckPermissionWire(t, raw, row.Agent, "list")
	}
	ordinary := callToolText(t, d, "status", paramJSON(t, map[string]any{"instance_id": "ordinary"}))
	for k := range apitest.LoadPermissionWire(t) {
		if k == "state" {
			continue
		}
		if _, ok := ordinary[k]; ok {
			t.Errorf("ordinary emitted %s", k)
		}
	}
}
