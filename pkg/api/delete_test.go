package api_test

import (
	"errors"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/internal/store"
)

func TestDeleteSingleValidIdReturnsOk(t *testing.T) {
	s, _ := apitest.SeedDeleteFixture(t)
	res, err := api.Delete(s, []string{"row-ended"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.Results["row-ended"] != "ok" {
		t.Errorf("Results = %v; want row-ended → ok", res.Results)
	}
	if _, err := s.GetSpawn("row-ended"); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("row-ended still exists after delete: %v", err)
	}
}

func TestDeleteBatchOfValidIdsAllReportOk(t *testing.T) {
	s, _ := apitest.SeedDeleteFixture(t)
	res, err := api.Delete(s, []string{"row-live", "row-ended"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.Results["row-live"] != "ok" || res.Results["row-ended"] != "ok" {
		t.Errorf("Results = %v; want both ok", res.Results)
	}
}

func TestDeleteMixedValidAndBogusReportsPerRow(t *testing.T) {
	// Per Epic 8 AC #3: partial-failure batch returns the per-row
	// map; the batch DOES NOT abort on the bogus id.
	s, _ := apitest.SeedDeleteFixture(t)
	res, err := api.Delete(s, []string{"row-live", "absent", "row-ended"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := map[string]string{
		"row-live":  "ok",
		"absent":    "ErrSpawnNotFound",
		"row-ended": "ok",
	}
	for k, v := range want {
		if got := res.Results[k]; got != v {
			t.Errorf("Results[%q] = %q; want %q", k, got, v)
		}
	}

	// Sanity: the two valid rows really were deleted; the absent id
	// wasn't somehow inserted as a side-effect.
	for _, id := range []string{"row-live", "row-ended"} {
		if _, err := s.GetSpawn(id); !errors.Is(err, store.ErrSpawnNotFound) {
			t.Errorf("%s should be deleted: %v", id, err)
		}
	}
}

func TestDeleteOnLiveRowBypassesGuards(t *testing.T) {
	// Delete is an admin verb — it does NOT consult state. A live row
	// is removed exactly the same way a terminal row is. The orphan
	// tmux session (if any) is left running; the verb makes no claim
	// about it.
	s, _ := apitest.SeedDeleteFixture(t)
	res, err := api.Delete(s, []string{"row-live"})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.Results["row-live"] != "ok" {
		t.Errorf("Results = %v; want ok", res.Results)
	}
}

func TestDeleteEmptyIdSliceReturnsEmptyMap(t *testing.T) {
	// Defense in depth: an empty input slice doesn't crash and
	// returns a non-nil empty map. The CLI rejects this at flag parse
	// (--claude-instance-id is required ≥1), but a future MCP caller
	// could in principle pass [].
	s, _ := apitest.SeedDeleteFixture(t)
	res, err := api.Delete(s, []string{})
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if res.Results == nil {
		t.Fatalf("Results = nil; want empty non-nil map")
	}
	if len(res.Results) != 0 {
		t.Errorf("Results = %v; want empty", res.Results)
	}
}
