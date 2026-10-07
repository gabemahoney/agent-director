package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpInitialize is the MCP initialize request line sent to `serve --stdio`.
const mcpInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05",` +
	`"capabilities":{},"clientInfo":{"name":"tmux-config-test","version":"0"}}}` + "\n"

// configRefusal is one refused config: the [tmux] settings and the keys
// outside [tmux] (keys: [defaults] expire_retention_days, b.sgw; [relay] and
// [pause] timeout_seconds, b.8q2; [pre_trust] lock_wait_seconds, b.kr4; each
// written when non-zero) written, the values its err_description must state
// as refused (nil: malformed type, only err_name and path are asserted) and
// the [tmux] settings that fix the file (the keys outside [tmux] dropped).
// A raw file is written as given instead, and refused for setting one key
// under the caseVariant names (b.p8n).
type configRefusal struct {
	name        string
	keys        apitest.ConfigKeys
	bad         []apitest.TmuxSetting
	refused     []apitest.ConfigRefusal
	raw         string
	caseVariant []string
	fix         []apitest.TmuxSetting
}

// configRefusals is the one table of refused [tmux], [defaults], [relay],
// [pause] and [pre_trust] values driving every surface check: a minimum, the
// derived grace rule, every table at once, a malformed type and db_path set
// under two letter cases of [store] (b.p8n). The per-key refusals are
// internal/config's config_errors_test.go and config_timeouts_test.go.
func configRefusals() []configRefusal {
	window, grace := config.TmuxStoppingWindowSeconds, config.TmuxPendingGraceSeconds
	create, kill := config.TmuxCreateTimeoutMs, config.TmuxKillExitWaitMs

	// A create timeout whose derived grace minimum exceeds the grace default.
	defaultBreakingCreate := int64(config.DefaultPendingGraceSeconds-config.PendingGraceMarginSeconds+1) * 1000
	defaultGraceMin := config.PendingGraceMinimumSeconds(defaultBreakingCreate, 0)
	// The largest create_timeout_ms plus pipe_close_wait_ms at which the grace default loads (b.n4q).
	defaultGraceTotal := int64(config.DefaultPendingGraceSeconds-config.PendingGraceMarginSeconds) * 1000

	return []configRefusal{
		{
			name:    "stopping_window_below_minimum",
			bad:     []apitest.TmuxSetting{apitest.TmuxInt(window, 10)},
			refused: []apitest.ConfigRefusal{{Key: window, Value: 10, Minimum: config.MinStoppingWindowSeconds}},
			fix:     []apitest.TmuxSetting{apitest.TmuxInt(window, 0)},
		},
		{
			name: "grace_default_below_derived_minimum",
			bad:  []apitest.TmuxSetting{apitest.TmuxInt(create, defaultBreakingCreate)},
			refused: []apitest.ConfigRefusal{{Key: grace, Minimum: defaultGraceMin,
				Derived: true, Create: defaultBreakingCreate, Pipe: config.DefaultPipeCloseWaitMs, Total: defaultGraceTotal}},
			fix: []apitest.TmuxSetting{apitest.TmuxInt(create, 0)},
		},
		{
			name: "every_table_refused",
			keys: apitest.ConfigKeys{RetentionDays: -1, RelayTimeoutSeconds: -1,
				PauseTimeoutSeconds: int64(config.MaxPauseTimeoutSeconds) + 1, PreTrustLockWaitSeconds: -1},
			bad: []apitest.TmuxSetting{apitest.TmuxInt(kill, -1)},
			refused: []apitest.ConfigRefusal{{Retention: true, Value: -1}, {RelayTimeout: true, Value: -1},
				{PauseTimeout: true, Value: int64(config.MaxPauseTimeoutSeconds) + 1},
				{PreTrustLockWait: true, Value: -1}, {Key: kill, Value: -1}},
			fix: []apitest.TmuxSetting{apitest.TmuxInt(kill, 0)},
		},
		{
			name: "string_stopping_window",
			bad:  []apitest.TmuxSetting{apitest.TmuxString(window, "ninety")},
		},
		{
			// One spelling names the seeded store, so a hook that read it would apply.
			name:        "db_path_under_two_letter_cases",
			raw:         "[Store]\ndb_path = \"~/.agent-director/state.db\"\n\n[store]\ndb_path = \"~/other.db\"\n",
			caseVariant: []string{"[Store] db_path", "[store] db_path"},
		},
	}
}

// refusedHome is a throwaway HOME holding a pending relay-on row seeded before
// the refused config was written. The row's pane process is this test process
// (SR-22.9), so a hook the refusal failed to stop would apply and be caught.
type refusedHome struct {
	home, cfgPath, instanceID string
}

func newRefusedHome(t *testing.T, rc configRefusal) refusedHome {
	t.Helper()
	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StatePending, "", hook.RelayModeOn, "", true, withTestProcessPane(t))
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	h := refusedHome{home: home, cfgPath: filepath.Join(directorDir(home), "config.toml"), instanceID: id}
	if rc.raw == "" {
		apitest.WriteKeysConfig(t, h.cfgPath, rc.keys, rc.bad...)
	} else if err := os.WriteFile(h.cfgPath, []byte(rc.raw), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return h
}

// repair rewrites the config to the row's valid state.
func (h refusedHome) repair(t *testing.T, rc configRefusal) {
	t.Helper()
	apitest.WriteTmuxConfig(t, h.cfgPath, rc.fix...)
}

// assertConfigRefused checks exit 1, empty stdout and a single
// ErrConfigMalformed envelope naming the config path and the row's refused values.
func assertConfigRefused(t *testing.T, rc configRefusal, h refusedHome, stdout, stderr string, code int) {
	t.Helper()
	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrConfigMalformed")
	want := apitest.DescConfigRefused(h.cfgPath, rc.refused...)
	if rc.caseVariant != nil {
		want = apitest.DescConfigCaseVariant(h.cfgPath, rc.caseVariant...)
	}
	apitest.AssertDescription(t, env.ErrDescription, want)
}

// assertRowUntouched checks, after the file is fixed, that the seeded row is
// still pending with no session id and that no permission request is stored.
func assertRowUntouched(t *testing.T, h refusedHome) {
	t.Helper()
	cols := rowColumns(t, h.home, h.instanceID)
	if session, _ := cols.ClaudeSessionID.(string); cols.State != store.StatePending || session != "" {
		t.Errorf("row state=%v claude_session_id=%v; want pending with no session id", cols.State, cols.ClaudeSessionID)
	}
	st, err := store.Open(stateDB(h.home))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	reqs, err := st.PermissionRequestsForSpawn(h.instanceID)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("permission requests stored = %+v; want none", reqs)
	}
}

// TestConfigRefusalStopsEverySurface drives each refused config, [tmux] values,
// [defaults] expire_retention_days (b.sgw), [relay] and [pause]
// timeout_seconds (b.8q2), [pre_trust] lock_wait_seconds (b.kr4) and a key
// set under two letter cases (b.p8n), through every surface (SR-4.1;
// AC-CFG-03/04, loading half of AC-RES-05).
func TestConfigRefusalStopsEverySurface(t *testing.T) {
	surfaces := []struct {
		name string
		run  func(t *testing.T, rc configRefusal, h refusedHome)
	}{
		{"list", func(t *testing.T, rc configRefusal, h refusedHome) {
			stdout, stderr, code := runCLIWithHome(t, h.home, "list")
			assertConfigRefused(t, rc, h, stdout, stderr, code)
		}},
		{"serve_stdio", func(t *testing.T, rc configRefusal, h refusedHome) {
			stdout, stderr, code, timedOut := runBounded(t, h.home, nil, mcpInitialize, true, surfaceDeadline, "serve", "--stdio")
			if timedOut {
				t.Fatalf("serve --stdio still running after %v with stdin open; stdout=%q", surfaceDeadline, stdout)
			}
			assertConfigRefused(t, rc, h, stdout, stderr, code)
		}},
		{"hook_session_start", func(t *testing.T, rc configRefusal, h refusedHome) {
			payload := `{"hook_event_name":"SessionStart","transcript_path":"/x/tmux-config-session.jsonl"}`
			// Bounded: a refusal that let SessionStart reach the handler could
			// sit in its identity wait up to the pending grace (SR-13.4).
			stdout, stderr, code, timedOut := runBounded(t, h.home, map[string]string{probe.EnvKey: h.instanceID},
				payload, false, surfaceDeadline, "hook")
			if timedOut {
				t.Fatalf("hook still running after %v (stderr=%q)", surfaceDeadline, stderr)
			}
			if code != 0 || stdout != "" {
				t.Errorf("hook exit=%d stdout=%q; want 0 and empty (stderr=%q)", code, stdout, stderr)
			}
			h.repair(t, rc)
			assertRowUntouched(t, h)
		}},
		{"hook_relayed_permission_denied", func(t *testing.T, rc configRefusal, h refusedHome) {
			bound := min(surfaceDeadline, time.Duration(config.DefaultRelayTimeoutSeconds)*time.Second/10)
			env := map[string]string{probe.EnvKey: h.instanceID, hook.EnvRelayMode: hook.RelayModeOn}
			payload := `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
			stdout, stderr, code, timedOut := runBounded(t, h.home, env, payload, false, bound, "hook")
			if timedOut {
				t.Fatalf("hook still running after %v: entered the relay poll (stderr=%q)", bound, stderr)
			}
			if code != 0 {
				t.Errorf("hook exit=%d want 0 (stderr=%q)", code, stderr)
			}
			if want := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"; stdout != want {
				t.Errorf("hook stdout=%q\nwant deny %q", stdout, want)
			}
			h.repair(t, rc)
			assertRowUntouched(t, h)
		}},
		{"db_free_verbs_run", func(t *testing.T, _ configRefusal, h refusedHome) {
			for _, shape := range dbFreeShapes {
				stdout, stderr, code := runCLIWithHome(t, h.home, shape.argv...)
				var payload map[string]any
				if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &payload) != nil {
					t.Errorf("%s: exit=%d stderr=%q stdout=%q; want 0, empty stderr, JSON envelope",
						shape.name, code, stderr, stdout)
				}
			}
		}},
		{"fixed_file_recovers", func(t *testing.T, rc configRefusal, h refusedHome) {
			stdout, stderr, code := runCLIWithHome(t, h.home, "list")
			assertConfigRefused(t, rc, h, stdout, stderr, code)
			h.repair(t, rc)
			stdout, stderr, code = runCLIWithHome(t, h.home, "list")
			if code != 0 || stderr != "" {
				t.Fatalf("list after fix: exit=%d stderr=%q; want 0 and empty", code, stderr)
			}
			if !strings.Contains(stdout, `"claude_instance_id":"`+h.instanceID+`"`) {
				t.Errorf("list after fix = %s; want the seeded row %s", stdout, h.instanceID)
			}
		}},
	}

	for _, rc := range configRefusals() {
		t.Run(rc.name, func(t *testing.T) {
			t.Parallel()
			for _, s := range surfaces {
				t.Run(s.name, func(t *testing.T) {
					t.Parallel()
					s.run(t, rc, newRefusedHome(t, rc))
				})
			}
		})
	}
}

// assertHungLookup checks an ErrTmuxUnresponsive whose description names the
// lookup and timeout.
func assertHungLookup(t *testing.T, errName, desc string, timeout time.Duration) {
	t.Helper()
	if errName != "ErrTmuxUnresponsive" {
		t.Errorf("err_name = %q; want ErrTmuxUnresponsive (description %q)", errName, desc)
	}
	apitest.AssertDescription(t, desc, apitest.DescCallTimeout(tmux.CallLookup, timeout))
}

// hungKillRow seeds a live row whose socket's lookup hangs and writes
// query_timeout_ms; it returns the HOME, the row's id and the config path.
func hungKillRow(t *testing.T, timeout time.Duration) (home, id, cfgPath string) {
	t.Helper()
	home, id, socket := seedRowOnSocket(t, store.StateWaiting)
	faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallLookup).Bound(fakeHangBound))
	cfgPath = filepath.Join(directorDir(home), "config.toml")
	writeQueryTimeout(t, cfgPath, timeout)
	return home, id, cfgPath
}

// TestTmuxConfigKillCLIHungLookup: the CLI reads [tmux] at startup and hands
// it to its tmux client: a kill whose lookup hangs fails naming the configured
// 0.3 s, well before the default query timeout (SR-20.6, AC-CFG-02). Each
// timeout class against the fake is pkg/api's TestTmuxTimeoutsApplied.
func TestTmuxConfigKillCLIHungLookup(t *testing.T) {
	const timeout = 300 * time.Millisecond
	home, id, _ := hungKillRow(t, timeout)

	began := time.Now()
	stdout, stderr, code := runSpawnCLI(t, home, buildFakeTmux(t), "kill", "--claude-instance-id", id)
	elapsed := time.Since(began)

	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrTmuxUnresponsive")
	assertHungLookup(t, env.ErrName, env.ErrDescription, timeout)
	if def := (config.Tmux{}).EffectiveQueryTimeout(); elapsed >= def {
		t.Errorf("kill took %v; want well under the default query timeout %v", elapsed, def)
	}
	assertInvocationKinds(t, home, "list-sessions")
}
