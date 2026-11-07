package api_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func manyIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("many-%02d", i)
	}
	return ids
}

func TestGetManyValidatesBeforeReading(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{})
	before := e.row(t)
	tooMany := append([]string{e.id}, manyIDs(20)...)
	cases := []struct {
		name        string
		ids         []string
		want        error
		description string
	}{
		{"nil", nil, api.ErrInvalidFlags, "ErrInvalidFlags: get-many: no ids given"},
		{"empty", []string{}, api.ErrInvalidFlags, "ErrInvalidFlags: get-many: no ids given"},
		{"cap", tooMany, api.ErrTooManyIds, ""},
		{"empty after live row", []string{e.id, ""}, api.ErrInvalidFlags, "ErrInvalidFlags: get-many: id at position 2 is empty"},
		{"repeat", []string{e.id, "distinctive-private-id", "distinctive-private-id"}, api.ErrInvalidFlags, "ErrInvalidFlags: get-many: id at position 3 repeats position 2"},
		{"cap before repeat", append(slices.Clone(tooMany[:20]), e.id), api.ErrTooManyIds, ""},
		{"cap before empty", append(slices.Clone(tooMany[:20]), ""), api.ErrTooManyIds, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.GetMany(tc.ids)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v; want %v", err, tc.want)
			}
			if tc.description != "" && err.Error() != tc.description {
				t.Errorf("description=%q; want %q", err, tc.description)
			}
			if errors.Is(err, api.ErrTooManyIds) && api.ErrDetails(err) != (api.TooManyIdsDetails{IdCap: 20}) {
				t.Errorf("details=%+v", api.ErrDetails(err))
			}
			if jsonOf(t, got) != `{"spawns":[],"not_found":[]}` {
				t.Errorf("failure result=%s", jsonOf(t, got))
			}
			if !reflect.DeepEqual(before, e.row(t)) || len(e.rec.SocketCalls()) != 0 || len(e.rec.Calls()) != 0 {
				t.Fatal("refusal read or changed the live row")
			}
		})
	}
	// Exactly the default cap of 20 ids passes.
	ids := manyIDs(20)
	got, err := c.GetMany(ids)
	if err != nil || jsonOf(t, got.Spawns) != "[]" || !slices.Equal(got.NotFound, ids) {
		t.Fatalf("20 ids: %+v %v", got, err)
	}
}

func TestGetManyOrderAndExactIDs(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	for _, id := range []string{"B", "a", "A"} {
		e.seedRow(t, killRowSpec{ID: id, State: store.StateEnded})
	}
	c, _ := e.client(t)
	for _, ids := range [][]string{{"B", "a", "A"}, {" a", "missing", "a ", "b"}, {"B", " a", "A", "missing", "a"}} {
		got, err := c.GetMany(ids)
		if err != nil {
			t.Fatal(err)
		}
		found, absent := []string{}, []string{}
		for _, id := range ids {
			if slices.Contains([]string{"B", "a", "A"}, id) {
				found = append(found, id)
			} else {
				absent = append(absent, id)
			}
		}
		actual := []string{}
		for _, row := range got.Spawns {
			actual = append(actual, row.InstanceID)
		}
		if !slices.Equal(actual, found) || !slices.Equal(got.NotFound, absent) || got.Spawns == nil || got.NotFound == nil {
			t.Fatalf("ids=%v result=%+v", ids, got)
		}
	}
}

func TestGetManyCapLoadedAtConstruction(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "[get_many]\nid_cap = 2\n")
	old, _, err := e.clientFor(t, path)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, "[get_many]\nid_cap = 1\n")
	newer, _, err := e.clientFor(t, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		c   *api.Client
		cap int
	}{{old, 2}, {newer, 1}} {
		if _, err := tc.c.GetMany(manyIDs(tc.cap)); err != nil {
			t.Fatal(err)
		}
		got, err := tc.c.GetMany(manyIDs(tc.cap + 1))
		if !errors.Is(err, api.ErrTooManyIds) || api.ErrDetails(err) != (api.TooManyIdsDetails{IdCap: tc.cap}) || jsonOf(t, got) != `{"spawns":[],"not_found":[]}` {
			t.Fatalf("cap %d: %+v %v", tc.cap, got, err)
		}
	}
}

func TestGetManyFailureKeepsEarlierWritesAndStops(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	first := e.seedRow(t, killRowSpec{State: store.StatePending, Agent: agentGone, Opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor)}})
	second := e.seedRow(t, killRowSpec{State: store.StatePending, Agent: agentGone, Opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor)}})
	third := e.seedRow(t, killRowSpec{State: store.StateWorking})
	c, _ := e.client(t)
	before := e.columns(t, third.ID)
	storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, second.ID)
	got, err := c.GetMany([]string{first.ID, second.ID, third.ID})
	if err == nil || errors.Is(err, api.ErrSpawnNotFound) || !strings.Contains(err.Error(), "injected write failure: reuse restore") {
		t.Fatalf("error=%v; want injected write failure", err)
	}
	if jsonOf(t, got) != `{"spawns":[],"not_found":[]}` {
		t.Fatalf("failure result=%s", jsonOf(t, got))
	}
	if state := mustGetSpawnState(t, e.st, first.ID); state != store.StateMissing {
		t.Fatalf("first write lost: %s", state)
	}
	if state := mustGetSpawnState(t, e.st, second.ID); state != store.StatePending {
		t.Fatalf("failed write committed: %s", state)
	}
	e.assertRowUnchanged(t, third.ID, before)
	e.assertNoTmuxCall(t)
}

func TestGetManyDoesNotWaitForWriter(t *testing.T) {
	t.Parallel()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	c := e.client(t, apitest.ConfigKeys{StoreBusyTimeoutMs: 10000})
	before := e.row(t)
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	start := time.Now()
	got, err := c.GetMany([]string{e.id})
	elapsed := time.Since(start)
	if err != nil || len(got.Spawns) != 1 || elapsed > 2*time.Second {
		t.Fatalf("read under lock: %+v %v after %s", got, err, elapsed)
	}
	if !reflect.DeepEqual(before, e.row(t)) {
		t.Fatal("write committed under held lock")
	}
	release()
	// Prove this read would write when no writer holds the lock.
	apitest.ObserveDisplayRead(t, e.s, e.id, store.StateWorking, apitest.VersionAdvanced, func() (apitest.DisplayReading, error) {
		r, err := c.GetMany([]string{e.id})
		if err != nil {
			return apitest.DisplayReading{}, err
		}
		return apitest.DisplayReading{State: r.Spawns[0].State, DisplayedState: r.Spawns[0].DisplayedState}, nil
	})
}

// Only stopped sockets carry between ids; held answers and budgets stay per id.
func TestGetManyStopsAnUnansweringSocket(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		call    tmux.Call
		failure tmux.Failure
		calls   [3]int // shared socket: lookups, listings, captures
	}{
		{"lookup timeout", tmux.CallLookup, tmux.FailTimeout, [3]int{1, 0, 0}},
		{"unreadable pane listing", tmux.CallListPanes, tmux.FailUnrecognized, [3]int{1, 1, 0}},
		{"no server", tmux.CallLookup, tmux.FailNoServer, [3]int{3, 0, 0}},
		{"capture timeout", tmux.CallCapture, tmux.FailTimeout, [3]int{3, 3, 3}},
		{"budget not carried", "", 0, [3]int{3, 3, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
			shared := []string{e.id, e.addRow(t, store.AgentCodex, apitest.TestSocket, "%7", 4343), e.addRow(t, store.AgentClaude, apitest.TestSocket, "%8", 4344)}
			other := e.addRow(t, store.AgentCodex, fmbSocket(1), "%9", 4345)
			ids := []string{shared[0], other, shared[1], shared[2]}
			c := e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60})
			_, _, baselined := apitest.RepeatUntilCommitted(func() bool {
				e.rec.Reset()
				if _, err := c.GetMany(ids); err != nil {
					t.Fatal(err)
				}
				for _, id := range ids {
					row, err := e.s.GetSpawn(id)
					if err != nil {
						t.Fatal(err)
					}
					if row.PaneSHA256 == "" {
						return false
					}
				}
				return true
			})
			if !baselined {
				t.Fatal("baselines never committed")
			}
			first := e.now
			committed := map[string]store.Spawn{}
			for _, id := range ids {
				row, err := e.s.GetSpawn(id)
				if err != nil || row.PaneSHA256 == "" {
					t.Fatalf("setup: row %s has no baseline: %+v %v", id, row, err)
				}
				committed[id] = row
			}
			e.now = e.now.Add(time.Second)
			var charge time.Duration
			var got api.GetManyResult
			attempts, elapsed, observed := apitest.RepeatUntilCommitted(func() bool {
				// Each attempt measures one call at the same injected time.
				e.rec.Reset()
				if tc.failure != 0 {
					e.rec.Script(apitest.TestSocket, tmuxfix.Script{Failure: tc.failure}, tc.call)
				} else {
					// Three calls fit one id's budget; two ids' six calls reach it.
					charge = (config.Tmux{}).EffectiveSweepBudget() / 6
					clock := tmuxfix.NewClock(e.now)
					e.rec.WithVirtualTime(clock, tmux.Timeouts{Query: charge, Action: charge})
					api.SetClockForTest(c, clock.Now)
				}
				var err error
				got, err = c.GetMany(ids)
				if err != nil || len(got.Spawns) != len(ids) || len(got.NotFound) != 0 {
					t.Fatalf("get-many = %+v, %v", got, err)
				}
				for _, id := range ids {
					row, err := e.s.GetSpawn(id)
					if err != nil {
						t.Fatal(err)
					}
					// Same-value samples update evidence without advancing the version.
					if row.DisplayEvidence == committed[id].DisplayEvidence {
						return false
					}
				}
				return true
			})
			if !observed {
				t.Fatalf("pane observations never committed in %d attempts over %v", attempts, elapsed)
			}
			calls := map[string][3]int{}
			for _, call := range e.rec.SocketCalls() {
				counts := calls[call.Socket]
				switch call.Call {
				case tmux.CallLookup:
					counts[0]++
				case tmux.CallListPanes:
					counts[1]++
				case tmux.CallCapture:
					counts[2]++
				default:
					t.Errorf("unexpected tmux call: %+v", call)
				}
				calls[call.Socket] = counts
			}
			wantCalls := map[string][3]int{apitest.TestSocket: tc.calls, fmbSocket(1): {1, 1, 1}}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Errorf("tmux calls per socket (lookup, listing, capture) = %v; want %v", calls, wantCalls)
			}
			for i, detail := range got.Spawns {
				id := ids[i]
				if detail.InstanceID != id {
					t.Fatalf("row %d = %s; want %s", i, detail.InstanceID, id)
				}
				row, err := e.s.GetSpawn(id)
				if err != nil {
					t.Fatal(err)
				}
				before := committed[id]
				reason := ""
				if tc.failure != 0 && id != other {
					reason = store.ReasonUnreadable
					if row.ObservationVersion != before.ObservationVersion+1 || row.PaneSHA256 != "" || row.PaneSinceMillis != 0 {
						t.Errorf("failed sample kept row %s's baseline: before=%+v after=%+v", id, before, row)
					}
				} else if row.ObservationVersion != before.ObservationVersion || row.PaneSHA256 != before.PaneSHA256 || row.PaneSinceMillis != before.PaneSinceMillis {
					t.Errorf("sampled row %s lost its baseline: before=%+v after=%+v", id, before, row)
				}
				// In particular, later stopped ids report unreadable, not deadline.
				checked := e.now.Add(time.Duration(i+1) * 3 * charge)
				assertPaneRecord(t, detail.Confirmations.DisplayedState, first, checked, reason)
			}
		})
	}
}
