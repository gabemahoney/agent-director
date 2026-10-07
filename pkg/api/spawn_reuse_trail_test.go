package api_test

// spawn_reuse_trail_test.go holds reuse's trail-event checks (SR-10.6, SR-14;
// AC-REUSE-12) and spawn's fail-open (SR-15): with the trail unwritable, the
// label scan, the held-name path and every reuse event's path return the same
// results and leave the same rows as with a working trail.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rutRequested is the session name the reuse calls request, never a row's
// recorded name.
const rutRequested = "reuse-requested"

// reuse's trail events.
const (
	rutReused   = "ad.spawn.reused"
	rutRestored = "ad.spawn.reuse_restored"
	rutNameHeld = "ad.launch.name_held"
)

// The full key sets of reuse's two events: SR-14's fields plus the envelope's.
var (
	rutReusedKeys   = []string{"event", "ts", "claude_instance_id", "prior_state", "archived_session_id", "lookup_outcome", "source"}
	rutRestoredKeys = []string{"event", "ts", "claude_instance_id", "applied", "launch_error", "restore_error", "source"}
)

// rutAssertOrder fails unless id's reuse events (reused, reuse_restored,
// name_held) written since mark are exactly want, in order.
func rutAssertOrder(t *testing.T, mark int, id string, want ...string) {
	t.Helper()
	var got []string
	for _, l := range readAPITrailLines(t)[mark:] {
		ev, _ := l["event"].(string)
		if l["claude_instance_id"] == id && (ev == rutReused || ev == rutRestored || ev == rutNameHeld) {
			got = append(got, ev)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("trail order = %q; want %q", got, want)
	}
}

// rutRestoreError is the store error text the WARN line in logs gives for
// id's failed restore ("" when there is none).
func rutRestoreError(logs, id string) string {
	prefix := "WARN: spawn: restoring instance " + id + " to its prior state after a failed launch failed: "
	for _, l := range strings.Split(logs, "\n") {
		if rest, ok := strings.CutPrefix(l, prefix); ok {
			return rest
		}
	}
	return ""
}

// rutAssertRestored fails unless id has exactly one ad.spawn.reuse_restored
// since mark, every field: applied as given, launch_error launchErr and
// restore_error the WARN line's text in logs (null when none).
func rutAssertRestored(t *testing.T, mark int, id string, applied bool, launchErr, logs string) {
	t.Helper()
	recs := ptRecords(t, mark, rutRestored, id)
	if len(recs) != 1 {
		t.Fatalf("%s records = %d; want 1: %v", rutRestored, len(recs), recs)
	}
	var restoreErr any
	if s := rutRestoreError(logs, id); s != "" {
		restoreErr = s
	}
	assertTrailRecord(t, recs[0], rutRestoredKeys, map[string]any{"claude_instance_id": id, "applied": applied,
		"launch_error": launchErr, "restore_error": restoreErr, "source": "ad_spawn"})
}

// rutRenamed is r's resumeRow recorded under rutRequested, so arrangeHeld
// places its holders under the requested name.
func rutRenamed(r reuseRow) resumeRow {
	rr := r.resumeRow
	rr.killRow = r.withName(rutRequested)
	return rr
}

// sftChildEnv gates TestSpawnTrailFailOpenChild and carries the id prefix.
const sftChildEnv = "AD_SPAWN_TRAIL_FAIL_CHILD"

// failOpenLinePrefix marks the child's result lines in its output.
const failOpenLinePrefix = "FAILOPEN|"

// failOpenReuseRuns reuses one row per event reuse writes and returns one
// line each, per-test values replaced; with a working trail it checks the event.
func failOpenReuseRuns(t *testing.T, working bool) []string {
	t.Helper()
	runs := []struct {
		name, event string
		arrange     func(*testing.T, *killEnv, reuseRow)
		held        bool // "duplicate session" with an old-label holder
	}{
		{name: "success", event: rutReused},
		{name: "server-restarted", event: "ad.provenance.disagree",
			arrange: func(t *testing.T, e *killEnv, r reuseRow) { rpvRestart(t, e, &r.resumeRow) }},
		{name: "restore-store-error", event: rutRestored, arrange: func(t *testing.T, e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}, tmux.CallCreate)
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
		}},
		{name: "duplicate-old", event: rutNameHeld, held: true},
	}
	var lines []string
	for _, run := range runs {
		e := newKillEnv(t)
		r := e.seedReusable(t, agentGone, reuseRowSpec{})
		if run.arrange != nil {
			run.arrange(t, e, r)
		}
		if run.held {
			e.arrangeHeld(t, rutRenamed(r), heldSpec{Holder: holderOld})
		}
		res, logs, err := e.reuseWith(t, &hookedReuseStore{st: e.st}, reuseParams(t, r, reuseRequest{Name: rutRequested}))
		c := e.columns(t, r.ID)
		l := fmt.Sprintf("reuse %s id=%s pre_trust=%s err=%v warn=%t state=%v ended_at=%v row_version=%v life=%v launch_started_at=%v parent=%v socket=%v",
			run.name, res.ClaudeInstanceID, res.PreTrust, err, rutRestoreError(logs, r.ID) != "", c.State, c.EndedAt, c.RowVersion,
			c.LifeNumber, c.LaunchStartedAt, c.ParentID, c.TmuxSocket)
		lines = append(lines, strings.NewReplacer(r.ID, "<id>", r.Socket, "<socket>", r.CWD, "<cwd>",
			r.Trust.dir, "<trust>", r.ParentID, "<parent>").Replace(l))
		if working && len(ptRecords(t, 0, run.event, r.ID)) == 0 {
			t.Errorf("working trail: %s wrote no %s", run.name, run.event)
		}
	}
	return lines
}

// failOpenPlainRuns runs plain spawns of ids prefix-<name>, each writing one
// name_held (the scan's, a held name's with the end write applied and failed),
// and returns one line each; with a working trail it checks the record.
func failOpenPlainRuns(t *testing.T, prefix string, working bool) []string {
	t.Helper()
	cases := []struct {
		name       string
		scan, late bool // a leftover refuses the label scan; or appears as the scan returns
		atCreate   func(t *testing.T, e heldEnv, id string)
	}{
		{name: "scan", scan: true},
		{name: "old", late: true},
		{name: "still-pending", atCreate: func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}},
	}
	var lines []string
	for _, tc := range cases {
		e := newHeldEnv(t)
		id := prefix + "-" + tc.name
		switch {
		case tc.scan:
			e.rec.SeedSessions(e.socket, e.leftover("old-life", "$5", id, 0))
		case !tc.late:
			e.rec.SeedSessions(e.socket, heldHolder("$4", tmux.Label{}, false))
		}
		if tc.atCreate != nil {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { tc.atCreate(t, e, id) })
		}
		run := e.spawnHeld(t, id, heldName, func() {
			if tc.late {
				e.rec.SeedSessions(e.socket, e.leftover(heldName, "$4", id, heldCreated))
			}
		})
		l := fmt.Sprintf("plain %s err=%v calls=%v warns=%d", tc.name, run.err, callKinds(e.rec), strings.Count(e.logs.String(), "WARN"))
		if c, err := apitest.ReadSpawnColumns(e.dbPath, id); err == nil {
			l += fmt.Sprintf(" state=%v row_version=%v ended_at=%v launch_started_at=%v", c.State, c.RowVersion, c.EndedAt, c.LaunchStartedAt)
		}
		lines = append(lines, l)
		if working && len(ptRecords(t, 0, rutNameHeld, id)) != 1 {
			t.Errorf("working trail: %s wrote no single %s", id, rutNameHeld)
		}
	}
	return lines
}

// TestSpawnTrailFailOpen (SR-15): with the trail unwritable, spawn's results,
// errors and rows equal those of a run with a working trail.
func TestSpawnTrailFailOpen(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	prefix := "failopen-" + uuid.NewString()[:8]
	want := append(failOpenReuseRuns(t, true), failOpenPlainRuns(t, prefix, true)...)

	cmd := exec.Command(os.Args[0], "-test.run=^TestSpawnTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), sftChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestSpawnTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, failOpenLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestSpawnTrailFailOpenChild is TestSpawnTrailFailOpen's child: it runs the
// spawns with an unwritable trail and prints their lines.
func TestSpawnTrailFailOpenChild(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	prefix := os.Getenv(sftChildEnv)
	if prefix == "" {
		t.Skip("run only as TestSpawnTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.failopen_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range append(failOpenReuseRuns(t, false), failOpenPlainRuns(t, prefix, false)...) {
		fmt.Println(failOpenLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
