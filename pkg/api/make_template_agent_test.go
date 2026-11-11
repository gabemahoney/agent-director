package api_test

// make_template_agent_test.go covers make-template's agent handling (SR-1.4,
// SR-4.4): the order of its checks beside the name, relay_mode and extra_env
// ones, each refusal leaving no templates dir, and the agent a stored template
// records.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// The make-template descriptions that open each refusal.
const (
	mtRelayStart      = `ErrTemplateMalformed: relay_mode "maybe" (want on/off)`
	mtAgentStart      = `ErrTemplateMalformed: agent "gemini" (want claude, codex or cursor)`
	mtAgentMissingTxt = "ErrTemplateAgentMissing: agent_args are set but no agent is given; pass agent claude, codex or cursor, or remove agent_args"
	mtReservedHome    = `ErrReservedEnvKey: extra_env key "HOME" sets HOME, which is reserved: `
)

func TestPaneOnlyMakeTemplateRefusalsWriteNothing(t *testing.T) {
	// Serial: each case uses a temporary HOME and checks the trail.
	const rejected = "private-lane-sentinel"
	for _, agent := range []string{"droid", "opencode"} {
		type refusal struct {
			name, want string
			p          api.MakeTemplateParams
		}
		cases := []refusal{
			{"joined model", "ErrSpawnDeniedFlag", api.MakeTemplateParams{AgentArgs: []string{"--model=" + rejected}}},
			{"custom model", "ErrSpawnDeniedFlag", api.MakeTemplateParams{AgentArgs: []string{"--model", "custom:" + rejected}}},
			{"relay", "ErrAgentCapabilityUnsupported", api.MakeTemplateParams{RelayMode: "on"}},
			{"allow", "ErrPermissionsUnsupported", api.MakeTemplateParams{Permissions: &api.MakeTemplatePermissions{Allow: []string{rejected}}}},
			{"deny", "ErrPermissionsUnsupported", api.MakeTemplateParams{Permissions: &api.MakeTemplatePermissions{Deny: []string{rejected}}}},
			{"ask", "ErrPermissionsUnsupported", api.MakeTemplateParams{Permissions: &api.MakeTemplatePermissions{Ask: []string{rejected}}}},
		}
		keys := []string{"FACTORY_API_KEY", "FACTORY_HOME_OVERRIDE"}
		if agent == "opencode" {
			keys = []string{"OPENROUTER_API_KEY", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_AUTH_CONTENT", "OPENCODE_PERMISSION"}
		}
		for _, key := range keys {
			for _, value := range []string{"", rejected} {
				cases = append(cases, refusal{"env/" + key, "ErrReservedEnvKey", api.MakeTemplateParams{ExtraEnv: map[string]string{key: value}}})
			}
		}
		for _, tc := range cases {
			for _, existing := range []bool{false, true} {
				name := "create"
				if existing {
					name = "replace"
				}
				t.Run(agent+"/"+tc.name+"/"+name, func(t *testing.T) {
					e := newSpawnEnv(t)
					c := agentClientWithConfig(t, e, "[experimental]\n"+agent+" = true\n")
					p := tc.p
					p.Agent, p.Name = agent, "lane"
					p.Overwrite = existing
					if existing {
						seedTemplateFile(t, p.Name)
					}
					err := e.checkCreationWrites(t, func() error {
						_, err := c.MakeTemplate(p)
						return err
					})
					assertOneName(t, err, tc.want)
					if strings.Contains(errText(err), rejected) {
						t.Errorf("refusal exposed a rejected value: %v", err)
					}
				})
			}
		}
	}
}

// Client advice lists an experimental agent only after its flag is enabled.
func TestMakeTemplateAgentChoicesFollowExperimentalFlag(t *testing.T) {
	// Serial: it sets HOME and checks all trail writes around each refusal.
	for _, tc := range []struct{ enabled, choices string }{
		{"false", "claude, codex or cursor"},
		{"true", "claude, codex, cursor or droid"},
	} {
		t.Run(tc.enabled, func(t *testing.T) {
			e := newSpawnEnv(t)
			c := agentClientWithConfig(t, e, "[experimental]\ndroid = "+tc.enabled+"\n")
			err := e.checkCreationWrites(t, func() error {
				_, err := c.MakeTemplate(api.MakeTemplateParams{Name: "choices", Agent: "gemini"})
				return err
			})
			assertOneName(t, err, "ErrTemplateMalformed")
			if want := `ErrTemplateMalformed: agent "gemini" (want ` + tc.choices + `)`; errText(err) != want {
				t.Errorf("refusal = %q; want %q", errText(err), want)
			}
		})
	}
}

// TestMakeTemplateAgentCheckOrder (SR-1.4): make-template checks the name, then relay_mode, then the agent
// (invalid, agent_args without one, codex permissions), then extra_env; the first failing check is the error
// and the call creates no templates dir.
func TestMakeTemplateAgentCheckOrder(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	args := []string{"--model", "x"}
	homeEnv := map[string]string{"HOME": "/x"}
	codexAllow := &api.MakeTemplatePermissions{Allow: []string{"Bash(jq)"}}
	cases := []struct {
		name, wantName, wantText string
		p                        api.MakeTemplateParams
	}{
		{"name over relay_mode", "ErrTemplateNameUnsafe", "", api.MakeTemplateParams{Name: "../x", Agent: "claude", RelayMode: "maybe"}},
		{"relay_mode over an invalid agent", "ErrTemplateMalformed", mtRelayStart, api.MakeTemplateParams{Name: "n", Agent: "gemini", RelayMode: "maybe"}},
		{"relay_mode over agent_args with no agent", "ErrTemplateMalformed", mtRelayStart, api.MakeTemplateParams{Name: "n", AgentArgs: args, RelayMode: "maybe"}},
		{"relay_mode over codex permissions", "ErrTemplateMalformed", mtRelayStart,
			api.MakeTemplateParams{Name: "n", Agent: "codex", Permissions: codexAllow, RelayMode: "maybe"}},
		{"an invalid agent over a reserved env key", "ErrTemplateMalformed", mtAgentStart, api.MakeTemplateParams{Name: "n", Agent: "gemini", ExtraEnv: homeEnv}},
		{"agent_args with no agent over a reserved env key", "ErrTemplateAgentMissing", mtAgentMissingTxt,
			api.MakeTemplateParams{Name: "n", AgentArgs: args, ExtraEnv: homeEnv}},
		{"codex permissions over a reserved env key", "ErrPermissionsUnsupported", permissionsText,
			api.MakeTemplateParams{Name: "n", Agent: "codex", Permissions: codexAllow, ExtraEnv: homeEnv}},

		{"cursor allow", "ErrPermissionsUnsupported", "ErrPermissionsUnsupported:", api.MakeTemplateParams{Name: "n", Agent: "cursor", Permissions: &api.MakeTemplatePermissions{Allow: []string{"Bash"}}}},
		{"cursor deny", "ErrPermissionsUnsupported", "ErrPermissionsUnsupported:", api.MakeTemplateParams{Name: "n", Agent: "cursor", Permissions: &api.MakeTemplatePermissions{Deny: []string{"Bash"}}}},
		{"cursor ask", "ErrPermissionsUnsupported", "ErrPermissionsUnsupported:", api.MakeTemplateParams{Name: "n", Agent: "cursor", Permissions: &api.MakeTemplatePermissions{Ask: []string{"Bash"}}}},
		{"cursor split flag before env", "ErrSpawnDeniedFlag", "ErrSpawnDeniedFlag: --sandbox", api.MakeTemplateParams{Name: "n", Agent: "cursor", AgentArgs: []string{"--sandbox", "sensitive-value"}, ExtraEnv: homeEnv}},
		{"cursor malformed delimiter", "ErrSpawnDeniedFlag", "ErrSpawnDeniedFlag:", api.MakeTemplateParams{Name: "n", Agent: "cursor", AgentArgs: []string{"chat", "--mode", "plan", "--"}}},
		{"a reserved env key with a valid agent", "ErrReservedEnvKey", mtReservedHome, api.MakeTemplateParams{Name: "n", Agent: "claude", ExtraEnv: homeEnv}},
		{"an upper-case agent", "ErrTemplateMalformed", `ErrTemplateMalformed: agent "Claude" (want claude, codex or cursor)`, api.MakeTemplateParams{Name: "n", Agent: "Claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := withTempHome(t)

			_, err := api.MakeTemplate(tc.p)

			assertOneName(t, err, tc.wantName)
			if !strings.HasPrefix(errText(err), tc.wantText) {
				t.Errorf("description %q\nwant it to start %q", errText(err), tc.wantText)
			}
			if _, err := os.Stat(filepath.Join(adConfigDir(home), "templates")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("templates dir: %v; want none created", err)
			}
		})
	}
}

// TestMakeTemplateStoresTheAgent (SR-4.4): the template records the agent the call names, its agent_args beside
// it, and no agent key when none is given; config.LoadTemplate reads each back as stored.
func TestMakeTemplateStoresTheAgent(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	args := []string{"--model", "x"}
	agentKey := regexp.MustCompile(`(?m)^agent\s*=`)
	cases := []struct {
		name     string
		p        api.MakeTemplateParams
		want     config.TemplateFile
		wantKey  bool
		wantBody string // a line the file holds, "" = none checked
	}{
		{"claude with agent_args", api.MakeTemplateParams{Agent: "claude", CWD: "/w", AgentArgs: args},
			config.TemplateFile{Agent: "claude", CWD: "/w", AgentArgs: args}, true, `agent = "claude"`},
		{"codex with agent_args", api.MakeTemplateParams{Agent: "codex", CWD: "/w", AgentArgs: args},
			config.TemplateFile{Agent: "codex", CWD: "/w", AgentArgs: args}, true, `agent = "codex"`},
		{"codex with no lists", api.MakeTemplateParams{Agent: "codex", CWD: "/w"},
			config.TemplateFile{Agent: "codex", CWD: "/w"}, true, `agent = "codex"`},
		{"codex with an empty permissions struct", api.MakeTemplateParams{Agent: "codex", CWD: "/w", Permissions: &api.MakeTemplatePermissions{}},
			config.TemplateFile{Agent: "codex", CWD: "/w", Permissions: &config.TemplatePermissions{}}, true, `agent = "codex"`},
		{"cursor", api.MakeTemplateParams{Agent: "cursor", CWD: "/w", AgentArgs: args}, config.TemplateFile{Agent: "cursor", CWD: "/w", AgentArgs: args}, true, `agent = "cursor"`},
		{"cursor mode and literal prompt", api.MakeTemplateParams{Agent: "cursor", AgentArgs: []string{"--plan", "--", "--resume\n多行 🦋"}}, config.TemplateFile{Agent: "cursor", AgentArgs: []string{"--plan", "--", "--resume\n多行 🦋"}}, true, `agent = "cursor"`},
		{"cursor empty permission lists", api.MakeTemplateParams{Agent: "cursor", Permissions: &api.MakeTemplatePermissions{}}, config.TemplateFile{Agent: "cursor", Permissions: &config.TemplatePermissions{}}, true, `agent = "cursor"`},
		{"no agent", api.MakeTemplateParams{CWD: "/w"}, config.TemplateFile{CWD: "/w"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempHome(t)
			tc.p.Name = "stored"

			res, err := api.MakeTemplate(tc.p)

			if err != nil {
				t.Fatalf("MakeTemplate: %v", err)
			}
			body, _ := os.ReadFile(res.Path)
			if agentKey.Match(body) != tc.wantKey || (tc.wantBody != "" && !strings.Contains(string(body), tc.wantBody)) {
				t.Errorf("template body:\n%s\nwant the agent key present = %v, holding %q", body, tc.wantKey, tc.wantBody)
			}
			got, err := config.LoadTemplate("stored")
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("LoadTemplate = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
