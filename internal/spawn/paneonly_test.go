package spawn

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

func TestPaneOnlyCompositionAndPreTrust(t *testing.T) {
	for _, agent := range []string{"droid"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "supervisor")
			cfg := config.Default()
			cfg.Defaults.RelayMode = "on"
			cfg.Defaults.InjectHelpHook = true
			cfg.Experimental.Droid = true
			cfg.Launch.DroidPath = "/opt/my tools/droid"
			args := []string{"--model", "ordinary-custom-model"}
			for _, off := range []bool{false, true} {
				r, err := Resolve(SpawnParams{Agent: agent, CWD: home, AgentArgs: args, NoPreTrust: off}, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := Validate(&r); err != nil {
					t.Fatal(err)
				}
				if _, err := ApplyDefaults(&r, cfg, nil); err != nil {
					t.Fatal(err)
				}
				r.LaunchStoreID, r.LaunchToken = "store", "token"
				c, err := ComposeLaunch(r, cfg)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, ".config", "agent-director", "launch", "store", "token", "droid-settings.json")
				if !reflect.DeepEqual(c.Create.Command, []string{cfg.Launch.DroidPath, "--settings", path}) || !reflect.DeepEqual(c.Files, []LaunchFile{{Path: path, Content: []byte(`{"model":"ordinary-custom-model"}`)}}) {
					t.Fatalf("composition: command=%q files=%+v", c.Create.Command, c.Files)
				}
				if c.Row.Agent != agent || c.Row.ParentID != "supervisor" || c.Row.RelayMode != "off" || !reflect.DeepEqual(c.Row.AgentArgs, args) || c.Create.Env["AGENT_DIRECTOR_AGENT"] != agent || c.Create.OrdinaryInputSafe {
					t.Fatalf("lane identity/settings: %+v", c)
				}
				if c.PreTrust != PreTrustSkipped || RunPreTrust(agent, c.PreTrust, home, nil, off, cfg.PreTrust) != PreTrustSkipped {
					t.Fatal("lane pre-trust was not skipped")
				}
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatalf("composition/pre-trust wrote files: %v, %v", entries, err)
				}
			}
		})
	}
}
