package api_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckGoWire(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedPermissionSurface(path); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, agent := range []string{store.AgentClaude, store.AgentCodex, store.AgentCursor} {
		status, err := api.Status(s, "stuck-"+agent)
		if err != nil {
			t.Fatal(err)
		}
		get, err := api.Get(s, api.RelayView{}, "stuck-"+agent)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []struct {
			verb string
			row  any
		}{{"status", status}, {"get", get}} {
			raw, err := json.Marshal(value.row)
			if err != nil {
				t.Fatal(err)
			}
			apitest.CheckPermissionWire(t, raw, agent, value.verb)
		}
	}
	rows, err := api.List(s, api.RelayView{}, api.ListParams{State: []string{"stuck"}, Limit: 3})
	if err != nil || len(rows.Spawns) != 3 {
		t.Fatalf("list=%+v %v", rows, err)
	}
	for _, row := range rows.Spawns {
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		apitest.CheckPermissionWire(t, raw, row.Agent, "list")
	}
}
