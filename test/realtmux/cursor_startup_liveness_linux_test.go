//go:build linux

package realtmux_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// The production gateway fails exec before runtime registration; no Cursor binary is invoked.
func TestCursorStartupExecutableFailurePublicReads(t *testing.T) {
	f := newSpawnFix(t)
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml")
	cfg, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	missingExecutable := filepath.Join(t.TempDir(), "nonexistent-cursor-runtime")
	cfg = append(cfg, []byte(fmt.Sprintf("\n[launch]\ncursor_path = %q\n", missingExecutable))...)
	if err = os.WriteFile(configPath, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	bin := buildHookCLI(t, t.TempDir())
	id := newInstanceID("cursor-startup")
	cmd := exec.Command(bin, "spawn", "--store-path", f.DBPath, "--agent", "cursor", "--instance-id", id, "--cwd", f.CWD, "--no-pre-trust", "--", "--model", "auto")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real CLI spawn: %v %s", err, out)
	}
	var launched api.SpawnResult
	if err = json.Unmarshal(out, &launched); err != nil || launched.InstanceID != id {
		t.Fatalf("spawn result=%s err=%v", out, err)
	}
	before := readRow(t, f.DBPath, id)
	pid, okPID := before.PanePID.(int64)
	start, okStart := before.PaneStarttime.(string)
	if !okPID || pid <= 0 || !okStart || start == "" || before.LaunchToken == "" {
		t.Fatalf("real gateway identity not recorded: %+v", before)
	}
	rt := f.at(t, before.TmuxSocket.(string))
	t.Cleanup(func() { rt.run(t, "kill-server") })
	pc := probe.NewProcChecker()
	waitFor(t, "exact startup gateway gone", func() bool {
		return tmux.JudgeProcess(pc, tmux.ProcIdentity{PID: int(pid), Starttime: start}) == tmux.ProcGone
	}, func() string { return procState(int(pid)) })
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.ReadCursorRuntime(id); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected independently absent runtime binding: %v", err)
	}
	before = readRow(t, f.DBPath, id)
	if before.State != store.StatePending || before.AgentArgs != `["--model","auto"]` {
		t.Fatalf("not pending with saved args before read: %+v", before)
	}
	t.Logf("production spawn recorded gateway=%d/%s; exact kernel verdict=gone; runtime binding absent; raw before=%+v", pid, start, before)
	for _, verb := range []string{"Get", "Status", "List"} {
		t.Run(verb, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				switch verb {
				case "Get":
					got, err := f.API.Get(id)
					if err != nil || got.State != store.StateMissing {
						t.Errorf("Get after exact pre-registration death=%+v err=%v; want missing", got, err)
					}
				case "Status":
					got, err := f.API.Status(id)
					if err != nil || got.State != store.StateMissing {
						t.Errorf("Status after exact pre-registration death=%+v err=%v; want missing", got, err)
					}
				case "List":
					got, err := f.API.List(api.ListParams{})
					if err != nil || len(got.Spawns) != 1 || got.Spawns[0].State != store.StateMissing {
						t.Errorf("List after exact pre-registration death=%+v err=%v; want missing", got, err)
					}
				}
			}
		})
	}
	after := readRow(t, f.DBPath, id)
	if after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 {
		t.Errorf("public reads did not persist missing exactly once: before=%+v after=%+v", before, after)
	}
	if after.AgentArgs != before.AgentArgs || after.LaunchToken != before.LaunchToken || after.LifeNumber != before.LifeNumber || after.PanePID != before.PanePID || after.PaneStarttime != before.PaneStarttime || after.SessionID != before.SessionID {
		t.Error("startup missing changed saved args/identity")
	}
	if _, err = st.ReadCursorRuntime(id); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("public read fabricated runtime: %v", err)
	}
	if got := readRow(t, f.DBPath, id); !reflect.DeepEqual(got, after) {
		t.Errorf("terminal inspection changed raw row: %+v", got)
	}
}
