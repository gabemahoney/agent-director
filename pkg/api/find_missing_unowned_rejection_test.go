package api_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unownedRecheckStore exposes only the sweep's required store contract and the
// all-status reader, with a fault restricted to candidate re-reads.
type unownedRecheckStore struct {
	api.FindMissingStore
	real       *store.Store
	fail       error
	candidates []string
	reads      [][]string
}

func (s *unownedRecheckStore) RecoveryRows(ids []string, settle store.SettleRead) ([]store.Spawn, error) {
	s.reads = append(s.reads, slices.Clone(ids))
	if s.fail != nil {
		for _, id := range ids {
			if slices.Contains(s.candidates, id) {
				return nil, s.fail
			}
		}
	}
	return s.real.RecoveryRows(ids, settle)
}

// b.9ps: a newly owned candidate, a failed membership re-read, or ambiguous
// server provenance must not publish an unowned session. Both spellings agree.
func TestFindMissingUnownedSessionsRejections(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"inserted row", "recheck error", "scope value"} {
		for _, canonical := range []bool{false, true} {
			t.Run(scenario+"/canonical="+map[bool]string{false: "false", true: "true"}[canonical], func(t *testing.T) {
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{State: store.StateEnded, NoSession: true})
				before, err := apitest.ReadSpawnColumns(e.dbPath, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				candidate, survivor := uuid.NewString(), uuid.NewString()
				created := fmNow.Unix()
				session := func(id, sessionID string) tmuxfix.SeedSession {
					return tmuxfix.SeedSession{ID: sessionID, Name: id, Created: created,
						Label: tmuxfix.Valid(tmuxfix.Token, id, e.st.StoreID())}
				}
				e.rec.SeedSessions(r.Socket, session(candidate, "$1")).SeedSessions(reportSocket(), session(survivor, "$2"))
				s := &unownedRecheckStore{FindMissingStore: e.st, real: e.st, candidates: []string{candidate, survivor}}
				lg := &recordingLogger{}
				var inserted *apitest.SpawnColumns
				mark := trailLen(t)
				noCandidateTrailAfter := mark
				switch scenario {
				case "inserted row":
					e.rec.AfterCall(tmux.CallLookup, func(call tmuxfix.SocketCall, _ error) {
						if call.Socket != r.Socket {
							return
						}
						if _, err := apitest.SeedSpawn(e.dbPath, candidate, store.StateEnded, "", "off", "", false); err != nil {
							t.Fatal(err)
						}
						cols, err := apitest.ReadSpawnColumns(e.dbPath, candidate)
						if err != nil {
							t.Fatal(err)
						}
						inserted = &cols
						// SeedSpawn itself transitions pending -> ended through
						// the test_seed hook. Prove that event is already here
						// before the lookup hook returns; reject all later trail.
						drainTrail(t)
						events := pendTrail(t, "ad.spawn.state_transition", candidate)
						if len(events) != 1 {
							t.Fatalf("seed transitions = %+v; want exactly one", events)
						}
						for key, value := range map[string]string{"prior_state": store.StatePending, "new_state": store.StateEnded, "triggering_event_name": "test_seed", "source": "ad_spawn_store"} {
							assertAPITrailStr(t, events[0], key, value)
						}
						if events[0]["soft_refresh"] != false {
							t.Errorf("seed soft_refresh = %v; want false", events[0]["soft_refresh"])
						}
						noCandidateTrailAfter = trailLen(t)
					})
				case "recheck error":
					s.fail = errors.New("candidate membership unavailable")
				case "scope value":
					e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
				}
				res := mustSweep(t, s, e.pc, fmSweep{canonical: canonical, tmux: e.rec, clock: e.clock, lg: lg})
				want := []map[string]any{unownedEntry(survivor, reportSocket(), "$2", survivor, created)}
				wantLog := []string(nil)
				if scenario == "recheck error" {
					want = []map[string]any{}
					wantLog = []string{"find-missing: RecoveryRows(unowned_sessions)(): candidate membership unavailable (continuing)"}
				}
				if got := unownedOf(t, res); !reflect.DeepEqual(got, want) {
					t.Errorf("unowned_sessions = %+v; want %+v", got, want)
				}
				if !reflect.DeepEqual(lg.lines, wantLog) {
					t.Errorf("diagnostics = %q; want %q", lg.lines, wantLog)
				}
				assertLists(t, res, nil, nil)
				if len(res.Checks) != 0 {
					t.Errorf("checks = %+v; want no live row", res.Checks)
				}
				assertReportLookupsOnly(t, e.rec, r.Socket)
				ids := []string{survivor}
				if scenario != "scope value" {
					ids = append(ids, candidate)
				}
				slices.Sort(ids)
				// The middle read is checkActiveRows' fixed selected-family
				// refresh, before reporting, and cannot include the new row.
				wantReads := [][]string{nil, {r.ID}, ids}
				if !reflect.DeepEqual(s.reads, wantReads) {
					t.Errorf("membership reads = %v; want exactly %v", s.reads, wantReads)
				}
				after, err := apitest.ReadSpawnColumns(e.dbPath, r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, before) {
					t.Errorf("finished row changed: before %+v after %+v", before, after)
				}
				if scenario == "inserted row" {
					if inserted == nil {
						t.Fatal("lookup hook did not insert candidate row")
					}
					after, err := apitest.ReadSpawnColumns(e.dbPath, candidate)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(after, *inserted) {
						t.Errorf("inserted row changed: before %+v after %+v", *inserted, after)
					}
				} else if _, err := e.st.GetSpawn(candidate); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("candidate row appeared: %v; want ErrSpawnNotFound", err)
				}
				if _, err := e.st.GetSpawn(survivor); !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("survivor row appeared: %v; want ErrSpawnNotFound", err)
				}
				drainTrail(t)
				assertNoTrailSince(t, noCandidateTrailAfter, candidate)
				assertNoTrailSince(t, mark, survivor)
			})
		}
	}
}
