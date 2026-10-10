package api_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestListFilters pins list's filters over apitest.SeedListFixture (SRD §12):
// state is OR within its list, every filter ANDs with the others,
// tmux_session_name is byte-exact and "" no filter, limit caps the rows; a
// query matching nothing gives a non-nil [] .
func TestListFilters(t *testing.T) {
	t.Parallel()
	all := []string{"row-a-wait-foo", "row-b-wait-foo-other", "row-c-work-bar", "row-d-ended-foo", "row-e-ask", "row-f-wait-no-label"}
	cases := []struct {
		name string
		p    api.ListParams
		want []string // sorted ids; nil with n > 0 checks only the count
		n    int
	}{
		{name: "no filters", want: all},
		{name: "one state", p: api.ListParams{State: []string{"waiting"}},
			want: []string{"row-a-wait-foo", "row-b-wait-foo-other", "row-f-wait-no-label"}},
		{name: "states OR together", p: api.ListParams{State: []string{"working", "ended"}},
			want: []string{"row-c-work-bar", "row-d-ended-foo"}},
		{name: "one label", p: api.ListParams{Labels: []string{"project=foo"}},
			want: []string{"row-a-wait-foo", "row-b-wait-foo-other", "row-d-ended-foo"}},
		{name: "labels AND together", p: api.ListParams{Labels: []string{"project=foo", "env=dev"}}, want: []string{"row-a-wait-foo"}},
		{name: "parent", p: api.ListParams{Parent: "row-a-wait-foo"}, want: []string{"row-b-wait-foo-other"}},
		{name: "cwd", p: api.ListParams{Cwd: "/opt"}, want: []string{"row-d-ended-foo", "row-f-wait-no-label"}},
		{name: "limit", p: api.ListParams{Limit: 2}, n: 2},
		{name: "state, label and cwd AND together", p: api.ListParams{State: []string{"waiting"}, Labels: []string{"project=foo"}, Cwd: "/tmp"},
			want: []string{"row-a-wait-foo", "row-b-wait-foo-other"}},
		{name: "no label matches", p: api.ListParams{Labels: []string{"nothing-matches=here"}}, want: []string{}},
		{name: "tmux_session_name narrows", p: api.ListParams{TmuxSessionName: "cd-row-c-work-bar"}, want: []string{"row-c-work-bar"}},
		{name: "tmux_session_name no match", p: api.ListParams{TmuxSessionName: "nonexistent"}, want: []string{}},
		{name: "tmux_session_name AND another state", p: api.ListParams{TmuxSessionName: "cd-row-c-work-bar", State: []string{"waiting"}},
			want: []string{}},
		{name: "tmux_session_name AND its state", p: api.ListParams{TmuxSessionName: "cd-row-c-work-bar", State: []string{"working"}},
			want: []string{"row-c-work-bar"}},
		{name: "empty tmux_session_name is no filter", p: api.ListParams{TmuxSessionName: ""}, want: all},
	}
	s, _ := apitest.SeedListFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := api.List(s, tc.p)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if res.Spawns == nil {
				t.Fatal("Spawns is nil; want a non-nil slice")
			}
			var got []string
			for _, r := range res.Spawns {
				got = append(got, r.ClaudeInstanceID)
			}
			sort.Strings(got)
			if (tc.want != nil && !equalStrings(got, tc.want)) || (tc.want == nil && len(got) != tc.n) {
				t.Errorf("ids = %v; want %v (count %d)", got, tc.want, tc.n)
			}
		})
	}
}

// failingListStore is a ListStore whose every read fails with errSentinel.
type failingListStore struct{}

func (failingListStore) ListSpawns(api.ListFilters) ([]api.Spawn, error) { return nil, errSentinel }

// TestListFailures: a negative limit (b.c4n) and a label not in key=value form
// with a non-empty key are refused before the store read, and every failure
// encodes spawns as [] (b.hbt).
func TestListFailures(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		p    api.ListParams
		want error
	}{
		"label with no separator": {api.ListParams{Labels: []string{"foo"}}, api.ErrListInvalidLabel},
		"label with an empty key": {api.ListParams{Labels: []string{"=value"}}, api.ErrListInvalidLabel},
		"negative limit":          {api.ListParams{Limit: -1}, api.ErrInvalidFlags},
		"store read fails":        {api.ListParams{Labels: []string{"k=v"}}, errSentinel},
	} {
		res, err := api.List(failingListStore{}, tc.p)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v; want %v", name, err, tc.want)
		}
		if got := jsonOf(t, res); got != `{"spawns":[]}` {
			t.Errorf("%s: List = %s; want {\"spawns\":[]}", name, got)
		}
	}
}

// TestListRowKeySetUnchanged pins the list row's exact JSON key set with every
// optional field present: jsonl_path and extra_env, seeded on the row, are dropped.
func TestListRowKeySetUnchanged(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	seedWaiting(t, dbPath, "row-parent")
	if _, err := apitest.SeedSpawn(dbPath, "row-max", store.StateEnded, "/tmp", "on", "", false,
		apitest.WithJsonlPath("/home/user/.claude/projects/-tmp/s.jsonl"),
		apitest.WithExtraEnv(map[string]string{"SECRET": "x"}),
		apitest.WithLivenessUnverifiedSince("2026-09-19T12:34:56Z"),
		apitest.WithLivenessNote("probe wall"),
	); err != nil {
		t.Fatalf("SeedSpawn(row-max): %v", err)
	}
	if err := apitest.SeedParentChild(dbPath, "row-parent", "row-max"); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	res, err := api.List(openDB(t, dbPath), api.ListParams{TmuxSessionName: "ts-row-max"})
	if err != nil || len(res.Spawns) != 1 {
		t.Fatalf("List = %+v, %v; want row-max alone", res.Spawns, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonOf(t, res.Spawns[0])), &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"claude_instance_id", "cwd", "ended_at", "labels", "last_seen_at", "liveness_note",
		"liveness_unverified_since", "parent_id", "relay_mode", "started_at", "state", "tmux_session_name"}
	if !equalStrings(got, want) {
		t.Errorf("ListRow keys = %v; want exactly %v (no jsonl_path, no extra_env, nothing new)", got, want)
	}
}

// equalStrings is a small helper so the test diffs are direct instead
// of reflect.DeepEqual's multi-line dump.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
