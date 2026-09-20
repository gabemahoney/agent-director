package spawn

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// baseRelaunchRow builds a persisted-shaped store.Spawn for the Relaunch
// path. GetSpawn is what normally materializes this from the DB; the
// Relaunch unit tests feed the row directly so the assertion is scoped to
// Relaunch's synthesis + composeEnv, not the store round-trip (a sibling
// owns the store round-trip test in spawns_test.go).
func baseRelaunchRow() store.Spawn {
	return store.Spawn{
		ClaudeInstanceID: "id-relaunch-1",
		CWD:              "/tmp/relaunch-cwd",
		TmuxSessionName:  "cd-relaunch-1",
		RelayMode:        "off",
		ClaudeArgs:       []string{"--model", "opus"},
		Labels:           map[string]string{"role": "worker"},
	}
}

// TestRelaunchRestoresExtraEnvVerbatim pins the Epic AC: Relaunch's
// synthesized Resolved carries in.Row.ExtraEnv verbatim, and composeEnv
// receives it so the env map handed to TmuxClient.NewSession contains the
// ExtraEnv keys/values. This is the resume-side restore of SR-10 — a
// resumed spawn keeps CLAUDE_CONFIG_DIR and any auth vars captured at
// launch.
func TestRelaunchRestoresExtraEnvVerbatim(t *testing.T) {
	withStubExe(t, "/bin/agent-director")

	row := baseRelaunchRow()
	row.ExtraEnv = map[string]string{
		"CLAUDE_CONFIG_DIR":       "/home/bee/.claude-alt",
		"ANTHROPIC_API_KEY":       "sk-ant-test",
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-ant-oat01-test",
	}
	tmux := &captureTmux{}

	if err := Relaunch(RelaunchInput{Row: row, SessionID: "session-uuid-1"}, tmux, config.Default()); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if !tmux.got.called {
		t.Fatal("tmux.NewSession not called")
	}

	// composeEnv received the row's ExtraEnv verbatim: every key/value
	// lands in the env map passed to NewSession.
	for k, want := range row.ExtraEnv {
		if got := tmux.got.envs[k]; got != want {
			t.Errorf("relaunch env[%q] = %q; want %q (ExtraEnv restored verbatim)", k, got, want)
		}
	}

	// The resume argv still carries `claude --resume <session_id> --settings`.
	if len(tmux.got.command) < 4 ||
		tmux.got.command[0] != "claude" ||
		tmux.got.command[1] != "--resume" ||
		tmux.got.command[2] != "session-uuid-1" ||
		tmux.got.command[3] != "--settings" {
		t.Errorf("resume argv prefix = %v", tmux.got.command)
	}
}

// TestRelaunchLeavesPermissionsNil pins SR-10's non-goal: Permissions is
// NOT persisted, so Relaunch cannot reconstruct it. It stays nil on the
// synthesized Resolved, which mergePermissions must tolerate (only the
// config-default deny applies). We assert the synthesized settings never
// carry a per-spawn allow/ask overlay by driving the full Relaunch: a nil
// Permissions must not panic and must still produce a session.
func TestRelaunchLeavesPermissionsNil(t *testing.T) {
	withStubExe(t, "/bin/agent-director")

	row := baseRelaunchRow()
	// A row never carries Permissions (no such column). Confirm the
	// synthesized Resolved keeps it nil by exercising the path — the
	// direct field check is done via the Resolved synthesized inside
	// Relaunch, mirrored here: the store.Spawn has no Permissions field,
	// so there is nothing to carry.
	tmux := &captureTmux{}
	if err := Relaunch(RelaunchInput{Row: row, SessionID: "s1"}, tmux, config.Default()); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	if !tmux.got.called {
		t.Fatal("tmux.NewSession not called")
	}
	// Guard against store.Spawn ever growing a Permissions field that a
	// future Relaunch might wire in without a persistence story: the row
	// type must not expose one.
	if _, has := reflect.TypeOf(store.Spawn{}).FieldByName("Permissions"); has {
		t.Error("store.Spawn grew a Permissions field; Relaunch must not reconstruct un-persisted Permissions")
	}
}

// TestRelaunchLegacyEmptyExtraEnvBaseline is the PM-required legacy
// baseline (Epic AC: "a row with '{}' extra_env resumes exactly as
// today"). A row whose ExtraEnv is empty/nil — the shape GetSpawn
// materializes from the '{}' default column — must produce EXACTLY the
// pre-SR-10 env set: no ExtraEnv keys leak in. The expected set is
// constructed from what composeEnv emits without ExtraEnv:
// AGENT_DIRECTOR_INSTANCE_ID, AGENT_DIRECTOR_RELAY_MODE, and one
// AGENT_DIRECTOR_LABEL_<KEY> per row label.
func TestRelaunchLegacyEmptyExtraEnvBaseline(t *testing.T) {
	withStubExe(t, "/bin/agent-director")

	row := baseRelaunchRow()
	// GetSpawn decodes '{}' into an empty non-nil map; nil behaves
	// identically through composeEnv's range. Assert both shapes yield
	// the same baseline set.
	for _, extra := range []map[string]string{nil, {}} {
		row.ExtraEnv = extra
		tmux := &captureTmux{}
		if err := Relaunch(RelaunchInput{Row: row, SessionID: "s1"}, tmux, config.Default()); err != nil {
			t.Fatalf("Relaunch: %v", err)
		}

		// The exact pre-change env key set: base keys + one label var per
		// row label. Nothing else.
		want := map[string]string{
			"AGENT_DIRECTOR_INSTANCE_ID": row.ClaudeInstanceID,
			"AGENT_DIRECTOR_RELAY_MODE":  row.RelayMode,
		}
		for k, v := range row.Labels {
			want["AGENT_DIRECTOR_LABEL_"+normalizeLabelKey(k)] = v
		}

		if !reflect.DeepEqual(tmux.got.envs, want) {
			t.Errorf("ExtraEnv=%v: relaunch env = %v; want exactly %v (no new keys vs pre-SR-10 baseline)",
				extra, tmux.got.envs, want)
		}
		// Explicit key-set guard so a future stray key is named, not just
		// diffed as a map.
		if got, wantKeys := sortedKeys(tmux.got.envs), sortedKeys(want); !reflect.DeepEqual(got, wantKeys) {
			t.Errorf("ExtraEnv=%v: env key set = %v; want %v", extra, got, wantKeys)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
