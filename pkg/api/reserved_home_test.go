package api_test

// reserved_home_test.go covers b.nas: HOME in extra_env would send the agent's
// hook events to another agent-director store, so spawn (the call's extra env
// or a template's) and resume (a row whose stored extra env has HOME) refuse a
// key that sets HOME, "HOME" or one such as "HOME=/tmp/b", with
// ErrReservedEnvKey before anything is written or launched. A spawn's relative
// or empty HOME is pinned by TestValidateOrder (internal/spawn), a reuse's
// refusal and the advice by advice_follow_reserved_home_test.go.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// reservedHomeRefusal is the ErrReservedEnvKey refusal of an extra env setting
// key to home, where "B" stands for a directory b holding a .claude.json, and
// CLAUDE_CONFIG_DIR to another such directory c when withCfg is set.
func reservedHomeRefusal(t *testing.T, key, home string, withCfg bool) envRefusal {
	t.Helper()
	b, c := seedTrustConfig(t, t.TempDir(), trustLacksEntry), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
	if home == "B" {
		home = b.dir
	}
	env := map[string]string{key: home, "TEAM": "core"}
	if withCfg {
		env["CLAUDE_CONFIG_DIR"] = c.dir
	}
	return envRefusal{extraEnv: env, key: key, name: "ErrReservedEnvKey", reason: "sets HOME, which is reserved", cfgs: []trustConfig{b, c}}
}

// TestSpawnRefusesHomeInExtraEnv: a key that sets HOME in extra_env, from the
// call or a template, is ErrReservedEnvKey quoting the key: no row written, no
// tmux call, no .claude.json touched.
func TestSpawnRefusesHomeInExtraEnv(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name, key string
		home      string // the key's value; "B" is a directory holding a .claude.json
		withCfg   bool   // CLAUDE_CONFIG_DIR is also set, to another such directory
		template  bool   // the extra env comes from a template, the call sets none
	}{
		{name: "absolute HOME", key: "HOME", home: "B"},
		{name: "HOME beside CLAUDE_CONFIG_DIR", key: "HOME", home: "B", withCfg: true},
		{name: "a template's HOME", key: "HOME", home: "B", template: true},
		{name: "key HOME=/tmp/b, which tmux splits at its first '='", key: "HOME=/tmp/b"},
		{name: "a template's key HOME=/tmp/b", key: "HOME=/tmp/b", template: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSpawnRefused(t, reservedHomeRefusal(t, tc.key, tc.home, tc.withCfg), tc.template)
		})
	}
}

// TestResumeRefusesHomeInExtraEnv: an ended or missing row whose stored extra
// env has a key that sets HOME, whatever its value, is ErrReservedEnvKey before
// the transcript lookup (so even with its transcript gone): no tmux call,
// nothing written (row, history, trail), no .claude.json touched.
func TestResumeRefusesHomeInExtraEnv(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name, state, key string
		home             string // the key's value; "B" is a directory holding a .claude.json
		withCfg          bool   // CLAUDE_CONFIG_DIR is also set, to another such directory
		noTranscript     bool   // the row's transcript is removed, so the lookup would fail
	}{
		{name: "ended, absolute HOME", state: store.StateEnded, key: "HOME", home: "B"},
		{name: "missing, absolute HOME beside CLAUDE_CONFIG_DIR", state: store.StateMissing, key: "HOME", home: "B", withCfg: true},
		{name: "ended, relative HOME", state: store.StateEnded, key: "HOME", home: "rel"},
		{name: "missing, empty HOME", state: store.StateMissing, key: "HOME"},
		{name: "ended, key HOME=/tmp/b", state: store.StateEnded, key: "HOME=/tmp/b"},
		{name: "ended, absolute HOME, transcript gone", state: store.StateEnded, key: "HOME", home: "B", noTranscript: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertResumeRefused(t, reservedHomeRefusal(t, tc.key, tc.home, tc.withCfg), tc.state, tc.noTranscript)
		})
	}
}
