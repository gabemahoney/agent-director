package main_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestStuckCLIWire: the CLI's status, get and list of the shared permission
// and pane surfaces print each reason's exact detail (list only the reason,
// filtered before the limit) and every row its agent's
// no_screen_change_availability, read under the largest settle time
// (apitest.SeedPaneSurface).
func TestStuckCLIWire(t *testing.T) {
	home := t.TempDir()
	if err := apitest.SeedPermissionSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedPaneSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
	read := func(args ...string) []byte {
		t.Helper()
		raw, stderr, code := runCLIWithHome(t, home, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%q: %d %s %s", args, code, raw, stderr)
		}
		return []byte(raw)
	}
	for _, surface := range apitest.StuckWireSurfaces {
		for _, agent := range surface.Agents {
			for _, verb := range []string{"status", "get"} {
				surface.Check(t, read(verb, "--instance-id", surface.Prefix+agent), agent, verb)
			}
		}
	}
	var listed struct {
		Spawns []json.RawMessage `json:"spawns"`
	}
	if raw := read("list", "--state", "stuck", "--limit", "5"); json.Unmarshal(raw, &listed) != nil || len(listed.Spawns) != 5 {
		t.Fatalf("list=%s; want the 5 stuck rows", raw)
	}
	for _, row := range listed.Spawns {
		apitest.CheckListedStuckWire(t, row)
	}
	for id, agent := range apitest.StuckWireOrdinary {
		apitest.CheckOrdinaryWire(t, read("status", "--instance-id", id), agent)
	}
}
