package storefix_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/writefailfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// proxyWrite is today's store write matching one kind: how to seed its row,
// run it, and observe what it changes.
type proxyWrite struct {
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
		kind:     storefix.WriteFailReuseArchive,
		failOpen: true,
		seed:     seedState(store.StateWorking, "sess-old"),
		write: func(s *store.Store, id string) error {
			sp, err := s.GetSpawn(id) // rotate to a new id every call: a blocked call still records its id
			if err != nil {
				return err
			}
			return s.RecordSessionStartIdentity(id, sp.ClaudeSessionID+"-next", "", false, 0, "")
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
		kind: storefix.WriteFailReuseReset,
		seed: seedState(store.StateEnded, ""),
		write: func(s *store.Store, id string) error {
			return s.ApplyHookTransition(id, store.StatePending, false, "test")
		},
		observe: observeSpawn,
	},
	{
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
		kind: storefix.WriteFailReuseRestore,
		seed: seedState(store.StatePending, ""),
		write: func(s *store.Store, id string) error {
			return s.ApplyHookTransition(id, store.StateEnded, false, "test")
		},
		observe: observeSpawn,
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
			t.Errorf("%v write on %q: err=%v changed=%v, want success", w.kind, id, err, changed)
		}
		return
	}
	if changed {
		t.Errorf("%v write on %q changed its row despite the injected failure", w.kind, id)
	}
	if w.failOpen {
		if err != nil {
			t.Errorf("%v write on %q: err=%v, want nil (fail-open)", w.kind, id, err)
		}
		return
	}
	if want := "injected write failure: " + blockedBy.String(); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%v write on %q: err=%v, want it to contain %q", w.kind, id, err, want)
	}
}

// TestInjectWriteFailure installs each kind on one id and runs every kind's
// proxy write: only the matching write on that id fails, and cleanup lifts it.
func TestInjectWriteFailure(t *testing.T) {
	const target, other = "wf-target", "wf-other"
	var covered []storefix.WriteFailureKind
	for _, w := range proxyWrites {
		covered = append(covered, w.kind)
	}
	if !reflect.DeepEqual(covered, writefailfix.Kinds()) {
		t.Fatalf("proxyWrites cover %v, want every kind %v", covered, writefailfix.Kinds())
	}
	for _, installed := range proxyWrites {
		for _, w := range proxyWrites {
			t.Run(fmt.Sprintf("%v/%v write", installed.kind, w.kind), func(t *testing.T) {
				s, dbPath := storefix.OpenTempStore(t)
				w.seed(t, s, dbPath, target)
				w.seed(t, s, dbPath, other)
				matching := installed.kind == w.kind

				t.Run("installed", func(t *testing.T) {
					storefix.InjectWriteFailure(t, dbPath, installed.kind, target)
					var blockedBy storefix.WriteFailureKind
					if matching {
						blockedBy = installed.kind
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
