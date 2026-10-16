package store_test

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// An empty runtime descriptor represents examined absence, never proof of runtime death.
func TestCursorStartupMissingConditionalWrite(t *testing.T) {
	for _, change := range []string{"unchanged", "binding inserted", "stale binding inserted", "row refreshed", "token replaced", "life replaced", "gateway PID replaced", "gateway start replaced", "subagent close failure"} {
		t.Run(change, func(t *testing.T) {
			f, _, b := cursorRowBinding(t)
			mix := f.seedCloseMix(b.InstanceID)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("DELETE FROM cursor_runtimes WHERE bound_instance_id=?", b.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.ReadCursorRuntime(b.InstanceID); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("absence not examined: %v", err)
			}
			permission, err := apitest.SeedPermissionRequest(f.path, b.InstanceID, "Bash")
			if err != nil {
				t.Fatal(err)
			}
			requestBefore, err := f.s.GetPermissionRequest(b.InstanceID, permission.RequestToken)
			if err != nil {
				t.Fatal(err)
			}
			// Seeding a request can change the row; examine the final setup snapshot.
			row, err = f.s.GetSpawn(b.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			absent := store.CursorRuntime{InstanceID: row.InstanceID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, SessionID: row.SessionID}
			var raced apitest.SpawnColumns
			var bindingBefore store.CursorRuntime
			if change == "binding inserted" || change == "stale binding inserted" {
				// A separate committed writer inserts AFTER absence was examined.
				// The row snapshot is unchanged, so only at-write NOT EXISTS can refuse.
				if ok, err := f.s.RegisterCursorRuntime(row, b); err != nil || !ok {
					t.Fatalf("raced registration: %v %v", ok, err)
				}
				if change == "stale binding inserted" {
					if _, err = db.Exec("UPDATE cursor_runtimes SET bound_life_number=bound_life_number+1 WHERE bound_instance_id=?", b.InstanceID); err != nil {
						t.Fatal(err)
					}
				}
				bindingBefore, err = f.s.ReadCursorRuntime(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
			} else if change == "subagent close failure" {
				storefix.InjectWriteFailure(t, f.path, storefix.WriteFailSubagentRecord, b.InstanceID)
			} else if change != "unchanged" {
				queries := map[string]string{
					"row refreshed":          "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?",
					"token replaced":         "UPDATE spawns SET launch_token='replacement' WHERE instance_id=?",
					"life replaced":          "UPDATE spawns SET life_number=life_number+1 WHERE instance_id=?",
					"gateway PID replaced":   "UPDATE spawns SET pane_pid=pane_pid+1 WHERE instance_id=?",
					"gateway start replaced": "UPDATE spawns SET pane_starttime='replacement' WHERE instance_id=?",
				}
				if _, err = db.Exec(queries[change], b.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
			raced = f.rawColumns(b.InstanceID)
			lo := time.Now().UnixMilli()
			prior, result, err := f.s.MarkCursorMissingIfSameLaunch(row, absent)
			hi := time.Now().UnixMilli()
			if change == "unchanged" {
				if err != nil || result != store.CondApplied || prior != row.State {
					t.Fatalf("exact dead gateway without registration did not close: prior=%q result=%v err=%v", prior, result, err)
				}
				after := f.rawColumns(b.InstanceID)
				if after.State != store.StateMissing || after.RowVersion != raced.RowVersion.(int64)+1 {
					t.Errorf("missing not applied once: %+v", after)
				}
				f.assertMixClosedInWindow(mix, lo, hi)
				requestAfter, err := f.s.GetPermissionRequest(b.InstanceID, permission.RequestToken)
				if err != nil || !requestAfter.Closed() || !requestAfter.ProvenGone() || requestAfter.Decision != "deny" {
					t.Errorf("missing did not close request: %+v %v", requestAfter, err)
				}
				if _, err := f.s.ReadCursorRuntime(b.InstanceID); !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("fabricated runtime binding: %v", err)
				}
				return
			}
			if change == "subagent close failure" {
				if err == nil || !strings.Contains(err.Error(), "injected write failure") {
					t.Errorf("subagent close did not reach injected rollback: prior=%q result=%v err=%v", prior, result, err)
				}
			} else if err != nil || result != store.CondChanged || prior != "" {
				t.Errorf("raced absent close: prior=%q result=%v err=%v", prior, result, err)
			}
			if !reflect.DeepEqual(raced, f.rawColumns(b.InstanceID)) {
				t.Error("absence close changed raced launch")
			}
			f.assertMixUnchanged(mix)
			requestAfter, readErr := f.s.GetPermissionRequest(b.InstanceID, permission.RequestToken)
			if readErr != nil || !reflect.DeepEqual(requestBefore, requestAfter) {
				t.Errorf("refused/failed close changed request: %+v %v", requestAfter, readErr)
			}
			if strings.Contains(change, "binding inserted") {
				bindingAfter, err := f.s.ReadCursorRuntime(b.InstanceID)
				if err != nil || !reflect.DeepEqual(bindingBefore, bindingAfter) {
					t.Errorf("absence close changed inserted binding: %+v %v", bindingAfter, err)
				}
			}
		})
	}
}
