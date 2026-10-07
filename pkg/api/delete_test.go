package api_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDeleteRows pins the admin delete (Epic 8 AC #3): each id gets its own
// result, a bogus id does not abort the batch, a live row goes like a finished
// one (no state guard), and no ids give an empty non-nil map.
func TestDeleteRows(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		ids  []string
		want map[string]string
	}{
		"live, bogus and ended": {[]string{"row-live", "absent", "row-ended"},
			map[string]string{"row-live": "ok", "absent": "ErrSpawnNotFound", "row-ended": "ok"}},
		"no ids": {[]string{}, map[string]string{}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := apitest.SeedDeleteFixture(t)
			res, err := api.DeleteRows(s, tc.ids)
			if err != nil || res.Results == nil || !reflect.DeepEqual(res.Results, tc.want) {
				t.Fatalf("DeleteRows = %#v, %v; want %v", res.Results, err, tc.want)
			}
			for id, outcome := range tc.want {
				if _, err := s.GetSpawn(id); outcome == "ok" && !errors.Is(err, store.ErrSpawnNotFound) {
					t.Errorf("%s: GetSpawn err = %v; want it deleted", id, err)
				}
			}
			if _, err := s.GetSpawn("absent"); !errors.Is(err, store.ErrSpawnNotFound) {
				t.Errorf("absent id: GetSpawn err = %v; want no row created", err)
			}
		})
	}
}
