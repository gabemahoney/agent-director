package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/bindingfix"
)

// R11-4 differs from pending-interrupted: no PID/start/session exists at the
// first cleanup observation. The same Launch publishes them during grace.
func TestClaudeLiveCleanupLateIdentity(t *testing.T) {
	for _, scenario := range []string{"late-identity", "never-identity", "replaced-identity"} {
		t.Run(scenario, func(t *testing.T) {
			r, clock := runnerFixture(t)
			socketFile(t, r.Socket)
			pid, start, halt := bindingfix.Child(t)
			id, session, token := "live-"+r.Manifest.RunID+"-late-start", "unit-late-session", "1111222233334444"
			dbPath := filepath.Join(r.Home, ".config/agent-director/state.db")
			if _, err := apitest.SeedSpawn(dbPath, id, "pending", r.Home, "off", "", true,
				apitest.WithLaunchIdentity(store.LaunchIdentity{Token: token, Socket: r.Socket, PaneID: "%1", PanePID: pid, PaneStarttime: start}),
				apitest.WithNoPID(), apitest.WithLaunchStartedAt(clock.Now().UnixMilli())); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			path := apitest.SeedJsonlUnder(t, filepath.Join(r.Home, ".claude"), r.Home, session)
			body := []byte("unit cleanup transcript for " + session + "\n")
			putFixture(t, path, body)
			putFixture(t, filepath.Join(r.Home, ".config/agent-director/ad-trail.jsonl"), nil)
			r.Manifest.Owned = []Owned{{InstanceID: id, LaunchToken: token, CWD: r.Home, Socket: r.Socket, Started: clock.Now()}}
			initial, err := r.Observe(context.Background(), id)
			if err != nil || Number(initial.Tables["spawns"][0], "pid") != 0 || Text(initial.Tables["spawns"][0], "session_id") != "" {
				t.Fatalf("late-identity fixture was already bound: %+v %v", initial, err)
			}
			kills, grace := 0, false
			r.Sleep = func(ctx context.Context, d time.Duration) error {
				clock.Advance(d)
				if d == time.Minute {
					grace = true
					if scenario != "never-identity" {
						applied, err := storefix.FireHook(db, id, "SessionStart", session, pid, start, storefix.HookTranscript(path, true))
						if err != nil || !applied.Applied {
							t.Fatalf("same-Launch SessionStart during grace: %+v %v", applied, err)
						}
						if scenario == "replaced-identity" {
							applied, err := storefix.FireHook(db, id, "SessionEnd", session, pid, start)
							if err != nil || !applied.Applied {
								t.Fatal("cannot end old unit Launch", err)
							}
							halt()
							if gone, err := ProcessGone(pid, start); !gone || err != nil {
								t.Fatal("old fixture process must exit before replacement", err)
							}
							row, err := db.GetSpawn(id)
							if err != nil {
								t.Fatal(err)
							}
							changed, _, err := db.MoveToPendingWithExit(id, row.Snapshot, clock.Now().UnixMilli(), "5555666677778888", r.Socket, "", store.LaunchOwner{}, store.BusySendDefaultsChoice{}, storefix.ExitEvidence(t, db, id))
							if err != nil || changed != store.CondApplied {
								t.Fatalf("cannot publish replacement Launch: %v %v", changed, err)
							}
						}
					}
				}
				return ctx.Err()
			}
			r.Commands.Execute = func(_ context.Context, argv, _ []string, _, _ io.Writer) (int, error) {
				if len(argv) < 4 || argv[2] != "--instance-id" || argv[3] != id {
					t.Fatalf("cleanup escaped its owned Launch: %q", argv)
				}
				switch argv[1] {
				case "read-pane", "get":
				case "kill":
					kills++
					if kills == 2 {
						halt() // The initial pending kill deliberately does not stop creation.
					}
				default:
					t.Fatalf("unexpected cleanup command: %q", argv)
				}
				return 0, nil
			}
			v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return context.Canceled }, nil)
			if err != nil || !grace {
				t.Fatalf("pending grace/retry cleanup did not run: grace=%v kills=%d verdict=%+v error=%v", grace, kills, v, err)
			}
			o := r.Manifest.Owned[0]
			if scenario == "replaced-identity" {
				if kills != 1 || o.Exited || o.LaunchToken != token || v.Status == "pass" {
					t.Errorf("replacement during grace must revoke retry kill and binding: kills=%d owner=%+v status=%s", kills, o, v.Status)
				}
				return
			}
			if kills != 2 {
				t.Errorf("owned pending Launch must receive its retry kill: %d", kills)
			}
			if scenario == "never-identity" {
				if o.Exited || v.Status == "pass" {
					t.Errorf("absent identity was accepted as clean exit: owner=%+v verdict=%+v", o, v)
				}
				if _, err := os.Stat(r.runtimeDir); err != nil {
					t.Error("uncertain cleanup discarded its private runtime", err)
				}
				return
			}
			if !o.Exited || o.SessionID != session || o.PID != pid || o.ProcessStart != start || o.LaunchToken != token {
				t.Errorf("cleanup must refresh and verify the same late-bound Launch after retry kill: %+v", o)
			}
			captured, readErr := os.ReadFile(filepath.Join(r.Evidence.Root, "raw/"+id+"-transcript.jsonl"))
			if readErr != nil || !bytes.Equal(captured, body) {
				t.Errorf("cleanup must collect with the new SessionID: bytes=%q error=%v", captured, readErr)
			}
			if _, err := os.Stat(r.runtimeDir); !os.IsNotExist(err) {
				t.Errorf("verified late-identity exit must remove its private runtime: %v", err)
			}
		})
	}
}

func TestClaudeLiveCleanupOwnedExitAndReserve(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("live runner uses Linux process identity")
	}
	start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !alive || !known {
		t.Fatal("test process identity unavailable")
	}
	n, err := strconv.ParseUint(start, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exited", "surviving", "unverified", "kill-failed", "pending-interrupted", "missing-row", "replaced-launch", "over-budget", "over-budget-surviving"} {
		t.Run(scenario, func(t *testing.T) {
			r, clock := runnerFixture(t)
			socketFile(t, r.Socket)
			if filepath.Dir(r.Socket) == r.Evidence.Root {
				t.Fatal("runtime socket is inside evidence")
			}
			st, err := os.Stat(r.runtimeDir)
			if err != nil || st.Mode().Perm() != 0700 {
				t.Fatalf("runtime directory is not private: %v", err)
			}
			state, pid, recordedStart := "missing", os.Getpid(), strconv.FormatUint(n+1, 10)
			if scenario == "surviving" || scenario == "over-budget-surviving" {
				recordedStart = start
			}
			if scenario == "unverified" {
				pid, recordedStart = 0, ""
			}
			if scenario == "pending-interrupted" {
				state = "pending"
			}
			overBudget := scenario == "over-budget" || scenario == "over-budget-surviving"
			var turns int64
			if overBudget {
				turns = int64(r.Manifest.TurnBudget + 1)
			}
			path, owned := seedRunner(t, r, state, true, apitest.WithPID(pid), apitest.WithProcStarttime(recordedStart), apitest.WithTurnStarts(turns))
			foreignSocket := filepath.Join(t.TempDir(), "foreign.sock")
			socketFile(t, foreignSocket)
			_, foreign := seedRunner(t, r, "waiting", true, apitest.WithTmuxSocket(foreignSocket))
			foreignBefore, err := Observe(context.Background(), path, foreign.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			putFixture(t, owned.Transcript, []byte("owned transcript\n"))
			line, _ := json.Marshal(map[string]string{"instance_id": owned.InstanceID, "event": "owned"})
			other, _ := json.Marshal(map[string]string{"instance_id": foreign.InstanceID, "event": "foreign"})
			putFixture(t, filepath.Join(r.Home, ".config/agent-director/ad-trail.jsonl"), append(append(append(line, '\n'), other...), '\n'))
			if scenario == "missing-row" {
				owned.InstanceID = "unrecorded-owned"
			}
			if scenario == "replaced-launch" {
				owned.LaunchToken = "earlier-owned-launch"
			}
			r.Manifest.Owned = []Owned{owned}
			candidate := filepath.Join(r.Kit, "candidate")
			backup := filepath.Join(t.TempDir(), "backup")
			putFixture(t, candidate, []byte("candidate"))
			putFixture(t, backup, []byte("backup"))
			var calls [][]string
			r.Commands.Execute = func(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
				if ctx.Err() != nil {
					t.Errorf("cleanup inherited canceled context: %v", ctx.Err())
				}
				deadline, ok := ctx.Deadline()
				if !ok || deadline.After(r.Manifest.Deadline) {
					t.Error("cleanup escaped total deadline")
				}
				if !reflect.DeepEqual(environment, []string{"TMUX=" + owned.Socket + ",0,0"}) {
					t.Error("cleanup changed socket")
				}
				want := []string{filepath.Join(r.Home, ".config/agent-director/bin/agent-director"), argv[1], "--instance-id", owned.InstanceID}
				if argv[1] == "read-pane" {
					want = append(want, "--n-lines", "80")
				}
				if !reflect.DeepEqual(argv, want) || (argv[1] != "read-pane" && argv[1] != "kill" && argv[1] != "get") {
					t.Errorf("unowned/global cleanup: %q", argv)
				}
				calls = append(calls, append([]string(nil), argv...))
				if scenario == "kill-failed" && argv[1] == "kill" {
					return 1, errors.New("kill refused")
				}
				return 0, nil
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			v, err := r.ExecutePhase(parent, func(ctx context.Context, run *Run) error {
				if scenario == "pending-interrupted" {
					cancel()
					return context.Canceled
				}
				if overBudget {
					// R2: exercise work stops at the cap; cleanup must still kill
					// this Launch, prove exit and retain its final evidence.
					s, err := run.Observe(ctx, owned.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
					if err := run.Bind(s); !errors.Is(err, ErrTurnBudget) {
						t.Fatalf("exercise did not refuse excess turn: %v", err)
					}
					return ErrTurnBudget
				}
				return nil
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "not_completed"
			if scenario == "exited" {
				wantStatus = "pass"
			}
			if overBudget {
				wantStatus = "fail"
			}
			if v.Status != wantStatus {
				t.Fatalf("cleanup %s verdict %+v; want %s", scenario, v, wantStatus)
			}
			wantExited := scenario == "exited" || scenario == "pending-interrupted" || scenario == "over-budget"
			if r.Manifest.Owned[0].Exited != wantExited {
				t.Fatalf("exit was not proven correctly: %+v", r.Manifest.Owned[0])
			}
			for _, path := range []string{r.Socket, r.runtimeDir} {
				_, err := os.Lstat(path)
				if wantExited && !os.IsNotExist(err) {
					t.Fatalf("verified cleanup retained %s: %v", path, err)
				}
				if !wantExited && err != nil {
					t.Fatalf("uncertain cleanup removed %s: %v", path, err)
				}
			}
			if st, err := os.Lstat(foreignSocket); err != nil || st.Mode()&os.ModeSocket == 0 {
				t.Fatal("unrelated runtime socket removed")
			}
			for _, problem := range r.Manifest.Problems {
				if problem.SignalID == "artifact-index" {
					t.Fatal("runtime socket contaminated evidence")
				}
			}
			if scenario == "missing-row" || scenario == "replaced-launch" {
				if len(calls) != 0 || !reflect.DeepEqual(r.Manifest.Owned, []Owned{owned}) {
					t.Fatal("missing/replaced row authorized cleanup or changed ownership")
				}
			}
			if overBudget {
				kills, reads := 0, 0
				for _, call := range calls {
					if call[1] == "kill" {
						kills++
					}
					if call[1] == "get" {
						reads++
					}
				}
				if kills == 0 || reads == 0 || r.Manifest.ObservedTurns != turns {
					t.Fatalf("budget suppressed cleanup: %q", calls)
				}
				budgetFailure := false
				for _, result := range v.Results {
					if result.SignalID == "turn-budget" && result.Status == "fail" {
						budgetFailure = true
					}
				}
				if !budgetFailure {
					t.Fatal("cleanup lost independent turn-budget failure")
				}
				final := readJSON[Snapshot](t, filepath.Join(r.Evidence.Root, "raw/final-"+owned.InstanceID+"-store.json"))
				if Number(final.Tables["spawns"][0], "turn_starts") != turns {
					t.Fatal("final budget evidence missing")
				}
			}
			if scenario == "pending-interrupted" && clock.Now().Sub(owned.Started) != 60*time.Second {
				t.Fatal("pending grace was not preserved in cleanup reserve")
			}
			if scenario == "surviving" && clock.Now().Sub(owned.Started) != 15*time.Second {
				t.Fatal("exit retry wait is unbounded or missing")
			}
			foreignAfter, err := Observe(context.Background(), path, foreign.InstanceID)
			if err != nil || !reflect.DeepEqual(foreignBefore.Tables, foreignAfter.Tables) {
				t.Fatal("unrelated row/session changed")
			}
			if string(readFixture(t, candidate)) != "candidate" || string(readFixture(t, backup)) != "backup" {
				t.Fatal("candidate or backup removed")
			}
			if scenario == "exited" || overBudget {
				if !bytes.Equal(readFixture(t, filepath.Join(r.Evidence.Root, "raw/"+owned.InstanceID+"-trail.jsonl")), append(line, '\n')) {
					t.Fatal("trail export includes unrelated instance")
				}
				if string(readFixture(t, filepath.Join(r.Evidence.Root, "raw/"+owned.InstanceID+"-transcript.jsonl"))) != "owned transcript\n" {
					t.Fatal("lost transcript")
				}
			}
		})
	}
}

func TestClaudeLiveRuntimeOwnership(t *testing.T) {
	for _, scenario := range []string{"changed-socket", "unexpected-entry"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := runnerFixture(t)
			original := r.Socket
			socketFile(t, original)
			kept := filepath.Join(t.TempDir(), "unrelated")
			if scenario == "changed-socket" {
				r.Socket = kept
			} else {
				kept = filepath.Join(r.runtimeDir, "unexpected")
			}
			putFixture(t, kept, []byte("retained"))
			v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return nil }, nil)
			if err != nil || v.Status != "not_completed" {
				t.Fatalf("unsafe runtime cleanup passed: %+v %v", v, err)
			}
			found := false
			for _, result := range v.Results {
				if result.SignalID == "runtime-cleanup" && result.Status == "not_completed" {
					found = true
				}
			}
			if !found || string(readFixture(t, kept)) != "retained" {
				t.Fatal("unexpected runtime content was removed or not reported")
			}
			if _, err := os.Stat(r.runtimeDir); err != nil {
				t.Fatal("runtime directory with unexpected ownership removed")
			}
			if scenario == "changed-socket" {
				if _, err := os.Lstat(original); err != nil {
					t.Fatal("changed socket authorized removal of original")
				}
			}
		})
	}
}

func TestClaudeLiveAwaitCancellationAndDialog(t *testing.T) {
	for _, state := range []string{"waiting", "check_permission", "ask_user"} {
		t.Run(state, func(t *testing.T) {
			r, _ := runnerFixture(t)
			_, owned := seedRunner(t, r, state, true)
			r.Manifest.Owned = []Owned{owned}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waits := 0
			r.Sleep = func(ctx context.Context, d time.Duration) error {
				waits++
				if d != 200*time.Millisecond {
					t.Errorf("wait = %v", d)
				}
				cancel()
				return ctx.Err()
			}
			_, err := r.Await(ctx, owned.InstanceID, time.Second, func(Snapshot) bool { return false })
			if err == nil {
				t.Fatal("uncompleted observation passed")
			}
			if state == "waiting" && (!errors.Is(err, context.Canceled) || waits != 1) {
				t.Fatalf("cancellation: waits=%d %v", waits, err)
			}
			if state != "waiting" && waits != 0 {
				t.Fatal("dialog was polled instead of refused")
			}
		})
	}
}

// TR10-1: procfs can open a task's stat and then return ESRCH when it exits.
// Exercise the exact reader used by ProcessGone without racing a real exit.
func TestClaudeLiveProcessGoneReadRace(t *testing.T) {
	unknown := errors.New("unit procfs read unavailable")
	for _, tc := range []struct {
		name     string
		pid      int
		start    string
		readErr  error
		wantGone bool
		wantRead bool
	}{
		{name: "wrapped-ESRCH", pid: 1234, start: "987", readErr: &os.PathError{Op: "read", Path: "/proc/1234/stat", Err: syscall.ESRCH}, wantGone: true, wantRead: true},
		{name: "unknown-error", pid: 1234, start: "987", readErr: &os.PathError{Op: "read", Path: "/proc/1234/stat", Err: unknown}, wantRead: true},
		{name: "missing-start", pid: 1234, readErr: syscall.ESRCH},
		{name: "invalid-PID", pid: 0, start: "987", readErr: syscall.ESRCH},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			gone, err := processGone(tc.pid, tc.start, func(path string) ([]byte, error) {
				reads++
				if path != "/proc/1234/stat" {
					t.Errorf("read escaped exact PID stat: %s", path)
				}
				return nil, tc.readErr
			})
			wantReads := 0
			if tc.wantRead {
				wantReads = 1
			}
			if gone != tc.wantGone || reads != wantReads {
				t.Errorf("gone=%t reads=%d; want %t/%d", gone, reads, tc.wantGone, wantReads)
			}
			if tc.wantGone {
				if err != nil {
					t.Errorf("ESRCH must prove exit: %v", err)
				}
			} else if err == nil || (tc.wantRead && !errors.Is(err, unknown)) {
				t.Errorf("unverifiable identity/read must retain its error: %v", err)
			}
		})
	}
	// The public wrapper must still read the actual process start identity.
	start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !alive || !known {
		t.Fatal("test process start unavailable")
	}
	if gone, err := ProcessGone(os.Getpid(), start); gone || err != nil {
		t.Fatalf("live exact process: gone=%t err=%v", gone, err)
	}
	n, err := strconv.ParseUint(start, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if gone, err := ProcessGone(os.Getpid(), strconv.FormatUint(n+1, 10)); !gone || err != nil {
		t.Fatalf("reused PID proves old identity gone: gone=%t err=%v", gone, err)
	}
}
