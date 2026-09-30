package api_test

// find_missing_grace_trail_test.go — SR-11.2 / SR-22.8: the trail side of the
// pending grace period. A pending row inside grace yields no
// ad.find_missing.tick and keeps its permission requests open; past grace it
// gets today's ticks with prior_state "pending". Trail helpers, trailRow and
// trailClock live in find_missing_trail_test.go; fmGrace in find_missing_test.go.

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// seedGracePendingRow seeds a pending row launched at launchAt, with one open
// permission request, and returns the open store.
func seedGracePendingRow(t *testing.T, id string, launchAt time.Time, opts ...apitest.SpawnOption) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	opts = append(opts, apitest.WithLaunchStartedAt(launchAt.UnixMilli()))
	if _, err := apitest.SeedSpawn(dbPath, id, store.StatePending, "/tmp", "off", "", true, opts...); err != nil {
		t.Fatalf("SeedSpawn %q: %v", id, err)
	}
	if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
		t.Fatalf("SeedPermissionRequest %q: %v", id, err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestFindMissingPendingGraceTrail: inside grace a pending row gets no tick of
// any reason and its permission request stays open; past grace the usual ticks appear.
func TestFindMissingPendingGraceTrail(t *testing.T) {
	launchAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	kinds := []struct {
		name     string
		identity trailRow        // the row's recorded identities (id unset)
		proc     procfix.Process // the answer for the row's recorded pid
		wantPast []string        // tick reasons past grace, in emit order
	}{
		{"sessionstart gone", trailRow{ssPID: 4242}, procfix.Gone(), []string{"proc_absent", "permission_orphan_closeout"}},
		{"pane gone", trailRow{panePID: 4243}, procfix.Gone(), []string{"proc_absent", "permission_orphan_closeout"}},
		{"sessionstart unreadable", trailRow{ssPID: 4244}, procfix.Unreadable(), []string{"probe_eacces"}},
	}
	ages := []struct {
		name   string
		age    time.Duration
		inside bool
	}{
		{"inside one second before grace ends", fmGrace - time.Second, true},
		{"past at exactly the grace period", fmGrace, false},
		{"past one second after grace ends", fmGrace + time.Second, false},
	}

	for ki, kind := range kinds {
		for ai, age := range ages {
			t.Run(kind.name+"/"+age.name, func(t *testing.T) {
				id := "grace-trail-" + string(rune('a'+ki)) + string(rune('0'+ai))
				st := seedGracePendingRow(t, id, launchAt, kind.identity.options()...)
				pc := procfix.New()
				pc.Set(kind.identity.pid(), kind.proc)
				before := len(readAPITrailLines(t))

				res, err := runFindMissing(st, pc, fmSweep{now: trailClock(launchAt.Add(age.age))})
				if err != nil {
					t.Fatalf("FindMissing: %v", err)
				}

				var reasons []string
				var procAbsent map[string]any
				for _, tick := range apiFindMissingTicksAt(t, before) {
					if tick["claude_instance_id"] != id {
						continue
					}
					reason, _ := tick["reconciliation_reason"].(string)
					reasons = append(reasons, reason)
					if reason == "proc_absent" {
						procAbsent = tick
					}
				}
				open, err := st.OpenPermissionRequestsForSpawn(id)
				if err != nil {
					t.Fatalf("OpenPermissionRequestsForSpawn: %v", err)
				}
				state, err := st.GetSpawnState(id)
				if err != nil {
					t.Fatalf("GetSpawnState: %v", err)
				}
				marked := slices.Contains(kind.wantPast, "proc_absent")

				if age.inside {
					if len(reasons) != 0 {
						t.Errorf("ticks = %v; want none inside grace", reasons)
					}
					if calls := pc.StartTimeCalls(); len(calls) != 0 {
						t.Errorf("StartTime calls = %v; want none for an inside-grace row", calls)
					}
					if len(res.IDs) != 0 || len(res.UnverifiedIDs) != 0 {
						t.Errorf("ids = %v, unverified_ids = %v; want both empty", res.IDs, res.UnverifiedIDs)
					}
					if state != store.StatePending || len(open) != 1 {
						t.Errorf("state = %q, open requests = %d; want pending, 1", state, len(open))
					}
					return
				}

				if !slices.Equal(reasons, kind.wantPast) {
					t.Errorf("ticks = %v; want %v", reasons, kind.wantPast)
				}
				if marked {
					assertAPITrailStr(t, procAbsent, "prior_state", store.StatePending)
					assertAPITrailStr(t, procAbsent, "new_state", store.StateMissing)
					if state != store.StateMissing || len(open) != 0 || !slices.Equal(res.IDs, []string{id}) {
						t.Errorf("state = %q, open requests = %d, ids = %v; want missing, 0, [%s]", state, len(open), res.IDs, id)
					}
				} else if state != store.StatePending || len(open) != 1 || !slices.Equal(res.UnverifiedIDs, []string{id}) {
					t.Errorf("state = %q, open requests = %d, unverified_ids = %v; want pending, 1, [%s]", state, len(open), res.UnverifiedIDs, id)
				}
			})
		}
	}
}
