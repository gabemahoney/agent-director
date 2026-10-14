//go:build linux

package realtmux_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/adminapi"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Persistence failure at admission, outcome or intentional publication stops the owned runtime truthfully.
func TestCursorPersistenceFailureStopsAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane persistence failures")
	}
	for _, stage := range []string{"input replacement", "input permission", "turn outcome", "end publication"} {
		t.Run(stage, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			row := readRow(t, f.DBPath, launch.ID)
			path := row.JSONLPath.(string)
			runtimePID := 0
			fmt.Sscan(readFileTrim(t, r.PID), &runtimePID)
			gatewayPID := int(row.PanePID.(int64))
			if stage == "turn outcome" {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "HOLD"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "held turn", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			}
			switch stage {
			case "input replacement", "turn outcome":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement must survive\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "input permission":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "end publication":
				if err := apitest.SeedSubagent(f.DBPath, launch.ID, "synthetic-cleanup-fault"); err != nil {
					t.Fatal(err)
				}
				storefix.InjectWriteFailure(t, f.DBPath, storefix.WriteFailSubagentRecord, launch.ID)
			}
			if stage == "turn outcome" {
				if err := os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if stage == "end publication" {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if _, err := f.API.Pause(ctx, api.PauseParams{InstanceID: launch.ID}); err == nil {
					t.Fatal("failed end publication reported successful pause")
				}
			} else {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "must not be admitted"}); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "gateway failed closed", func() bool { return pidGone(gatewayPID) }, func() string { return procState(gatewayPID) })
			waitFor(t, "owned runtime gone", func() bool { return pidGone(runtimePID) }, func() string { return procState(runtimePID) })
			if after := readRow(t, f.DBPath, launch.ID); after.State == store.StateEnded {
				t.Fatal("persistence failure fabricated ended")
			}
			failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID)
			if len(failures) != 1 {
				t.Fatalf("observable failure missing: %v", failures)
			}
			for _, frame := range r.Frames(t) {
				if strings.Contains(string(frame["params"]), "must not be admitted") {
					t.Fatal("failed persistence admitted prompt")
				}
			}
			if stage == "input replacement" || stage == "turn outcome" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "replacement must survive\n" {
					t.Fatalf("replacement mutated %q %v", raw, err)
				}
			}
		})
	}
}

func TestCursorHumanAdminDeleteLeavesRunningPaneUntracked(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane operator deletion")
	}
	f, r, launch := newCursorPane(t, false)
	waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
	runtimePID := 0
	fmt.Sscan(readFileTrim(t, r.PID), &runtimePID)
	result, err := adminapi.Delete(f.API, []string{launch.ID})
	if err != nil || result.Results[launch.ID] != "ok" {
		t.Fatalf("operator delete %+v %v", result, err)
	}
	st, err := store.Open(f.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err = st.GetSpawn(launch.ID); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("operator delete did not remove row: %v", err)
	}
	if pidGone(runtimePID) || pidGone(int(launch.Row.PanePID.(int64))) {
		t.Fatal("operator row deletion unexpectedly terminated conversation")
	}
	if got := launch.rt.formatInt(t, launch.Row.PaneID.(string), "#{pane_pid}"); got != int(launch.Row.PanePID.(int64)) {
		t.Fatal("operator delete changed actual pane")
	}
}

func TestCursorStoppingOneLaunchPreservesOtherLaunchAndWorker(t *testing.T) {
	if testing.Short() {
		t.Skip("independent real launch survivors")
	}
	f1, r1, launch1 := newCursorPaneScenario(t, false, "independent-worker")
	waitFor(t, "first waiting", func() bool { return readRow(t, f1.DBPath, launch1.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f1.DBPath, launch1.ID)) })
	workerPID := 0
	waitFor(t, "independent worker", func() bool {
		raw, err := os.ReadFile(r1.WorkerPID)
		if err == nil {
			fmt.Sscan(string(raw), &workerPID)
		}
		return workerPID > 0
	}, func() string { return "worker pid not announced" })
	workerIdentity, ok := readStat(workerPID)
	if !ok || workerIdentity.gone() {
		t.Fatal("worker not independently alive before stop")
	}
	launch1.rt.trackPane(workerPID)
	f2, r2, launch2 := newCursorPane(t, false)
	waitFor(t, "second waiting", func() bool { return readRow(t, f2.DBPath, launch2.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f2.DBPath, launch2.ID)) })
	before := readRow(t, f2.DBPath, launch2.ID)
	secondRuntime := readFileTrim(t, r2.PID)
	if _, err := f1.API.Pause(context.Background(), api.PauseParams{InstanceID: launch1.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first gateway completely exited", func() bool { return pidGone(int(launch1.Row.PanePID.(int64))) }, func() string { return procState(int(launch1.Row.PanePID.(int64))) })
	observed, alive := readStat(workerPID)
	if !alive || observed.gone() || observed.start != workerIdentity.start {
		t.Fatalf("independent worker disappeared or changed after gateway exit: %s", procState(workerPID))
	}
	after := readRow(t, f2.DBPath, launch2.ID)
	if before != after || secondRuntime != readFileTrim(t, r2.PID) {
		t.Fatal("another row or runtime changed by first stop")
	}
	if _, err := f2.API.SendKeys(api.SendKeysParams{InstanceID: launch2.ID, Text: "other row remains usable"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "other row turn completes", func() bool {
		for _, frame := range r2.Frames(t) {
			if strings.Contains(string(frame["params"]), "other row remains usable") {
				return readRow(t, f2.DBPath, launch2.ID).State == store.StateWaiting
			}
		}
		return false
	}, func() string { return fmt.Sprint(readRow(t, f2.DBPath, launch2.ID)) })
	if _, err := f2.API.Pause(context.Background(), api.PauseParams{InstanceID: launch2.ID}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r1.WorkerRelease, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "released independent worker", func() bool { return pidGone(workerPID) }, func() string { return procState(workerPID) })
}
