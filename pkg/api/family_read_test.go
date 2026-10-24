package api_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// retainedDisplayFamily links a waiting grandparent and parent above an
// owned-pane member before it enters idle_screen, the only Displayed State a
// read shows (b.zpv), and returns that entry's status.
func retainedDisplayFamily(t *testing.T) (*paneEnv, []string, api.StatusResult) {
	t.Helper()
	e := newPaneEnv(t, paneSpec{agent: store.AgentClaude})
	// Entry precedes the real writer clock on any execution date; subsequent
	// observations use the hook's persisted timestamp, never a sleep.
	e.now = time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Millisecond)
	ids := []string{"grandparent", "parent", e.id}
	for _, id := range ids[:2] {
		if _, err := apitest.SeedSpawn(e.dbPath, id, store.StateWaiting, "", "off", "", false); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(ids); i++ {
		if err := apitest.SeedParentChild(e.dbPath, ids[i-1], ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	return e, ids, enterFamilyDisplay(t, e)
}

// enterFamilyDisplay commits an idle_screen episode through the owned-pane
// client, so a subsequent Stop retains it on raw waiting.
func enterFamilyDisplay(t *testing.T, e *paneEnv) api.StatusResult {
	t.Helper()
	return enterIdleScreen(t, e, e.client(t, apitest.ConfigKeys{IdleScreenTimeoutSeconds: 60}), 0)
}

// addDisplayedMember adds a working Claude row on its own pane of e's socket
// and enters idle_screen on it with e's clock, returning its id.
func addDisplayedMember(t *testing.T, e *paneEnv, paneID string, panePID int) string {
	t.Helper()
	id := e.addRow(t, store.AgentClaude, apitest.TestSocket, paneID, panePID)
	other := *e
	other.id = id
	enterFamilyDisplay(t, &other)
	return id
}

type familyHistoryValue struct {
	state, settled string
	changed        int64
}

func readFamilyHistories(t *testing.T, path string, ids []string) map[string]familyHistoryValue {
	t.Helper()
	out := make(map[string]familyHistoryValue, len(ids))
	for _, id := range ids {
		state, settled, changed, err := apitest.ReadFamilyHistory(path, id)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = familyHistoryValue{state, settled, changed}
	}
	return out
}

// orderedFamilyStore changes only the unspecified list order, leaving real
// filtering, observations and transactions intact.
type orderedFamilyStore struct {
	*store.Store
	ids []string
}

func (s orderedFamilyStore) ListSpawns(f api.ListFilters) ([]api.Spawn, error) {
	rows, err := s.Store.ListSpawns(f)
	rank := map[string]int{}
	for i, id := range s.ids {
		rank[id] = i
	}
	sort.Slice(rows, func(i, j int) bool { return rank[rows[i].InstanceID] < rank[rows[j].InstanceID] })
	return rows, err
}

// b.ob8 F1: raw waiting cannot remove a retained display's family contribution.
// Each surface must return its own first committing response coherently; a
// subsequent status read must not repair a stale get/list assertion. The
// retained display reads displayed_state idle_screen beside the member's
// settled state: waiting, except under the original reader clock, the Stop's
// own instant, where settle_ms has not passed and it is still working (b.zpv).
func TestFamilyRetainedDisplayExitFirstRead(t *testing.T) {
	t.Parallel()
	display := api.StateIdleScreen
	for _, ms := range []int64{0, 5000} {
		for _, first := range []string{"status", "get", "list", "list ancestors first", "list descendant first", "list filtered limit", "status original reader clock"} {
			originalClock := first == "status original reader clock"
			if ms == 0 && originalClock {
				continue
			}
			if ms > 0 && first != "status" && first != "get" && first != "list" && !originalClock {
				continue // the ordered public List variants intentionally use zero settling
			}
			t.Run(fmt.Sprintf("settle=%d/%s", ms, first), func(t *testing.T) {
				e, ids, entered := retainedDisplayFamily(t)
				before := readFamilyHistories(t, e.dbPath, ids)
				for _, h := range before {
					if h != (familyHistoryValue{store.StateWorking, store.StateWorking, 0}) {
						t.Fatalf("initial family history=%+v", before)
					}
				}
				e.s.SetSettleMs(ms)
				if hook := storefix.ApplyAgentHook(t, e.s, e.id, "Stop", ""); !hook.Applied {
					t.Fatalf("Stop held: %+v", hook)
				}
				if row := e.row(t); row.State != store.StateWaiting || row.CommittedDisplay().State != display {
					t.Fatalf("Stop did not retain the display on raw waiting: %+v", row)
				}
				if after := readFamilyHistories(t, e.dbPath, ids); !reflect.DeepEqual(after, before) {
					t.Errorf("raw hook removed retained contribution: before=%+v after=%+v", before, after)
				}
				_, rawStamp, err := apitest.ReadSettle(e.dbPath, e.id)
				if err != nil {
					t.Fatal(err)
				}
				e.now = time.UnixMilli(rawStamp).Add(10 * time.Second)
				c := e.client(t, apitest.ConfigKeys{StateSettleMs: ms, IdleScreenTimeoutSeconds: 60})
				// The settled Status seam separates the original reader clock
				// from later observation time, without changing stored history.
				settle := store.SettleRead{SettleMs: ms, NowMs: rawStamp}
				s := &readLeaseStore{observationReader: e.s}
				mark := trailMark(t)
				// The original reader clock is the Stop's own instant, before
				// settle_ms has passed: its settled state is still working.
				wantState := store.StateWaiting
				if originalClock {
					wantState = store.StateWorking
				}
				// read wants the member in wantState, showing displayed ("" for none).
				read := func(displayed string, mustCommit bool) map[string]familyProjection {
					t.Helper()
					var got map[string]familyProjection
					_, _, committed := apitest.RepeatUntilCommitted(func() bool {
						prior := e.row(t)
						s.settles = nil
						got = map[string]familyProjection{}
						switch first {
						case "status", "status original reader clock":
							var r api.StatusResult
							var err error
							if originalClock {
								r, err = api.StatusSettledForTest(s, api.RelayView{Now: func() time.Time { return e.now }}, settle, e.id)
							} else {
								r, err = c.Status(e.id)
							}
							if err != nil {
								t.Fatal(err)
							}
							got[e.id] = familyProjection{r.State, r.FamilyState, r.DisplayedState, r.StateDetail}
						case "get":
							r, err := c.Get(e.id)
							if err != nil {
								t.Fatal(err)
							}
							got[e.id] = familyProjection{r.State, r.FamilyState, r.DisplayedState, r.StateDetail}
						default:
							var listed api.ListResult
							var err error
							if first == "list" {
								listed, err = c.List(api.ListParams{})
							} else {
								order := ids
								params := api.ListParams{}
								if first == "list descendant first" || first == "list filtered limit" {
									order = []string{e.id, ids[1], ids[0]}
								}
								if first == "list filtered limit" {
									// Naming idle_screen applies order's member-first
									// order before the limit; a lifecycle filter
									// alone keeps the store's LIMIT, which picks any
									// waiting row.
									params = api.ListParams{State: []string{store.StateWaiting, api.StateIdleScreen}, Limit: 1}
								}
								listed, err = api.List(orderedFamilyStore{e.s, order}, api.RelayView{Now: func() time.Time { return e.now }}, params)
							}
							if err != nil {
								t.Fatal(err)
							}
							wantCount := len(ids)
							if first == "list filtered limit" {
								wantCount = 1
							}
							if len(listed.Spawns) != wantCount {
								t.Fatalf("list=%+v", listed)
							}
							for _, r := range listed.Spawns {
								got[r.InstanceID] = familyProjection{state: r.State, family: r.FamilyState, displayed: r.DisplayedState}
							}
						}
						return !mustCommit || apitest.VersionAdvanced(prior, e.row(t))
					})
					if !committed || got[e.id].state != wantState || got[e.id].displayed != displayed {
						t.Fatalf("first committing %s=%+v committed=%v; want %s showing %q", first, got, committed, wantState, displayed)
					}
					if first == "status" || first == "get" || originalClock {
						wantDetail := api.StateDetail{}
						if displayed != "" {
							wantDetail = entered.StateDetail
						}
						if !reflect.DeepEqual(got[e.id].detail, wantDetail) {
							t.Errorf("retained detail=%+v; want %+v", got[e.id].detail, wantDetail)
						}
					}
					for id, r := range got {
						if id != e.id && (r.state != store.StateWaiting || r.displayed != "") {
							t.Errorf("ancestor changed own state: %+v", r)
						}
					}
					return got
				}
				if ms > 0 {
					for _, p := range read(display, true) {
						if p.family != store.StateWorking {
							t.Errorf("pending exit lost family: %+v", p)
						}
					}
					if after := readFamilyHistories(t, e.dbPath, ids); !reflect.DeepEqual(after, before) {
						t.Errorf("first pending exit changed history: %+v", after)
					}
					e.now = e.now.Add(time.Duration(ms-1) * time.Millisecond)
					read(display, false)
					if events := displayEvents(t, mark, e.id); len(events) != 0 {
						t.Fatalf("early exit records=%v", events)
					}
					e.now = e.now.Add(time.Millisecond)
				}
				from := time.Now().UnixMilli()
				got := read("", true)
				to := time.Now().UnixMilli()
				after := readFamilyHistories(t, e.dbPath, ids)
				stamp := after[e.id].changed
				for _, id := range ids {
					if h := after[id]; h != (familyHistoryValue{store.StateWaiting, store.StateWorking, stamp}) || stamp < from || stamp > to {
						t.Errorf("committed exit history %s=%+v; want waiting/working/%d in [%d,%d]", id, h, stamp, from, to)
					}
				}
				wantFamily := store.StateWaiting
				if ms > 0 && e.now.UnixMilli() < stamp+ms {
					wantFamily = store.StateWorking
				}
				if originalClock {
					// The committing observation itself reads its families under
					// the reader's original SettleRead; no later read supplies them.
					if !reflect.DeepEqual(s.settles, []store.SettleRead{settle}) || settle.NowMs >= stamp+ms {
						t.Fatalf("observation settles=%+v reader=%d family deadline=%d; want one observation under %+v before the independent deadline", s.settles, settle.NowMs, stamp+ms, settle)
					}
					// This first committing response must retain working even if
					// the later observation clock has passed the family deadline.
					wantFamily = store.StateWorking
				}
				for id, r := range got {
					if r.family != wantFamily {
						t.Errorf("first committed %s row %s family=%s; want %s", first, id, r.family, wantFamily)
					}
				}
				events := displayEvents(t, mark, e.id)
				if len(events) != 1 {
					t.Fatalf("exit events=%v", events)
				}
				assertDisplayEvent(t, events[0], display, store.StateWaiting, e.now)
				// Independent readers may lag the observation clock. Read the durable
				// writer timestamp's boundary, never seed or backdate that transition.
				for _, offset := range []int64{ms - 1, ms, ms + 1} {
					if ms == 0 && offset < 0 {
						continue
					}
					e.now = time.UnixMilli(stamp + offset)
					wantFamily := store.StateWaiting
					if ms > 0 && offset < ms {
						wantFamily = store.StateWorking
					}
					for _, id := range ids {
						s, g, l := clientReported(t, c, id)
						if s.FamilyState != wantFamily || g.FamilyState != wantFamily || l.FamilyState != wantFamily {
							t.Errorf("family boundary %s/+%d: %s/%s/%s; want %s", id, offset, s.FamilyState, g.FamilyState, l.FamilyState, wantFamily)
						}
					}
				}
				if stable := readFamilyHistories(t, e.dbPath, ids); !reflect.DeepEqual(stable, after) {
					t.Errorf("repeated reads restarted history: before=%+v after=%+v", after, stable)
				}
			})
		}
	}
}

type familyProjection struct {
	state, family, displayed string
	detail                   api.StateDetail
}

// readFamilyProjection saves the response under test before any verifying read.
func readFamilyProjection(s observationReader, view api.RelayView, verb, id string) (map[string]familyProjection, error) {
	out := map[string]familyProjection{}
	switch verb {
	case "status":
		r, err := api.Status(s, view, id)
		out[id] = familyProjection{r.State, r.FamilyState, r.DisplayedState, r.StateDetail}
		return out, err
	case "get":
		r, err := api.Get(s, view, id)
		out[id] = familyProjection{r.State, r.FamilyState, r.DisplayedState, r.StateDetail}
		return out, err
	default:
		r, err := api.List(s, view, api.ListParams{})
		for _, row := range r.Spawns {
			if _, duplicate := out[row.InstanceID]; duplicate {
				return nil, fmt.Errorf("list repeated row %s", row.InstanceID)
			}
			out[row.InstanceID] = familyProjection{state: row.State, family: row.FamilyState, displayed: row.DisplayedState}
		}
		return out, err
	}
}

// retainedFamilyProjection is the original committed view: waiting ancestors
// and a waiting member showing idle_screen, all contributing working. List
// carries no detail.
func retainedFamilyProjection(ids []string, entered api.StatusResult, verb string) map[string]familyProjection {
	id := ids[len(ids)-1]
	want := map[string]familyProjection{id: {store.StateWaiting, store.StateWorking, api.StateIdleScreen, entered.StateDetail}}
	if verb == "list" {
		want[id] = familyProjection{state: store.StateWaiting, family: store.StateWorking, displayed: api.StateIdleScreen}
		for _, ancestor := range ids[:len(ids)-1] {
			want[ancestor] = familyProjection{state: store.StateWaiting, family: store.StateWorking}
		}
	}
	return want
}

// committedFamilyProjection is a committed zero-settle exit's view: the member
// waiting without detail, its ancestors' families waiting from the same commit.
func committedFamilyProjection(ids []string, verb string) map[string]familyProjection {
	want := map[string]familyProjection{ids[len(ids)-1]: {state: store.StateWaiting, family: store.StateWaiting}}
	if verb == "list" {
		for _, ancestor := range ids[:len(ids)-1] {
			want[ancestor] = familyProjection{state: store.StateWaiting, family: store.StateWaiting}
		}
	}
	return want
}

// b.ob8 B1: a genuine hook that changes ownership after the read's row snapshot
// but before its exit observation commits refuses that observation. The read
// returns the whole earlier display/detail/family snapshot, never a mix; the
// refused observation writes, refreshes and records nothing.
func TestFamilyExitOwnershipRaceKeepsEarlierSnapshot(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"status", "get", "list"} {
		for _, event := range []string{"Stop", "SessionEnd"} {
			t.Run(verb+"/"+event, func(t *testing.T) {
				e, ids, entered := retainedDisplayFamily(t)
				if a := storefix.ApplyAgentHook(t, e.s, e.id, "Stop", ""); !a.Applied {
					t.Fatalf("Stop=%+v", a)
				}
				e.now = time.Now().Add(time.Minute)
				// Ancestors precede the member: a refused exit must leave
				// already-listed ancestors with their original families.
				s := &readLeaseStore{observationReader: orderedFamilyStore{e.s, ids}}
				raced := false
				var hooked store.Spawn
				var hookedHistory map[string]familyHistoryValue
				var mark int
				// The first request read follows every reader's row snapshot
				// and precedes the observation's write.
				s.afterFirst = func() error {
					if raced {
						return nil
					}
					raced = true
					before := e.row(t)
					if a := storefix.ApplyAgentHook(t, e.s, e.id, event, ""); !a.Applied {
						t.Fatalf("concurrent %s=%+v", event, a)
					}
					hooked = e.row(t)
					if hooked.RowVersion <= before.RowVersion {
						t.Fatalf("%s did not change ownership: before=%+v after=%+v", event, before, hooked)
					}
					hookedHistory, mark = readFamilyHistories(t, e.dbPath, ids), trailMark(t)
					return nil
				}
				var got map[string]familyProjection
				var err error
				apitest.RepeatUntilCommitted(func() bool {
					s.reads, s.writes = 0, nil
					got, err = readFamilyProjection(s, api.RelayView{Now: func() time.Time { return e.now }}, verb, e.id)
					return raced || err != nil
				})
				want := retainedFamilyProjection(ids, entered, verb)
				if !raced || err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("raced read=%+v err=%v raced=%v; want the earlier coherent %+v", got, err, raced, want)
				}
				if !reflect.DeepEqual(s.writes, []string{"ObserveNeedsIntelligence"}) {
					t.Errorf("observations=%v; want one refused exit", s.writes)
				}
				if after := e.row(t); !reflect.DeepEqual(after, hooked) {
					t.Errorf("refused exit changed row: hooked=%+v after=%+v", hooked, after)
				}
				if after := readFamilyHistories(t, e.dbPath, ids); !reflect.DeepEqual(after, hookedHistory) {
					t.Errorf("refused exit changed histories: hooked=%+v after=%+v", hookedHistory, after)
				}
				if events := displayEvents(t, mark, e.id); len(events) != 0 {
					t.Errorf("refused exit emitted %v", events)
				}
			})
		}
	}
}

// b.ob8 B1: take the real pool connection only after the exit commits. The
// response must precede its release and still report that committed exit,
// with its ancestors' families, including a list whose later member's
// observation encounters that lease. No subsequent read may repair the response.
func TestFamilyExitSkipsLeasedConnection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verb, backend string
		laterMember   bool
	}{
		{"status", "cursor missing view", false},
		{"list", "store", true},
	} {
		t.Run(fmt.Sprintf("%s/%s/later=%v", tc.verb, tc.backend, tc.laterMember), func(t *testing.T) {
			e, ids, _ := retainedDisplayFamily(t)
			want := committedFamilyProjection(ids, tc.verb)
			order := []string{e.id, ids[1], ids[0]}
			var later string
			var laterBefore store.Spawn
			if tc.laterMember {
				later = addDisplayedMember(t, e, "%7", 4343)
				if a := storefix.ApplyAgentHook(t, e.s, later, "Stop", ""); !a.Applied {
					t.Fatalf("later Stop=%+v", a)
				}
				var err error
				if laterBefore, err = e.s.GetSpawn(later); err != nil {
					t.Fatal(err)
				}
				order = append(order, later)
				want[later] = familyProjection{state: store.StateWaiting, family: store.StateWorking, displayed: api.StateIdleScreen}
			}
			if a := storefix.ApplyAgentHook(t, e.s, e.id, "Stop", ""); !a.Applied {
				t.Fatalf("Stop=%+v", a)
			}
			e.now = time.Now().Add(time.Minute)
			var reader observationReader = orderedFamilyStore{e.s, order}
			if tc.backend == "cursor missing view" {
				reader = e.s.WithCursorMissingReads(nil)
			}
			take, leased, release := holdReadConnection(t, e.s, e.id)
			committed := false
			var leaseErr error
			s := &readLeaseStore{observationReader: reader, afterObservation: func(row store.Spawn, shown store.DisplayDetail) {
				if row.InstanceID == e.id && shown.State == store.StateWaiting && !committed {
					committed = true
					leaseErr = take()
				}
			}}
			mark := trailMark(t)
			var got map[string]familyProjection
			var err error
			readUnderLease(t, leased, release, func() {
				apitest.RepeatUntilCommitted(func() bool {
					s.writes = nil
					got, err = readFamilyProjection(s, api.RelayView{Now: func() time.Time { return e.now }}, tc.verb, e.id)
					return committed || err != nil
				})
			})
			if !committed || leaseErr != nil || err != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("leased read=%+v err=%v committed=%v lease=%v; want its committed exit %+v", got, err, committed, leaseErr, want)
			}
			if !reflect.DeepEqual(s.writes, []string{"ObserveNeedsIntelligence"}) {
				t.Errorf("observations under lease=%v; want only the first committed exit", s.writes)
			}
			// Verifying reads happen only after the response and lease release.
			release()
			if row := e.row(t); row.State != store.StateWaiting || row.DisplayObservation != "" {
				t.Errorf("exit not durable: %+v", row)
			}
			for id, h := range readFamilyHistories(t, e.dbPath, ids) {
				if h.state != store.StateWaiting || h.changed == 0 {
					t.Errorf("committed family %s=%+v", id, h)
				}
			}
			if events := displayEvents(t, mark, e.id); len(events) != 1 {
				t.Errorf("exit events=%v; want one", events)
			} else {
				assertDisplayEvent(t, events[0], api.StateIdleScreen, store.StateWaiting, e.now)
			}
			if later != "" {
				after, err := e.s.GetSpawn(later)
				if err != nil || !reflect.DeepEqual(after, laterBefore) {
					t.Errorf("later skipped observation changed row: before=%+v after=%+v err=%v", laterBefore, after, err)
				}
				if events := displayEvents(t, mark, later); len(events) != 0 {
					t.Errorf("later skipped observation emitted %v", events)
				}
			}
		})
	}
}

// b.ob8 B1: a committing list reports its own committed exits. A filter and
// limit on the retained display drop its only candidate (the waiting ancestors
// never display it), leaving an empty list. Two members exiting under one
// parent, listed after their ancestors, both report waiting and so do the
// ancestors: the later commit's families supersede the earlier commit's
// without reverting the earlier member. Either way the list reads its rows
// once and observes each exiting member once.
func TestFamilyExitListReportsCommittedState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		siblings bool
	}{
		{"idle_screen filter", false},
		{"siblings ancestors first", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := api.ListParams{State: []string{api.StateIdleScreen}, Limit: 1}
			if tc.siblings {
				params = api.ListParams{}
			}
			var (
				e              *paneEnv
				members, order []string
				s              *readLeaseStore
				reads, mark    int
			)
			// exited builds a family whose members have each had an accepted
			// Stop, with the trail mark taken after their entry records.
			exited := func() {
				var ids []string
				e, ids, _ = retainedDisplayFamily(t)
				members = []string{e.id}
				order = []string{e.id, ids[1], ids[0]}
				if tc.siblings {
					sibling := e.addRow(t, store.AgentClaude, apitest.TestSocket, "%7", 4343)
					if err := apitest.SeedParentChild(e.dbPath, ids[1], sibling); err != nil {
						t.Fatal(err)
					}
					other := *e
					other.id = sibling
					enterFamilyDisplay(t, &other)
					members = append(members, sibling)
					order = []string{ids[0], ids[1], e.id, sibling}
				}
				for _, id := range members {
					if a := storefix.ApplyAgentHook(t, e.s, id, "Stop", ""); !a.Applied {
						t.Fatalf("Stop %s=%+v", id, a)
					}
				}
				e.now = time.Now().Add(time.Minute)
				s = &readLeaseStore{observationReader: orderedFamilyStore{e.s, order}, afterRow: func() error { reads++; return nil }}
				mark = trailMark(t)
			}
			rows := func() []store.Spawn {
				out := make([]store.Spawn, len(members))
				for i, id := range members {
					row, err := e.s.GetSpawn(id)
					if err != nil {
						t.Fatal(err)
					}
					out[i] = row
				}
				return out
			}
			exited()
			var got api.ListResult
			var err error
			_, _, committed := apitest.RepeatUntilCommitted(func() bool {
				before := rows()
				reads, s.writes = 0, nil
				got, err = api.List(s, api.RelayView{Now: func() time.Time { return e.now }}, params)
				advanced := 0
				for i, after := range rows() {
					if apitest.VersionAdvanced(before[i], after) {
						advanced++
					}
				}
				if err != nil || advanced == len(members) {
					return true
				}
				// An exited member's observation writes nothing, so a list
				// that committed only some exits cannot be repeated on them.
				if advanced > 0 {
					exited()
				}
				return false
			})
			want := map[string]familyProjection{}
			if tc.siblings {
				for _, id := range order {
					want[id] = familyProjection{state: store.StateWaiting, family: store.StateWaiting}
				}
			}
			listed := map[string]familyProjection{}
			for _, r := range got.Spawns {
				listed[r.InstanceID] = familyProjection{state: r.State, family: r.FamilyState}
			}
			if !committed || err != nil || got.Spawns == nil || len(got.Spawns) != len(want) || !reflect.DeepEqual(listed, want) {
				t.Fatalf("committing list=%+v %v committed=%v; want %+v", got, err, committed, want)
			}
			wantWrites := make([]string, len(members))
			for i := range wantWrites {
				wantWrites[i] = "ObserveNeedsIntelligence"
			}
			if reads != 1 || !reflect.DeepEqual(s.writes, wantWrites) {
				t.Errorf("list repeated work: lists=%d observations=%v; want one list and %v", reads, s.writes, wantWrites)
			}
			for id, h := range readFamilyHistories(t, e.dbPath, order) {
				if h.state != store.StateWaiting || h.changed == 0 {
					t.Errorf("committed family %s=%+v", id, h)
				}
			}
			for _, id := range members {
				if events := displayEvents(t, mark, id); len(events) != 1 {
					t.Errorf("exit events %s=%v; want one", id, events)
				} else {
					assertDisplayEvent(t, events[0], api.StateIdleScreen, store.StateWaiting, e.now)
				}
			}
		})
	}
}

// b.ob8 F1 repair: reporting a committed exit's families cannot make an
// excluded malformed row a new dependency of an otherwise successful
// filtered/limited list.
func TestFamilyExitListIgnoresExcludedMalformedRows(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		bad  apitest.SpawnOption
	}{
		{"agent_args", apitest.WithRawAgentArgs("{bad")},
		{"labels", apitest.WithRawLabels("{bad")},
		{"extra_env", apitest.WithRawExtraEnv("{bad")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ids, _ := retainedDisplayFamily(t)
			if a := storefix.ApplyAgentHook(t, e.s, e.id, "Stop", ""); !a.Applied {
				t.Fatalf("Stop=%+v", a)
			}
			if _, err := apitest.SeedSpawn(e.dbPath, "excluded-ended", store.StateEnded, "", "off", "", false, tc.bad); err != nil {
				t.Fatal(err)
			}
			e.now = time.Now().Add(time.Minute)
			reads := 0
			s := &readLeaseStore{observationReader: orderedFamilyStore{e.s, []string{e.id, ids[1], ids[0]}}, afterRow: func() error { reads++; return nil }}
			var got api.ListResult
			var err error
			_, _, committed := apitest.RepeatUntilCommitted(func() bool {
				before := e.row(t)
				reads = 0
				got, err = api.List(s, api.RelayView{Now: func() time.Time { return e.now }}, api.ListParams{State: []string{store.StateWaiting}, Limit: 1})
				return apitest.VersionAdvanced(before, e.row(t)) || err != nil
			})
			if !committed || err != nil || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != e.id || got.Spawns[0].State != store.StateWaiting || got.Spawns[0].FamilyState != store.StateWaiting {
				t.Fatalf("first committed filtered list=%+v %v committed=%v; want the exited member waiting/waiting", got, err, committed)
			}
			if reads != 1 {
				t.Errorf("list enumerated %d times; want one initial filtered read", reads)
			}
		})
	}
}

// b.ob8 F1: immediate command/terminal writes discard a retained display on
// raw waiting and update its ancestors before any public read can repair them.
func TestFamilyRetainedDisplayImmediateExit(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{store.StateEnded, store.StateMissing} {
		t.Run(terminal, func(t *testing.T) {
			e, ids, _ := retainedDisplayFamily(t)
			if a := storefix.ApplyAgentHook(t, e.s, e.id, "Stop", ""); !a.Applied {
				t.Fatalf("Stop=%+v", a)
			}
			before := e.row(t)
			mark := trailMark(t)
			if terminal == store.StateEnded {
				if a := storefix.ApplyAgentHook(t, e.s, e.id, "SessionEnd", ""); !a.Applied {
					t.Fatalf("SessionEnd=%+v", a)
				}
			} else if _, res, err := e.s.MarkMissingIfSameLife(e.id, before.Snapshot); err != nil || res != store.CondApplied {
				t.Fatalf("missing=%v %v", res, err)
			}
			if row := e.row(t); row.State != terminal || row.DisplayObservation != "" {
				t.Fatalf("terminal row=%+v", row)
			}
			histories := readFamilyHistories(t, e.dbPath, ids)
			stamp := histories[e.id].changed
			for id, h := range histories {
				if h != (familyHistoryValue{store.StateWaiting, store.StateWorking, stamp}) || stamp == 0 {
					t.Errorf("immediate family %s=%+v", id, h)
				}
			}
			_, at, err := apitest.ReadSettle(e.dbPath, e.id)
			if err != nil {
				t.Fatal(err)
			}
			events := displayEvents(t, mark, e.id)
			if len(events) != 1 {
				t.Fatalf("terminal events=%v", events)
			}
			assertDisplayEvent(t, events[0], api.StateIdleScreen, terminal, time.UnixMilli(at))
		})
	}
}

func TestFamilyMembershipAndReadScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, parent, child, leaf, relay, want string }{
		{"lone waiting", "waiting", "", "", "", "waiting"},
		{"lone working", "working", "", "", "", "working"},
		{"grandchild working", "waiting", "waiting", "working", "", "working"},
		{"all waiting", "waiting", "waiting", "waiting", "", "waiting"},
		{"pending descendant", "waiting", "pending", "", "", "working"},
		{"ask user descendant", "waiting", "ask_user", "", "", "working"},
		{"legacy permission descendant", "waiting", "check_permission", "", "off", "working"},
		{"relay permission descendant", "waiting", "check_permission", "", "on", "working"},
		{"ended intermediate", "waiting", "ended", "working", "", "working"},
		{"missing intermediate", "waiting", "missing", "working", "", "working"},
		{"ended member excluded", "waiting", "ended", "", "", "waiting"},
		{"missing member excluded", "waiting", "missing", "", "", "waiting"},
		{"no live members", "ended", "missing", "ended", "", "waiting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "state.db")
			seed := func(id, state, relay string) {
				if _, err := apitest.SeedSpawn(dbPath, id, state, "/family/"+id, relay, "", true); err != nil {
					t.Fatal(err)
				}
			}
			seed("parent", tc.parent, "")
			if tc.child != "" {
				seed("child", tc.child, tc.relay)
				if tc.child == store.StateCheckPermission {
					if _, err := apitest.SeedPermissionRequest(dbPath, "child", "Bash"); err != nil {
						t.Fatal(err)
					}
				}
				if err := apitest.SeedParentChild(dbPath, "parent", "child"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.leaf != "" {
				seed("leaf", tc.leaf, "")
				if err := apitest.SeedParentChild(dbPath, "child", "leaf"); err != nil {
					t.Fatal(err)
				}
			}
			c := settleClientFor(t, dbPath, 0, tmuxfix.NewRecorder(), time.Now)
			status, row, listed := clientReported(t, c, "parent")
			if status.FamilyState != tc.want || row.FamilyState != tc.want || listed.FamilyState != tc.want {
				t.Fatalf("family status/get/list=%q/%q/%q; want %q", status.FamilyState, row.FamilyState, listed.FamilyState, tc.want)
			}
			if status.State != tc.parent || len(row.PermissionRequests) != 0 || len(listed.PermissionRequests) != 0 || len(row.UnprovenRequests) != 0 {
				t.Fatalf("descendant changed ancestor's own result: %+v %+v", status, row)
			}
			for _, filter := range []api.ListParams{{Cwd: "/family/parent"}, {State: []string{tc.parent}, Limit: 1}, {TmuxSessionName: "ts-parent", Limit: 1}} {
				res, err := c.List(filter)
				if err != nil || len(res.Spawns) != 1 || res.Spawns[0].InstanceID != "parent" || res.Spawns[0].FamilyState != tc.want {
					t.Fatalf("filtered family: %+v %v", res, err)
				}
			}
		})
	}
}

func TestFamilyClientReopenUsesDurableIndependentHistory(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	clock := tmuxfix.NewClock(time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
	if _, err := apitest.SeedSpawn(dbPath, "family-reopen", "working", "", "", "", true, apitest.WithSettle("waiting", clock.Now().UnixMilli()+settleMax)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetFamilyHistory(dbPath, "family-reopen", "working", "waiting", clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	c := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), clock.Now)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Duration(settleMax) * time.Millisecond)
	reopened := settleClientFor(t, dbPath, settleMax, tmuxfix.NewRecorder(), clock.Now)
	status, row, listed := clientReported(t, reopened, "family-reopen")
	if status.State != "waiting" || status.FamilyState != "working" || row.FamilyState != "working" || listed.FamilyState != "working" {
		t.Fatalf("independent reopened reports: %+v %+v %+v", status, row, listed)
	}
}

func TestFamilyCursorStoredMembershipWhileWriterHeld(t *testing.T) {
	// Serial: deadline-sensitive held writer fixture runs outside the parallel wave.
	e, c, b, _ := cursorReadLaunch(t)
	if _, err := apitest.SeedSpawn(e.dbPath, "family-parent", "waiting", "/family-parent", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedParentChild(e.dbPath, "family-parent", b.InstanceID); err != nil {
		t.Fatal(err)
	}
	e.pc.Set(b.GatewayPID, procfix.Gone())
	e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	defer release()
	start := time.Now()
	status, err := c.Status(b.InstanceID)
	if err != nil || status.State != "missing" || status.FamilyState != "working" {
		t.Fatalf("projected missing discarded stored live member: %+v %v", status, err)
	}
	parent, row, listed := clientReported(t, c, "family-parent")
	if parent.State != "waiting" || parent.FamilyState != "working" || row.FamilyState != "working" || listed.FamilyState != "working" {
		t.Fatalf("stored descendant excluded: %+v %+v %+v", parent, row, listed)
	}
	res, err := c.List(api.ListParams{Cwd: "/family-parent", Limit: 1})
	if err != nil || len(res.Spawns) != 1 || res.Spawns[0].FamilyState != "working" {
		t.Fatalf("filtered stored family=%+v %v", res, err)
	}
	if time.Since(start) > time.Second {
		t.Error("family reads waited for writer lock")
	}
	if got := e.columns(t, b.InstanceID); got.State == "missing" {
		t.Fatal("contended projection persisted terminal state")
	}
}
