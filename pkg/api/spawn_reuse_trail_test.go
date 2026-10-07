package api_test

// spawn_reuse_trail_test.go covers reuse's own trail events (SR-10.6, SR-14;
// AC-REUSE-12): ad.spawn.reused, exactly one per applied change, written once
// the create returns; ad.spawn.reuse_restored, exactly one per restore
// attempt; none of either on the paths that make no change or no restore;
// and fail-open. ad.launch.name_held (launch reuse) is in
// spawn_reuse_trail_held_test.go. It runs on the reuse fixture
// (spawn_reuse_fixture_test.go).

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
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

// rutAssertFields fails unless rec holds exactly keys, each of want's with its
// value (nil: present and null).
func rutAssertFields(t *testing.T, rec map[string]any, keys []string, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%v[%q] = %v (present %t); want %v", rec["event"], k, got, ok, v)
		}
	}
	got := make([]string, 0, len(rec))
	for k := range rec {
		got = append(got, k)
	}
	slices.Sort(got)
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Errorf("%v keys = %q; want %q", rec["event"], got, sorted)
	}
}

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
// since mark, applied as given, launch_error launchErr and restore_error the
// WARN line's text in logs (null when none).
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
	rutAssertFields(t, recs[0], rutRestoredKeys, map[string]any{"claude_instance_id": id, "applied": applied,
		"launch_error": launchErr, "restore_error": restoreErr, "source": "ad_spawn"})
}

// TestSpawnReuseTrailReused: an applied reuse writes exactly one ad.spawn.reused,
// every field, once the create on the reset row returns.
func TestSpawnReuseTrailReused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		a     agentState
		spec  reuseRowSpec
		setup rpvSetup
	}{
		{name: "ended with a session id", a: agentGone},
		{name: "missing with a session id", a: agentGone, spec: reuseRowSpec{State: store.StateMissing}},
		{name: "no session id gives a null archived_session_id", a: agentGone, spec: reuseRowSpec{NoSessionID: true}},
		{name: "missing with neither pid nor session id", a: agentNotRecorded,
			spec: reuseRowSpec{State: store.StateMissing, NoSessionID: true}},
		{name: "server restarted, lookup still gone", a: agentGone, setup: rpvRestart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, tc.a, tc.spec)
			if tc.setup != nil {
				tc.setup(t, e, &r.resumeRow)
			}
			mark := trailMark(t)
			atCreate, stateAtCreate := -1, any(nil)
			e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
				if c.InstanceID == r.ID && atCreate < 0 {
					atCreate, stateAtCreate = len(ptRecords(t, mark, rutReused, r.ID)), e.columns(t, r.ID).State
				}
			})

			res, logs, err := e.reuseWith(t, &hookedReuseStore{st: e.st}, reuseParams(t, r, reuseRequest{Name: rutRequested}))

			if err != nil || res.ClaudeInstanceID != r.ID {
				t.Fatalf("reuse = %+v, %v (log %q); want %s", res, err, logs, r.ID)
			}
			if atCreate != 0 || stateAtCreate != store.StatePending {
				t.Errorf("at the create: %s records = %d, state %v; want none on the reset (pending) row",
					rutReused, atCreate, stateAtCreate)
			}
			recs := ptRecords(t, mark, rutReused, r.ID)
			if len(recs) != 1 {
				t.Fatalf("%s records = %d; want 1: %v", rutReused, len(recs), recs)
			}
			state, archived := cmp.Or(tc.spec.State, store.StateEnded), any(nil)
			if !tc.spec.NoSessionID {
				archived = r.Spawn.ClaudeSessionID
			}
			rutAssertFields(t, recs[0], rutReusedKeys, map[string]any{"claude_instance_id": r.ID, "prior_state": state,
				"archived_session_id": archived, "lookup_outcome": "gone", "source": "ad_spawn"})
			rutAssertOrder(t, mark, r.ID, rutReused)
		})
	}
}

// TestSpawnReuseTrailRestored: each failed launch but a timeout writes one
// ad.spawn.reuse_restored after ad.spawn.reused, its fields following the restore.
func TestSpawnReuseTrailRestored(t *testing.T) {
	t.Parallel()
	create := func(s tmuxfix.Script) func(*testing.T, *killEnv, reuseRow, *hookedReuseStore) {
		return func(_ *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
			e.rec.Script(r.Socket, s, tmux.CallCreate)
		}
	}
	unavailable := create(tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1})
	cases := []struct {
		name    string
		arrange []func(*testing.T, *killEnv, reuseRow, *hookedReuseStore)
		held    bool // "duplicate session" with the holder vanished
		errName string
		applied bool
	}{
		{name: "tmux unavailable at the create", arrange: arr(unavailable), errName: "ErrTmuxNotAvailable", applied: true},
		{name: "other create failure", errName: "ErrTmuxSessionCreate", applied: true,
			arrange: arr(create(tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}))},
		{name: "unlabelled session", errName: "ErrTmuxSessionCreate", applied: true,
			arrange: arr(func(_ *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
					Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
			})},
		{name: "duplicate session, holder vanished", held: true, errName: "ErrTmuxSessionCreate", applied: true},
		{name: "row changed after the reset", errName: "ErrTmuxNotAvailable",
			arrange: arr(unavailable, func(t *testing.T, e *killEnv, r reuseRow, w *hookedReuseStore) {
				parent := e.seedRelative(t, r.ID, false)
				w.afterReset(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			})},
		{name: "row removed at the create", errName: "ErrTmuxNotAvailable",
			arrange: arr(unavailable, func(t *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
					if err := e.st.DeleteSpawn(r.ID); err != nil {
						t.Errorf("DeleteSpawn: %v", err)
					}
				})
			})},
		{name: "restore store error", errName: "ErrTmuxNotAvailable",
			arrange: arr(unavailable, func(t *testing.T, e *killEnv, r reuseRow, _ *hookedReuseStore) {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
			})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			w := &hookedReuseStore{st: e.st}
			for _, a := range tc.arrange {
				a(t, e, r, w)
			}
			if tc.held {
				e.arrangeHeld(t, rutRenamed(r), heldSpec{Holder: holderVanished})
			}
			mark := trailMark(t)

			_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))

			if got := ptErrName(err); got != tc.errName {
				t.Fatalf("reuse err = %v (%s); want %s", err, got, tc.errName)
			}
			rutAssertRestored(t, mark, r.ID, tc.applied, tc.errName, logs)
			if strings.HasSuffix(tc.name, "store error") && rutRestoreError(logs, r.ID) == "" {
				t.Errorf("no restore WARN line in %q; want restore_error to carry its store error", logs)
			}
			want := []string{rutReused, rutRestored}
			if tc.held {
				want = append(want, rutNameHeld)
			}
			rutAssertOrder(t, mark, r.ID, want...)
		})
	}
}

// arr lists arrangements.
func arr(fns ...func(*testing.T, *killEnv, reuseRow, *hookedReuseStore)) []func(*testing.T, *killEnv, reuseRow, *hookedReuseStore) {
	return fns
}

// rutRenamed is r's resumeRow recorded under rutRequested, so arrangeHeld
// places its holders under the requested name.
func rutRenamed(r reuseRow) resumeRow {
	rr := r.resumeRow
	rr.killRow = r.withName(rutRequested)
	return rr
}

// TestSpawnReuseTrailOtherPaths: refusals, an unapplied reset and store failures
// write no reuse event; a success or launch timeout writes reused alone.
func TestSpawnReuseTrailOtherPaths(t *testing.T) {
	t.Parallel()
	inject := func(k storefix.WriteFailureKind) func(*testing.T, *killEnv, *reuseRow, *hookedReuseStore) {
		return func(t *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
			storefix.InjectWriteFailure(t, e.dbPath, k, r.ID)
		}
	}
	cases := []struct {
		name    string
		arrange func(*testing.T, *killEnv, *reuseRow, *hookedReuseStore)
		errName string // "" = success
		reused  int
	}{
		{name: "success", reused: 1},
		{name: "launch timeout", errName: "ErrTmuxUnresponsive", reused: 1,
			arrange: func(_ *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
			}},
		{name: "refused: left over from an earlier life", errName: "ErrTmuxSessionConflict",
			arrange: func(t *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
				e.seedHolder(t, r.killRow, holderOld)
			}},
		{name: "refused: requested name held by another row", errName: "ErrTmuxSessionConflict",
			arrange: func(t *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
				e.seedHolder(t, r.withName(rutRequested), holderForeign)
			}},
		{name: "refused: tmux unavailable at the lookup", errName: "ErrTmuxNotAvailable",
			arrange: func(_ *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}, tmux.CallLookup)
			}},
		{name: "refused: live pending row", errName: "ErrInstanceIdCollision",
			arrange: func(t *testing.T, e *killEnv, r *reuseRow, _ *hookedReuseStore) {
				*r = e.reusePending(t, agentAlive, reuseRowSpec{}, reuseRequest{})
			}},
		{name: "reset not applied: row changed before it", errName: "ErrInstanceIdCollision",
			arrange: func(t *testing.T, e *killEnv, r *reuseRow, w *hookedReuseStore) {
				parent := e.seedRelative(t, r.ID, false)
				w.beforeReset(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}},
		{name: "store failure: archive", errName: "ErrInternal", arrange: inject(storefix.WriteFailReuseArchive)},
		{name: "store failure: reset", errName: "ErrInternal", arrange: inject(storefix.WriteFailReuseReset)},
		{name: "store failure: permission-request deletion", errName: "ErrInternal",
			arrange: inject(storefix.WriteFailReusePermissionDelete)},
		{name: "store failure: read", errName: "ErrInternal",
			arrange: func(_ *testing.T, _ *killEnv, _ *reuseRow, w *hookedReuseStore) { w.failRead(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			w := &hookedReuseStore{st: e.st}
			if tc.arrange != nil {
				tc.arrange(t, e, &r, w)
			}
			mark := trailMark(t)

			_, logs, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{Name: rutRequested}))

			if got := ptErrName(err); (err == nil) != (tc.errName == "") || (err != nil && got != tc.errName) {
				t.Fatalf("reuse err = %v (%s; log %q); want %q", err, got, logs, tc.errName)
			}
			for ev, want := range map[string]int{rutReused: tc.reused, rutRestored: 0, rutNameHeld: 0} {
				if got := ptRecords(t, mark, ev, r.ID); len(got) != want {
					t.Errorf("%s records = %d; want %d: %v", ev, len(got), want, got)
				}
			}
		})
	}
}

// rutChildEnv gates TestSpawnReuseTrailFailOpenChild and carries a marker.
const rutChildEnv = "AD_SPAWN_REUSE_TRAIL_FAIL_CHILD"

// rutLinePrefix marks the child's result lines in its output.
const rutLinePrefix = "RUT|"

// rutFailOpenRun is one fail-open reuse: its arrangement (held: "duplicate
// session" with an old-label holder) and the event a working trail gets.
type rutFailOpenRun struct {
	name    string
	arrange func(*testing.T, *killEnv, reuseRow)
	held    bool
	event   string
}

// rutFailOpenRuns reuses one row per run and returns one line each, with the
// result, error and row columns, every per-test value replaced; with a
// working trail it also checks each run wrote its event.
func rutFailOpenRuns(t *testing.T) []string {
	t.Helper()
	runs := []rutFailOpenRun{
		{name: "success", event: rutReused},
		{name: "server-restarted", event: "ad.provenance.disagree",
			arrange: func(t *testing.T, e *killEnv, r reuseRow) { rpvRestart(t, e, &r.resumeRow) }},
		{name: "unavailable", event: rutRestored, arrange: func(_ *testing.T, e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}, tmux.CallCreate)
		}},
		{name: "restore-store-error", event: rutRestored, arrange: func(t *testing.T, e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}, tmux.CallCreate)
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, r.ID)
		}},
		{name: "duplicate-old", event: rutNameHeld, held: true},
		{name: "timeout", event: rutReused, arrange: func(_ *testing.T, e *killEnv, r reuseRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallCreate)
		}},
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
		l := fmt.Sprintf("%s id=%s pre_trust=%s err=%v warn=%t state=%v ended_at=%v row_version=%v life=%v launch_started_at=%v parent=%v socket=%v",
			run.name, res.ClaudeInstanceID, res.PreTrust, err, rutRestoreError(logs, r.ID) != "", c.State, c.EndedAt, c.RowVersion,
			c.LifeNumber, c.LaunchStartedAt, c.ParentID, c.TmuxSocket)
		lines = append(lines, strings.NewReplacer(r.ID, "<id>", r.Socket, "<socket>", r.CWD, "<cwd>",
			r.Trust.dir, "<trust>", r.ParentID, "<parent>").Replace(l))
		if os.Getenv(rutChildEnv) == "" && len(ptRecords(t, 0, run.event, r.ID)) == 0 {
			t.Errorf("working trail: %s wrote no %s", run.name, run.event)
		}
	}
	return lines
}

// TestSpawnReuseTrailFailOpen: with the trail unwritable, reuse's results,
// errors and rows equal those of a run with a working trail.
func TestSpawnReuseTrailFailOpen(t *testing.T) {
	t.Parallel()
	want := rutFailOpenRuns(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestSpawnReuseTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), rutChildEnv+"=child-"+uuid.NewString()[:8])
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestSpawnReuseTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, rutLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestSpawnReuseTrailFailOpenChild is TestSpawnReuseTrailFailOpen's child: it
// runs the reuses with a 0500 .agent-director and prints their lines.
func TestSpawnReuseTrailFailOpenChild(t *testing.T) {
	t.Parallel()
	if os.Getenv(rutChildEnv) == "" {
		t.Skip("run only as TestSpawnReuseTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.reuse_trail_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range rutFailOpenRuns(t) {
		fmt.Println(rutLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
