package spawn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

func TestPaneOnlyCompositionAndPreTrust(t *testing.T) {
	for _, agent := range []string{"droid", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "supervisor")
			cfg := config.Default()
			cfg.Defaults.RelayMode = "on"
			cfg.Defaults.InjectHelpHook = true
			cfg.Experimental.Droid = true
			cfg.Experimental.OpenCode = true
			t.Setenv("OPENROUTER_API_KEY", "synthetic-composition-key")
			cfg.Launch.DroidPath = "/opt/my tools/droid"
			cfg.Launch.OpenCodePath = "/opt/my tools/opencode"
			args := []string{"--model", "ordinary-custom-model"}
			if agent == "opencode" {
				args[1] = "openrouter/provider/model"
			}
			for _, off := range []bool{false, true} {
				r, err := Resolve(SpawnParams{Agent: agent, CWD: home, AgentArgs: args, NoPreTrust: off, ExtraEnv: map[string]string{"OPENCODE_DISABLE_AUTOUPDATE": "0"}}, cfg)
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
				if agent == "droid" {
					path := filepath.Join(home, ".config", "agent-director", "launch", "store", "token", "droid-settings.json")
					if !reflect.DeepEqual(c.Create.Command, []string{cfg.Launch.DroidPath, "--settings", path}) || !reflect.DeepEqual(c.Files, []LaunchFile{{Path: path, Content: []byte(`{"model":"ordinary-custom-model"}`)}}) {
						t.Fatalf("composition: command=%q files=%+v", c.Create.Command, c.Files)
					}
					keys := []string{"FACTORY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "FACTORY_HOME_OVERRIDE"}
					// A nil unset list retains the documented legacy exclusion-to-unset alias.
					unsets := c.Create.UnsetEnv
					if unsets == nil {
						unsets = c.Create.ExcludeEnv
					}
					if !slices.Equal(c.Create.ExcludeEnv, keys) || !slices.Equal(unsets, keys) || len(c.Create.ForwardEnv) != 0 {
						t.Fatalf("Droid environment policy: %+v", c.Create)
					}
				} else {
					if !slices.Equal(c.Create.Command, []string{cfg.Launch.OpenCodePath, "--model", args[1]}) || len(c.Files) != 0 {
						t.Fatalf("composition: command=%q files=%+v", c.Create.Command, c.Files)
					}
					var got, want any
					if err := json.Unmarshal([]byte(c.Create.Env["OPENCODE_CONFIG_CONTENT"]), &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(`{"enabled_providers":["openrouter"],"permission":{"edit":"ask","bash":"ask"},"autoupdate":false,"provider":{"openrouter":{"options":{"apiKey":"{env:OPENROUTER_API_KEY}"}}}}`), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) || c.Create.Env["OPENCODE_DISABLE_AUTOUPDATE"] != "1" {
						t.Fatalf("native configuration: %+v", c.Create.Env)
					}
					if !slices.Equal(c.Create.ExcludeEnv, []string{"OPENROUTER_API_KEY", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"}) || !slices.Equal(c.Create.UnsetEnv, []string{"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"}) || !slices.Equal(c.Create.ForwardEnv, []string{"OPENROUTER_API_KEY"}) {
						t.Fatalf("OpenCode environment policy: %+v", c.Create)
					}
					cfg.Launch.OpenCodePath = ""
					bare, err := ComposeLaunch(r, cfg)
					if err != nil || !slices.Equal(bare.Create.Command, []string{"opencode", "--model", args[1]}) {
						t.Fatalf("default program: %+v, %v", bare, err)
					}
					cfg.Launch.OpenCodePath = "/opt/my tools/opencode"
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
