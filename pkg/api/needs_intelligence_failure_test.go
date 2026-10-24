package api_test

// needs_intelligence_failure_test.go — a needs_intelligence display 0.16/0.17
// committed (b.ob8), which no read reports since b.zpv: the row reads its
// own state, and the first read or lifecycle write that commits ends the
// display at once, with its exit record, whatever the settle time.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// storedNeedsIntelligenceEnv is a relayEnv on apitest.SeedNeedsIntelligenceSurface's
// needs-intelligence-claude: check_permission, its request A fallen back with
// its clock stored, holding the needs_intelligence display an earlier release
// committed for it. Its clock reads an hour after that display's entry.
func storedNeedsIntelligenceEnv(t *testing.T) *relayEnv {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedNeedsIntelligenceSurface(dbPath); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	storefix.RegisterStorePath(t, s, dbPath)
	e := &relayEnv{s: s, dbPath: dbPath, id: "needs-intelligence-claude", pc: procfix.New(), now: displayAt.Add(time.Hour)}
	if row, err := s.GetSpawn(e.id); err != nil || row.CommittedDisplay().State != api.StateNeedsIntelligence {
		t.Fatalf("stored display=%+v %v", row, err)
	}
	return e
}

// TestStoredNeedsIntelligenceEndsAtOnce (b.zpv): a read that cannot commit
// (held lock, failed write) reads check_permission on status, get and list
// with no Displayed State, keeps the stored display and records nothing; the
// next committing read ends it at once, with no settle time or the largest,
// with one exit to check_permission at that read; later reads write nothing.
func TestStoredNeedsIntelligenceEndsAtOnce(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, settle := range []int64{0, settleMax} {
		for _, failure := range []string{"held lock", "failed write"} {
			t.Run(failure+"/"+time.Duration(settle*int64(time.Millisecond)).String(), func(t *testing.T) {
				e := storedNeedsIntelligenceEnv(t)
				c := needsIntelligenceClient(t, e, settle, 0)
				mark := trailMark(t)
				release := func() {}
				if failure == "held lock" {
					release = apitest.HoldWriteLock(t, e.dbPath, time.Minute)
				} else {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailDisplayObservation, e.id)
				}
				before, err := e.s.GetSpawn(e.id)
				if err != nil {
					t.Fatal(err)
				}
				assertDisplayState(t, c, e.id, store.StateCheckPermission)
				if _, row, listed := clientReported(t, c, e.id); row.StateDetail != (api.StateDetail{}) || listed.DisplayedState != "" {
					t.Errorf("get=%+v list=%+v; want no Displayed State", row.StateDetail, listed)
				}
				if after, err := e.s.GetSpawn(e.id); err != nil || after.DisplayObservation != before.DisplayObservation || after.ObservationVersion != before.ObservationVersion {
					t.Fatalf("an uncommitted read changed the row: before=%+v after=%+v %v", before, after, err)
				}
				if events := displayEvents(t, mark, e.id); len(events) != 0 {
					t.Fatalf("an uncommitted read emitted %v", events)
				}
				if failure != "held lock" {
					return
				}
				release()
				e.now = e.now.Add(time.Millisecond)
				observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
				ended := e.now
				if row, err := e.s.GetSpawn(e.id); err != nil || row.DisplayObservation != "" {
					t.Fatalf("the exit left the display: %+v %v", row, err)
				}
				version := observationVersion(t, e)
				e.now = e.now.Add(time.Hour)
				observeStatus(t, c, e.id, store.StateCheckPermission, nil)
				if got := observationVersion(t, e); got != version {
					t.Errorf("a later read wrote: observation_version %d -> %d", version, got)
				}
				events := displayEvents(t, mark, e.id)
				if len(events) != 1 {
					t.Fatalf("events=%v; want the exit once", events)
				}
				assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, store.StateCheckPermission, ended)
			})
		}
	}
}

// An authoritative write on a row holding the stored display ends it with its
// exit at the write's own commit time, before any read: a terminal write and
// the find-missing repair of a check_permission row nothing holds any more.
func TestStoredNeedsIntelligenceTerminalExitIsImmediate(t *testing.T) {
	// Serial: it reads the shared trail using fixture row ids reused by other tests.
	for _, tc := range []struct {
		name, state string
		write       func(*testing.T, *relayEnv)
	}{
		{"ended", store.StateEnded, func(t *testing.T, e *relayEnv) {
			if err := seedAgentState(e.s, e.dbPath, e.id, store.StateEnded); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing", store.StateMissing, func(t *testing.T, e *relayEnv) {
			row, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if _, res, err := e.s.MarkMissingIfSameLife(e.id, row.Snapshot); err != nil || res != store.CondApplied {
				t.Fatalf("missing=%v %v", res, err)
			}
		}},
		{"repair", store.StateWorking, func(t *testing.T, e *relayEnv) {
			// Nothing holds the row once its only request is proven gone.
			requestFacts(t, e, apitest.PermissionProof(displayAt))
			row, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if state, res, err := e.s.RepairCheckPermissionIfSameLife(e.id, row.Snapshot); err != nil || res != store.CondApplied || state != store.StateWorking {
				t.Fatalf("repair=%q %v %v", state, res, err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := storedNeedsIntelligenceEnv(t)
			c := needsIntelligenceClient(t, e, settleMax, 0)
			before, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			mark := trailMark(t)
			tc.write(t, e)
			// The write itself ended the display: no read has happened yet.
			after, err := e.s.GetSpawn(e.id)
			if err != nil {
				t.Fatal(err)
			}
			if after.DisplayObservation != "" || after.ObservationVersion <= before.ObservationVersion || after.PermissionEpoch != before.PermissionEpoch+1 {
				t.Errorf("write left the display behind: before=%+v after=%+v", before, after)
			}
			assertDisplayState(t, c, e.id, tc.state)
			assertDisplayState(t, c, e.id, tc.state)
			_, stamp, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("immediate exit attempts=%v", events)
			}
			// The exit must use the authoritative commit time.
			assertDisplayEvent(t, events[0], api.StateNeedsIntelligence, tc.state, time.UnixMilli(stamp))
		})
	}
}

// TestStoredNeedsIntelligenceExitTrailFailOpen: an unwritable trail does not
// undo the committed exit.
func TestStoredNeedsIntelligenceExitTrailFailOpen(t *testing.T) {
	t.Parallel()
	if os.Getenv("AD_TEST_DISPLAY_TRAIL_CHILD") == "1" {
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
		e := storedNeedsIntelligenceEnv(t)
		c := needsIntelligenceClient(t, e, 0, 0)
		observeStatus(t, c, e.id, store.StateCheckPermission, apitest.VersionAdvanced)
		row, err := e.s.GetSpawn(e.id)
		if err != nil || row.DisplayObservation != "" {
			t.Fatalf("trail failure undid the exit: %+v %v", row, err)
		}
		return
	}
	// The child must actually run and pass this test: an unmatched -test.run
	// pattern exits 0 having run nothing.
	cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), "AD_TEST_DISPLAY_TRAIL_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("unwritable trail child: %v\n%s", err, out)
	}
}
