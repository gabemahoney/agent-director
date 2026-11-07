package spawn

import (
	"maps"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestComposeEnv: the base keys, one AGENT_DIRECTOR_LABEL_<normalised key>
// per label and ExtraEnv verbatim, the same map on every composition.
func TestComposeEnv(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		InstanceID:          "id-abc",
		RelayMode:           "on",
		AgentDirectorLabels: map[string]string{"my-key": "v1", "another.k": "v2", "alreadyOK": "v3", "123numeric": "v4"},
		ExtraEnv:            map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "FOO": "bar"},
	}}
	want := map[string]string{
		"AGENT_DIRECTOR_INSTANCE_ID":      "id-abc",
		"AGENT_DIRECTOR_RELAY_MODE":       "on",
		"AGENT_DIRECTOR_LABEL_MY_KEY":     "v1",
		"AGENT_DIRECTOR_LABEL_ANOTHER_K":  "v2",
		"AGENT_DIRECTOR_LABEL_ALREADYOK":  "v3",
		"AGENT_DIRECTOR_LABEL_123NUMERIC": "v4",
		"ANTHROPIC_API_KEY":               "sk-ant-test",
		"FOO":                             "bar",
	}
	for i := 0; i < 2; i++ {
		if got := composeEnv(r); !reflect.DeepEqual(got, want) {
			t.Fatalf("composeEnv #%d = %v; want %v", i+1, got, want)
		}
	}
}

// TestComposeEnvByAgent: a Codex launch's environment adds the agent marker and
// an empty BASH_ENV (SR-10.1), a caller's BASH_ENV and CODEX_HOME win or pass
// through verbatim; a Claude launch's environment is exactly the base keys and
// ExtraEnv, whether the agent is named or left unset.
func TestComposeEnvByAgent(t *testing.T) {
	base := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-abc", "AGENT_DIRECTOR_RELAY_MODE": "on", "AGENT_DIRECTOR_LABEL_ROLE": "w"}
	with := func(extra ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(extra); i += 2 {
			out[extra[i]] = extra[i+1]
		}
		return out
	}
	cases := []struct {
		name     string
		agent    string
		extraEnv map[string]string
		want     map[string]string
	}{
		{"codex", store.AgentCodex, nil, with("AGENT_DIRECTOR_AGENT", "codex", "BASH_ENV", "")},
		{"codex caller BASH_ENV wins", store.AgentCodex, map[string]string{"BASH_ENV": "/etc/be"}, with("AGENT_DIRECTOR_AGENT", "codex", "BASH_ENV", "/etc/be")},
		{"codex CODEX_HOME passes through", store.AgentCodex, map[string]string{"CODEX_HOME": "/c/h"}, with("AGENT_DIRECTOR_AGENT", "codex", "BASH_ENV", "", "CODEX_HOME", "/c/h")},
		{"claude named", store.AgentClaude, map[string]string{"FOO": "bar"}, with("FOO", "bar")},
		{"claude unset", "", map[string]string{"BASH_ENV": "/etc/be"}, with("BASH_ENV", "/etc/be")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Resolved{SpawnParams: SpawnParams{Agent: tc.agent, InstanceID: "id-abc", RelayMode: "on",
				AgentDirectorLabels: map[string]string{"role": "w"}, ExtraEnv: tc.extraEnv}}
			if got := composeEnv(r); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("composeEnv = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestLaunchEnvClaudeConfigDir pins b.uzn: a fresh claude launch's session env
// carries the CLAUDE_CONFIG_DIR pre-trust targets: extra_env's when the key is
// present (empty included), else agent-director's own when set, else none, and
// its row records the same value. A resumed one carries only the row's extra
// env value, ignoring the resumer's. Codex and Cursor launches never take it
// from agent-director's env; the caller's extra env map is never written.
func TestLaunchEnvClaudeConfigDir(t *testing.T) {
	composers := []struct {
		name    string
		fresh   bool // a spawn: it inherits agent-director's value and carries a row whose extra env is checked
		compose func(t *testing.T, agent string, extra map[string]string) (env, row map[string]string)
	}{
		{"ComposeLaunch", true, func(t *testing.T, agent string, extra map[string]string) (map[string]string, map[string]string) {
			e := newLaunchEnv(t)
			if agent == store.AgentCursor {
				installCompositionGateway(t)
			}
			e.r.Agent, e.r.ExtraEnv = agent, extra
			c, err := ComposeLaunch(e.r, e.cfg)
			if err != nil {
				t.Fatalf("ComposeLaunch: %v", err)
			}
			return c.Create.Env, c.Row.ExtraEnv
		}},
		{"ComposeRelaunch", false, func(t *testing.T, agent string, extra map[string]string) (map[string]string, map[string]string) {
			e := newRelaunchEnv(t)
			if agent == store.AgentCursor {
				installCompositionGateway(t)
				e.row.JSONLPath = "/synthetic/s1.jsonl"
			}
			e.row.Agent, e.row.ExtraEnv, e.row.SessionID = agent, extra, "s1"
			c, err := ComposeRelaunch(RelaunchInput{Row: e.row, SessionID: "s1", Socket: e.socket, Token: relaunchToken, StoreID: relaunchStoreID}, e.cfg)
			if err != nil {
				t.Fatalf("ComposeRelaunch: %v", err)
			}
			return c.Create.Env, nil
		}},
	}
	cases := []struct {
		name, agent, inherited string
		extra                  map[string]string
		want                   string
		set                    bool // a fresh launch's env has CLAUDE_CONFIG_DIR, valued want
	}{
		{"claude inherited only", store.AgentClaude, "/inherited/cfg", nil, "/inherited/cfg", true},
		{"claude inherited, other extra_env keys", store.AgentClaude, "/inherited/cfg", map[string]string{"KEEP": "1"}, "/inherited/cfg", true},
		{"claude relative inherited kept", store.AgentClaude, "rel", nil, "rel", true},
		{"claude extra_env wins", store.AgentClaude, "/inherited/cfg", map[string]string{"CLAUDE_CONFIG_DIR": "/extra/cfg"}, "/extra/cfg", true},
		{"claude empty extra_env value kept", store.AgentClaude, "/inherited/cfg", map[string]string{"CLAUDE_CONFIG_DIR": ""}, "", true},
		{"claude neither", store.AgentClaude, "", nil, "", false},
		{"codex", store.AgentCodex, "/inherited/cfg", nil, "", false},
		{"cursor", store.AgentCursor, "/inherited/cfg", nil, "", false},
	}
	for _, c := range composers {
		for _, tc := range cases {
			t.Run(c.name+"/"+tc.name, func(t *testing.T) {
				t.Setenv("CLAUDE_CONFIG_DIR", tc.inherited)
				extra := maps.Clone(tc.extra)
				env, row := c.compose(t, tc.agent, extra)
				want, wantSet := tc.want, tc.set
				if _, ok := tc.extra["CLAUDE_CONFIG_DIR"]; !c.fresh && !ok {
					want, wantSet = "", false
				}
				if got, set := env["CLAUDE_CONFIG_DIR"]; set != wantSet || got != want {
					t.Errorf("env CLAUDE_CONFIG_DIR = %q (set %v); want %q (set %v)", got, set, want, wantSet)
				}
				if got, set := row["CLAUDE_CONFIG_DIR"]; c.fresh && (set != tc.set || got != tc.want) {
					t.Errorf("row extra env CLAUDE_CONFIG_DIR = %q (set %v); want %q (set %v)", got, set, tc.want, tc.set)
				}
				if !maps.Equal(extra, tc.extra) {
					t.Errorf("caller's extra env = %v after the composition; want unchanged %v", extra, tc.extra)
				}
			})
		}
	}
}

func TestNormalizeLabelKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo", "FOO"},
		{"my-key", "MY_KEY"},
		{"a.b.c", "A_B_C"},
		{"123x", "123X"},
		{"foo  bar", "FOO__BAR"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeLabelKey(tc.in); got != tc.want {
			t.Errorf("normalizeLabelKey(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}
