package main_test

// find-missing through the built CLI: the production start-time reader judges
// real processes (SR-3.8), and the production tmux client asks test/fake-tmux
// about rows with no process evidence (SR-11.3). The verdict and trail rules
// themselves are pkg/api's find_missing_*_test.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// findMissingResult is the find-missing CLI stdout envelope.
type findMissingResult = api.SweepResult

// runFindMissing runs find-missing under home with the fake tmux first on
// PATH and extraEnv, requires exit 0 and returns the parsed envelope.
func runFindMissing(t *testing.T, home string, extraEnv map[string]string, verbs ...string) findMissingResult {
	t.Helper()
	verb := "find-missing"
	if len(verbs) > 0 {
		verb = verbs[0]
	}
	stdout, stderr, code := runSpawnCLIEnv(t, home, buildFakeTmux(t), extraEnv, verb)
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}
	var r findMissingResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("find-missing stdout is not JSON-parseable: %v\nstdout=%q", err, stdout)
	}
	apitest.AssertConfirmationsShape(t, stdout)
	if r.Checks == nil || r.IDs == nil || r.UnverifiedIDs == nil || !strings.Contains(stdout, `"count":`) || !strings.Contains(stdout, `"unverified":`) {
		t.Errorf("stdout = %s; want count and unverified keys even at 0, and ids and unverified_ids arrays, never null", stdout)
	}
	return r
}

// seedFindMissingRow seeds one row under home's store with opts.
func seedFindMissingRow(t *testing.T, home, id, state string, opts ...apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(stateDB(home), id, state, "/tmp", "off", "", true, opts...); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestFindMissingCLIArgvTwinDiscrimination: of two byte-identical `sleep`
// children recorded as their rows' agent processes, only the reaped one's row
// is marked missing with one proc_absent tick, even though the live twin
// carries the reaped row's id in its environment; the live twin's stays working.
func TestFindMissingCLIArgvTwinDiscrimination(t *testing.T) {
	testRecoveryCLIArgvTwinDiscrimination(t, "find-missing")
}
func TestSweepCLIArgvTwinDiscrimination(t *testing.T) {
	testRecoveryCLIArgvTwinDiscrimination(t, "sweep")
}
func testRecoveryCLIArgvTwinDiscrimination(t *testing.T, verb string) {
	home := t.TempDir()
	const killedID, liveID = "id-twin-killed", "id-twin-live"
	var killed *exec.Cmd
	for _, id := range []string{killedID, liveID} {
		cmd := exec.Command("sleep", "3600")
		if id == liveID {
			cmd.Env = append(os.Environ(), probe.EnvKey+"="+killedID)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start sleep: %v", err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		pid := cmd.Process.Pid
		start := procstat.ReadStarttime(t, pid)
		seedFindMissingRow(t, home, id, store.StateWorking, apitest.WithPID(pid), apitest.WithProcStarttime(start),
			apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket, PaneID: apitest.TestPaneID,
				PanePID: pid, PaneStarttime: start}))
		if id == killedID {
			killed = cmd
		}
	}
	if err := killed.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	_, _ = killed.Process.Wait() // reaped: the pid is gone, not a zombie
	// b.915: finished rows do not join the two live-at-selection twins.
	seedFindMissingRow(t, home, "id-twin-ended", store.StateEnded, apitest.WithNoPane())
	seedFindMissingRow(t, home, "id-twin-missing", store.StateMissing, apitest.WithNoPane())

	res := runFindMissing(t, home, nil, verb)
	if !slices.Equal(res.IDs, []string{killedID}) || res.Count != 1 || res.Unverified != 0 {
		t.Fatalf("find-missing = %+v; want exactly [%s] marked, none unverified", res, killedID)
	}
	if len(res.Checks) != 2 {
		t.Fatalf("checks = %+v; want both selected twins", res.Checks)
	}
	for i, id := range []string{killedID, liveID} {
		check := res.Checks[i]
		state, process := store.StateMissing, "exited"
		if id == liveID {
			state, process = store.StateWorking, "alive"
		}
		if check.InstanceID != id || check.State != state || check.ProcessLiveness != process || check.Completion != "complete" || check.Reason != nil || check.Confirmations.ProcessLiveness.Status != "confirmed" || check.Confirmations.ProcessLiveness.Source == nil || *check.Confirmations.ProcessLiveness.Source != "process" {
			t.Errorf("twin check = %+v; want %s/%s/%s with process evidence", check, id, state, process)
		}
	}
	for id, want := range map[string]string{killedID: store.StateMissing, liveID: store.StateWorking} {
		if got := rowColumns(t, home, id).State; got != want {
			t.Errorf("row %s state = %v; want %s", id, got, want)
		}
	}
	ticks := eventsOf(readTrailLines(t, home), "ad.find_missing.tick")
	if len(ticks) != 1 || ticks[0]["instance_id"] != killedID || ticks[0]["reconciliation_reason"] != "proc_absent" ||
		ticks[0]["source"] != "ad_find_missing" {
		t.Errorf("ticks = %v; want one proc_absent tick for %s from ad_find_missing", ticks, killedID)
	}
}

// b.915: finished history must not inflate either CLI spelling's output.
func TestB915SweepOutputScalesWithFinishedRows(t *testing.T) {
	for _, verb := range []string{"sweep", "find-missing"} {
		t.Run(verb, func(t *testing.T) {
			sizes := map[int]int{}
			for _, finished := range []int{0, 400} {
				home := t.TempDir()
				const killedID = "id-b915-killed"
				cmd := exec.Command("sleep", "3600")
				if err := cmd.Start(); err != nil {
					t.Fatalf("start sleep: %v", err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				pid := cmd.Process.Pid
				start := procstat.ReadStarttime(t, pid)
				seedFindMissingRow(t, home, killedID, store.StateWorking, apitest.WithPID(pid), apitest.WithProcStarttime(start),
					apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket, PaneID: apitest.TestPaneID,
						PanePID: pid, PaneStarttime: start}))
				for i := 0; i < finished; i++ {
					state := store.StateEnded
					if i%3 == 0 {
						state = store.StateMissing
					}
					seedFindMissingRow(t, home, fmt.Sprintf("id-b915-fin-%04d", i), state, apitest.WithNoPane())
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatalf("kill: %v", err)
				}
				_, _ = cmd.Process.Wait() // no zombie can qualify as a live process

				stdout, stderr, code := runSpawnCLIEnv(t, home, buildFakeTmux(t), nil, verb)
				if code != 0 {
					t.Fatalf("%s exit = %d\nstderr=%s", verb, code, stderr)
				}
				var got api.SweepResult
				if err := json.Unmarshal([]byte(stdout), &got); err != nil {
					t.Fatalf("stdout not JSON: %v", err)
				}
				sizes[finished] = len(stdout)
				t.Logf("verb=%s finished_rows=%d stdout_bytes=%d checks=%d ids=%v count=%d",
					verb, finished, len(stdout), len(got.Checks), got.IDs, got.Count)
				if got.Count != 1 || !slices.Equal(got.IDs, []string{killedID}) || got.Unverified != 0 || got.UnverifiedIDs == nil || len(got.UnverifiedIDs) != 0 {
					t.Errorf("legacy counters changed: %+v", got)
				}
				if len(got.Checks) != 1 || got.Checks[0].InstanceID != killedID || got.Checks[0].State != store.StateMissing {
					t.Errorf("checks = %+v; want only %s, live at selection and now missing", got.Checks, killedID)
				}
			}
			// The only retained row has the same ID and values in both runs;
			// fixed-width UTC millisecond evidence makes stdout lengths equal.
			if sizes[400] != sizes[0] {
				t.Errorf("stdout grew with finished history: %d bytes with 400 rows; want %d with none", sizes[400], sizes[0])
			}
		})
	}
}

// TestFindMissingCLITmuxAnswers: rows recording no process evidence, one of
// them pending past the default grace period, are decided by one lookup on
// their socket: Gone marks each tmux_absent; tmux unavailable (the socket's
// permission-denied reply) marks none and notes each
// process_not_seen_tmux_unchecked.
func TestFindMissingCLITmuxAnswers(t *testing.T) { testRecoveryCLITmuxAnswers(t, "find-missing") }
func TestSweepCLITmuxAnswers(t *testing.T)       { testRecoveryCLITmuxAnswers(t, "sweep") }
func testRecoveryCLITmuxAnswers(t *testing.T, verb string) {
	const workingID, pendingID = "id-fm-tmux-a-working", "id-fm-tmux-b-pending"
	rowIDs := []string{workingID, pendingID}
	for _, tc := range []struct {
		name   string
		inject []faketmuxfix.Injection
		gone   bool
	}{
		{name: "gone", gone: true},
		{name: "tmux unavailable", inject: []faketmuxfix.Injection{
			faketmuxfix.Reply(tmux.CallLookup, tmuxfix.SocketDenied(apitest.TestSocket))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tables := faketmuxfix.Tables{Dir: t.TempDir()}
			if len(tc.inject) > 0 {
				tables.Inject(t, apitest.TestSocket, tc.inject...)
			}
			seedFindMissingRow(t, home, workingID, store.StateWorking,
				apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: apitest.TestSocket}))
			pastGrace := time.Now().Add(-2 * time.Duration(config.DefaultPendingGraceSeconds) * time.Second)
			seedFindMissingRow(t, home, pendingID, store.StatePending,
				apitest.WithNoPane(), apitest.WithLaunchStartedAt(pastGrace.UnixMilli()))

			res := runFindMissing(t, home, map[string]string{faketmuxfix.EnvTables: tables.Dir}, verb)
			assertInvocationKinds(t, home, "list-sessions") // one lookup, one socket

			marked, unverified := rowIDs, []string{}
			if !tc.gone {
				marked, unverified = []string{}, rowIDs
			}
			slices.Sort(res.IDs)
			slices.Sort(res.UnverifiedIDs)
			if !slices.Equal(res.IDs, marked) || !slices.Equal(res.UnverifiedIDs, unverified) {
				t.Errorf("ids = %v, unverified_ids = %v; want %v, %v", res.IDs, res.UnverifiedIDs, marked, unverified)
			}
			ticks := eventsOf(readTrailLines(t, home), "ad.find_missing.tick")
			if len(ticks) != len(rowIDs) {
				t.Fatalf("ticks = %v; want one per row", ticks)
			}
			for _, tk := range ticks {
				want := "process_not_seen_tmux_unchecked"
				if tc.gone {
					want = "tmux_absent"
				}
				if tk["reconciliation_reason"] != want || tk["source"] != "ad_find_missing" {
					t.Errorf("tick %v; want reason %s from ad_find_missing", tk, want)
				}
			}
		})
	}
}
