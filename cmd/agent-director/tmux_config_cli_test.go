package main_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// surfaceDeadline bounds every run that must stop on its own (serve with stdin
// held open, the relayed PermissionRequest hook); a run still alive is killed.
const surfaceDeadline = 10 * time.Second

// tmuxRefusal is one refused [tmux] config: the settings written, the phrases
// its err_description must contain (nil: malformed type, only err_name and
// path are asserted) and the settings that fix the file.
type tmuxRefusal struct {
	name    string
	bad     []apitest.TmuxSetting
	phrases []string
	fix     []apitest.TmuxSetting
}

// tmuxRefusals is the one table of refused values driving every surface check.
func tmuxRefusals() []tmuxRefusal {
	window, bound, grace := config.TmuxStoppingWindowSeconds, config.TmuxStartingSessionSeconds, config.TmuxPendingGraceSeconds
	create, pipe, kill := config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs, config.TmuxKillExitWaitMs
	key := func(k config.TmuxKey) string { return "[tmux] " + k.Name() }
	below := func(k config.TmuxKey, minimum int64) string {
		return fmt.Sprintf("below its safe minimum %d %s", minimum, k.Unit())
	}
	effective := func(k config.TmuxKey, v int64) string { return fmt.Sprintf("%s %d", k.Name(), v) }
	const tail = "A missing key, or 0, gives the default."

	// A raised create timeout lifts the grace minimum above the grace floor.
	const raisedCreate = 15000
	raisedGraceMin := config.PendingGraceMinimumSeconds(raisedCreate, 0)
	// A create timeout whose derived grace minimum exceeds the grace default.
	defaultBreakingCreate := int64(config.DefaultPendingGraceSeconds-config.PendingGraceMarginSeconds+1) * 1000
	defaultGraceMin := config.PendingGraceMinimumSeconds(defaultBreakingCreate, 0)

	return []tmuxRefusal{
		{
			name:    "stopping_window_below_minimum",
			bad:     []apitest.TmuxSetting{apitest.TmuxInt(window, 10)},
			phrases: []string{key(window) + " = 10", below(window, config.MinStoppingWindowSeconds), tail},
			fix:     []apitest.TmuxSetting{apitest.TmuxInt(window, 0)},
		},
		{
			name:    "starting_session_bound_below_minimum",
			bad:     []apitest.TmuxSetting{apitest.TmuxInt(bound, config.MinStartingSessionSeconds-1)},
			phrases: []string{fmt.Sprintf("%s = %d", key(bound), config.MinStartingSessionSeconds-1), below(bound, config.MinStartingSessionSeconds), tail},
		},
		{
			name:    "negative_without_minimum",
			bad:     []apitest.TmuxSetting{apitest.TmuxInt(kill, -1)},
			phrases: []string{key(kill) + " = -1, which must be positive", tail},
			fix:     []apitest.TmuxSetting{apitest.TmuxInt(kill, 0)},
		},
		{
			name: "grace_below_derived_minimum",
			bad: []apitest.TmuxSetting{
				apitest.TmuxInt(create, raisedCreate), apitest.TmuxInt(grace, config.PendingGraceFloorSeconds),
			},
			phrases: []string{
				fmt.Sprintf("%s = %d", key(grace), config.PendingGraceFloorSeconds), below(grace, raisedGraceMin),
				effective(create, raisedCreate), effective(pipe, config.DefaultPipeCloseWaitMs), tail,
			},
		},
		{
			name: "grace_default_below_derived_minimum",
			bad:  []apitest.TmuxSetting{apitest.TmuxInt(create, defaultBreakingCreate)},
			phrases: []string{
				key(grace) + " is missing or 0", fmt.Sprintf("its default, %d,", config.DefaultPendingGraceSeconds),
				below(grace, defaultGraceMin), effective(create, defaultBreakingCreate),
				effective(pipe, config.DefaultPipeCloseWaitMs), tail,
			},
			fix: []apitest.TmuxSetting{apitest.TmuxInt(create, 0)},
		},
		{
			name: "float_starting_session_bound",
			bad:  []apitest.TmuxSetting{apitest.TmuxFloat(bound, float64(config.MinStartingSessionSeconds)+0.5)},
		},
		{
			name: "string_stopping_window",
			bad:  []apitest.TmuxSetting{apitest.TmuxString(window, "ninety")},
		},
		{
			name: "bool_query_timeout",
			bad:  []apitest.TmuxSetting{apitest.TmuxBool(config.TmuxQueryTimeoutMs, true)},
		},
	}
}

// refusedHome is a throwaway HOME holding a pending relay-on row seeded before
// the refused config was written.
type refusedHome struct {
	home, cfgPath, instanceID string
}

func newRefusedHome(t *testing.T, rc tmuxRefusal) refusedHome {
	t.Helper()
	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StatePending, "", hook.RelayModeOn, "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	h := refusedHome{home: home, cfgPath: filepath.Join(directorDir(home), "config.toml"), instanceID: id}
	apitest.WriteTmuxConfig(t, h.cfgPath, rc.bad...)
	return h
}

// repair rewrites the config to the row's valid state.
func (h refusedHome) repair(t *testing.T, rc tmuxRefusal) {
	t.Helper()
	apitest.WriteTmuxConfig(t, h.cfgPath, rc.fix...)
}

// assertConfigRefused checks a non-zero exit, empty stdout and a single
// ErrConfigMalformed envelope naming the config path and the row's phrases.
func assertConfigRefused(t *testing.T, rc tmuxRefusal, h refusedHome, stdout, stderr string, code int) {
	t.Helper()
	if code == 0 {
		t.Fatalf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout=%q want empty", stdout)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrConfigMalformed" {
		t.Errorf("err_name=%q want ErrConfigMalformed", env.ErrName)
	}
	for _, want := range append([]string{h.cfgPath}, rc.phrases...) {
		if !strings.Contains(env.ErrDescription, want) {
			t.Errorf("err_description missing %q\ngot: %s", want, env.ErrDescription)
		}
	}
}

// runBounded runs the binary in home with env, writing stdin and closing it
// unless holdOpen; a run still alive at deadline is killed and timedOut is true.
func runBounded(t *testing.T, home string, env map[string]string, stdin string, holdOpen bool,
	deadline time.Duration, args ...string) (stdout, stderr string, code int, timedOut bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.WaitDelay = time.Second
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_, _ = in.Write([]byte(stdin)) // the process may already have exited
	if !holdOpen {
		_ = in.Close()
	}
	err = cmd.Wait()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && ctx.Err() == nil {
		t.Fatalf("wait: %v", err)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode(), ctx.Err() != nil
}

// getSpawn reads a row back through the store-backed `get` verb.
func getSpawn(t *testing.T, home, id string) map[string]any {
	t.Helper()
	stdout, stderr, code := runCLIWithHome(t, home, "get", "--claude-instance-id", id)
	if code != 0 {
		t.Fatalf("get exit=%d stderr=%q", code, stderr)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(stdout), &row); err != nil {
		t.Fatalf("parse get %q: %v", stdout, err)
	}
	return row
}

// assertRowUntouched checks, after the file is fixed, that the seeded row is
// still pending with no session id and that no permission request is stored.
func assertRowUntouched(t *testing.T, h refusedHome) {
	t.Helper()
	row := getSpawn(t, h.home, h.instanceID)
	if row["state"] != store.StatePending || row["claude_session_id"] != "" {
		t.Errorf("row state=%v claude_session_id=%v; want pending with no session id",
			row["state"], row["claude_session_id"])
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

// TestTmuxConfigRefusalStopsEverySurface drives each refused [tmux] value
// through every surface (SR-4.1; AC-CFG-03/04, loading half of AC-RES-05).
func TestTmuxConfigRefusalStopsEverySurface(t *testing.T) {
	surfaces := []struct {
		name string
		run  func(t *testing.T, rc tmuxRefusal, h refusedHome)
	}{
		{"list", func(t *testing.T, rc tmuxRefusal, h refusedHome) {
			stdout, stderr, code := runCLIWithHome(t, h.home, "list")
			if code != 1 {
				t.Errorf("exit=%d want 1", code)
			}
			assertConfigRefused(t, rc, h, stdout, stderr, code)
		}},
		{"serve_stdio", func(t *testing.T, rc tmuxRefusal, h refusedHome) {
			initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05",` +
				`"capabilities":{},"clientInfo":{"name":"tmux-config-test","version":"0"}}}` + "\n"
			stdout, stderr, code, timedOut := runBounded(t, h.home, nil, initialize, true, surfaceDeadline, "serve", "--stdio")
			if timedOut {
				t.Fatalf("serve --stdio still running after %v with stdin open; stdout=%q", surfaceDeadline, stdout)
			}
			assertConfigRefused(t, rc, h, stdout, stderr, code)
		}},
		{"hook_session_start", func(t *testing.T, rc tmuxRefusal, h refusedHome) {
			payload := `{"hook_event_name":"SessionStart","transcript_path":"/x/tmux-config-session.jsonl"}`
			stdout, stderr, code := runCLIWithEnv(t, h.home, map[string]string{probe.EnvKey: h.instanceID}, payload, "hook")
			if code != 0 || stdout != "" {
				t.Errorf("hook exit=%d stdout=%q; want 0 and empty (stderr=%q)", code, stdout, stderr)
			}
			h.repair(t, rc)
			assertRowUntouched(t, h)
		}},
		{"hook_relayed_permission_denied", func(t *testing.T, rc tmuxRefusal, h refusedHome) {
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
		{"db_free_verbs_run", func(t *testing.T, _ tmuxRefusal, h refusedHome) {
			for _, shape := range dbFreeShapes {
				stdout, stderr, code := runCLIWithHome(t, h.home, shape.argv...)
				var payload map[string]any
				if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &payload) != nil {
					t.Errorf("%s: exit=%d stderr=%q stdout=%q; want 0, empty stderr, JSON envelope",
						shape.name, code, stderr, stdout)
				}
			}
		}},
		{"fixed_file_recovers", func(t *testing.T, rc tmuxRefusal, h refusedHome) {
			stdout, stderr, code := runCLIWithHome(t, h.home, "list")
			assertConfigRefused(t, rc, h, stdout, stderr, code)
			h.repair(t, rc)
			stdout, stderr, code = runCLIWithHome(t, h.home, "list")
			if code != 0 || stderr != "" {
				t.Fatalf("list after fix: exit=%d stderr=%q; want 0 and empty", code, stderr)
			}
			var res listResult
			if err := json.Unmarshal([]byte(stdout), &res); err != nil {
				t.Fatalf("parse list %q: %v", stdout, err)
			}
			if len(res.Spawns) != 1 || res.Spawns[0].ClaudeInstanceID != h.instanceID {
				t.Errorf("list after fix = %+v; want the seeded row %s", res.Spawns, h.instanceID)
			}
		}},
	}

	for _, rc := range tmuxRefusals() {
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
