package api_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Decode the public shape so the b.9ps regression compiles before the new field exists.
func unownedOf(t *testing.T, res api.FindMissingResult) []map[string]any {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	field, ok := env["unowned_sessions"]
	if !ok {
		t.Errorf("result %s has no unowned_sessions; want it always present (b.9ps)", raw)
		return nil
	}
	var out []map[string]any
	if err := json.Unmarshal(field, &out); err != nil || out == nil {
		t.Errorf("unowned_sessions = %s; want an array, [] when empty (err %v)", field, err)
	}
	return out // Preserve returned ordering: sorting here would hide a product defect.
}

func unownedEntry(id, socket, sessionID, name string, created int64) map[string]any {
	return map[string]any{"instance_id": id, "tmux_socket": socket, "tmux_session_id": sessionID,
		"tmux_session_name": name, "created_at": time.Unix(created, 0).UTC().Format(time.RFC3339)}
}

func assertUnownedLookups(t *testing.T, rec *tmuxfix.Recorder, sockets ...string) {
	t.Helper()
	var got []string
	for _, c := range rec.SocketCalls() {
		if c.Call != tmux.CallLookup {
			t.Errorf("unexpected tmux mutation or extra call: %+v", c)
		}
		got = append(got, c.Socket)
	}
	slices.Sort(got)
	if want := slices.Sorted(slices.Values(sockets)); !slices.Equal(got, want) {
		t.Errorf("lookup sockets = %v; want exactly one each on %v", got, want)
	}
}

// b.9ps: scan live/finished/default sockets, filter by store and every row status,
// return exactly the public fields in deterministic order, and leave rows/trail untouched.
func TestFindMissingUnownedSessions(t *testing.T) {
	// Serial: it sets TMUX/TMUX_TMPDIR and checks all trail records since its mark.
	def := filepath.Join(userSocketDir(useOwnTmuxTmpdir(t)), "default")
	liveSocket := filepath.Join(t.TempDir(), "live")
	finishedSocket := filepath.Join(t.TempDir(), "finished")
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seeds := []struct{ id, state, socket string }{
		{"b9ps-live", store.StateWorking, liveSocket},
		{"b9ps-ended", store.StateEnded, finishedSocket},
		{"b9ps-missing", store.StateMissing, finishedSocket},
	}
	before := map[string]apitest.SpawnColumns{}
	for i, sd := range seeds {
		_, err := apitest.SeedSpawn(dbPath, sd.id, sd.state, "/tmp", "off", "", i == 0,
			apitest.WithTmuxSocket(sd.socket), apitest.WithNoPane(),
			apitest.WithPID(notePanePID), apitest.WithProcStarttime(fmStart))
		if err != nil {
			t.Fatal(err)
		}
		before[sd.id], err = apitest.ReadSpawnColumns(dbPath, sd.id)
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	created := fmNow.Add(-time.Hour).Unix()
	own := func(id, sessionID string) tmuxfix.SeedSession {
		return tmuxfix.SeedSession{ID: sessionID, Name: "session-" + id, Created: created,
			Label: tmuxfix.Valid(tmuxfix.Token, id, s.StoreID())}
	}
	rec := tmuxfix.NewRecorder().
		SeedSessions(liveSocket, own("b9ps-live", "$1"), own("orphan-z", "$9"), own("orphan-a", "$2")).
		SeedSessions(finishedSocket, own("b9ps-ended", "$1"), own("b9ps-missing", "$2"), own("orphan-finished", "$3"),
			tmuxfix.SeedSession{ID: "$4", Name: "other-store", Label: tmuxfix.Valid(tmuxfix.Token, "foreign", tmuxfix.OtherStoreID)},
			tmuxfix.SeedSession{ID: "$5", Name: "unlabelled"},
			tmuxfix.SeedSession{ID: "$6", Name: "invalid", LabelSet: true}).
		SeedSessions(def, own("orphan-default", "$7"))
	want := []map[string]any{
		unownedEntry("orphan-z", liveSocket, "$9", "session-orphan-z", created),
		unownedEntry("orphan-a", liveSocket, "$2", "session-orphan-a", created),
		unownedEntry("orphan-finished", finishedSocket, "$3", "session-orphan-finished", created),
		unownedEntry("orphan-default", def, "$7", "session-orphan-default", created),
	}
	slices.SortFunc(want, func(a, b map[string]any) int {
		if c := strings.Compare(a["tmux_socket"].(string), b["tmux_socket"].(string)); c != 0 {
			return c
		}
		return strings.Compare(a["tmux_session_id"].(string), b["tmux_session_id"].(string))
	})
	trailAt := trailLen(t)
	for _, canonical := range []bool{false, true} {
		res := mustSweep(t, s, paneChecker(procfix.Alive(fmStart)), fmSweep{tmux: rec, canonical: canonical})
		if got := unownedOf(t, res); !reflect.DeepEqual(got, want) {
			t.Errorf("canonical=%v unowned_sessions = %v; want %v", canonical, got, want)
		}
		assertLists(t, res, nil, nil)
	}
	// Each invocation gets its own lookup; no mutation, capture, or pane call is allowed.
	assertUnownedLookups(t, rec, liveSocket, finishedSocket, def, liveSocket, finishedSocket, def)
	for _, sd := range seeds {
		after, err := apitest.ReadSpawnColumns(dbPath, sd.id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, before[sd.id]) {
			t.Errorf("row %s changed: before=%+v after=%+v", sd.id, before[sd.id], after)
		}
	}
	drainTrail(t)
	for _, entry := range want {
		assertNoTrailSince(t, trailAt, entry["instance_id"].(string))
	}
}

// b.9ps: reporting reuses row lookups and their stopped sockets and virtual-time budget.
func TestFindMissingUnownedSessionsSweepBudget(t *testing.T) {
	// Serial: it sets TMUX/TMUX_TMPDIR with t.Setenv.
	for _, tc := range []struct {
		name   string
		budget time.Duration
		stop   bool
		calls  int
		found  bool
	}{
		{"row lookup reused", 0, false, 2, true},
		{"stopped socket not retried", 0, true, 2, false},
		{"already spent calls nothing", fmBudgetSpent, false, 0, false},
		{"row spends shared budget", time.Nanosecond, false, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := filepath.Join(userSocketDir(useOwnTmuxTmpdir(t)), "default")
			st, _ := seedPaneRow(t, apitest.WithPID(fskUnreadablePID), apitest.WithProcStarttime(fmStart), apitest.WithNoPane())
			orphan := tmuxfix.SeedSession{ID: "$1", Name: "orphan", Created: fmNow.Unix(),
				Label: tmuxfix.Valid(tmuxfix.Token, "b9ps-gone", st.StoreID())}
			rec := tmuxfix.NewRecorder().SeedSessions(apitest.TestSocket, orphan).SeedSessions(def, orphan)
			if tc.stop {
				rec.Script(apitest.TestSocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
			}
			res := mustSweep(t, st, fskChecker(), fmSweep{tmux: rec, budget: tc.budget})
			want := []map[string]any{}
			if tc.found {
				for _, socket := range []string{apitest.TestSocket, def} {
					want = append(want, unownedEntry("b9ps-gone", socket, "$1", "orphan", orphan.Created))
				}
				slices.SortFunc(want, func(a, b map[string]any) int {
					return strings.Compare(a["tmux_socket"].(string), b["tmux_socket"].(string))
				})
			} else if tc.stop {
				// The other socket still answers after the first socket stops.
				want = append(want, unownedEntry("b9ps-gone", def, "$1", "orphan", orphan.Created))
			}
			if got := unownedOf(t, res); !reflect.DeepEqual(got, want) {
				t.Errorf("unowned_sessions = %v; want %v", got, want)
			}
			var sockets []string
			if tc.calls > 0 {
				sockets = append(sockets, apitest.TestSocket)
			}
			if tc.calls > 1 {
				sockets = append(sockets, def)
			}
			assertUnownedLookups(t, rec, sockets...)
		})
	}
}
