package realtmux_test

import (
	"fmt"
	"testing"
)

func TestCursorSelectionNewAndExistingServer(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t/explicit=%t", existing, explicit), func(t *testing.T) {
				for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
					t.Setenv(key, "synthetic-client-"+key)
				}
				selected := map[string]string(nil)
				if explicit {
					selected = map[string]string{"CURSOR_CONFIG_DIR": "/explicit/config with space", "CURSOR_DATA_DIR": "/explicit/data"}
				}
				var globals, unrelated map[string]string
				var unrelatedID string
				f, r, _ := newCursorPaneScenario(t, false, "", cursorPaneOptions{ExtraEnv: selected, Unconfigured: !explicit, BeforeLaunch: func(f *spawnFix) {
					if !existing {
						return
					}
					s := f.startSession(t, "unrelated")
					unrelatedID = s.ID
					globals, unrelated = map[string]string{}, map[string]string{}
					for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
						f.must(t, "set-environment", "-g", key, "synthetic-server-"+key)
						f.must(t, "set-environment", "-t", unrelatedID, key, "synthetic-unrelated-"+key)
						globals[key] = f.must(t, "show-environment", "-g", key)
						unrelated[key] = f.must(t, "show-environment", "-t", unrelatedID, key)
					}
				}})
				got := r.Selection(t)
				for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
					want, present := selected[key]
					if present {
						if got[key] == nil || *got[key] != want {
							t.Fatalf("%s received %v", key, got[key])
						}
					} else if got[key] != nil {
						t.Fatalf("%s inherited selection", key)
					}
					if existing && (f.must(t, "show-environment", "-g", key) != globals[key] || f.must(t, "show-environment", "-t", unrelatedID, key) != unrelated[key]) {
						t.Fatalf("%s global/unrelated session mutated", key)
					}
				}
			})
		}
	}
}

func TestCursorDirectorySelectionPublicCallers(t *testing.T) {
	for _, surface := range []string{"Go", "Go release wrapper", "CLI", "MCP", "Bun installed"} {
		t.Run(surface, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", launch.ID)
			t.Setenv("CURSOR_API_KEY", "synthetic-parent-private")
			t.Setenv("CURSOR_CONFIG_DIR", "/parent/config")
			caller := newPaneCaller(t, f, surface, buildHookCLI(t, t.TempDir()))
			defer caller.close()
			for _, selected := range []map[string]string{{"CURSOR_CONFIG_DIR": "/caller/config with space", "CURSOR_DATA_DIR": "/caller/data"}, nil} {
				id := newInstanceID("c")
				caller.must("spawn", map[string]any{"agent": "cursor", "instance_id": id, "cwd": f.CWD, "no_pre_trust": true, "extra_env": selected})
				f.launched(t, id)
				waitFor(t, "child selected runtime", func() bool { row := readRow(t, f.DBPath, id); return row.State == "waiting" }, func() string { return fmt.Sprint(readRow(t, f.DBPath, id)) })
				got := r.Selection(t)
				for key, want := range selected {
					if got[key] == nil || *got[key] != want {
						t.Fatalf("%s caller selection lost %s", surface, key)
					}
				}
				if selected == nil && (got["CURSOR_CONFIG_DIR"] != nil || got["CURSOR_DATA_DIR"] != nil) {
					t.Fatal("unconfigured child inherited parent selection")
				}
				if row := readRow(t, f.DBPath, id); row.ParentID != launch.ID {
					t.Fatalf("child association lost: %v", row.ParentID)
				}
				if parent := readRow(t, f.DBPath, launch.ID); parent.State != "waiting" || pidGone(int(parent.PanePID.(int64))) {
					t.Fatal("child changed unrelated parent runtime")
				}
				if got["CURSOR_API_KEY"] != nil || got["AGENT_CLI_CREDENTIAL_STORE"] != nil {
					t.Fatal("caller credential selector inherited")
				}
			}
		})
	}
}
