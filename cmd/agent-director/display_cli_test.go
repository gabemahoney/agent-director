package main_test

import (
	"encoding/json"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDisplayCLIWire: the CLI's status, get and list of the shared
// idle_screen surface print its exact wire, displayed_state beside state
// working (list no detail, `--state idle_screen` selecting it), and every row
// its agent's idle_screen_availability, read under the largest settle time
// (apitest.SeedIdleScreenSurface). The stored needs_intelligence rows are
// TestDisplayedStateContractCLI's (b.zpv).
func TestDisplayCLIWire(t *testing.T) {
	home := t.TempDir()
	if err := apitest.SeedNeedsIntelligenceSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedIdleScreenSurface(stateDB(home)); err != nil {
		t.Fatal(err)
	}
	apitest.SettleVariant{Config: &apitest.SettleOnConfig}.WriteConfig(t, filepath.Join(directorDir(home), "config.toml"))
	read := func(args ...string) []byte {
		t.Helper()
		raw, stderr, code := runCLIWithHome(t, home, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%q: %d %s %s", args, code, raw, stderr)
		}
		return []byte(raw)
	}
	for _, surface := range apitest.DisplayWireSurfaces {
		for _, agent := range surface.Agents {
			for _, verb := range []string{"status", "get"} {
				surface.Check(t, read(verb, "--instance-id", surface.Prefix+agent), agent, verb)
			}
		}
		var listed struct {
			Spawns []json.RawMessage `json:"spawns"`
		}
		raw := read("list", "--state", surface.State, "--limit", strconv.Itoa(len(surface.Agents)))
		if json.Unmarshal(raw, &listed) != nil || len(listed.Spawns) != len(surface.Agents) {
			t.Fatalf("list --state %s=%s; want its %d rows", surface.State, raw, len(surface.Agents))
		}
		for _, row := range listed.Spawns {
			var got struct {
				DisplayedState string `json:"displayed_state"`
			}
			if json.Unmarshal(row, &got) != nil || got.DisplayedState != surface.State {
				t.Errorf("list --state %s listed %s", surface.State, row)
			}
			apitest.CheckListedDisplayWire(t, row)
		}
	}
	for id, agent := range apitest.DisplayWireOrdinary {
		apitest.CheckOrdinaryWire(t, read("status", "--instance-id", id), agent)
	}
}

// b.x9o: separate CLI processes agree on owned samples and a later failed
// capture retains the first pane evidence without remaining confirmed.
func TestDisplayCLIConfirmation(t *testing.T) {
	home, id, socket := seedRowOnSocket(t, store.StateWorking)
	if _, err := apitest.BindReadSurfaceProcess(stateDB(home), id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	session := ownSession(t, home, id, "pane")
	session.Panes[0].PID = os.Getpid()
	tables := faketmuxfix.Tables{}
	tables.Write(t, socket, fakeTable(session))
	fake := buildFakeTmux(t)
	first := ""
	for _, reason := range []string{"", "unreadable"} {
		if reason != "" {
			tables.Inject(t, socket, faketmuxfix.ExitCode(tmux.CallCapture, 1))
		}
		for _, verb := range []string{"status", "get", "list"} {
			args := []string{verb}
			if verb != "list" {
				args = append(args, "--instance-id", id)
			}
			from := time.Now()
			raw, stderr, code := runSpawnCLI(t, home, fake, args...)
			to := time.Now()
			if code != 0 || stderr != "" {
				t.Fatalf("%s: %d %s %s", verb, code, stderr, raw)
			}
			row := []byte(raw)
			if verb == "list" {
				var list struct {
					Spawns []json.RawMessage `json:"spawns"`
				}
				if err := json.Unmarshal(row, &list); err != nil || len(list.Spawns) != 1 {
					t.Fatalf("list %s: %v", raw, err)
				}
				row = list.Spawns[0]
			}
			first = apitest.CheckPaneConfirmationWire(t, row, first, reason, from, to)
		}
	}
}
