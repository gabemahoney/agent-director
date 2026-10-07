package main_test

// find-missing through the built CLI: the production start-time reader judges
// real processes (SR-3.8), and the production tmux client asks test/fake-tmux
// about rows with no process evidence (SR-11.3). The verdict and trail rules
// themselves are pkg/api's find_missing_*_test.go.

import (
	"encoding/json"
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
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// findMissingResult is the find-missing CLI stdout envelope.
type findMissingResult struct {
	Count         int      `json:"count"`
	IDs           []string `json:"ids"`
	Unverified    int      `json:"unverified"`
	UnverifiedIDs []string `json:"unverified_ids"`
}

// runFindMissing runs find-missing under home with the fake tmux first on
// PATH and extraEnv, requires exit 0 and returns the parsed envelope.
func runFindMissing(t *testing.T, home string, extraEnv map[string]string) findMissingResult {
	t.Helper()
	stdout, stderr, code := runSpawnCLIEnv(t, home, buildFakeTmux(t), extraEnv, "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}
	var r findMissingResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("find-missing stdout is not JSON-parseable: %v\nstdout=%q", err, stdout)
	}
	if r.IDs == nil || r.UnverifiedIDs == nil || !strings.Contains(stdout, `"count":`) || !strings.Contains(stdout, `"unverified":`) {
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

	res := runFindMissing(t, home, nil)
	if !slices.Equal(res.IDs, []string{killedID}) || res.Count != 1 || res.Unverified != 0 {
		t.Fatalf("find-missing = %+v; want exactly [%s] marked, none unverified", res, killedID)
	}
	for id, want := range map[string]string{killedID: store.StateMissing, liveID: store.StateWorking} {
		if got := rowColumns(t, home, id).State; got != want {
			t.Errorf("row %s state = %v; want %s", id, got, want)
		}
	}
	ticks := eventsOf(readTrailLines(t, home), "ad.find_missing.tick")
	if len(ticks) != 1 || ticks[0]["claude_instance_id"] != killedID || ticks[0]["reconciliation_reason"] != "proc_absent" ||
		ticks[0]["source"] != "ad_find_missing" {
		t.Errorf("ticks = %v; want one proc_absent tick for %s from ad_find_missing", ticks, killedID)
	}
}

// TestFindMissingCLITmuxAnswers: rows recording no process evidence, one of
// them pending past the default grace period, are decided by one lookup on
// their socket: Gone marks each tmux_absent; tmux unavailable (the socket's
// permission-denied reply) marks none and notes each
// process_not_seen_tmux_unchecked.
func TestFindMissingCLITmuxAnswers(t *testing.T) {
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

			res := runFindMissing(t, home, map[string]string{faketmuxfix.EnvTables: tables.Dir})
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
