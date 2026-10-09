package store_test

// Failed store writes (SR-5.8, SR-20.3): on a closed store, or through
// writefailfix's trigger for the write's kind (storefix.InjectWriteFailure),
// each write returns an error and no outcome and leaves the row as it was.
// Reuse's failures are in reuse_refused_test.go and reuse_restore_test.go.

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestWritesFailClosed checks each write's store-failure path: an error, a
// zero result (no CondResult, prior state, snapshot or version) and the row
// unchanged.
func TestWritesFailClosed(t *testing.T) {
	type result = []any
	cases := []struct {
		name  string
		state string
		kind  storefix.WriteFailureKind // 0: the store is closed instead
		write func(s *store.Store, id string, sp store.Spawn) (result, error)
	}{
		{"SpawnState on a closed store", store.StateWaiting, 0, func(s *store.Store, id string, _ store.Spawn) (result, error) {
			state, found, err := s.SpawnState(id)
			return result{state, found}, err
		}},
		{"AdoptIdentityIfUnchanged on a closed store", store.StateWaiting, 0, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, err := s.AdoptIdentityIfUnchanged(id, sp.Snapshot, createdIdentity())
			return result{res}, err
		}},
		{"RecordLaunchIdentity on a closed store", store.StatePending, 0, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, err := s.RecordLaunchIdentity(id, sp.RowVersion, goodToken, createdIdentity())
			return result{res}, err
		}},
		{"MoveToPending on a closed store", store.StateEnded, 0, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, v, err := s.MoveToPending(id, sp.Snapshot, launchStart, goodToken, "/tmp/ad-fail-sock", "", store.LaunchOwner{})
			return result{res, v}, err
		}},
		{"RestoreAfterFailedResume on a closed store", store.StatePending, 0, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, err := s.RestoreAfterFailedResume(id, sp.RowVersion, store.ResumePrior{State: store.StateEnded})
			return result{res}, err
		}},
		{"NoteUnreportedIfSameLife on a closed store", store.StatePending, 0, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, err := s.NoteUnreportedIfSameLife(id, sp.Snapshot)
			return result{res}, err
		}},
		{"ReleaseLaunchOwner on a closed store", store.StatePending, 0, func(s *store.Store, id string, _ store.Spawn) (result, error) {
			res, err := s.ReleaseLaunchOwner(id, goodToken)
			return result{res}, err
		}},
		{"MarkMissingIfSameLife", store.StatePending, storefix.WriteFailReuseRestore, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			prior, res, err := s.MarkMissingIfSameLife(id, sp.Snapshot)
			return result{prior, res}, err
		}},
		{"AdoptIdentityIfSameLife", store.StatePending, storefix.WriteFailLaunchIdentity, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, now, err := s.AdoptIdentityIfSameLife(id, sp.Snapshot, createdIdentity())
			return result{res, now}, err
		}},
		{"EndHeldLaunch", store.StatePending, storefix.WriteFailReuseRestore, func(s *store.Store, id string, sp store.Spawn) (result, error) {
			res, err := s.EndHeldLaunch(id, launchStart, time.Now())
			return result{res}, err
		}},
		{"the agent's SessionEnd hook", store.StatePending, storefix.WriteFailReuseRestore, func(s *store.Store, id string, _ store.Spawn) (result, error) {
			pid, start, err := storefix.AgentHookParent(s, id)
			if err != nil {
				return nil, err
			}
			applied, err := storefix.FireHook(s, id, "SessionEnd", "", pid, start)
			return result{applied}, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			id := f.seed(tc.state, "", apitest.WithLaunchIdentity(fullIdentity()), apitest.WithLaunchStartedAt(launchStart),
				apitest.WithRowVersion(0))
			sp := rvExamine(t, f, id)
			if tc.kind == 0 {
				if err := f.s.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			} else {
				storefix.InjectWriteFailure(t, f.path, tc.kind, id)
			}
			before := f.rawColumns(id)
			got, err := tc.write(f.s, id, sp)
			if err == nil {
				t.Fatalf("%s succeeded (%v); want a store error", tc.name, got)
			}
			for i, v := range got {
				if !reflect.ValueOf(v).IsZero() {
					t.Errorf("result %d = %#v with the error; want the zero value", i, v)
				}
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
				t.Errorf("row changed:\n before %+v\n after  %+v", before, after)
			}
		})
	}
}

// TestMarkMissingCloseFailureMarksNothing pins b.146 rule 12: when the close
// of the row's open permission requests fails inside the mark's transaction,
// the mark returns the error with no outcome, the row keeps its state and
// version, every request stays as it was and nothing is emitted.
func TestMarkMissingCloseFailureMarksNothing(t *testing.T) {
	f := newV5Store(t)
	id := f.seed(store.StateCheckPermission, "", apitest.WithLaunchIdentity(fullIdentity()))
	decided := seedRequest(t, f, id)
	seedRequest(t, f, id)
	seedRequest(t, f, id)
	ok, err := f.s.DecidePermissionRequest(id, decided, "allow", "", store.WriterProcessDecide)
	wantBool(t, "DecidePermissionRequest", ok, err, true)
	storefix.InjectWriteFailure(t, f.path, storefix.WriteFailPermissionDecision, id)
	sp, before, requests := rvExamine(t, f, id), f.rawColumns(id), f.reuseRequests(t, id)
	mark := store.TrailMark(t)

	prior, res, err := f.s.MarkMissingIfSameLife(id, sp.Snapshot)

	if want := "injected write failure: " + storefix.WriteFailPermissionDecision.String(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("MarkMissingIfSameLife err = %v; want it to contain %q", err, want)
	}
	if prior != "" || res != 0 {
		t.Errorf("MarkMissingIfSameLife = %q, %v with the error; want \"\", 0", prior, res)
	}
	if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
		t.Errorf("row changed:\n before %+v\n after  %+v", before, after)
	}
	if after := f.reuseRequests(t, id); !reflect.DeepEqual(after, requests) {
		t.Errorf("requests changed:\n before %+v\n after  %+v", requests, after)
	}
	for _, ev := range []string{"ad.row_mutation.committed", "ad.find_missing.tick"} {
		if lines := store.TrailEventsSince(t, mark, ev, id); len(lines) != 0 {
			t.Errorf("%s lines = %v; want none", ev, lines)
		}
	}
}
