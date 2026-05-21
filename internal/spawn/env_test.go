package spawn

import (
	"reflect"
	"sort"
	"testing"
)

func TestComposeEnvBaseKeys(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID: "id-abc",
		RelayMode:        "on",
	}}
	env := composeEnv(r)
	if env["CLAUDE_DIRECTOR_INSTANCE_ID"] != "id-abc" {
		t.Errorf("CLAUDE_DIRECTOR_INSTANCE_ID = %q; want id-abc", env["CLAUDE_DIRECTOR_INSTANCE_ID"])
	}
	if env["CLAUDE_DIRECTOR_RELAY_MODE"] != "on" {
		t.Errorf("CLAUDE_DIRECTOR_RELAY_MODE = %q; want on", env["CLAUDE_DIRECTOR_RELAY_MODE"])
	}
}

func TestComposeEnvLabelsAreNormalized(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID: "id-abc",
		RelayMode:        "off",
		AgentDirectorLabels: map[string]string{
			"my-key":     "v1",
			"another.k":  "v2",
			"alreadyOK":  "v3",
			"123numeric": "v4",
		},
	}}
	env := composeEnv(r)
	cases := map[string]string{
		"CLAUDE_DIRECTOR_LABEL_MY_KEY":     "v1",
		"CLAUDE_DIRECTOR_LABEL_ANOTHER_K":  "v2",
		"CLAUDE_DIRECTOR_LABEL_ALREADYOK":  "v3",
		"CLAUDE_DIRECTOR_LABEL_123NUMERIC": "v4",
	}
	for k, want := range cases {
		if env[k] != want {
			t.Errorf("env[%q] = %q; want %q", k, env[k], want)
		}
	}
}

func TestComposeEnvExtraEnvPassthrough(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID: "id-abc",
		RelayMode:        "off",
		ExtraEnv: map[string]string{
			"ANTHROPIC_API_KEY":       "sk-ant-test",
			"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test",
			"FOO":                     "bar",
		},
	}}
	env := composeEnv(r)
	wants := map[string]string{
		"ANTHROPIC_API_KEY":       "sk-ant-test",
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test",
		"FOO":                     "bar",
	}
	for k, want := range wants {
		if env[k] != want {
			t.Errorf("env[%q] = %q; want %q", k, env[k], want)
		}
	}
}

func TestComposeEnvDeterministic(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID:     "id",
		RelayMode:            "off",
		AgentDirectorLabels: map[string]string{"k1": "v1", "k2": "v2"},
		ExtraEnv:             map[string]string{"E": "1"},
	}}
	env1 := composeEnv(r)
	env2 := composeEnv(r)
	// Two compositions of the same input must yield exactly the same map.
	if !reflect.DeepEqual(keysSorted(env1), keysSorted(env2)) {
		t.Fatalf("composeEnv key set not stable: %v vs %v", keysSorted(env1), keysSorted(env2))
	}
}

func keysSorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
