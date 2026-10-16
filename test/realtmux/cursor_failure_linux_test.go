//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/adminapi"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Persistence failure at admission, outcome or intentional publication stops the owned runtime truthfully.
func TestCursorPersistenceFailureStopsAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("real pane persistence failures")
	}
	for _, stage := range []string{"input replacement", "input permission", "queued admission", "queued cancellation", "turn outcome", "end publication", "rotation confirmation", "rotation publication"} {
		t.Run(stage, func(t *testing.T) {
			f, r, launch := newCursorPane(t, false)
			// Keep this owned server stable while the fault destroys its gateway pane.
			sentinel := launch.rt.startSession(t, uniqueName())
			waitFor(t, "fresh waiting", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWaiting }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
			row := readRow(t, f.DBPath, launch.ID)
			path := row.JSONLPath.(string)
			runtimePID := 0
			fmt.Sscan(readFileTrim(t, r.PID), &runtimePID)
			gatewayPID := int(row.PanePID.(int64))
			if stage == "turn outcome" || strings.HasPrefix(stage, "queued") {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "HOLD"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "held turn", func() bool { return readRow(t, f.DBPath, launch.ID).State == store.StateWorking }, func() string { return fmt.Sprint(readRow(t, f.DBPath, launch.ID)) })
				waitFor(t, "held prompt receipt", func() bool {
					for _, frame := range r.Frames(t) {
						if string(frame["method"]) == `"session/prompt"` {
							return true
						}
					}
					return false
				}, nil)
			}
			if strings.HasPrefix(stage, "rotation ") {
				r.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "/new"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "real pane rotation request", func() bool {
					n := 0
					for _, frame := range r.Frames(t) {
						if string(frame["method"]) == `"session/new"` {
							n++
						}
					}
					return n == 2
				}, nil)
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "real-rotation-held"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "real pane held record", func() bool {
					raw, err := os.ReadFile(path)
					return err == nil && strings.Contains(string(raw), `"kind":"input_held"`)
				}, nil)
			}
			if stage == "queued cancellation" {
				if _, err := f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "queued-never-dispatch"}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "durable queued acceptance", func() bool {
					raw, err := os.ReadFile(path)
					return err == nil && strings.Contains(string(raw), `"kind":"submission_accepted"`) && strings.Contains(string(raw), "queued-never-dispatch")
				}, nil)
			}
			switch stage {
			case "input replacement", "turn outcome", "queued admission", "queued cancellation", "rotation confirmation":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement must survive\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "rotation publication":
				storefix.InjectWriteFailure(t, f.DBPath, storefix.WriteFailReuseArchive, launch.ID)
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
			if strings.HasPrefix(stage, "rotation ") {
				if err := os.WriteFile(r.NewRelease, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if stage == "turn outcome" {
				if err := os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if stage == "queued cancellation" {
				launch.rt.run(t, "send-keys", "-t", launch.Row.PaneID.(string), "C-c")
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
			if strings.HasPrefix(stage, "rotation ") {
				actual, err := store.Open(f.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				after, readErr := actual.GetSpawn(launch.ID)
				history, historyErr := actual.ListSessionHistory(launch.ID, after.LifeNumber)
				bound, bindErr := actual.ReadCursorRuntime(launch.ID)
				actual.Close()
				if readErr != nil || historyErr != nil || bindErr != nil || after.SessionID != row.SessionID || bound.SessionID != after.SessionID || len(history) != 0 {
					t.Fatalf("failed actual rotation fabricated identity/history: %+v %+v %+v errors=%v/%v/%v", after, bound, history, readErr, bindErr, historyErr)
				}
				raw, err := os.ReadFile(r.Raw)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), "real-rotation-held") {
					t.Fatal("failed actual rotation sent provisional bytes")
				}
			}
			failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID)
			if len(failures) != 1 {
				t.Fatalf("observable failure missing: %v", failures)
			}
			if stage == "rotation publication" {
				if !strings.Contains(fmt.Sprint(failures[0]["reason"]), "Cursor rotation report failed; publication may already be committed; held input refused") {
					t.Fatalf("actual failed rotation lost exact notice: %v", failures)
				}
				inspected, err := f.API.Get(launch.ID)
				if err != nil || inspected.SessionID != row.SessionID || len(inspected.PriorSessions) != 0 {
					t.Fatalf("public failed-row inspection: %+v %v", inspected, err)
				}
				available, err := os.ReadFile(inspected.JSONLPath)
				if err != nil || !strings.Contains(string(available), `"kind":"input_held"`) || strings.Contains(string(available), `"kind":"session_end"`) {
					t.Fatalf("available records invented successful end: %q %v", available, err)
				}
				// Follow the documented live-row cleanup, then inspect public status.
				if inspected.State != store.StateEnded && inspected.State != store.StateMissing {
					if _, err := f.API.Kill(api.KillParams{InstanceID: launch.ID}); err != nil {
						t.Fatal(err)
					}
					if _, err := f.API.FindMissing(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				status, err := f.API.Status(launch.ID)
				if err != nil || status.State != store.StateEnded && status.State != store.StateMissing {
					t.Fatalf("cleanup did not reconcile failed row: %+v %v", status, err)
				}
				freshID := newInstanceID("cursor-recovery")
				cmd := exec.Command(buildHookCLI(t, t.TempDir()), "spawn", "--store-path", f.DBPath, "--agent", "cursor", "--instance-id", freshID, "--cwd", f.CWD, "--no-pre-trust")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fresh unique instance after reconciliation: %v %s", err, out)
				}
				fresh := f.launched(t, freshID)
				t.Cleanup(func() { fresh.rt.run(t, "kill-server") })
				waitFor(t, "fresh recovery instance ready", func() bool {
					pane, err := f.API.ReadPane(api.ReadPaneParams{InstanceID: freshID, NLines: 100})
					return err == nil && strings.Contains(pane.Pane, "Cursor ready")
				}, nil)
				if raw, err := os.ReadFile(r.Raw); err != nil || strings.Contains(string(raw), "real-rotation-held") {
					t.Fatalf("fresh instance replayed refused input: %v", err)
				}
				if _, err := f.API.Pause(context.Background(), api.PauseParams{InstanceID: freshID}); err != nil {
					t.Fatal(err)
				}
			}
			if pidGone(sentinel.PanePID) {
				t.Fatal("failure cleanup killed the independent owned sentinel")
			}
			for _, frame := range r.Frames(t) {
				if strings.Contains(string(frame["params"]), "must not be admitted") || strings.Contains(string(frame["params"]), "queued-never-dispatch") {
					t.Fatal("failed persistence admitted prompt")
				}
			}
			if stage == "input replacement" || stage == "turn outcome" || strings.HasPrefix(stage, "queued") || stage == "rotation confirmation" {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "replacement must survive\n" {
					t.Fatalf("replacement mutated %q %v", raw, err)
				}
				// Inspect the original protected records that remain available. The
				// replacement is not history, and an acceptance is not execution.
				available, err := os.ReadFile(path + ".original")
				if err != nil {
					t.Fatal(err)
				}
				var queuedID string
				for _, line := range strings.Split(strings.TrimSpace(string(available)), "\n") {
					var rec struct {
						Kind string `json:"kind"`
						Data struct {
							ID   string `json:"submission_id"`
							Text string `json:"text"`
						} `json:"data"`
					}
					if err := json.Unmarshal([]byte(line), &rec); err != nil {
						t.Fatal(err)
					}
					if rec.Kind == "submission_accepted" && rec.Data.Text == "queued-never-dispatch" {
						queuedID = rec.Data.ID
					}
					if rec.Kind == "session_end" || rec.Kind == "turn_outcome" || (queuedID != "" && rec.Data.ID == queuedID && rec.Kind != "submission_accepted") {
						t.Fatalf("partial protected history falsely confirms completion or queued execution: %s", rec.Kind)
					}
				}
				if stage == "queued cancellation" && queuedID == "" {
					t.Fatal("available records lost the previously accepted queued identity")
				}
				before := len(r.Frames(t))
				if err := os.WriteFile(r.Release, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if len(r.Frames(t)) != before || !pidGone(runtimePID) {
					t.Fatal("protected inspection recovered transient work")
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
