package spawn

import (
	"reflect"
	"testing"
)

// TestComposeEnv: the base keys, one AGENT_DIRECTOR_LABEL_<normalised key>
// per label and ExtraEnv verbatim, the same map on every composition.
func TestComposeEnv(t *testing.T) {
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID:    "id-abc",
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
