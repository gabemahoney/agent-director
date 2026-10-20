package api_test

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestPermissionStuckCursorMissingPrecedesFilterAndLimit(t *testing.T) {
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
	watchStuckClient(c, e.st)
	assertStuckState(t, c, binding.InstanceID, "stuck")
	survivor, err := apitest.SeedSpawn(e.dbPath, "", store.StateCheckPermission, "", "on", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apitest.SeedPermissionRequest(e.dbPath, survivor, "Read", facts); err != nil {
		t.Fatal(err)
	}
	e.pc.Set(binding.GatewayPID, procfix.Gone())
	listed, err := c.List(api.ListParams{State: []string{"stuck"}, Limit: 1})
	if err != nil || len(listed.Spawns) != 1 || listed.Spawns[0].InstanceID != survivor {
		t.Fatalf("missing consumed stuck limit: %+v %v", listed, err)
	}
	// The row the limit skipped is persisted missing, whatever the row order.
	if row, err := e.st.GetSpawn(binding.InstanceID); err != nil || row.State != store.StateMissing {
		t.Fatalf("binding row not persisted missing: %+v %v", row, err)
	}
	missing, err := c.List(api.ListParams{State: []string{store.StateMissing}, Limit: 1})
	if err != nil || len(missing.Spawns) != 1 || missing.Spawns[0].InstanceID != binding.InstanceID || missing.Spawns[0].StuckReason != "" || len(missing.Spawns[0].PermissionRequests) != 0 {
		t.Fatalf("missing projection=%+v %v", missing, err)
	}
}
