package api_test

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckFailedUpdateKeepsCommittedDetails(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, failure := range []string{"held lock", "failed write"} {
		t.Run(failure, func(t *testing.T) {
			e := newRelayEnv(t, store.ProcessIdentity{})
			e.now = stuckAt
			stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
			c := stuckClient(t, e, 0, 0)
			before := assertStuckState(t, c, e.id, "stuck")
			stuckFacts(t, e, apitest.PermissionAnswer(store.PaneAnswerOutside))
			if failure == "held lock" {
				release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				defer release()
			} else {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailStuckObservation, e.id)
			}
			e.now = e.now.Add(time.Hour)
			mark := trailMark(t)
			got := assertStuckState(t, c, e.id, "stuck")
			if got.StuckInfo != before.StuckInfo {
				t.Errorf("uncommitted clear changed details: before=%+v after=%+v", before, got)
			}
			for _, ev := range readAPITrailLines(t)[mark:] {
				if ev["instance_id"] == e.id && ev["event"] == "ad.spawn.stuck_transition" {
					t.Errorf("failed update emitted %v", ev)
				}
			}
		})
	}
}

func TestPermissionStuckTrailFailOpen(t *testing.T) {
	t.Parallel()
	if os.Getenv("AD_TEST_STUCK_TRAIL_CHILD") == "1" {
		dir := adConfigDir(apiTrailDir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
		if err := trail.Emit(context.Background(), "ad.test.fail_open_probe", map[string]any{}); err == nil {
			t.Fatal("expected unwritable trail")
		}
		e := newRelayEnv(t, store.ProcessIdentity{})
		e.now = stuckAt
		stuckFacts(t, e, apitest.PermissionTimes(stuckAt.Add(-time.Hour), time.Time{}, stuckAt.Add(-time.Minute), time.Time{}, time.Time{}, ""))
		c := stuckClient(t, e, 0, 0)
		assertStuckState(t, c, e.id, "stuck")
		row, err := e.s.GetSpawn(e.id)
		if err != nil || row.CommittedStuckDisplay().State != "stuck" {
			t.Fatalf("trail failure undid commit: %+v %v", row, err)
		}
		return
	}
	// The child must actually run and pass this test: an unmatched -test.run
	// pattern exits 0 having run nothing.
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "AD_TEST_STUCK_TRAIL_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("unwritable trail child: %v\n%s", err, out)
	}
}
