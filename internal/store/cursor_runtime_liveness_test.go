package store_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

func TestCursorMissingRefusesReplacedAuthority(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"connection rotated", "UPDATE cursor_runtimes SET bound_connection_id='replacement' WHERE bound_instance_id=?"},
		{"session rotated", "UPDATE cursor_runtimes SET bound_session_id='replacement' WHERE bound_instance_id=?"},
		{"runtime PID replaced", "UPDATE cursor_runtimes SET bound_runtime_pid=bound_runtime_pid+1 WHERE bound_instance_id=?"},
		{"runtime start replaced", "UPDATE cursor_runtimes SET bound_runtime_start='replacement' WHERE bound_instance_id=?"},
		{"runtime termination changed", "UPDATE cursor_runtimes SET bound_terminated=1 WHERE bound_instance_id=?"},
		{"launch replaced", "UPDATE spawns SET launch_token='replacement',life_number=life_number+1 WHERE instance_id=?"},
		{"row refreshed", "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, row, b := cursorRowBinding(t)
			mix := f.seedCloseMix(row.InstanceID)
			// The examined snapshot includes the committed setup, before the replacement.
			row, err := f.s.GetSpawn(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(tc.query, row.InstanceID); err != nil {
				t.Fatal(err)
			}
			before := f.rawColumns(row.InstanceID)
			bindingBefore, err := f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			prior, result, err := f.s.MarkCursorMissingIfSameLaunch(row, b)
			if err != nil || result != store.CondChanged || prior != "" {
				t.Fatalf("stale missing: %q %v %v", prior, result, err)
			}
			if !reflect.DeepEqual(before, f.rawColumns(row.InstanceID)) {
				t.Fatal("stale close changed current launch")
			}
			bindingAfter, err := f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil || !reflect.DeepEqual(bindingBefore, bindingAfter) {
				t.Fatalf("stale close changed binding: %+v %v", bindingAfter, err)
			}
			f.assertMixUnchanged(mix)
		})
	}
}

func TestCursorResumedGatewayMissingGuardsRetainedDescriptor(t *testing.T) {
	for _, change := range []string{"none", "registered current", "old descriptor changed", "path replaced", "socket replaced", "pane replaced", "row restored"} {
		t.Run(change, func(t *testing.T) {
			f, old, b := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE spawns SET launch_token='2222222222222222',pane_pid=pane_pid+100,row_version=row_version+1 WHERE instance_id=?`, old.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err := f.s.GetSpawn(old.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if row.Identity.Token == b.Token || row.Identity.PanePID == b.GatewayPID {
				t.Fatal("setup retained current descriptor")
			}
			queries := map[string]string{
				"old descriptor changed": `UPDATE cursor_runtimes SET bound_connection_id='replacement' WHERE bound_instance_id=?`,
				"path replaced":          `UPDATE spawns SET jsonl_path='replacement' WHERE instance_id=?`,
				"socket replaced":        `UPDATE spawns SET tmux_socket='replacement' WHERE instance_id=?`,
				"pane replaced":          `UPDATE spawns SET pane_id='%replacement' WHERE instance_id=?`,
				"row restored":           `UPDATE spawns SET launch_token='restored',row_version=row_version+1 WHERE instance_id=?`,
			}
			if change == "registered current" {
				current := b
				current.Token = row.Identity.Token
				current.GatewayPID = row.Identity.PanePID
				current.Connection = "resumed-connection"
				if ok, err := f.s.RegisterCursorRuntime(row, current); err != nil || !ok {
					t.Fatalf("current registration %v %v", ok, err)
				}
			} else if q := queries[change]; q != "" {
				if _, err := db.Exec(q, row.InstanceID); err != nil {
					t.Fatal(err)
				}
			}
			before := f.rawColumns(row.InstanceID)
			bindingBefore, err := f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			view := f.s.WithCursorMissingReads([]store.CursorMissingProof{{Row: row, Binding: b}})
			projected, readErr := view.GetSpawn(row.InstanceID)
			prior, result, writeErr := f.s.MarkCursorMissingIfSameLaunch(row, b)
			if change == "none" {
				if readErr != nil || projected.State != store.StateMissing || writeErr != nil || result != store.CondApplied || prior != row.State {
					t.Fatalf("retained descriptor read/write disagree: row=%+v read=%v prior=%s result=%v write=%v", projected, readErr, prior, result, writeErr)
				}
				return
			}
			if readErr == nil && projected.State == store.StateMissing {
				t.Fatal("stale proof projected replacement missing")
			}
			if writeErr != nil || result != store.CondChanged || prior != "" {
				t.Fatalf("stale proof mutated: %q %v %v", prior, result, writeErr)
			}
			bindingAfter, err := f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil || !reflect.DeepEqual(before, f.rawColumns(row.InstanceID)) || !reflect.DeepEqual(bindingBefore, bindingAfter) {
				t.Fatal("stale proof changed current row/binding")
			}
		})
	}
}
