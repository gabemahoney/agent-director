package main_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// findMissingTicks returns home's ad.find_missing.tick trail lines.
func findMissingTicks(t *testing.T, home string) []map[string]any {
	t.Helper()
	return trailEvents(t, home, "ad.find_missing.tick")
}

// realChild is a real OS process a find-missing test records on a row: its
// pid and its start time as the start-time reader reports it.
type realChild struct {
	cmd       *exec.Cmd
	pid       int
	starttime string // /proc/<pid>/stat field 22, verbatim decimal ticks
}

// childEnv is the test's environment without any inherited instance id, plus
// probe.EnvKey=instanceID when instanceID is non-empty.
func childEnv(instanceID string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, probe.EnvKey+"=")
	})
	if instanceID != "" {
		env = append(env, probe.EnvKey+"="+instanceID)
	}
	return env
}

// startChild starts cmd with childEnv(instanceID), reads its real start time,
// and kills and reaps it in cleanup.
func startChild(t *testing.T, cmd *exec.Cmd, instanceID string) *realChild {
	t.Helper()
	cmd.Env = childEnv(instanceID)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", cmd.Args, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return &realChild{cmd: cmd, pid: cmd.Process.Pid, starttime: procstat.ReadStarttime(t, cmd.Process.Pid)}
}

// spawnRealChild starts a long-lived `sleep`, carrying instanceID in its
// environment unless instanceID is empty.
func spawnRealChild(t *testing.T, instanceID string) *realChild {
	t.Helper()
	return startChild(t, exec.Command("sleep", "3600"), instanceID)
}

// reapChild kills c and waits for it, so its pid is gone (not a zombie)
// before find-missing runs.
func reapChild(t *testing.T, c *realChild) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatalf("reapChild: kill pid %d: %v", c.pid, err)
	}
	if _, err := c.cmd.Process.Wait(); err != nil {
		t.Fatalf("reapChild: wait pid %d: %v", c.pid, err)
	}
}

// spawnReapedChild returns the identity of a process that has already exited
// and been reaped.
func spawnReapedChild(t *testing.T) *realChild {
	t.Helper()
	c := spawnRealChild(t, "")
	reapChild(t, c)
	return c
}

// spawnDeadParentOfLiveChild starts a shell carrying instanceID that forks a
// `sleep` inheriting it, then reaps the shell; the orphaned sleep lives on and
// is killed in cleanup. It returns the dead shell.
func spawnDeadParentOfLiveChild(t *testing.T, instanceID string) *realChild {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 3600 >/dev/null 2>&1 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	parent := startChild(t, cmd, instanceID)
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("read forked child pid: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("forked child pid %q: %v", line, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })
	reapChild(t, parent)
	procstat.ReadStarttime(t, childPID) // the child outlived its parent
	return parent
}

// recordProcess records sessionStart as the row's SessionStart identity and
// pane as its pane identity; a nil process is not recorded.
func recordProcess(sessionStart, pane *realChild) []apitest.SpawnOption {
	id := store.LaunchIdentity{Socket: apitest.TestSocket}
	if pane != nil {
		id.PaneID, id.PanePID, id.PaneStarttime = apitest.TestPaneID, pane.pid, pane.starttime
	}
	opts := []apitest.SpawnOption{apitest.WithLaunchIdentity(id)}
	if sessionStart != nil {
		opts = append(opts, apitest.WithPID(sessionStart.pid), apitest.WithProcStarttime(sessionStart.starttime))
	}
	return opts
}

// seedRow seeds one row through apitest.SeedSpawn with opts.
func seedRow(t *testing.T, dbPath, id, state, relayMode string, opts ...apitest.SpawnOption) {
	t.Helper()
	if _, err := apitest.SeedSpawn(dbPath, id, state, "/tmp", relayMode, "", false, opts...); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// seedDeadRow seeds a row whose recorded pane process is a reaped child.
func seedDeadRow(t *testing.T, dbPath, id, state, relayMode string) {
	t.Helper()
	seedRow(t, dbPath, id, state, relayMode, recordProcess(nil, spawnReapedChild(t))...)
}

// findMissingResult is the find-missing CLI stdout envelope.
type findMissingResult struct {
	Count         int      `json:"count"`
	IDs           []string `json:"ids"`
	Unverified    int      `json:"unverified"`
	UnverifiedIDs []string `json:"unverified_ids"`
}

func parseFindMissingResult(t *testing.T, stdout string) findMissingResult {
	t.Helper()
	var r findMissingResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("find-missing stdout is not JSON-parseable: %v\nstdout=%q", err, stdout)
	}
	return r
}

// runFindMissing runs the built find-missing under home, requires exit 0 and
// returns the parsed envelope with the raw stdout.
func runFindMissing(t *testing.T, home string) (findMissingResult, string) {
	t.Helper()
	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}
	return parseFindMissingResult(t, stdout), stdout
}

// readSpawnState reads the current state of a spawn row directly.
func readSpawnState(t *testing.T, dbPath, instanceID string) string {
	t.Helper()
	state, _ := readSpawnRow(t, dbPath, instanceID)
	return state
}

// findMissingHome bootstraps a throwaway HOME and returns it with its state.db.
func findMissingHome(t *testing.T) (home, dbPath string) {
	t.Helper()
	home = t.TempDir()
	bootstrapDB(t, home)
	return home, stateDB(home)
}

// TestFindMissingCLIArgvTwinDiscrimination: of two byte-identical `sleep`
// children, only the reaped one's row is marked; the live twin's stays live.
func TestFindMissingCLIArgvTwinDiscrimination(t *testing.T) {
	home, dbPath := findMissingHome(t)
	const killedID, liveID = "id-twin-killed", "id-twin-live"

	killed := spawnRealChild(t, killedID)
	live := spawnRealChild(t, liveID)
	seedRow(t, dbPath, killedID, "working", "off", recordProcess(killed, killed)...)
	seedRow(t, dbPath, liveID, "working", "off", recordProcess(live, live)...)
	reapChild(t, killed)

	res, _ := runFindMissing(t, home)
	if !slices.Equal(res.IDs, []string{killedID}) || res.Count != 1 {
		t.Fatalf("find-missing marked %v (count=%d); want exactly [%s]", res.IDs, res.Count, killedID)
	}
	if got := readSpawnState(t, dbPath, killedID); got != "missing" {
		t.Errorf("killed twin state = %q; want missing", got)
	}
	if got := readSpawnState(t, dbPath, liveID); got != "working" {
		t.Errorf("survivor twin state = %q; want working (must stay live)", got)
	}
}

// TestFindMissingCLILiveProcessKeepsRow: a recorded process alive with its start
// time keeps the row live, whether or not its environment carries the id.
func TestFindMissingCLILiveProcessKeepsRow(t *testing.T) {
	cases := []struct {
		name   string
		envID  bool
		record func(c *realChild) []apitest.SpawnOption
	}{
		{"SessionStart identity, no id in environment", false,
			func(c *realChild) []apitest.SpawnOption { return recordProcess(c, nil) }},
		{"pane identity, no id in environment", false,
			func(c *realChild) []apitest.SpawnOption { return recordProcess(nil, c) }},
		{"both identities agree, id in environment", true,
			func(c *realChild) []apitest.SpawnOption { return recordProcess(c, c) }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, dbPath := findMissingHome(t)
			id := "id-fm-live-" + strconv.Itoa(i)
			envID := ""
			if tc.envID {
				envID = id
			}
			seedRow(t, dbPath, id, "working", "off", tc.record(spawnRealChild(t, envID))...)

			res, _ := runFindMissing(t, home)
			if len(res.IDs) != 0 || len(res.UnverifiedIDs) != 0 {
				t.Errorf("ids = %v, unverified_ids = %v; want both []", res.IDs, res.UnverifiedIDs)
			}
			if got := readSpawnState(t, dbPath, id); got != "working" {
				t.Errorf("state = %q; want working", got)
			}
		})
	}
}

// TestFindMissingCLIInheritedIDChildStillMarked: a reaped agent process marks
// its row proc_absent even while another live process carries the row's id.
func TestFindMissingCLIInheritedIDChildStillMarked(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, id string) []apitest.SpawnOption
	}{
		{"child of the dead SessionStart process survives", func(t *testing.T, id string) []apitest.SpawnOption {
			return recordProcess(spawnDeadParentOfLiveChild(t, id), nil)
		}},
		{"child of the dead pane process survives", func(t *testing.T, id string) []apitest.SpawnOption {
			return recordProcess(nil, spawnDeadParentOfLiveChild(t, id))
		}},
		{"leaked copy in an unrelated live process", func(t *testing.T, id string) []apitest.SpawnOption {
			spawnRealChild(t, id)
			return recordProcess(nil, spawnReapedChild(t))
		}},
		{"live SessionStart process, reaped pane process", func(t *testing.T, id string) []apitest.SpawnOption {
			return recordProcess(spawnRealChild(t, id), spawnReapedChild(t))
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, dbPath := findMissingHome(t)
			id := "id-fm-carrier-" + strconv.Itoa(i)
			seedRow(t, dbPath, id, "working", "off", tc.setup(t, id)...)

			res, _ := runFindMissing(t, home)
			if !slices.Equal(res.IDs, []string{id}) || readSpawnState(t, dbPath, id) != "missing" {
				t.Fatalf("ids = %v, state = %q; want [%s], missing", res.IDs, readSpawnState(t, dbPath, id), id)
			}
			ticks := findMissingTicks(t, home)
			if len(ticks) != 1 || ticks[0]["reconciliation_reason"] != "proc_absent" {
				t.Errorf("ticks = %v; want one proc_absent", ticks)
			}
			if d := trailEvents(t, home, "ad.provenance.disagree"); len(d) != 0 {
				t.Errorf("ad.provenance.disagree = %v; want none", d)
			}
		})
	}
}

// TestFindMissingCLIPostRebootShape: every recorded process gone marks every row
// with no refusal, and the envelope carries unverified 0 and unverified_ids [].
func TestFindMissingCLIPostRebootShape(t *testing.T) {
	home, dbPath := findMissingHome(t)
	rebootIDs := []string{"id-postreboot-a", "id-postreboot-b", "id-postreboot-c"}
	for _, id := range rebootIDs {
		seedDeadRow(t, dbPath, id, "working", "off")
	}

	res, stdout := runFindMissing(t, home)
	if res.Count != len(rebootIDs) || !slices.Equal(res.IDs, rebootIDs) {
		t.Errorf("count = %d, ids = %v; want %d, %v", res.Count, res.IDs, len(rebootIDs), rebootIDs)
	}
	for _, id := range rebootIDs {
		if got := readSpawnState(t, dbPath, id); got != "missing" {
			t.Errorf("post-reboot row %s state = %q; want missing", id, got)
		}
	}
	if res.Unverified != 0 || res.UnverifiedIDs == nil || len(res.UnverifiedIDs) != 0 {
		t.Errorf("unverified = %d, unverified_ids = %v; want 0, []", res.Unverified, res.UnverifiedIDs)
	}
	for _, key := range []string{`"unverified":`, `"unverified_ids":`} {
		if !strings.Contains(stdout, key) {
			t.Errorf("stdout missing %s key: %s", key, stdout)
		}
	}
}

// seedOrphanWithRequest seeds a check_permission row whose recorded process is
// reaped, with one open permission request; it returns the request token.
func seedOrphanWithRequest(t *testing.T, dbPath, id string) string {
	t.Helper()
	seedDeadRow(t, dbPath, id, "check_permission", "on")
	req, err := apitest.SeedPermissionRequest(dbPath, id, "Bash")
	if err != nil {
		t.Fatalf("seed permission request: %v", err)
	}
	return req.RequestToken
}

// TestFindMissingTrailEmitsRowMutation: denying a marked row's open permission
// request writes exactly one find_missing update row-mutation line.
func TestFindMissingTrailEmitsRowMutation(t *testing.T) {
	home, dbPath := findMissingHome(t)
	seedOrphanWithRequest(t, dbPath, "id-fm-trail-1")

	runFindMissing(t, home)

	rm := rowMutationCommittedLines(readTrailLines(t, home))
	if len(rm) != 1 {
		t.Fatalf("ad.row_mutation.committed line count = %d; want 1", len(rm))
	}
	if rm[0]["writer_process"] != "find_missing" || rm[0]["mutation_kind"] != "update" {
		t.Errorf("writer_process = %v, mutation_kind = %v; want find_missing, update",
			rm[0]["writer_process"], rm[0]["mutation_kind"])
	}
}

// TestFindMissingTrailEmitsProcAbsentTick: a row whose recorded process is
// reaped yields exactly one proc_absent tick.
func TestFindMissingTrailEmitsProcAbsentTick(t *testing.T) {
	home, dbPath := findMissingHome(t)
	const orphanID = "id-fm-tick-pa-1"
	seedDeadRow(t, dbPath, orphanID, "working", "off")

	runFindMissing(t, home)

	ticks := findMissingTicks(t, home)
	if len(ticks) != 1 {
		t.Fatalf("ad.find_missing.tick line count = %d; want 1", len(ticks))
	}
	want := map[string]any{
		"reconciliation_reason": "proc_absent",
		"claude_instance_id":    orphanID,
		"new_state":             "missing",
		"source":                "ad_find_missing",
	}
	for k, v := range want {
		if ticks[0][k] != v {
			t.Errorf("%s = %v; want %v", k, ticks[0][k], v)
		}
	}
}

// TestFindMissingTrailEmitsPermissionOrphanCloseoutTick: marking a row with an
// open request yields a proc_absent tick and a closeout tick with the token.
func TestFindMissingTrailEmitsPermissionOrphanCloseoutTick(t *testing.T) {
	home, dbPath := findMissingHome(t)
	const orphanID = "id-fm-tick-poc-1"
	token := seedOrphanWithRequest(t, dbPath, orphanID)

	runFindMissing(t, home)

	byReason := map[any]map[string]any{}
	for _, tk := range findMissingTicks(t, home) {
		byReason[tk["reconciliation_reason"]] = tk
	}
	for _, reason := range []string{"proc_absent", "permission_orphan_closeout"} {
		tk := byReason[reason]
		if tk == nil {
			t.Fatalf("no %s tick in %v", reason, byReason)
		}
		if tk["claude_instance_id"] != orphanID || tk["source"] != "ad_find_missing" {
			t.Errorf("%s: claude_instance_id = %v, source = %v; want %q, ad_find_missing",
				reason, tk["claude_instance_id"], tk["source"], orphanID)
		}
	}
	if got := byReason["permission_orphan_closeout"]["request_token"]; got != token {
		t.Errorf("permission_orphan_closeout: request_token = %v; want %q", got, token)
	}
}
