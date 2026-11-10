package api_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Stored membership precedes recovery (b.x9o SR-3.8): a selected Cursor
// projects missing without refilling its slot. A deferred save cannot add it
// to a later stored missing filter. Long-fallen-back requests keep their
// check_permission report and Cursor has no idle_screen display (b.zpv).
func TestCursorMissingFollowsStoredFilterAndLimit(t *testing.T) {
	t.Parallel()
	e, _, binding, token := cursorReadLaunch(t)
	e.pc.Set(binding.GatewayPID, procfix.Alive(binding.GatewayStart))
	e.pc.Set(binding.RuntimePID, procfix.Alive(binding.RuntimeStart))
	facts := apitest.PermissionTimes(e.clock.Now().Add(-time.Hour), time.Time{}, e.clock.Now().Add(-time.Minute), time.Time{}, time.Time{}, "")
	pr, err := e.st.GetPermissionRequest(binding.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetPermissionFacts(e.dbPath, pr.RequestID, facts); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SetSettle(e.dbPath, binding.InstanceID, store.StateCheckPermission, 0); err != nil {
		t.Fatal(err)
	}
	c, _ := e.client(t)
	watchDisplayClient(c, e.st)
	assertDisplayState(t, c, binding.InstanceID, store.StateCheckPermission)
	survivor, err := apitest.SeedSpawn(e.dbPath, "zz-cursor-filter-survivor", store.StateCheckPermission, "", "on", "", false,
		apitest.WithNoPane(), apitest.WithPID(binding.RuntimePID), apitest.WithProcStarttime(binding.RuntimeStart))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, survivor, "Read", facts); err != nil {
		t.Fatal(err)
	}
	assertDisplayState(t, c, survivor, store.StateCheckPermission)
	selected, err := e.st.ListSpawns(store.ListFilters{State: []string{store.StateCheckPermission}, Limit: 1, StoredDisplay: true})
	if err != nil || len(selected) != 1 || selected[0].InstanceID != binding.InstanceID {
		t.Fatalf("fixture must select the Cursor before recovery: %+v %v", selected, err)
	}
	before, survivorBefore := e.columns(t, binding.InstanceID), e.columns(t, survivor)
	requestBefore, err := e.st.GetPermissionRequest(binding.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	e.pc.Set(binding.GatewayPID, procfix.Gone())
	// Persisting missing is best effort: under a held write lock neither list
	// can write it, so each sees the binding missing only by its own
	// projection, whatever the row order.
	apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	listed, err := c.List(api.ListParams{State: []string{store.StateCheckPermission}, Limit: 1})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != binding.InstanceID || listed.Spawns[0].State != store.StateMissing ||
		listed.Spawns[0].DisplayedState != "" || listed.Spawns[0].IdleScreenAvailability != api.IdleScreenUnavailable || len(listed.Spawns[0].PermissionRequests) != 0 {
		t.Fatalf("selected Cursor slot was replaced or not projected: %+v %v", listed, err)
	}
	missing, err := c.List(api.ListParams{State: []string{store.StateMissing}, Limit: 1})
	if err != nil || len(missing.Spawns) != 0 {
		t.Fatalf("deferred missing was added to stored selection: %+v %v", missing, err)
	}
	requestAfter, err := e.st.GetPermissionRequest(binding.InstanceID, token)
	if err != nil || requestAfter != requestBefore || !reflect.DeepEqual(before, e.columns(t, binding.InstanceID)) || !reflect.DeepEqual(survivorBefore, e.columns(t, survivor)) {
		t.Fatalf("deferred projection changed row/request facts: before=%+v after=%+v err=%v", requestBefore, requestAfter, err)
	}
}
