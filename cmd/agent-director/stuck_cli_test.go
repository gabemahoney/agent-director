package main_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckCLIWire(t *testing.T) {
	home := t.TempDir()
	if err := apitest.SeedPermissionSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"claude", "codex", "cursor"} {
		for _, verb := range []string{"status", "get"} {
			raw, stderr, code := runCLIWithHome(t, home, verb, "--instance-id", "stuck-"+agent)
			if code != 0 || stderr != "" {
				t.Fatalf("%s/%s: %d %s %s", agent, verb, code, raw, stderr)
			}
			apitest.CheckPermissionWire(t, []byte(raw), agent, verb)
		}
	}
	raw, stderr, code := runCLIWithHome(t, home, "list", "--state", "stuck", "--limit", "3")
	if code != 0 || stderr != "" {
		t.Fatalf("list: %d %s %s", code, raw, stderr)
	}
	var listed struct {
		Spawns []json.RawMessage `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(raw), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Spawns) != 3 {
		t.Fatalf("list=%s", raw)
	}
	for _, row := range listed.Spawns {
		var identity struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(row, &identity); err != nil {
			t.Fatal(err)
		}
		apitest.CheckPermissionWire(t, row, identity.Agent, "list")
	}
	raw, _, code = runCLIWithHome(t, home, "status", "--instance-id", "ordinary")
	if code != 0 {
		t.Fatal(raw)
	}
	var ordinary map[string]any
	if err := json.Unmarshal([]byte(raw), &ordinary); err != nil {
		t.Fatal(err)
	}
	for k := range apitest.LoadPermissionWire(t) {
		if k == "state" {
			continue
		}
		if _, ok := ordinary[k]; ok {
			t.Errorf("ordinary emitted %s", k)
		}
	}
}
