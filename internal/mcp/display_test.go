package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDisplayMCPWire: the status, get and list tools over the shared
// needs_intelligence and idle_screen surfaces return each state's exact detail
// (list only the state, its `state` filter naming either one) and every row
// its agent's idle_screen_availability, read under the largest settle time
// (apitest.SeedIdleScreenSurface).
func TestDisplayMCPWire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedNeedsIntelligenceSurface(path); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedIdleScreenSurface(path); err != nil {
		t.Fatal(err)
	}
	d := settleDispatcher(t, path, &apitest.SettleOnConfig)
	read := func(verb, id string) []byte {
		return []byte(toolText(t, callTool(t, d, verb, paramJSON(t, map[string]any{"instance_id": id}))))
	}
	for _, surface := range apitest.DisplayWireSurfaces {
		for _, agent := range surface.Agents {
			for _, verb := range []string{"status", "get"} {
				surface.Check(t, read(verb, surface.Prefix+agent), agent, verb)
			}
		}
		list := callToolText(t, d, "list", paramJSON(t, map[string]any{"state": []string{surface.State}, "limit": len(surface.Agents)}))
		var rows []json.RawMessage
		if err := json.Unmarshal(list["spawns"], &rows); err != nil || len(rows) != len(surface.Agents) {
			t.Fatalf("list state %s=%s; want its %d rows", surface.State, list["spawns"], len(surface.Agents))
		}
		for _, raw := range rows {
			var got struct {
				State string `json:"state"`
			}
			if json.Unmarshal(raw, &got) != nil || got.State != surface.State {
				t.Errorf("list state %s listed %s", surface.State, raw)
			}
			apitest.CheckListedDisplayWire(t, raw)
		}
	}
	for id, agent := range apitest.DisplayWireOrdinary {
		apitest.CheckOrdinaryWire(t, read("status", id), agent)
	}
}
