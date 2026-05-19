package api_test

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/store"
)

// seedListFixture inserts 6 spawn rows with mixed states/labels/parents/
// cwds so each filter case has both matching and non-matching rows to
// distinguish "filter worked" from "everything happened to match".
//
// Layout (each row's instance id reads as a quick identity):
//
//   row-a-wait-foo:        waiting, labels project=foo+env=dev, cwd /tmp,  no parent
//   row-b-wait-foo-other:  waiting, label  project=foo,         cwd /tmp,  parent=row-a-wait-foo
//   row-c-work-bar:        working, label  project=bar,         cwd /tmp,  no parent
//   row-d-ended-foo:       ended,   label  project=foo,         cwd /opt,  no parent
//   row-e-ask:             ask_user, no labels,                 cwd /tmp,  no parent
//   row-f-wait-no-label:   waiting, no labels,                  cwd /opt,  no parent
func seedListFixture(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	insert := func(id, parent, state, cwd string, labels map[string]string) {
		t.Helper()
		row := store.Spawn{
			ClaudeInstanceID: id,
			ParentID:         parent,
			CWD:              cwd,
			TmuxSessionName:  "cd-" + id,
			RelayMode:        "off",
			Labels:           labels,
		}
		if err := s.InsertPending(row); err != nil {
			t.Fatalf("InsertPending %s: %v", id, err)
		}
		if state != store.StatePending {
			if err := s.ApplyHookTransition(id, state, false); err != nil {
				t.Fatalf("ApplyHookTransition %s: %v", id, err)
			}
		}
	}

	insert("row-a-wait-foo", "", store.StateWaiting, "/tmp",
		map[string]string{"project": "foo", "env": "dev"})
	insert("row-b-wait-foo-other", "row-a-wait-foo", store.StateWaiting, "/tmp",
		map[string]string{"project": "foo"})
	insert("row-c-work-bar", "", store.StateWorking, "/tmp",
		map[string]string{"project": "bar"})
	insert("row-d-ended-foo", "", store.StateEnded, "/opt",
		map[string]string{"project": "foo"})
	insert("row-e-ask", "", store.StateAskUser, "/tmp", nil)
	insert("row-f-wait-no-label", "", store.StateWaiting, "/opt", nil)
	return s
}

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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
	s := seedListFixture(t)
	// `foo` has no `=` → ErrListInvalidLabel; store never invoked.
	_, err := api.List(s, api.ListParams{Labels: []string{"foo"}})
	if !errors.Is(err, api.ErrListInvalidLabel) {
		t.Fatalf("err = %v; want ErrListInvalidLabel", err)
	}
}

func TestListEmptyLabelKeyRejected(t *testing.T) {
	s := seedListFixture(t)
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
	s := seedListFixture(t)
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
