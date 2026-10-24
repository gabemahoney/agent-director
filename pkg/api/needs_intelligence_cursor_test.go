package api_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestCursorMissingPrecedesFilterAndLimit: a Cursor binding whose gateway is
// gone projects missing before list --state and limit apply, so it never takes
// a check_permission row's place; a long fallen-back request reads
// check_permission with no Displayed State (b.zpv).
func TestCursorMissingPrecedesFilterAndLimit(t *testing.T) {
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
	survivor, err := apitest.SeedSpawn(e.dbPath, "", store.StateCheckPermission, "", "on", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, survivor, "Read", facts); err != nil {
		t.Fatal(err)
	}
	assertDisplayState(t, c, survivor, store.StateCheckPermission)
	e.pc.Set(binding.GatewayPID, procfix.Gone())
	// Persisting missing is best effort: under a held write lock neither list
	// can write it, so each sees the binding missing only by its own
	// projection, whatever the row order.
	apitest.HoldWriteLock(t, e.dbPath, time.Minute)
	listed, err := c.List(api.ListParams{State: []string{store.StateCheckPermission}, Limit: 1})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != survivor || listed.Spawns[0].DisplayedState != "" {
		t.Fatalf("missing consumed the check_permission limit: %+v %v", listed, err)
	}
	missing, err := c.List(api.ListParams{State: []string{store.StateMissing}, Limit: 1})
	if err != nil || len(missing.Spawns) != 1 || missing.Spawns[0].InstanceID != binding.InstanceID || len(missing.Spawns[0].PermissionRequests) != 0 {
		t.Fatalf("missing projection=%+v %v", missing, err)
	}
}
