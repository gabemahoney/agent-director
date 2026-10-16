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
