package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestStuckMCPWire: the status, get and list tools over the shared
// permission and pane surfaces return each reason's exact detail (list only
// the reason, filtered before the limit) and every row its agent's
// no_screen_change_availability, read under the largest settle time
// (apitest.SeedPaneSurface).
func TestStuckMCPWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedPermissionSurface(path); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedPaneSurface(path); err != nil {
		t.Fatal(err)
	}
	d := settleDispatcher(t, path, &apitest.SettleOnConfig)
	read := func(verb, id string) []byte {
		return []byte(toolText(t, callTool(t, d, verb, paramJSON(t, map[string]any{"instance_id": id}))))
	}
	for _, surface := range apitest.StuckWireSurfaces {
		for _, agent := range surface.Agents {
			for _, verb := range []string{"status", "get"} {
				surface.Check(t, read(verb, surface.Prefix+agent), agent, verb)
			}
		}
	}
	list := callToolText(t, d, "list", paramJSON(t, map[string]any{"state": []string{"stuck"}, "limit": 5}))
	var rows []json.RawMessage
	if err := json.Unmarshal(list["spawns"], &rows); err != nil || len(rows) != 5 {
		t.Fatalf("list=%s; want the 5 stuck rows", list["spawns"])
	}
	for _, raw := range rows {
		apitest.CheckListedStuckWire(t, raw)
	}
	for id, agent := range apitest.StuckWireOrdinary {
		apitest.CheckOrdinaryWire(t, read("status", id), agent)
	}
}
