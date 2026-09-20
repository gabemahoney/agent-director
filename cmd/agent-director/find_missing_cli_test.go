package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procstat"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// findMissingTickLines filters trail lines for ad.find_missing.tick events.
func findMissingTickLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.find_missing.tick" {
			out = append(out, l)
		}
	}
	return out
}

// realChild is a live OS process spawned by a find-missing acceptance test,
// carrying a known AGENT_DIRECTOR_INSTANCE_ID in its environment. The engine's
// per-row checker reads this child's real /proc/<pid>/stat (field 22) and
// /proc/<pid>/environ to reach an evidence-based verdict, so the seeded row
// must carry the child's ACTUAL pid + starttime (captured via spawnRealChild).
type realChild struct {
	cmd       *exec.Cmd
	pid       int
	starttime string // /proc/<pid>/stat field 22, verbatim decimal ticks
}

// spawnRealChild starts a long-lived `sleep` process whose environment carries
// AGENT_DIRECTOR_INSTANCE_ID=instanceID, then captures its real pid and its
// real proc_starttime (field 22 of /proc/<pid>/stat). Those two values are what
// the caller seeds onto the spawn row so the Linux liveness checker verifies the
// child against the live OS. The env id is set so the checker's environ
// tiebreaker (starttime-match → readable environ HAS id → verified-alive) can
// discriminate byte-identical argv twins by instance id.
//
// The process is registered for cleanup (Kill + Wait) so no strays leak out of
// the sandbox; callers that kill it themselves mid-test call reapChild, and the
// cleanup Wait then becomes a harmless no-op.
func spawnRealChild(t *testing.T, instanceID string) *realChild {
	t.Helper()
	// 3600s sleep: outlives any single test; killed explicitly or reaped in
	// cleanup. Byte-identical argv across twins is intentional — the whole point
	// of scenario (a) is that argv alone can't tell two children apart.
	cmd := exec.Command("sleep", "3600")
	cmd.Env = append(os.Environ(), "AGENT_DIRECTOR_INSTANCE_ID="+instanceID)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawnRealChild(%s): start: %v", instanceID, err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return &realChild{cmd: cmd, pid: pid, starttime: procstat.ReadStarttime(t, pid)}
}

// reapChild kills a spawned child and Waits so its /proc entry vanishes before
// find-missing runs. Waiting is essential: a killed-but-unreaped child lingers
// as a zombie with a live /proc/<pid>/stat, which the checker would read as
// alive. After the Wait the pid is gone (ENOENT) and the checker returns
// provably-dead.
func reapChild(t *testing.T, c *realChild) {
	t.Helper()
	if err := c.cmd.Process.Kill(); err != nil {
		t.Fatalf("reapChild: kill pid %d: %v", c.pid, err)
	}
	if _, err := c.cmd.Process.Wait(); err != nil {
		t.Fatalf("reapChild: wait pid %d: %v", c.pid, err)
	}
}

// parseFindMissingResult unmarshals the find-missing CLI stdout envelope
// (FindMissingResult JSON: count, ids, unverified, unverified_ids).
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

// readSpawnState reads the current state of a spawn row directly.
func readSpawnState(t *testing.T, dbPath, instanceID string) string {
	t.Helper()
	state, _ := readSpawnRow(t, dbPath, instanceID)
	return state
}

// TestFindMissingCLIArgvTwinDiscrimination proves the per-row checker
// discriminates two byte-identical-argv children by real pid + starttime (and,
// for the survivor, the environ instance-id tiebreaker). Two `sleep 3600`
// children are spawned — indistinguishable by argv — each holding its own
// AGENT_DIRECTOR_INSTANCE_ID in env; each spawn row is seeded with that child's
// ACTUAL pid + starttime. One child is killed AND reaped so its /proc entry
// vanishes; the other stays live. find-missing must mark EXACTLY the killed
// row missing and leave the survivor in its live state. Subsumes b.94s.
func TestFindMissingCLIArgvTwinDiscrimination(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const killedID = "id-twin-killed"
	const liveID = "id-twin-live"

	killed := spawnRealChild(t, killedID)
	live := spawnRealChild(t, liveID)

	// Seed each row with its child's REAL pid + starttime so the checker
	// verifies liveness against the live OS, not a fixture.
	if _, err := apitest.SeedSpawn(dbPath, killedID, "working", "/tmp", "off", "",
		false, apitest.WithPID(killed.pid), apitest.WithProcStarttime(killed.starttime)); err != nil {
		t.Fatalf("seed killed twin: %v", err)
	}
	if _, err := apitest.SeedSpawn(dbPath, liveID, "working", "/tmp", "off", "",
		false, apitest.WithPID(live.pid), apitest.WithProcStarttime(live.starttime)); err != nil {
		t.Fatalf("seed live twin: %v", err)
	}

	// Kill + reap ONLY the killed twin: its /proc/<pid> now returns ENOENT.
	reapChild(t, killed)

	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	res := parseFindMissingResult(t, stdout)
	if res.Count != 1 || len(res.IDs) != 1 || res.IDs[0] != killedID {
		t.Fatalf("find-missing marked %v (count=%d); want exactly [%s]", res.IDs, res.Count, killedID)
	}
	if got := readSpawnState(t, dbPath, killedID); got != "missing" {
		t.Errorf("killed twin state = %q; want missing", got)
	}
	if got := readSpawnState(t, dbPath, liveID); got != "working" {
		t.Errorf("survivor twin state = %q; want working (must stay live)", got)
	}
}

// TestFindMissingCLIInheritedIDChildStillMarked proves the recorded pid +
// starttime are authoritative: a surviving CHILD holding the inherited instance
// id does NOT rescue a row whose recorded pid is gone. Construction: spawn a
// parent `sleep` holding the instance id in env, then spawn a grandchild that
// INHERITS the parent's env (same instance id). The spawn row records the
// PARENT's pid + starttime. Kill + reap the PARENT; the child keeps running,
// still carrying AGENT_DIRECTOR_INSTANCE_ID. Because the row's recorded pid is
// gone (ENOENT), the checker returns provably-dead and the row is marked
// missing — the surviving child's env is irrelevant to a pid-anchored verdict.
func TestFindMissingCLIInheritedIDChildStillMarked(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const inheritedID = "id-inherited"

	// Parent holds the instance id; the row will record ITS pid + starttime.
	parent := spawnRealChild(t, inheritedID)

	// Grandchild inherits the parent's env (same AGENT_DIRECTOR_INSTANCE_ID)
	// and survives the parent's death. `sh -c 'exec sleep 3600'` gives us a
	// long-lived process holding the inherited id after the parent is reaped.
	survivor := exec.Command("sh", "-c", "exec sleep 3600")
	survivor.Env = append(os.Environ(), "AGENT_DIRECTOR_INSTANCE_ID="+inheritedID)
	if err := survivor.Start(); err != nil {
		t.Fatalf("start inherited-id survivor: %v", err)
	}
	t.Cleanup(func() {
		_ = survivor.Process.Kill()
		_, _ = survivor.Process.Wait()
	})

	if _, err := apitest.SeedSpawn(dbPath, inheritedID, "working", "/tmp", "off", "",
		false, apitest.WithPID(parent.pid), apitest.WithProcStarttime(parent.starttime)); err != nil {
		t.Fatalf("seed inherited-id row: %v", err)
	}

	// The recorded pid dies; the survivor keeps the id alive but at a DIFFERENT pid.
	reapChild(t, parent)

	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	res := parseFindMissingResult(t, stdout)
	if res.Count != 1 || len(res.IDs) != 1 || res.IDs[0] != inheritedID {
		t.Fatalf("find-missing marked %v (count=%d); want exactly [%s] "+
			"(pid+starttime authoritative; surviving child's env must not rescue it)",
			res.IDs, res.Count, inheritedID)
	}
	if got := readSpawnState(t, dbPath, inheritedID); got != "missing" {
		t.Errorf("inherited-id row state = %q; want missing", got)
	}
}

// TestFindMissingCLIPostRebootShape proves the post-reboot shape reconciles
// with NO refusal (the removed degraded-mode guard would have aborted here).
// Rows carry NULL identity (no pid/starttime seeded) and no live process holds
// their instance id, so the environ probe set is effectively empty for them.
// Under the guard-free per-row engine these NULL-identity rows fall through to
// the probe-set-diff fallback and are ALL marked missing: count>0, no refusal.
// The CLI envelope must also carry the additive unverified fields:
// unverified=0 and unverified_ids=[] (no permission-walled rows in this shape).
func TestFindMissingCLIPostRebootShape(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	// NULL identity: no WithPID / WithProcStarttime — pid+starttime stay NULL,
	// routing each row through the SR-7.5 environ probe-set fallback. The ids are
	// randomized-unique so no unrelated live process in the sandbox holds them.
	rebootIDs := []string{
		"id-postreboot-" + strconv.Itoa(os.Getpid()) + "-a",
		"id-postreboot-" + strconv.Itoa(os.Getpid()) + "-b",
		"id-postreboot-" + strconv.Itoa(os.Getpid()) + "-c",
	}
	for _, id := range rebootIDs {
		if _, err := apitest.SeedSpawn(dbPath, id, "working", "/tmp", "off", "", false); err != nil {
			t.Fatalf("seed post-reboot row %s: %v", id, err)
		}
	}

	stdout, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0 (no refusal post-reboot)\nstderr=%s", code, stderr)
	}

	res := parseFindMissingResult(t, stdout)
	if res.Count == 0 {
		t.Fatalf("find-missing count = 0; post-reboot shape must reconcile (no refusal), count>0\nstdout=%s", stdout)
	}
	if res.Count != len(rebootIDs) {
		t.Errorf("find-missing count = %d; want %d (all NULL-identity rows marked)", res.Count, len(rebootIDs))
	}
	for _, id := range rebootIDs {
		if got := readSpawnState(t, dbPath, id); got != "missing" {
			t.Errorf("post-reboot row %s state = %q; want missing", id, got)
		}
	}

	// The additive envelope fields must be present and, in this shape, empty:
	// no permission-walled rows → unverified=0, unverified_ids=[].
	if res.Unverified != 0 {
		t.Errorf("unverified = %d; want 0 (no EACCES rows in post-reboot shape)", res.Unverified)
	}
	if res.UnverifiedIDs == nil {
		t.Errorf("unverified_ids is absent/null; want [] (additive field must be present)")
	}
	if len(res.UnverifiedIDs) != 0 {
		t.Errorf("unverified_ids = %v; want []", res.UnverifiedIDs)
	}
	// Assert the raw JSON key is present (additive-field contract), not merely
	// that the parsed slice is empty.
	if !strings.Contains(stdout, `"unverified_ids"`) {
		t.Errorf("stdout missing unverified_ids key: %s", stdout)
	}
	if !strings.Contains(stdout, `"unverified"`) {
		t.Errorf("stdout missing unverified key: %s", stdout)
	}
}

// TestFindMissingTrailEmitsRowMutation runs `agent-director find-missing`
// against a DB that has one live spawn with an open permission_requests row
// (simulating a Claude instance whose process has vanished). It asserts that
// exactly one ad.row_mutation.committed trail line is emitted with
// writer_process="find_missing" and mutation_kind="update" — confirming that
// CloseOrphanedPermissionRequests drives the row-mutation event via the same
// DecidePermissionRequest path as the decide verb.
//
// The orphan row carries NULL identity (no seeded pid/starttime), so it routes
// through the SR-7.5 environ probe-set fallback. No live process holds its
// instance id, so the probe set never contains it and the sweep marks it
// missing — no /proc anchor needed under the guard-free per-row engine.
func TestFindMissingTrailEmitsRowMutation(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	// Seed a spawn in a live state with an open permission request.
	const orphanID = "id-fm-trail-1"
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-trail-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, orphanID, testRequestToken, "Bash", `{"cmd":"ls"}`)

	_, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	// CloseOrphanedPermissionRequests calls DecidePermissionRequest("deny",
	// DecisionReasonFindMissing, WriterProcessFindMissing) once per open row.
	// With one open row, exactly one ad.row_mutation.committed line must appear.
	lines := readTrailLines(t, home)
	rm := rowMutationCommittedLines(lines)
	if len(rm) != 1 {
		t.Fatalf("ad.row_mutation.committed line count = %d; want 1", len(rm))
	}
	if rm[0]["writer_process"] != "find_missing" {
		t.Errorf("writer_process = %v; want find_missing", rm[0]["writer_process"])
	}
	if rm[0]["mutation_kind"] != "update" {
		t.Errorf("mutation_kind = %v; want update", rm[0]["mutation_kind"])
	}
}

// TestFindMissingTrailEmitsProcAbsentTick confirms that find-missing emits one
// ad.find_missing.tick line with reconciliation_reason="proc_absent" when a
// live spawn is absent and has no open permission_requests rows. The orphan row
// carries NULL identity, so it routes through the environ probe-set fallback; no
// live process holds its id, so the sweep marks it missing.
func TestFindMissingTrailEmitsProcAbsentTick(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const orphanID = "id-fm-tick-pa-1"
	// Use working state / relay_mode=off: no open permission_requests rows,
	// so only one tick (proc_absent) should be emitted.
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-tick-pa-1", "working", "off")

	_, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	lines := readTrailLines(t, home)
	ticks := findMissingTickLines(lines)
	// One spawn, no open permission requests → exactly one proc_absent tick.
	if len(ticks) != 1 {
		t.Fatalf("ad.find_missing.tick line count = %d; want 1", len(ticks))
	}
	tick := ticks[0]
	if tick["reconciliation_reason"] != "proc_absent" {
		t.Errorf("reconciliation_reason = %v; want proc_absent", tick["reconciliation_reason"])
	}
	if tick["claude_instance_id"] != orphanID {
		t.Errorf("claude_instance_id = %v; want %q", tick["claude_instance_id"], orphanID)
	}
	if tick["new_state"] != "missing" {
		t.Errorf("new_state = %v; want missing", tick["new_state"])
	}
	if tick["source"] != "ad_find_missing" {
		t.Errorf("source = %v; want ad_find_missing", tick["source"])
	}
}

// TestFindMissingTrailEmitsPermissionOrphanCloseoutTick confirms that
// find-missing emits one ad.find_missing.tick line with
// reconciliation_reason="permission_orphan_closeout" — carrying the
// request_token — when it closes an open permission_requests row for a swept
// spawn. In addition to the orphan-closeout tick, a proc_absent tick must also
// appear for the spawn transition itself. The orphan row carries NULL identity
// and routes through the environ probe-set fallback.
func TestFindMissingTrailEmitsPermissionOrphanCloseoutTick(t *testing.T) {
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")

	const orphanID = "id-fm-tick-poc-1"
	seedSpawnRow(t, dbPath, orphanID, "cd-fm-tick-poc-1", "check_permission", "on")
	seedOpenPermissionRequest(t, dbPath, orphanID, testRequestToken, "Bash", `{"cmd":"ls"}`)

	_, stderr, code := runCLIWithEnv(t, home, map[string]string{}, "", "find-missing")
	if code != 0 {
		t.Fatalf("find-missing exit = %d; want 0\nstderr=%s", code, stderr)
	}

	lines := readTrailLines(t, home)
	ticks := findMissingTickLines(lines)

	// Expect two ticks: proc_absent (spawn sweep) + permission_orphan_closeout
	// (open permission_requests row closed by CloseOrphanedPermissionRequests).
	var procAbsent, orphanCloseout map[string]any
	for _, tk := range ticks {
		switch tk["reconciliation_reason"] {
		case "proc_absent":
			procAbsent = tk
		case "permission_orphan_closeout":
			orphanCloseout = tk
		}
	}

	if procAbsent == nil {
		t.Fatalf("no proc_absent tick found in %d ad.find_missing.tick line(s)", len(ticks))
	}
	if orphanCloseout == nil {
		t.Fatalf("no permission_orphan_closeout tick found in %d ad.find_missing.tick line(s)", len(ticks))
	}

	// proc_absent: the orphan's instance id and source must be present.
	if procAbsent["claude_instance_id"] != orphanID {
		t.Errorf("proc_absent: claude_instance_id = %v; want %q", procAbsent["claude_instance_id"], orphanID)
	}
	if procAbsent["source"] != "ad_find_missing" {
		t.Errorf("proc_absent: source = %v; want ad_find_missing", procAbsent["source"])
	}

	// permission_orphan_closeout: must carry the request_token and instance id.
	if orphanCloseout["claude_instance_id"] != orphanID {
		t.Errorf("permission_orphan_closeout: claude_instance_id = %v; want %q",
			orphanCloseout["claude_instance_id"], orphanID)
	}
	if orphanCloseout["request_token"] != testRequestToken {
		t.Errorf("permission_orphan_closeout: request_token = %v; want %q",
			orphanCloseout["request_token"], testRequestToken)
	}
	if orphanCloseout["source"] != "ad_find_missing" {
		t.Errorf("permission_orphan_closeout: source = %v; want ad_find_missing", orphanCloseout["source"])
	}
}
