package store_test

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorMissingProjectionCapacity(t *testing.T) {
	f, _, binding := cursorRowBinding(t)
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	proofs := make([]store.CursorMissingProof, 0, 1100)
	for i := 0; i < 1100; i++ {
		id, err := apitest.SeedSpawn(f.path, fmt.Sprintf("capacity-%04d", i), store.StatePending, "", "off", "", false,
			apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: binding.Token, PanePID: binding.GatewayPID, PaneStarttime: binding.GatewayStart}))
		if err != nil {
			t.Fatal(err)
		}
		row, err := f.s.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		b := binding
		b.InstanceID, b.Life = id, row.LifeNumber
		// Mix registered runtimes with examined absence in the same SQL snapshot.
		if i%2 == 0 {
			if ok, err := f.s.RegisterCursorRuntime(row, b); err != nil || !ok {
				t.Fatalf("register %s: %v %v", id, ok, err)
			}
		} else {
			b = store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime}
		}
		if _, err := db.Exec("UPDATE spawns SET state='working' WHERE instance_id=?", id); err != nil {
			t.Fatal(err)
		}
		row, err = f.s.GetSpawn(id)
		if err != nil {
			t.Fatal(err)
		}
		proofs = append(proofs, store.CursorMissingProof{Row: row, Binding: b})
	}
	for _, n := range []int{365, 1100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			projected := f.s.WithCursorMissingReads(proofs[:n])
			rows, err := projected.ListSpawns(store.ListFilters{State: []string{store.StateMissing}, Limit: 1, Settle: store.SettleRead{SettleMs: 60000, NowMs: 1}})
			if err != nil || len(rows) != 1 {
				t.Fatalf("settled missing before LIMIT over %d proofs: rows=%d err=%v", n, len(rows), err)
			}
			if rows[0].State != store.StateMissing || rows[0].ReportedState != store.StateMissing {
				t.Fatalf("projected row=%+v", rows[0])
			}
			durable, err := f.s.GetSpawn(rows[0].InstanceID)
			if err != nil || durable.State != store.StateWorking {
				t.Fatalf("projection mutated durable row: %+v %v", durable, err)
			}
		})
	}
}
