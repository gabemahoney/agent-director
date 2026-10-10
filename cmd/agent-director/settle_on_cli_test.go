package main_test

// settle_on_cli_test.go (b.3ie): status, get and list with [state] settle_ms
// set, through the built CLI: the read-surface cases with only the state
// changed, list --state on the settled state ahead of --limit, and hook
// processes writing with the configured settle time while status reads it
// without waiting. The settle-off goldens are settle_off_cli_test.go's.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSettleOnCLI: with the largest settle time, status, get and list over the
// read-surface store print each settle-off golden with only the state read as
// settled: no new field and no new state value; list filters on it.
func TestSettleOnCLI(t *testing.T) {
	cases := apitest.LoadReadSurfaceCases(t, filepath.Join("testdata", "settle_off"))
	home := t.TempDir()
	if _, err := apitest.SeedReadSurfaceStore(stateDB(home)); err != nil {
		t.Fatalf("SeedReadSurfaceStore: %v", err)
	}
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
	for _, c := range cases.Cases {
		stdout, stderr, code := runCLIWithHome(t, home, c.CLIArgs()...)
		if code != 0 || stderr != "" {
			t.Errorf("%s %q: exit %d, stderr %q; want 0 and empty", c.Name, c.CLIArgs(), code, stderr)
			continue
		}
		cases.CheckSettleOn(t, c, stdout)
	}
}

// cliListIDs runs list under home with args and returns the rows' ids in order.
func cliListIDs(t *testing.T, home string, args ...string) []string {
	t.Helper()
	stdout, stderr, code := runCLIWithHome(t, home, append([]string{"list"}, args...)...)
	var res struct {
		Spawns []struct {
			ID string `json:"claude_instance_id"`
		} `json:"spawns"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
		t.Fatalf("list %q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
	}
	ids := []string{}
	for _, s := range res.Spawns {
		ids = append(ids, s.ID)
	}
	return ids
}

// TestSettleListStateCLI: list --state matches the settled state with a
// settle time, in the statement ahead of --limit (--state waiting --limit 1 is
// the third raw-waiting row, the first that reads waiting), and the raw state
// with none.
func TestSettleListStateCLI(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(fmt.Sprintf("settle time %v", settled), func(t *testing.T) {
			home := t.TempDir()
			rows, err := apitest.SeedSettleFilterRows(stateDB(home), time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if settled {
				apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
			}
			for _, tc := range []struct {
				state string
				limit int
			}{{store.StateWaiting, 0}, {store.StateWorking, 0}, {store.StateWaiting, 1}, {store.StateWorking, 1}} {
				args := []string{"--state", tc.state}
				if tc.limit > 0 {
					args = append(args, "--limit", strconv.Itoa(tc.limit))
				}
				want := apitest.SettleFilterIDs(rows, settled, tc.limit, tc.state)
				if got := cliListIDs(t, home, args...); !slices.Equal(got, want) {
					t.Errorf("list %q = %v; want %v", args, got, want)
				}
			}
		})
	}
}

// cliStatus runs status for id under home, failing the test unless it exits 0
// within statusBudget, and returns its state.
func cliStatus(t *testing.T, home, id string) string {
	t.Helper()
	const statusBudget = 5 * time.Second // the TS client's 30 s call deadline, with room
	start := time.Now()
	stdout, stderr, code := runCLIWithHome(t, home, "status", "--claude-instance-id", id)
	if took := time.Since(start); took > statusBudget {
		t.Errorf("status took %v with settle_ms at its largest; want under %v (no read waits to settle)", took, statusBudget)
	}
	var res struct {
		State string `json:"state"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
		t.Fatalf("status: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	return res.State
}

// TestSettleHooksCLI: with the largest settle time, a working agent's Stop and
// next PreToolUse, each a hook process that exits at once, leave status at
// working throughout and the raw state at its last write: each hook judged the
// value it replaced with the configured settle time (a hook writing with none
// would make the flap's base the unsettled waiting). A terminal SessionEnd
// reads ended at once. status answers at once at every step.
func TestSettleHooksCLI(t *testing.T) {
	home := t.TempDir()
	const id = "id-3ie-hooks"
	if _, err := apitest.SeedSpawn(stateDB(home), id, store.StateWorking, "/tmp", "off", "", true,
		withTestProcessPane(t)); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
	env := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": id}
	for _, step := range []struct{ payload, raw, reported string }{
		{`{"hook_event_name":"Stop"}`, store.StateWaiting, store.StateWorking},
		{`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, store.StateWorking, store.StateWorking},
		{`{"hook_event_name":"Stop"}`, store.StateWaiting, store.StateWorking},
		{`{"hook_event_name":"SessionEnd","reason":"prompt_input_exit"}`, store.StateEnded, store.StateEnded},
	} {
		if stdout, stderr, code := runHook(t, home, env, step.payload); code != 0 || stdout != "" {
			t.Fatalf("hook %s: exit %d, stdout %q; want 0 and empty (stderr %q)", step.payload, code, stdout, stderr)
		}
		if raw := rowColumns(t, home, id).State; raw != step.raw {
			t.Fatalf("raw state after %s = %v; want %s", step.payload, raw, step.raw)
		}
		if got := cliStatus(t, home, id); got != step.reported {
			t.Errorf("status after %s = %q; want %q", step.payload, got, step.reported)
		}
	}
}
