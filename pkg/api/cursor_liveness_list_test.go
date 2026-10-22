package api_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorListReconcilesGatewayBeforeProjectionAndFilters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		gateway    procfix.Process
		permission bool
		state      []string
		limit      int
		missing    bool
		deferSave  bool
	}{
		{name: "gone working", gateway: procfix.Gone(), missing: true},
		{name: "recycled permission", gateway: procfix.Alive(apitest.DarwinProcStarttime), permission: true, missing: true},
		{name: "missing filter includes newly missing", gateway: procfix.Gone(), state: []string{store.StateMissing}, missing: true},
		{name: "working limit excludes newly missing", gateway: procfix.Gone(), state: []string{store.StateWorking}, limit: 1, missing: true},
		// b.6uf: the reported subtest, with the save forced to defer on both List calls.
		{name: "working limit excludes newly missing save deferred", gateway: procfix.Gone(), state: []string{store.StateWorking}, limit: 1, missing: true, deferSave: true},
		{name: "alive unchanged", gateway: procfix.Alive(apitest.LinuxProcStarttime)},
		{name: "unknown unchanged", gateway: procfix.Unreadable()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			e.pc.Set(b.GatewayPID, tc.gateway)
			e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
			if tc.permission {
				if err := apitest.SetSettle(e.dbPath, b.InstanceID, store.StateCheckPermission, e.clock.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
			cursorReadSQL(t, e.dbPath, "UPDATE spawns SET labels=? WHERE instance_id=?", `{"list-liveness":"target"}`, b.InstanceID)
			// The in-scope survivor establishes the limit after state reconciliation.
			survivor, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "", "", false)
			if err != nil {
				t.Fatal(err)
			}
			cursorReadSQL(t, e.dbPath, "UPDATE spawns SET labels=? WHERE instance_id=?", `{"list-liveness":"target"}`, survivor)
			sibling, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "", "", false, apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: "list-alive", PanePID: b.GatewayPID + 20, PaneStarttime: b.GatewayStart}))
			if err != nil {
				t.Fatal(err)
			}
			e.pc.Set(b.GatewayPID+20, procfix.Alive(b.GatewayStart))
			before, siblingBefore, survivorBefore := e.columns(t, b.InstanceID), e.columns(t, sibling), e.columns(t, survivor)
			prBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if tc.deferSave {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailMissingDeadlineOverrun, b.InstanceID)
			}
			// Each List call's deferred missing-row saves, as "id: error", and how
			// many were the target's on a deadline (context.DeadlineExceeded: its
			// 1 s ran out, or its earlier abandoned save still held the store): the
			// only deferral a fixture with no competing writer allows (b.6uf).
			var deferred [2][]string
			var ownDeadline [2]int
			params := api.ListParams{Labels: []string{"list-liveness=target"}, State: tc.state, Limit: tc.limit}
			for i := 0; i < 2; i++ {
				api.SetCursorMissingDeferredForTest(c, func(id string, err error) {
					deferred[i] = append(deferred[i], id+": "+err.Error())
					if id == b.InstanceID && errors.Is(err, context.DeadlineExceeded) {
						ownDeadline[i]++
					}
				})
				got, err := c.List(params)
				if err != nil {
					t.Fatal(err)
				}
				want := map[string]string{survivor: store.StateWorking}
				targetState := store.StateWorking
				if tc.permission {
					targetState = store.StateCheckPermission
				}
				if tc.missing {
					targetState = store.StateMissing
				}
				if len(tc.state) == 0 || tc.state[0] == targetState {
					want[b.InstanceID] = targetState
				}
				if len(tc.state) > 0 && tc.state[0] != store.StateWorking {
					delete(want, survivor)
				}
				states := make(map[string]string)
				for _, row := range got.Spawns {
					states[row.InstanceID] = row.State
					if row.State == store.StateMissing && len(row.PermissionRequests) != 0 {
						t.Error("missing row exposes open permission requests")
					}
				}
				if !reflect.DeepEqual(states, want) || len(got.Spawns) != len(want) {
					t.Errorf("List states = %v (%d rows), want %v", states, len(got.Spawns), want)
				}
			}
			after := e.columns(t, b.InstanceID)
			pr, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(deferred[0]) + len(deferred[1]); !tc.missing && n != 0 {
				t.Errorf("alive/unknown List deferred a save; any deferral is wrong here: call 1 %q, call 2 %q", deferred[0], deferred[1])
			} else if n != ownDeadline[0]+ownDeadline[1] {
				t.Errorf("List deferred saves other than the missing target's on a deadline: call 1 %q, call 2 %q", deferred[0], deferred[1])
			}
			if tc.missing {
				// The save is best effort: saved exactly once, or wholly unsaved
				// with both calls having deferred it on a deadline.
				rowSame, prSame := reflect.DeepEqual(before, after), reflect.DeepEqual(prBefore, pr)
				saved := after.State == store.StateMissing && after.RowVersion == before.RowVersion.(int64)+1 && !pr.AwaitsAnswer()
				deferredUnsaved := ownDeadline == [2]int{1, 1} && rowSame && prSame && pr.AwaitsAnswer()
				observed := fmt.Sprintf("state=%v version=%v (before %v) open=%v, unchanged row=%v request=%v, deferred call 1 %q, call 2 %q", after.State, after.RowVersion, before.RowVersion, pr.AwaitsAnswer(), rowSame, prSame, deferred[0], deferred[1])
				if tc.deferSave && !deferredUnsaved {
					t.Errorf("forced deferral: want the save deferred on a deadline on both List calls and row and request unchanged, got %s", observed)
				} else if !saved && !deferredUnsaved {
					t.Errorf("List did not atomically mark missing once and close request: %s", observed)
				}
			} else if !reflect.DeepEqual(before, after) || !pr.AwaitsAnswer() {
				t.Error("alive/unknown List mutated launch or request")
			}
			if !reflect.DeepEqual(siblingBefore, e.columns(t, sibling)) || !reflect.DeepEqual(survivorBefore, e.columns(t, survivor)) {
				t.Error("List changed a live Cursor bystander or non-Cursor survivor")
			}
		})
	}
}

func TestCursorListLostGatewayErrorsAndValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                        string
		params                      api.ListParams
		wantErr                     error
		absentBinding, closeFailure bool
	}{
		{name: "absent binding", absentBinding: true},
		{name: "request close fails", closeFailure: true},
		{name: "negative limit before mutation", params: api.ListParams{Limit: -1}, wantErr: api.ErrInvalidFlags},
		{name: "invalid label before mutation", params: api.ListParams{Labels: []string{"invalid"}}, wantErr: api.ErrListInvalidLabel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			e.pc.Set(b.GatewayPID, procfix.Gone())
			if tc.absentBinding {
				cursorReadSQL(t, e.dbPath, "DELETE FROM cursor_runtimes WHERE bound_instance_id=?", b.InstanceID)
			}
			if tc.closeFailure {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailPermissionDecision, b.InstanceID)
			}
			before := e.columns(t, b.InstanceID)
			prBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			params := tc.params
			params.TmuxSessionName = before.TmuxSessionName.(string)
			if tc.absentBinding {
				// Exact gateway loss is sufficient without a runtime registration.
				for i := 0; i < 2; i++ {
					got, err := c.List(params)
					if err != nil || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != b.InstanceID || got.Spawns[0].State != store.StateMissing {
						t.Errorf("absent binding after exact gateway loss: rows=%+v err=%v; want missing", got.Spawns, err)
					}
				}
				after := e.columns(t, b.InstanceID)
				pr, err := e.st.GetPermissionRequest(b.InstanceID, token)
				if err != nil || after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 || !pr.Closed() || pr.ProvenGone() || pr.ProvenGoneHow != "" || pr.Decision != "deny" || !pr.DeliveredAt.IsZero() {
					t.Errorf("absent binding missing close not atomic/idempotent: before=%+v after=%+v request=%+v err=%v", before, after, pr, err)
				}
				return
			}
			got, err := c.List(params)
			if err == nil {
				t.Error("List reported success for known-dead unsafe launch")
			} else if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("List error = %v, want %v", err, tc.wantErr)
			}
			if got.Spawns == nil || len(got.Spawns) != 0 {
				t.Errorf("failed List rows = %+v, want non-nil empty slice", got.Spawns)
			}
			prAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, e.columns(t, b.InstanceID)) || !reflect.DeepEqual(prBefore, prAfter) {
				t.Error("failed List partially mutated launch or request")
			}
			if tc.wantErr != nil && len(e.pc.StartTimeCalls()) != 0 {
				t.Error("invalid List params reached process reconciliation")
			}
		})
	}
}
