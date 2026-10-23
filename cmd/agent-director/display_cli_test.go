package main_test

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDisplayCLIWire: the CLI's status, get and list of the shared
// needs_intelligence and idle_screen surfaces print each state's exact detail
// (list only the state, `--state` naming either one) and every row its agent's
// idle_screen_availability, read under the largest settle time
// (apitest.SeedIdleScreenSurface).
func TestDisplayCLIWire(t *testing.T) {
	home := t.TempDir()
	if err := apitest.SeedNeedsIntelligenceSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedIdleScreenSurface(stateDB(home)); err != nil {
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
	for _, surface := range apitest.DisplayWireSurfaces {
		for _, agent := range surface.Agents {
			for _, verb := range []string{"status", "get"} {
				surface.Check(t, read(verb, "--instance-id", surface.Prefix+agent), agent, verb)
			}
		}
		var listed struct {
			Spawns []json.RawMessage `json:"spawns"`
		}
		raw := read("list", "--state", surface.State, "--limit", strconv.Itoa(len(surface.Agents)))
		if json.Unmarshal(raw, &listed) != nil || len(listed.Spawns) != len(surface.Agents) {
			t.Fatalf("list --state %s=%s; want its %d rows", surface.State, raw, len(surface.Agents))
		}
		for _, row := range listed.Spawns {
			var got struct {
				State string `json:"state"`
			}
			if json.Unmarshal(row, &got) != nil || got.State != surface.State {
				t.Errorf("list --state %s listed %s", surface.State, row)
			}
			apitest.CheckListedDisplayWire(t, row)
		}
	}
	for id, agent := range apitest.DisplayWireOrdinary {
		apitest.CheckOrdinaryWire(t, read("status", "--instance-id", id), agent)
	}
}
