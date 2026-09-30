package storefix_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// proxyWrite is one of today's store writes a kind matches: how to seed its
// row, run it, and observe what it changes.
type proxyWrite struct {
	name     string
	kind     storefix.WriteFailureKind
	failOpen bool // the store method swallows the failure; only the effect shows it
	seed     func(t *testing.T, s *store.Store, dbPath, id string)
	write    func(s *store.Store, id string) error
	observe  func(t *testing.T, s *store.Store, dbPath, id string) any
}

// seedState seeds id in state through apitest.SeedSpawn.
func seedState(state, sessionID string) func(*testing.T, *store.Store, string, string) {
	return func(t *testing.T, _ *store.Store, dbPath, id string) {
		t.Helper()
		if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", sessionID, false); err != nil {
			t.Fatalf("SeedSpawn(%q, %q): %v", id, state, err)
		}
	}
}

// seedLaunchPending seeds id as a fresh launch's pending row: a token and the
// test socket, with the six server and pane identity columns NULL.
func seedLaunchPending(t *testing.T, _ *store.Store, dbPath, id string) {
	t.Helper()
	launch := store.LaunchIdentity{Token: "0123456789abcdef", Socket: apitest.TestSocket}
	if _, err := apitest.SeedSpawn(dbPath, id, store.StatePending, "", "", "", false, apitest.WithLaunchIdentity(launch)); err != nil {
		t.Fatalf("SeedSpawn(%q, pending): %v", id, err)
	}
	c := observeColumns(t, nil, dbPath, id).(apitest.SpawnColumns)
	for name, v := range identityColumns(c) {
		if v != nil {
			t.Fatalf("seeded %q: %s = %v, want NULL", id, name, v)
		}
	}
}

// heldLaunchStartMs is the launch start seedHeldLaunch gives the row, which
// endHeldLaunch passes as the insert's (SR-5.3).
const heldLaunchStartMs int64 = 1700000000000

// seedHeldLaunch seeds id as a plain spawn's row after "duplicate session":
// pending at row_version 0 with a token, the test socket, no pane and launch
// start heldLaunchStartMs (SR-9.4).
func seedHeldLaunch(t *testing.T, _ *store.Store, dbPath, id string) {
	t.Helper()
	launch := store.LaunchIdentity{Token: "0123456789abcdef", Socket: apitest.TestSocket}
	if _, err := apitest.SeedSpawn(dbPath, id, store.StatePending, "", "", "", false,
		apitest.WithLaunchIdentity(launch), apitest.WithLaunchStartedAt(heldLaunchStartMs)); err != nil {
		t.Fatalf("SeedSpawn(%q, pending): %v", id, err)
	}
}

// endHeldLaunch runs the plain spawn's end write (store.EndHeldLaunch)
// against the seeded launch start; anything but CondApplied is an error.
func endHeldLaunch(s *store.Store, id string) error {
	res, err := s.EndHeldLaunch(id, heldLaunchStartMs, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("EndHeldLaunch(%q) = %v, want CondApplied", id, res)
	}
	return err
}

// recordLaunchIdentity runs the identity write with the row's current version
// and token, as the launch that produced them would; anything but CondApplied
// is an error.
func recordLaunchIdentity(s *store.Store, id string) error {
	sp, err := s.GetSpawn(id)
	if err != nil {
		return err
	}
	res, err := s.RecordLaunchIdentity(id, sp.RowVersion, sp.Identity.Token, store.LaunchIdentity{
		ServerPID: 4101, ServerStart: 1700000000, ServerStarttime: "5101",
		PaneID: "%41", PanePID: 4102, PaneStarttime: "5102",
	})
	if err == nil && res != store.CondApplied {
		err = fmt.Errorf("RecordLaunchIdentity(%q) = %v, want CondApplied", id, res)
	}
	return err
}

// agentHook fires event at id's row as its own agent (storefix.FireHook from
// the row's pane process); a hook the gate does not apply is an error.
func agentHook(s *store.Store, id, event, sessionID string) error {
	pid, start, err := storefix.AgentHookParent(s, id)
	if err != nil {
		return err
	}
	applied, err := storefix.FireHook(s, id, event, sessionID, pid, start)
	if err == nil && !applied.Applied {
		err = fmt.Errorf("%s on %q not applied (reason %q)", event, id, applied.Reason)
	}
	return err
}

// observeColumns returns every stored column of the spawns row, raw.
func observeColumns(t *testing.T, _ *store.Store, dbPath, id string) any {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%q): %v", id, err)
	}
	return c
}

// identityColumns names the six server and pane identity columns of c.
func identityColumns(c apitest.SpawnColumns) map[string]any {
	return map[string]any{
		"tmux_server_pid": c.TmuxServerPID, "tmux_server_started": c.TmuxServerStarted,
		"tmux_server_starttime": c.TmuxServerStarttime, "pane_id": c.PaneID,
		"pane_pid": c.PanePID, "pane_starttime": c.PaneStarttime,
	}
}

// observeSpawn returns the whole spawns row.
func observeSpawn(t *testing.T, s *store.Store, _, id string) any {
	t.Helper()
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%q): %v", id, err)
	}
	return sp
}

var proxyWrites = []proxyWrite{
	{
		name:     "SessionStart rotation archive",
		kind:     storefix.WriteFailReuseArchive,
		failOpen: true,
		seed:     seedState(store.StateWorking, "sess-old"),
		write: func(s *store.Store, id string) error {
			sp, err := s.GetSpawn(id) // rotate to a new id every call: a blocked call still records its id
			if err != nil {
				return err
			}
			return agentHook(s, id, "SessionStart", sp.ClaudeSessionID+"-next")
		},
		observe: func(t *testing.T, _ *store.Store, dbPath, id string) any {
			t.Helper()
			h, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives(%q): %v", id, err)
			}
			return h
		},
	},
	{
		// Resume's move to pending: a finished row takes no hook (it records
		// no pane, SR-22.9), so no hook write moves it to pending.
		name: "resume move to pending",
		kind: storefix.WriteFailReuseReset,
		seed: seedState(store.StateEnded, ""),
		write: func(s *store.Store, id string) error {
			sp, err := s.GetSpawn(id)
			if err != nil {
				return err
			}
			res, _, err := s.MoveToPending(id, sp.Snapshot, 1700000000000, "0123456789abcdef", apitest.TestSocket, "")
			if err == nil && res != store.CondApplied {
				err = fmt.Errorf("MoveToPending(%q) = %v, want CondApplied", id, res)
			}
			return err
		},
		observe: observeSpawn,
	},
	{
		name: "spawn delete cascade",
		kind: storefix.WriteFailReusePermissionDelete,
		seed: func(t *testing.T, s *store.Store, _, id string) { storefix.SeedCheckPermission(t, s, id) },
		write: func(s *store.Store, id string) error {
			return s.DeleteSpawn(id) // cascades to the id's permission request
		},
		observe: func(t *testing.T, s *store.Store, _, id string) any {
			t.Helper()
			rows, err := s.OpenPermissionRequestsForSpawn(id)
			if err != nil {
				t.Fatalf("OpenPermissionRequestsForSpawn(%q): %v", id, err)
			}
			return rows
		},
	},
	{
		name: "SessionEnd ended transition",
		kind: storefix.WriteFailReuseRestore,
		seed: seedState(store.StatePending, ""),
		write: func(s *store.Store, id string) error {
			return agentHook(s, id, "SessionEnd", "")
		},
		observe: observeSpawn,
	},
	{
		// The raw row: a blocked end write leaves it pending at version 0 with
		// its launch start and no ended_at (SR-5.8).
		name:    "held launch end write",
		kind:    storefix.WriteFailReuseRestore,
		seed:    seedHeldLaunch,
		write:   endHeldLaunch,
		observe: observeColumns,
	},
	{
		// The raw row: a blocked write leaves the identity columns NULL and
		// row_version where it was.
		name:    "launch identity write",
		kind:    storefix.WriteFailLaunchIdentity,
		seed:    seedLaunchPending,
		write:   recordLaunchIdentity,
		observe: observeColumns,
	},
}

// runWrite runs w on id and asserts it was blocked by the injected blockedBy
// failure, or took effect when blockedBy is 0 (not a kind).
func runWrite(t *testing.T, s *store.Store, dbPath string, w proxyWrite, id string, blockedBy storefix.WriteFailureKind) {
	t.Helper()
	before := w.observe(t, s, dbPath, id)
	err := w.write(s, id)
	changed := !reflect.DeepEqual(before, w.observe(t, s, dbPath, id))
	if blockedBy == 0 {
		if err != nil || !changed {
			t.Errorf("%s on %q: err=%v changed=%v, want success", w.name, id, err, changed)
		}
		return
	}
	if changed {
		t.Errorf("%s on %q changed its row despite the injected failure", w.name, id)
	}
	if w.failOpen {
		if err != nil {
			t.Errorf("%s on %q: err=%v, want nil (fail-open)", w.name, id, err)
		}
		return
	}
	if want := "injected write failure: " + blockedBy.String(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s on %q: err=%v, want it to contain %q", w.name, id, err, want)
	}
}

// TestInjectWriteFailure installs each kind on one id and runs every proxy
// write: only the kind's own writes on that id fail, and cleanup lifts them.
func TestInjectWriteFailure(t *testing.T) {
	const target, other = "wf-target", "wf-other"
	covered := map[storefix.WriteFailureKind]bool{}
	for _, w := range proxyWrites {
		covered[w.kind] = true
	}
	for _, k := range writefailfix.Kinds() {
		if !covered[k] {
			t.Fatalf("no proxy write for kind %v", k)
		}
	}
	for _, installed := range writefailfix.Kinds() {
		for _, w := range proxyWrites {
			t.Run(fmt.Sprintf("%v/%s", installed, w.name), func(t *testing.T) {
				s, dbPath := storefix.OpenTempStore(t)
				w.seed(t, s, dbPath, target)
				w.seed(t, s, dbPath, other)
				matching := installed == w.kind

				t.Run("installed", func(t *testing.T) {
					storefix.InjectWriteFailure(t, dbPath, installed, target)
					var blockedBy storefix.WriteFailureKind
					if matching {
						blockedBy = installed
					}
					runWrite(t, s, dbPath, w, target, blockedBy)
					runWrite(t, s, dbPath, w, other, 0)
				})

				if matching {
					runWrite(t, s, dbPath, w, target, 0) // the installing subtest's cleanup removed the failure
				}
			})
		}
	}
}
