package api_test

// list_subagents_test.go covers api.ListSubagents over a real store (SR-11.1 to
// 11.4, 6.1, 6.2): the tracked flag, records only, every launch of the current life, the
// order, the JSON shape with explicit nulls, the time encoding, the failure
// results, and that a read changes nothing. The store read is tested here only.

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The signatures SR-11.1 names, pinned at compile time: the field types of
// SubagentRecord (a nullable field is a pointer) and every function and method.
var (
	_ func(api.ListSubagentsStore, string) (api.ListSubagentsResult, error) = api.ListSubagents
	_ func(*api.Client, string) (api.ListSubagentsResult, error)            = (*api.Client).ListSubagents
	_ interface {
		ListSubagentRecords(string) (bool, []api.SubagentRow, error)
	} = api.ListSubagentsStore(nil)
	_ = api.ListSubagentsResult{Tracked: false, Subagents: []api.SubagentRecord(nil)}
	_ = api.SubagentRecord{
		SubagentID: "", InstanceID: "", State: "",
		SessionID: (*string)(nil), SubagentType: (*string)(nil), Description: (*string)(nil),
		Model: (*string)(nil), ParentType: (*string)(nil), ParentID: (*string)(nil),
		ToolUseID: (*string)(nil), RunMode: (*string)(nil), CloseReason: (*string)(nil),
		FirstStartedAt: (*time.Time)(nil), StartedAt: (*time.Time)(nil),
		StoppedAt: (*time.Time)(nil), LostAt: (*time.Time)(nil),
	}
)

// emptySubagentsJSON is what every failure and an untracked row encode as
// (with tracked true for a tracked row without records).
const emptySubagentsJSON = `{"tracked":false,"subagents":[]}`

// subagentKeys are the SR-6.1 keys of every record, and no others.
var subagentKeys = []string{"subagent_id", "instance_id", "session_id", "subagent_type", "description",
	"model", "parent_type", "parent_id", "tool_use_id", "run_mode", "first_started_at", "started_at",
	"stopped_at", "lost_at", "state", "close_reason"}

// newSubagentStore seeds a waiting row id (tracked unless opts say otherwise)
// into a fresh store and returns its path and the open store.
func newSubagentStore(t *testing.T, id string, opts ...apitest.SpawnOption) (string, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seedWaiting(t, dbPath, id, opts...)
	return dbPath, openDB(t, dbPath)
}

// seedSub seeds one subagents entry of id.
func seedSub(t *testing.T, dbPath, id, subID string, opts ...apitest.SubagentOption) {
	t.Helper()
	if err := apitest.SeedSubagent(dbPath, id, subID, opts...); err != nil {
		t.Fatalf("SeedSubagent(%s, %s): %v", id, subID, err)
	}
}

// listSubs reads id's subagents through api.ListSubagents, failing on an error.
func listSubs(t *testing.T, s api.ListSubagentsStore, id string) api.ListSubagentsResult {
	t.Helper()
	res, err := api.ListSubagents(s, id)
	if err != nil {
		t.Fatalf("ListSubagents(%s): %v", id, err)
	}
	return res
}

// subIDs is the subagent ids of res, in order.
func subIDs(res api.ListSubagentsResult) []string {
	ids := []string{}
	for _, r := range res.Subagents {
		ids = append(ids, r.SubagentID)
	}
	return ids
}

// recordJSON is the i-th record of res as its JSON object, key to raw value.
func recordJSON(t *testing.T, res api.ListSubagentsResult, i int) map[string]string {
	t.Helper()
	if i >= len(res.Subagents) {
		t.Fatalf("result has %d records; want one at index %d", len(res.Subagents), i)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOf(t, res.Subagents[i])), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	out := map[string]string{}
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

// assertRecordJSON checks the record's JSON object is exactly want: the SR-6.1 keys
// and no others, each with its value.
func assertRecordJSON(t *testing.T, got, want map[string]string) {
	t.Helper()
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	wantKeys := slices.Clone(subagentKeys)
	slices.Sort(keys)
	slices.Sort(wantKeys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("record keys = %v; want exactly %v", keys, wantKeys)
	}
	for _, k := range subagentKeys {
		if got[k] != want[k] {
			t.Errorf("%s = %s; want %s", k, got[k], want[k])
		}
	}
}

// TestListSubagentsTracked: tracked is the row's flag, with no records either
// way, and an untracked row (a codex one included) reads exactly {"tracked":false,"subagents":[]}.
func TestListSubagentsTracked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		opts []apitest.SpawnOption
		want string
	}{
		{"a row launched since", nil, `{"tracked":true,"subagents":[]}`},
		{"a row from before subagent tracking", []apitest.SpawnOption{apitest.WithNoSubagentTracking()}, emptySubagentsJSON},
		{"a codex row, launched with no subagent events registered", codexRowOpts(), emptySubagentsJSON},
		{"a cursor row never tracks subagents", []apitest.SpawnOption{apitest.WithAgent("cursor")}, emptySubagentsJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := newSubagentStore(t, "tracked-row", tc.opts...)
			if got := jsonOf(t, listSubs(t, s, "tracked-row")); got != tc.want {
				t.Errorf("ListSubagents = %s; want %s", got, tc.want)
			}
		})
	}
}

// TestListSubagentsRecordsOnly: current-life records across launches come back,
// each subagent_id once, in any state; earlier lives, observations, claimable
// launches and other rows' records never do.
func TestListSubagentsRecordsOnly(t *testing.T) {
	t.Parallel()
	dbPath, s := newSubagentStore(t, "subs-main", apitest.WithLifeNumber(4))
	seedWaiting(t, dbPath, "subs-other")
	const earlier = "earlier-launch-token"
	lost := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateLost), apitest.WithSubagentCloseReason(store.SubagentCloseAgentGone)}
	stopped := []apitest.SubagentOption{apitest.WithSubagentState(store.SubagentStateStopped), apitest.WithSubagentCloseReason(store.SubagentCloseStopEvent)}
	seedSub(t, dbPath, "subs-main", "run", apitest.WithSubagentLife(3), apitest.WithSubagentDescription("hidden same-id"), apitest.WithSubagentState("lost"))
	seedSub(t, dbPath, "subs-main", "hidden-only", apitest.WithSubagentLife(3))
	seedSub(t, dbPath, "subs-main", "run")
	seedSub(t, dbPath, "subs-main", "stop", stopped...)
	seedSub(t, dbPath, "subs-main", "lost", lost...)
	seedSub(t, dbPath, "subs-main", "earlier-launch", append([]apitest.SubagentOption{apitest.WithSubagentLaunchToken(earlier)}, lost...)...)
	seedSub(t, dbPath, "subs-main", "observation", apitest.WithSubagentConfirmed(false))
	seedSub(t, dbPath, "subs-main", "earlier-observation", apitest.WithSubagentConfirmed(false), apitest.WithSubagentLaunchToken(earlier))
	if err := apitest.SeedSubagentLaunch(dbPath, "subs-main", "toolu_claimable"); err != nil {
		t.Fatalf("SeedSubagentLaunch: %v", err)
	}
	seedSub(t, dbPath, "subs-other", "run") // the same subagent_id on another row
	seedSub(t, dbPath, "subs-other", "other-only")

	res := listSubs(t, s, "subs-main")
	for _, r := range res.Subagents {
		if r.SubagentID == "run" && (r.Description != nil || r.State != "running") {
			t.Errorf("same id did not select current entry: %+v", r)
		}
	}
	if want := []string{"earlier-launch", "lost", "run", "stop"}; !slices.Equal(subIDs(res), want) {
		t.Errorf("subs-main records = %v; want %v", subIDs(res), want)
	}
	for _, r := range res.Subagents {
		if r.InstanceID != "subs-main" {
			t.Errorf("record %s has instance_id %q; want subs-main", r.SubagentID, r.InstanceID)
		}
	}
	states := map[string]string{}
	for _, r := range res.Subagents {
		states[r.SubagentID] = r.State
	}
	if want := map[string]string{"run": "running", "stop": "stopped", "lost": "lost", "earlier-launch": "lost"}; !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v; want %v", states, want)
	}
	if other := listSubs(t, s, "subs-other"); !slices.Equal(subIDs(other), []string{"other-only", "run"}) {
		t.Errorf("subs-other records = %v; want [other-only run]", subIDs(other))
	}
}

// ms returns a pointer to a Unix-millisecond time, for a table's optional first start.
func ms(v int64) *int64 { return &v }

// TestListSubagentsOrder: records come back by first_started_at ascending with
// unknown ones last, equal times and unknown ones tied by subagent_id, whatever
// order they were stored in.
func TestListSubagentsOrder(t *testing.T) {
	t.Parallel()
	type sub struct {
		id    string
		first *int64
	}
	for _, tc := range []struct {
		name string
		subs []sub // in the order stored
		want []string
	}{
		{"distinct times", []sub{{"x", ms(300)}, {"z", ms(100)}, {"y", ms(200)}}, []string{"z", "y", "x"}},
		{"equal times tie by id", []sub{{"b", ms(100)}, {"c", ms(100)}, {"a", ms(100)}}, []string{"a", "b", "c"}},
		{"several unknown last, tied by id", []sub{{"nullb", nil}, {"late", ms(5000)}, {"nulla", nil}, {"early", ms(100)}}, []string{"early", "late", "nulla", "nullb"}},
		{"times then ids within a time", []sub{{"z", ms(100)}, {"a", ms(200)}, {"m", ms(100)}}, []string{"m", "z", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath, s := newSubagentStore(t, "order-row")
			for _, sb := range tc.subs {
				var opts []apitest.SubagentOption
				if sb.first != nil {
					opts = append(opts, apitest.WithSubagentFirstStartedAt(*sb.first))
				}
				seedSub(t, dbPath, "order-row", sb.id, opts...)
			}
			if got := subIDs(listSubs(t, s, "order-row")); !slices.Equal(got, tc.want) {
				t.Errorf("order = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestListSubagentsUnknownFieldsAreNull: a record with every nullable field
// unknown has all 16 SR-6.1 keys, the unknown ones explicit null, none omitted.
func TestListSubagentsUnknownFieldsAreNull(t *testing.T) {
	t.Parallel()
	dbPath, s := newSubagentStore(t, "null-row")
	seedSub(t, dbPath, "null-row", "bare")
	res := listSubs(t, s, "null-row")
	want := map[string]string{"subagent_id": `"bare"`, "instance_id": `"null-row"`, "state": `"running"`}
	for _, k := range subagentKeys {
		if _, ok := want[k]; !ok {
			want[k] = "null"
		}
	}
	assertRecordJSON(t, recordJSON(t, res, 0), want)
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOf(t, res)), &top); err != nil || len(top) != 2 || top["tracked"] == nil || top["subagents"] == nil {
		t.Errorf("result keys = %v, %v; want exactly tracked and subagents", top, err)
	}
}

// TestListSubagentsFullRecord: a record with every field set round-trips, for each
// value of state, close_reason, parent_type and run_mode; the unset reason of a running record is null.
func TestListSubagentsFullRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, state, closeReason, parentType, runMode string
	}{
		{"running agent child in the background", "running", "", "agent", "background"},
		{"stopped subagent child in the foreground", "stopped", "stop_event", "subagent", "foreground"},
		{"lost, absent from snapshot", "lost", "absent_from_snapshot", "agent", "background"},
		{"lost, agent gone", "lost", "agent_gone", "subagent", "foreground"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath, s := newSubagentStore(t, "full-row")
			const desc = `survey "the" tree: café`
			opts := []apitest.SubagentOption{
				apitest.WithSubagentState(tc.state), apitest.WithSubagentSessionID("sess-1"),
				apitest.WithSubagentType("general-purpose"), apitest.WithSubagentDescription(desc),
				apitest.WithSubagentModel("model-x"), apitest.WithSubagentParent(tc.parentType, "parent-1"),
				apitest.WithSubagentToolUseID("toolu_1"), apitest.WithSubagentRunMode(tc.runMode),
				apitest.WithSubagentFirstStartedAt(launchFracMillis), apitest.WithSubagentStartedAt(launchFracMillis),
			}
			want := map[string]string{
				"subagent_id": `"sub-1"`, "instance_id": `"full-row"`, "session_id": `"sess-1"`,
				"subagent_type": `"general-purpose"`, "description": jsonOf(t, desc), "model": `"model-x"`,
				"parent_type": jsonOf(t, tc.parentType), "parent_id": `"parent-1"`, "tool_use_id": `"toolu_1"`,
				"run_mode": jsonOf(t, tc.runMode), "first_started_at": `"` + launchFracJSON + `"`,
				"started_at": `"` + launchFracJSON + `"`, "stopped_at": "null", "lost_at": "null",
				"state": jsonOf(t, tc.state), "close_reason": "null",
			}
			if tc.closeReason != "" {
				opts = append(opts, apitest.WithSubagentCloseReason(tc.closeReason))
				want["close_reason"] = jsonOf(t, tc.closeReason)
			}
			switch tc.state {
			case "stopped":
				opts = append(opts, apitest.WithSubagentStoppedAt(launchFracMillis))
				want["stopped_at"] = `"` + launchFracJSON + `"`
			case "lost":
				opts = append(opts, apitest.WithSubagentLostAt(launchFracMillis))
				want["lost_at"] = `"` + launchFracJSON + `"`
			}
			seedSub(t, dbPath, "full-row", "sub-1", opts...)
			res := listSubs(t, s, "full-row")
			assertRecordJSON(t, recordJSON(t, res, 0), want)
			if r := res.Subagents[0]; r.State != tc.state || r.ParentType == nil || *r.ParentType != tc.parentType ||
				r.Description == nil || *r.Description != desc {
				t.Errorf("typed record = %+v; want state %s, parent_type %s, description %q", r, tc.state, tc.parentType, desc)
			}
		})
	}
}

// TestListSubagentsTimeEncoding: each of the four times encodes as launch_started_at's
// string (the launch_started_at_test.go pairs) and ends in Z, and its typed field is
// that instant in UTC.
func TestListSubagentsTimeEncoding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		millis int64
		want   string
	}{
		{"millisecond fraction", launchFracMillis, launchFracJSON},
		{"fraction ending in zero", launchTrailZeroMillis, launchTrailZeroJSON},
		{"whole second", launchWholeMillis, launchWholeJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath, s := newSubagentStore(t, "time-row")
			seedSub(t, dbPath, "time-row", "timed", apitest.WithSubagentFirstStartedAt(tc.millis), apitest.WithSubagentStartedAt(tc.millis),
				apitest.WithSubagentStoppedAt(tc.millis), apitest.WithSubagentLostAt(tc.millis))
			res := listSubs(t, s, "time-row")
			rec := res.Subagents[0]
			js := recordJSON(t, res, 0)
			typed := map[string]*time.Time{"first_started_at": rec.FirstStartedAt, "started_at": rec.StartedAt, "stopped_at": rec.StoppedAt, "lost_at": rec.LostAt}
			for key, got := range typed {
				if js[key] != `"`+tc.want+`"` || !strings.HasSuffix(tc.want, "Z") {
					t.Errorf("%s = %s; want %q", key, js[key], tc.want)
				}
				if got == nil || got.Location() != time.UTC || !got.Equal(time.UnixMilli(tc.millis)) {
					t.Errorf("typed %s = %v; want %v in UTC", key, got, time.UnixMilli(tc.millis).UTC())
				}
			}
		})
	}
}

// failingSubagentStore returns err, with a tracked flag and a record that must be discarded.
type failingSubagentStore struct{ err error }

func (f failingSubagentStore) ListSubagentRecords(string) (bool, []api.SubagentRow, error) {
	return true, []api.SubagentRow{{SubagentID: "discarded"}}, f.err
}

// TestListSubagentsFailures: an unknown id matches ErrSpawnNotFound and a failing
// store returns its error; each gives tracked false and a non-nil empty slice (encoding []).
func TestListSubagentsFailures(t *testing.T) {
	t.Parallel()
	_, realStore := newSubagentStore(t, "known-row")
	for _, tc := range []struct {
		name string
		st   api.ListSubagentsStore
		want error
	}{
		{"unknown id", realStore, store.ErrSpawnNotFound},
		{"failing store", failingSubagentStore{err: errInjectedStore}, errInjectedStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, err := api.ListSubagents(tc.st, "absent-row")
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v; want %v", err, tc.want)
			}
			if res.Tracked || res.Subagents == nil || len(res.Subagents) != 0 || jsonOf(t, res) != emptySubagentsJSON {
				t.Errorf("result = %#v (%s); want tracked false and a non-nil empty slice, %s", res, jsonOf(t, res), emptySubagentsJSON)
			}
		})
	}
}

// TestListSubagentsReadsOnly: a read changes no spawns column (row_version included),
// no subagent or claimable-launch row and writes no trail record, and it succeeds
// while another connection holds the store's write lock.
func TestListSubagentsReadsOnly(t *testing.T) {
	t.Parallel()
	const id = "readonly-row"
	dbPath, s := newSubagentStore(t, id, apitest.WithLifeNumber(4))
	seedSub(t, dbPath, id, "run", apitest.WithSubagentLife(3), apitest.WithSubagentDescription("hidden"))
	seedSub(t, dbPath, id, "run", apitest.WithSubagentFirstStartedAt(launchFracMillis))
	seedSub(t, dbPath, id, "obs", apitest.WithSubagentConfirmed(false))
	if err := apitest.SeedSubagentLaunch(dbPath, id, "toolu_claimable"); err != nil {
		t.Fatalf("SeedSubagentLaunch: %v", err)
	}
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	rows, err := apitest.ReadSubagentRows(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSubagentRows: %v", err)
	}
	mark := trailMark(t)

	release := apitest.HoldWriteLock(t, dbPath, time.Minute)
	res := listSubs(t, s, id)
	release()
	if want := []string{"run"}; !slices.Equal(subIDs(res), want) {
		t.Errorf("records read under the write lock = %v; want %v", subIDs(res), want)
	}

	if after, err := apitest.ReadSpawnColumns(dbPath, id); err != nil || !reflect.DeepEqual(after, cols) {
		t.Errorf("spawns row after the read = %+v, %v; want it unchanged: %+v", after, err, cols)
	}
	if after, err := apitest.ReadSubagentRows(dbPath, id); err != nil || !reflect.DeepEqual(after, rows) {
		t.Errorf("subagent rows after the read = %+v, %v; want them unchanged: %+v", after, err, rows)
	}
	assertNoTrailSince(t, mark, id)
}
