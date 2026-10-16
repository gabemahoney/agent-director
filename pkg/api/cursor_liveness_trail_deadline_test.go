package api_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// Confirmed terminal writes must return within their original budget even when audit I/O stalls.
func TestCursorReadsWithBlockedTrail(t *testing.T) {
	// Serial: it holds the process-wide trail singleton's mutex and reads the shared trail.
	for _, tc := range []struct {
		verb          string
		callerContext string
	}{
		{"Get", "none"}, {"Status", "none"}, {"List", "none"},
		{"Get", "deadline"}, {"Status", "deadline"}, {"List", "deadline"},
		{"Get", "cancel"}, {"Status", "cancel"}, {"List", "cancel"},
	} {
		verb := tc.verb
		t.Run(fmt.Sprintf("%s/caller=%s", verb, tc.callerContext), func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			e.pc.Set(b.GatewayPID, procfix.Gone())
			e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
			before := e.columns(t, b.InstanceID)
			mark := len(readAPITrailLines(t))
			writer := trail.Default()
			baseline := writer.Status()
			if baseline.Pending != 0 {
				t.Fatalf("prior audit is still pending: %+v", baseline)
			}
			gate := &trailEncodingGate{entered: make(chan struct{}), release: make(chan struct{})}
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			holder := make(chan error, 1)
			go func() {
				holder <- trail.Emit(context.Background(), "ad.test.cursor_deadline_holder", map[string]any{"gate": gate})
			}()
			// Unblock before joining on every failure/panic path, including fixture cleanup.
			defer func() {
				release()
				select {
				case err := <-holder:
					if err != nil {
						t.Errorf("holder Emit: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("holder did not finish after release")
				}
			}()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("encoder did not acquire the real writer mutex")
			}
			heldAt := time.Now()
			observer, err := store.Open(e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			read := func() (string, error) {
				switch verb {
				case "Get":
					row, err := c.Get(b.InstanceID)
					if err != nil && !reflect.DeepEqual(row, api.SpawnRow{AgentArgs: []string{}, Labels: map[string]string{}, PermissionRequests: []api.PermissionRequestInfo{}, UnprovenRequests: []api.PermissionRequestInfo{}, PriorSessions: []api.PriorSession{}}) {
						t.Errorf("Get failure shape: %+v", row)
					}
					return row.State, err
				case "Status":
					row, err := c.Status(b.InstanceID)
					if err != nil && !reflect.DeepEqual(row, api.StatusResult{}) {
						t.Errorf("Status failure shape: %+v", row)
					}
					return row.State, err
				default:
					rows, err := c.List(api.ListParams{})
					if err != nil {
						if !reflect.DeepEqual(rows, api.ListResult{Spawns: []api.ListRow{}}) {
							t.Errorf("List failure shape: %+v", rows)
						}
						return "", err
					}
					if len(rows.Spawns) != 1 || rows.Spawns[0].InstanceID != b.InstanceID {
						return "", fmt.Errorf("List returned unexpected rows: %+v", rows.Spawns)
					}
					return rows.Spawns[0].State, nil
				}
			}
			type outcome struct {
				state string
				err   error
				took  time.Duration
			}
			done := make(chan outcome, 1)
			returned := false
			defer func() {
				if !returned {
					release()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("public read did not finish after release")
					}
				}
			}()
			restore := func() {}
			cancelCaller := func() {}
			var wantErr error
			if tc.callerContext != "none" {
				var ctx context.Context
				if tc.callerContext == "deadline" {
					ctx, cancelCaller = context.WithTimeout(context.Background(), 500*time.Millisecond)
					wantErr = context.DeadlineExceeded
				} else {
					ctx, cancelCaller = context.WithCancel(context.Background())
					wantErr = context.Canceled
				}
				defer cancelCaller()
				restore = api.SetStoreContextForTest(c, ctx)
				defer restore()
			}
			start := time.Now()
			go func() {
				state, err := read()
				done <- outcome{state, err, time.Since(start)}
			}()
			const limit = time.Second + 500*time.Millisecond
			deadline := time.NewTimer(limit)
			defer deadline.Stop()
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			committed := false
			observe := func() {
				row, err := observer.GetSpawn(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if row.State != store.StateMissing {
					return
				}
				pr, err := observer.GetPermissionRequest(b.InstanceID, token)
				if err != nil {
					t.Fatal(err)
				}
				if row.RowVersion != before.RowVersion.(int64)+1 || pr.AwaitsAnswer() || !pr.Closed() ||
					pr.Decision != "deny" || pr.DecisionReason != "find_missing" || pr.ProvenGone() || pr.ProvenGoneHow != "" {
					t.Fatalf("independent terminal commit: row=%+v request=%+v", row, pr)
				}
				committed = true
				if tc.callerContext == "cancel" {
					cancelCaller()
				}
				t.Logf("independent connection observed missing/version+1/closed deny at +%v while mutex held", time.Since(start))
			}
			var got outcome
		wait:
			for {
				select {
				case got = <-done:
					returned = true
					break wait
				case <-ticker.C:
					if !committed {
						observe()
					}
				case <-deadline.C:
					t.Fatalf("%s did not return within %v while real trail mutex remains held", verb, limit)
				}
			}
			if !committed {
				observe()
			}
			if !committed {
				t.Fatal("missing was not independently committed while trail mutex held")
			}
			if wantErr != nil {
				if !errors.Is(got.err, wantErr) || got.state != "" || got.took > limit {
					t.Fatalf("public read = %+v; want deadline/empty result within %v despite confirmed commit", got, limit)
				}
			} else if got.err != nil || got.state != store.StateMissing || got.took > limit {
				t.Fatalf("public read = %+v; want confirmed success/missing within %v", got, limit)
			}
			restore()
			pending := writer.Status()
			if pending.Pending != 2 || pending.Failed != baseline.Failed || pending.Rejected != baseline.Rejected {
				t.Fatalf("held audit status = %+v; want exactly deny and tick pending without failures/rejections", pending)
			}
			terminal := e.columns(t, b.InstanceID)
			t.Logf("%s returned in %v; real mutex still held for %v; pending=%d", verb, got.took, time.Since(heldAt), pending.Pending)
			release()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := trail.Emit(ctx, "ad.test.cursor_deadline_drained", nil); err != nil {
				t.Fatalf("FIFO drain: %v", err)
			}
			if status := writer.Status(); status.Pending != 0 || status.Failed != baseline.Failed || status.Rejected != baseline.Rejected {
				t.Fatalf("drained audit status: %+v", status)
			}
			if state, err := read(); err != nil || state != store.StateMissing {
				t.Fatalf("new inspection = %q, %v", state, err)
			}
			if !reflect.DeepEqual(terminal, e.columns(t, b.InstanceID)) {
				t.Error("new inspection repeated the terminal mutation")
			}
			var events []string
			for _, row := range readAPITrailLines(t)[mark:] {
				if row["instance_id"] != b.InstanceID {
					continue
				}
				if row["request_token"] != token {
					t.Errorf("unexpected request token: %+v", row)
				}
				event, _ := row["event"].(string)
				events = append(events, event)
				switch event {
				case "ad.row_mutation.committed":
					for key, want := range map[string]any{"decision": "deny", "decision_reason": "find_missing", "writer_process": store.WriterProcessFindMissing, "mutation_kind": "update", "source": "ad_store", "tool_name": "Bash"} {
						if row[key] != want {
							t.Errorf("deny %s = %v; want %v", key, row[key], want)
						}
					}
				case "ad.find_missing.tick":
					if row["reconciliation_reason"] != "permission_orphan_closeout" || row["source"] != "ad_find_missing" || row["prior_state"] != nil || row["new_state"] != nil {
						t.Errorf("orphan tick fields: %+v", row)
					}
				}
			}
			if !reflect.DeepEqual(events, []string{"ad.row_mutation.committed", "ad.find_missing.tick"}) {
				t.Errorf("audit events = %v; want deny then orphan tick exactly once each", events)
			}
			t.Log("FIFO drained: pending=0; deny then orphan tick exactly once; terminal row unchanged on new inspection")
		})
	}
}
