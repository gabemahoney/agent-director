package api_test

// reserved_home_test.go covers b.nas: HOME in extra_env would send the agent's
// hook events to another agent-director store, so spawn (the call's extra env
// or a template's) and resume (a row whose stored extra env has HOME) refuse a
// key that sets HOME, "HOME" or one such as "HOME=/tmp/b", with
// ErrReservedEnvKey before anything is written or launched. A spawn's relative
// or empty HOME is pinned by TestValidateOrder (internal/spawn), a reuse's
// refusal and the advice by advice_follow_reserved_home_test.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reservedHomeEnv is an extra env setting key to home, where "B" stands for
// b's directory, and CLAUDE_CONFIG_DIR to c's when withCfg is set.
func reservedHomeEnv(key, home string, withCfg bool, b, c trustConfig) map[string]string {
	if home == "B" {
		home = b.dir
	}
	env := map[string]string{key: home, "TEAM": "core"}
	if withCfg {
		env["CLAUDE_CONFIG_DIR"] = c.dir
	}
	return env
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
			env := newSpawnEnv(t)
			own := seedTrustConfig(t, env.home, trustLacksEntry)
			b, c := seedTrustConfig(t, t.TempDir(), trustLacksEntry), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			p := api.SpawnParams{CWD: t.TempDir(), ExtraEnv: reservedHomeEnv(tc.key, tc.home, tc.withCfg, b, c)}
			if tc.template {
				body := fmt.Sprintf("[extra_env]\n%q = %q\n", tc.key, p.ExtraEnv[tc.key])
				if _, err := apitest.SeedTemplate(filepath.Join(env.home, ".agent-director", "templates"), "home-env", body); err != nil {
					t.Fatalf("SeedTemplate: %v", err)
				}
				p.Template, p.ExtraEnv = "home-env", nil
			}
			rows := listIDs(t, env.c)

			_, err := env.c.Spawn(p)

			assertOneSentinel(t, err, spawn.ErrReservedEnvKey)
			want := "ErrReservedEnvKey: extra_env key " + strconv.Quote(tc.key) + " sets HOME, which is reserved: "
			if desc := errText(err); !strings.HasPrefix(desc, want) || !strings.Contains(desc, "; remove "+strconv.Quote(tc.key)+" from extra_env, and ") {
				t.Errorf("description %q\nwant it to start %q and say to remove that key", desc, want)
			}
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); !reflect.DeepEqual(ids, rows) {
				t.Errorf("List ids = %q; want unchanged %q", ids, rows)
			}
			for _, cfg := range []trustConfig{own, b, c} {
				cfg.check(t, "", false, "after the refused spawn")
			}
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
			e := newKillEnv(t)
			b, c := seedTrustConfig(t, t.TempDir(), trustLacksEntry), seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			spec := e.resumableSpec(rlkSettled(e), agentGone, apitest.WithExtraEnv(reservedHomeEnv(tc.key, tc.home, tc.withCfg, b, c)))
			spec.State = tc.state
			r := e.seedResumableRow(t, spec)
			if tc.noTranscript {
				if err := os.Remove(r.JSONLPath); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
			}
			before := e.snapshotResume(t, r)

			_, err := e.resume(r.ID)

			assertOneName(t, err, "ErrReservedEnvKey")
			want := "ErrReservedEnvKey: resume of instance " + r.ID + ": the row's extra_env key " + strconv.Quote(tc.key) + " sets HOME, which is reserved: "
			if desc := errText(err); !strings.HasPrefix(desc, want) || !strings.Contains(desc, "nothing was written and nothing was launched") {
				t.Errorf("description %q\nwant it to start %q and say nothing was written or launched", desc, want)
			}
			e.assertKillCalls(t)
			e.assertResumeWroteNothing(t, before)
			for _, cfg := range []trustConfig{b, c} {
				cfg.check(t, "", false, "after the refused resume")
			}
		})
	}
}
