package api_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorReadsRefuseWriterContention(t *testing.T) {
	// Serial: deadline-sensitive held-writer fixture runs outside the parallel API fixture wave.
	for _, verb := range []string{"Get", "Status", "List"} {
		t.Run(verb, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			e.pc.Set(b.GatewayPID, procfix.Gone())
			e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
			before := e.columns(t, b.InstanceID)
			requestBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}

			writer, err := sql.Open("sqlite", e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			writer.SetMaxOpenConns(1)
			var mode string
			if err := writer.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Fatalf("WAL prerequisite: mode=%q err=%v", mode, err)
			}
			// Prewarm the independent WAL reader before taking the writer lock.
			reader, err := sql.Open("sqlite", e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			prewarmStart := time.Now()
			var warmState string
			if err := reader.QueryRow("SELECT state FROM spawns WHERE instance_id=?", b.InstanceID).Scan(&warmState); err != nil || warmState != before.State {
				t.Fatalf("WAL prewarm: state=%q err=%v", warmState, err)
			}
			t.Logf("WAL prewarm start=%s elapsed=%v", prewarmStart.Format(time.RFC3339Nano), time.Since(prewarmStart))
			if _, err := writer.Exec("BEGIN IMMEDIATE"); err != nil {
				t.Fatal(err)
			}
			held := true
			release := func() {
				if held {
					if _, err := writer.Exec("ROLLBACK"); err != nil {
						t.Errorf("release writer: %v", err)
					}
					held = false
				}
			}
			defer release()

			readStart := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			var state string
			err = reader.QueryRowContext(ctx, "SELECT state FROM spawns WHERE instance_id=?", b.InstanceID).Scan(&state)
			cancel()
			t.Logf("held WAL read start=%s elapsed=%v", readStart.Format(time.RFC3339Nano), time.Since(readStart))
			if err != nil || state != before.State {
				t.Fatalf("WAL read while writer held: state=%q err=%v", state, err)
			}

			reached := make(chan struct{})
			api.SetProcCheckerForTest(c, &cursorReadBarrier{pc: e.pc, once: func() { close(reached) }})
			type result struct {
				err     error
				state   string
				count   int
				nilList bool
				elapsed time.Duration
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				var r result
				switch verb {
				case "Get":
					row, err := c.Get(b.InstanceID)
					r.err, r.state = err, row.State
				case "Status":
					row, err := c.Status(b.InstanceID)
					r.err, r.state = err, row.State
				case "List":
					rows, err := c.List(api.ListParams{})
					r.err, r.count, r.nilList = err, len(rows.Spawns), rows.Spawns == nil
				}
				r.elapsed = time.Since(start)
				done <- r
			}()
			select {
			case <-reached:
				t.Logf("writer held; independent WAL read passed; exact gateway process check reached at %v; default busy timeout=%dms; observed=%s", time.Since(start), config.DefaultStoreBusyTimeoutMs, time.Now().Format(time.RFC3339Nano))
			case r := <-done:
				t.Fatalf("invalid setup: read returned before gateway reconciliation: %+v", r)
			case <-time.After(2 * time.Second):
				release()
				<-done
				t.Fatal("invalid setup: gateway process check not reached")
			}

			var r result
			returnedWhileHeld := false
			select {
			case r = <-done:
				returnedWhileHeld = true
			case <-time.After(time.Second):
				t.Errorf("%s still blocked one second after gateway check while independent writer lock remains held", verb)
			}
			// These assertions run before releasing the writer, even on the RED path.
			requestAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, e.columns(t, b.InstanceID)) || !reflect.DeepEqual(requestBefore, requestAfter) || !requestAfter.AwaitsAnswer() {
				t.Error("contended read partially changed launch or permission request while writer held")
			}
			if returnedWhileHeld {
				if !errors.Is(r.err, api.ErrStoreBusy) {
					t.Errorf("read error while lock held = %v; want ErrStoreBusy", r.err)
				}
				if r.state != "" || r.count != 0 || verb == "List" && r.nilList {
					t.Errorf("failed read exposed stale/partial output: %+v", r)
				}
				t.Logf("returned while writer held: elapsed=%v err=%v observed=%s", r.elapsed, r.err, time.Now().Format(time.RFC3339Nano))
			}
			releasedAt := time.Since(start)
			release()
			if !returnedWhileHeld {
				select {
				case r = <-done:
					t.Logf("RED cleanup joined worker: writer released at %v; return elapsed=%v err=%v state=%q rows=%d", releasedAt, r.elapsed, r.err, r.state, r.count)
				case <-time.After(12 * time.Second):
					t.Fatal("worker did not finish after writer release")
				}
			}
		})
	}
}
