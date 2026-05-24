package api_test

import (
	"errors"
	"sort"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// idsOf is a small projection so test diffs read as a sorted id slice
// rather than a wall of Spawn structs. Sort is applied because the
// SRD §12 contract leaves order unspecified — tests would be flaky
// otherwise.
func idsOf(rows []api.ListRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ClaudeInstanceID)
	}
	sort.Strings(out)
	return out
}

func TestListNoFiltersReturnsAllRows(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"row-a-wait-foo", "row-b-wait-foo-other", "row-c-work-bar",
		"row-d-ended-foo", "row-e-ask", "row-f-wait-no-label",
	}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListSingleStateFilter(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{State: []string{"waiting"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-a-wait-foo", "row-b-wait-foo-other", "row-f-wait-no-label"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListMultiStateFilter(t *testing.T) {
	// state filter is OR within the slice, AND with other filters
	// (none used here).
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{State: []string{"working", "ended"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-c-work-bar", "row-d-ended-foo"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListSingleLabelFilter(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{Labels: []string{"project=foo"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-a-wait-foo", "row-b-wait-foo-other", "row-d-ended-foo"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListMultipleLabelsAndTogether(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{
		Labels: []string{"project=foo", "env=dev"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Only row-a has both labels.
	want := []string{"row-a-wait-foo"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListParentFilter(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{Parent: "row-a-wait-foo"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-b-wait-foo-other"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListCwdFilter(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{Cwd: "/opt"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-d-ended-foo", "row-f-wait-no-label"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListLimitCapsResults(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Spawns) != 2 {
		t.Errorf("len(spawns) = %d; want 2", len(res.Spawns))
	}
}

func TestListCombinedFiltersAndTogether(t *testing.T) {
	// state=waiting AND label project=foo AND cwd=/tmp.
	// Matches row-a (project=foo, waiting, /tmp) and row-b (project=foo,
	// waiting, /tmp). Excludes row-c (working), row-d (/opt + ended),
	// row-e (no label), row-f (no label).
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{
		State:  []string{"waiting"},
		Labels: []string{"project=foo"},
		Cwd:    "/tmp",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-a-wait-foo", "row-b-wait-foo-other"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListInvalidLabelSyntaxRejected(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	// `foo` has no `=` → ErrListInvalidLabel; store never invoked.
	_, err := api.List(s, api.ListParams{Labels: []string{"foo"}})
	if !errors.Is(err, api.ErrListInvalidLabel) {
		t.Fatalf("err = %v; want ErrListInvalidLabel", err)
	}
}

func TestListEmptyLabelKeyRejected(t *testing.T) {
	s, _ := apitest.SeedListFixture(t)
	// `=value` has an empty key — would coerce json_extract into a
	// degenerate path. Reject at the verb seam.
	_, err := api.List(s, api.ListParams{Labels: []string{"=value"}})
	if !errors.Is(err, api.ErrListInvalidLabel) {
		t.Fatalf("err = %v; want ErrListInvalidLabel", err)
	}
}

func TestListResultSliceNeverNil(t *testing.T) {
	// JSON-stability invariant: even a no-match query encodes as
	// `{"spawns": []}`, never `{"spawns": null}`. Callers that walk
	// the slice without nil checks (jq, the MCP client) depend on it.
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{Labels: []string{"nothing-matches=here"}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Spawns == nil {
		t.Fatalf("Spawns is nil; want empty non-nil slice")
	}
	if len(res.Spawns) != 0 {
		t.Fatalf("len(spawns) = %d; want 0", len(res.Spawns))
	}
}

func TestListTmuxSessionNameFilterNarrows(t *testing.T) {
	// Fixture rows all carry tmux_session_name = "cd-" + id. The filter
	// is byte-exact, so picking one of those names returns just that row.
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{TmuxSessionName: "cd-row-c-work-bar"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-c-work-bar"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListTmuxSessionNameFilterNoMatchEmpty(t *testing.T) {
	// A non-matching name returns an empty (non-nil) slice — same
	// JSON-stability invariant as TestListResultSliceNeverNil.
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{TmuxSessionName: "nonexistent"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Spawns == nil {
		t.Fatalf("Spawns is nil; want empty non-nil slice")
	}
	if len(res.Spawns) != 0 {
		t.Errorf("len(spawns) = %d; want 0", len(res.Spawns))
	}
}

func TestListTmuxSessionNameAndCombinesWithState(t *testing.T) {
	// AND-combine: tmux name pinpoints one row; pairing it with a state
	// that does NOT match that row must return zero rows. (Confirms the
	// filter is AND'd, not OR'd.)
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{
		TmuxSessionName: "cd-row-c-work-bar",
		State:           []string{"waiting"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Spawns) != 0 {
		t.Errorf("len(spawns) = %d; want 0 (state filter excludes working row)", len(res.Spawns))
	}
	// And pairing with the matching state returns exactly the row.
	res, err = api.List(s, api.ListParams{
		TmuxSessionName: "cd-row-c-work-bar",
		State:           []string{"working"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"row-c-work-bar"}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestListTmuxSessionNameEmptyIsPermissive(t *testing.T) {
	// Explicit empty string in ListParams must not emit a SQL clause —
	// the result must equal the no-filter result.
	s, _ := apitest.SeedListFixture(t)
	res, err := api.List(s, api.ListParams{TmuxSessionName: ""})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		"row-a-wait-foo", "row-b-wait-foo-other", "row-c-work-bar",
		"row-d-ended-foo", "row-e-ask", "row-f-wait-no-label",
	}
	if got := idsOf(res.Spawns); !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
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
