package api_test

// kill_trail_failopen_test.go: kill's trail is fail-open (SR-6.4). A child
// run of this test binary, whose HOME's .agent-director is mode 0500 before
// the trail singleton opens, must give the same results and rows as a run
// with a working trail.

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

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// ktrChildEnv gates TestKillTrailFailOpenChild and carries the id prefix.
const ktrChildEnv = "AD_KILL_TRAIL_FAIL_CHILD"

// ktrLinePrefix marks the child's result lines in its output.
const ktrLinePrefix = "KTR|"

// ktrFailOpenRuns kills one row per return-path shape, ids prefix-<name>, and
// returns one line per kill: its result, error text and the row's columns.
func ktrFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	cases := []struct {
		name  string
		spec  killRowSpec
		setup func(*testing.T, *killEnv, *killRow)
	}{
		{name: "ours", setup: ktrDies},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true}, setup: ktrDies},
		{name: "leftover", spec: killRowSpec{NoSession: true},
			setup: func(t *testing.T, e *killEnv, r *killRow) {
				e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			}},
		{name: "no-pane", spec: killRowSpec{NoSession: true}},
		{name: "rebound", setup: ktrRebind},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		tc.spec.ID = prefix + "-" + tc.name
		r := e.seedRow(t, tc.spec)
		if tc.setup != nil {
			tc.setup(t, e, &r)
		}
		res, err := e.kill(r.ID)
		c := e.columns(t, r.ID)
		lines = append(lines, fmt.Sprintf("%s kill_sent=%t err=%v state=%v row_version=%v server=%v/%v/%v pane=%v/%v/%v",
			tc.name, res.KillSent, err, c.State, c.RowVersion, c.TmuxServerPID, c.TmuxServerStarted,
			c.TmuxServerStarttime, c.PaneID, c.PanePID, c.PaneStarttime))
	}
	return lines
}

// TestKillTrailFailOpen: with the trail unwritable, kill's results, errors and
// rows equal those of a run with a working trail.
func TestKillTrailFailOpen(t *testing.T) {
	prefix := "kill-failopen-" + uuid.NewString()[:8]
	want := ktrFailOpenRuns(t, prefix)
	for _, l := range want {
		id := prefix + "-" + strings.Fields(l)[0]
		if n := len(killCalled(t, id)); n != 1 {
			t.Fatalf("working trail: ad.kill.called records for %s = %d; want 1", id, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestKillTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), ktrChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestKillTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, ktrLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestKillTrailFailOpenChild is TestKillTrailFailOpen's child: it runs the
// kills with an unwritable trail and prints their lines.
func TestKillTrailFailOpenChild(t *testing.T) {
	prefix := os.Getenv(ktrChildEnv)
	if prefix == "" {
		t.Skip("run only as TestKillTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.kill_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range ktrFailOpenRuns(t, prefix) {
		fmt.Println(ktrLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
