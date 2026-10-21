package api_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestStuckGoWire: status, get and list of the shared permission and pane
// surfaces give each reason's exact detail (list only the reason, its
// filter applied before the limit) and every row its agent's
// no_screen_change_availability.
func TestStuckGoWire(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedPermissionSurface(path); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedPaneSurface(path); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, cfg)
	c, err := api.New(api.Options{StorePath: path, ConfigPath: cfg, TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	marshal := func(v any, err error) []byte {
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, surface := range apitest.StuckWireSurfaces {
		for _, agent := range surface.Agents {
			surface.Check(t, marshal(c.Status(surface.Prefix+agent)), agent, "status")
			surface.Check(t, marshal(c.Get(surface.Prefix+agent)), agent, "get")
		}
	}
	rows, err := c.List(api.ListParams{State: []string{"stuck"}, Limit: 5})
	if err != nil || len(rows.Spawns) != 5 {
		t.Fatalf("list=%+v %v; want the 5 stuck rows", rows, err)
	}
	for _, row := range rows.Spawns {
		apitest.CheckListedStuckWire(t, marshal(row, nil))
	}
	for id, agent := range apitest.StuckWireOrdinary {
		apitest.CheckOrdinaryWire(t, marshal(c.Status(id)), agent)
	}
}
