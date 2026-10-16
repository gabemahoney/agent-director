//go:build linux

package cursorhost

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Capture only an already-failed bootstrap, without the store lease or reconciliation.
func rotationStartupFailure(t *testing.T, c *cursorSession, barrier *recordBarrier) {
	t.Helper()
	rotationFailureDiagnostics(t, c, barrier, []string{"spawns", "cursor_runtimes"})
}

func rotationFailureDiagnostics(t *testing.T, c *cursorSession, barrier *recordBarrier, tables []string) {
	t.Helper()
	stacks := make([]byte, 4<<20)
	n := runtime.Stack(stacks, true)
	t.Logf("startup failure goroutines (truncated=%v):\n%s", n == len(stacks), stacks[:n])
	paths := []string{c.e.runtime.Trace, c.e.runtime.Sent, c.e.path + ".reports"}
	if barrier != nil && barrier.transcriptFile != nil {
		if info, err := barrier.transcriptFile.Stat(); err == nil {
			paths = append(paths, filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "cursor-transcripts", info.Name()))
		} else {
			t.Logf("startup current transcript stat: %v", err)
		}
	} else if len(tables) > 2 {
		t.Log("rotation SQL failure: no protected-record pause configured")
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Logf("startup file %s: %v", path, err)
			continue
		}
		if len(tables) > 2 {
			if info, err := f.Stat(); err == nil && info.Size() > 256<<10 {
				f.Seek(-(256 << 10), io.SeekEnd)
			}
		}
		raw, err := io.ReadAll(io.LimitReader(f, 256<<10))
		f.Close()
		t.Logf("startup file %s (max 256KiB): %s; error=%v", path, raw, err)
	}
	if barrier != nil {
		select {
		case <-barrier.entered:
			t.Log("startup record barrier entered=true")
		default:
			t.Log("startup record barrier entered=false")
		}
		select {
		case <-barrier.release:
			t.Log("startup record barrier released=true")
		default:
			t.Log("startup record barrier released=false")
		}
	}
	if dir := os.Getenv("AD_TEST_CURSOR_BARRIER_DIR"); dir != "" {
		for _, name := range []string{"entered", "release"} {
			_, err := os.Stat(filepath.Join(dir, name))
			t.Logf("startup report barrier %s: %v", name, err)
		}
	} else {
		t.Log("startup report barrier not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	db, err := sql.Open("sqlite", "file:"+c.e.path+"?mode=ro&_pragma=busy_timeout(0)")
	if err != nil {
		t.Logf("startup raw store open: %v", err)
		return
	}
	defer db.Close()
	if barrier == nil && len(tables) > 2 {
		var path string
		if err := db.QueryRowContext(ctx, "SELECT jsonl_path FROM spawns WHERE instance_id=?", c.e.binding.InstanceID).Scan(&path); err != nil {
			t.Logf("rotation SQL current transcript path: %v", err)
		} else if f, err := os.Open(path); err != nil {
			t.Logf("rotation SQL current transcript open: %v", err)
		} else {
			if info, err := f.Stat(); err == nil && info.Size() > 256<<10 {
				f.Seek(-(256 << 10), io.SeekEnd)
			}
			raw, err := io.ReadAll(io.LimitReader(f, 256<<10))
			f.Close()
			t.Logf("rotation SQL protected transcript tail (max256KiB): %s; error=%v", raw, err)
		}
	}
	for _, table := range tables {
		identityColumn := "instance_id"
		if table == "cursor_runtimes" {
			identityColumn = "bound_instance_id"
		}
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE "+identityColumn+"=?", c.e.binding.InstanceID)
		if err != nil {
			t.Logf("startup raw %s: %v", table, err)
			continue
		}
		columns, err := rows.Columns()
		if err == nil {
			for rows.Next() {
				values, pointers := make([]any, len(columns)), make([]any, len(columns))
				for j := range values {
					pointers[j] = &values[j]
				}
				if err := rows.Scan(pointers...); err != nil {
					t.Logf("startup raw scan: %v", err)
					break
				}
				t.Logf("startup raw %s columns=%v values=%v", table, columns, values)
			}
		}
		t.Logf("startup raw %s columns error=%v rows error=%v", table, err, rows.Err())
		rows.Close()
	}
}
