package spawn

import (
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
