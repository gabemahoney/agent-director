package api_test

// invalid_env_key_test.go covers b.vpb: tmux splits each extra_env entry at its
// first '=', so a key that is empty or holds '=' or a NUL byte would set
// another variable than pre-trust and resume read (the key
// "CLAUDE_CONFIG_DIR=/x" sets CLAUDE_CONFIG_DIR). Spawn (the call's extra env
// or a template's) and resume (a row whose stored extra env has one) refuse it
// with ErrReservedEnvKey, the description saying what is wrong with the key,
// before anything is written or launched. The order against the reserved keys
// is pinned by TestValidateOrder (internal/spawn), the advice by
// advice_follow_invalid_env_key_test.go.

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// invalidEnvKeyRefusal is the ErrReservedEnvKey refusal of an extra env holding
// key, its description saying problem, with "<cfg>" in key standing for a
// directory holding a .claude.json.
func invalidEnvKeyRefusal(t *testing.T, key, problem string) envRefusal {
	t.Helper()
	c := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	key = strings.ReplaceAll(key, "<cfg>", c.dir)
	return envRefusal{extraEnv: map[string]string{key: "", "TEAM": "core"}, key: key,
		name: "ErrReservedEnvKey", reason: "is not a valid env-var name", detail: problem, cfgs: []trustConfig{c}}
}

// TestSpawnRefusesInvalidEnvKey: a malformed extra_env key, from the call or a
// template, is ErrReservedEnvKey quoting the key and saying what is wrong with
// it: no row written, no tmux call, no .claude.json touched.
func TestSpawnRefusesInvalidEnvKey(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name, key, problem string
		template           bool // the extra env comes from a template, the call sets none
	}{
		{name: "empty key", key: "", problem: "it is empty"},
		{name: "key CLAUDE_CONFIG_DIR=<cfg>, which tmux reads as CLAUDE_CONFIG_DIR", key: "CLAUDE_CONFIG_DIR=<cfg>", problem: "it contains '='"},
		{name: "key with a NUL byte", key: "A\x00B", problem: "it contains a NUL byte"},
		{name: "a template's key CLAUDE_CONFIG_DIR=<cfg>", key: "CLAUDE_CONFIG_DIR=<cfg>", problem: "it contains '='", template: true},
		{name: "a template's empty key", key: "", problem: "it is empty", template: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSpawnRefused(t, invalidEnvKeyRefusal(t, tc.key, tc.problem), tc.template)
		})
	}
}

// TestResumeRefusesInvalidEnvKey: an ended or missing row whose stored extra
// env has a malformed key is ErrReservedEnvKey, saying what is wrong with the
// key, before the transcript lookup (so even with its transcript gone): no tmux
// call, nothing written (row, history, trail), no .claude.json touched.
func TestResumeRefusesInvalidEnvKey(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name, state, key, problem string
		noTranscript              bool // the row's transcript is removed, so the lookup would fail
	}{
		{name: "ended, key CLAUDE_CONFIG_DIR=<cfg>", state: store.StateEnded, key: "CLAUDE_CONFIG_DIR=<cfg>", problem: "it contains '='"},
		{name: "missing, empty key", state: store.StateMissing, key: "", problem: "it is empty"},
		{name: "ended, key with a NUL byte", state: store.StateEnded, key: "A\x00B", problem: "it contains a NUL byte"},
		{name: "ended, key CLAUDE_CONFIG_DIR=<cfg>, transcript gone", state: store.StateEnded, key: "CLAUDE_CONFIG_DIR=<cfg>",
			problem: "it contains '='", noTranscript: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertResumeRefused(t, invalidEnvKeyRefusal(t, tc.key, tc.problem), tc.state, tc.noTranscript)
		})
	}
}
