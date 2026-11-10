package api_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cursorStartupRead is id's state as verb reports it; marked is whether a
// Sweep listed id in its ids (always false for the reads).
func cursorStartupRead(c *api.Client, verb, id string) (state string, marked bool, err error) {
	switch verb {
	case "Get":
		r, err := c.Get(id)
		return r.State, false, err
	case "Status":
		r, err := c.Status(id)
		return r.State, false, err
	case "Sweep":
		r, err := c.Sweep(context.Background())
		if err != nil {
			return "", false, err
		}
		marked = slices.Contains(r.IDs, id)
		for _, check := range r.Checks {
			if check.InstanceID == id {
				if marked && check.State != store.StateMissing {
					return "", false, fmt.Errorf("sweep ids %v list a row checked %s", r.IDs, check.State)
				}
				return check.State, marked, nil
			}
		}
		return "", marked, nil
	default:
		r, err := c.List(api.ListParams{})
		if err != nil {
			return "", false, err
		}
		for _, row := range r.Spawns {
			if row.InstanceID == id {
				return row.State, false, nil
			}
		}
		return "", false, nil
	}
}

// A pending Cursor launch whose Helper gateway is exactly gone reads missing on
// every read and sweep, also inside the pending grace period or while its
// launch owner lives (b.x9o, MI Q2A); an alive or unknown gateway is noted
// only when no pending protection holds the row, and untouched while it does.
func TestCursorStartupAbsentBindingReads(t *testing.T) {
	// Serial: deadline-sensitive real-store setup runs outside the parallel API fixture wave.
	for _, verb := range []string{"Get", "Status", "List", "Sweep"} {
		for _, protection := range []string{"none", "grace", "owner"} {
			for _, verdict := range []string{"gone", "alive", "unknown"} {
				t.Run(verb+"/"+protection+"/"+verdict, func(t *testing.T) {
					cursorStartupAbsentBindingRead(t, verb, protection, verdict)
				})
			}
		}
	}
}

func cursorStartupAbsentBindingRead(t *testing.T, verb, protection, verdict string) {
	setupStart := time.Now()
	e, c, b, token := cursorReadLaunch(t)
	t.Logf("launch setup start=%s elapsed=%v", setupStart.Format(time.RFC3339Nano), time.Since(setupStart))
	cursorReadSQL(t, e.dbPath, "DELETE FROM cursor_runtimes WHERE bound_instance_id=?", b.InstanceID)
	cursorReadSQL(t, e.dbPath, "UPDATE spawns SET state='pending',settled_state='pending',agent_args=? WHERE instance_id=?", `["--model","auto"]`, b.InstanceID)
	switch protection {
	case "grace":
		cursorReadSQL(t, e.dbPath, "UPDATE spawns SET launch_started_at=? WHERE instance_id=?", e.clock.Now().UnixMilli(), b.InstanceID)
	case "owner":
		owner := e.newPID()
		e.pc.Set(owner, procfix.Alive(apitest.LinuxProcStarttime))
		cursorReadSQL(t, e.dbPath, "UPDATE spawns SET launch_owner_pid=?,launch_owner_starttime=?,launch_owner_pidns=? WHERE instance_id=?",
			owner, apitest.LinuxProcStarttime, selfPIDNamespace(t), b.InstanceID)
	}
	switch verdict {
	case "gone":
		e.pc.Set(b.GatewayPID, procfix.Gone())
	case "alive":
		e.pc.Set(b.GatewayPID, procfix.Alive(b.GatewayStart))
	default:
		e.pc.Set(b.GatewayPID, procfix.Unreadable())
	}
	before := e.columns(t, b.InstanceID)
	requestBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	want := store.StatePending
	if verdict == "gone" {
		want = store.StateMissing
	}
	for i := 0; i < 2; i++ {
		callStart := time.Now()
		state, marked, err := cursorStartupRead(c, verb, b.InstanceID)
		t.Logf("startup read start=%s elapsed=%v", callStart.Format(time.RFC3339Nano), time.Since(callStart))
		if err != nil || state != want {
			t.Errorf("%s gateway=%s no runtime binding: state=%q err=%v want=%s", verb, verdict, state, err, want)
		}
		// Only the sweep that made the mark lists it.
		if wantMarked := verb == "Sweep" && i == 0 && verdict == "gone"; marked != wantMarked {
			t.Errorf("%s pass %d listed=%v want %v", verb, i+1, marked, wantMarked)
		}
	}
	after := e.columns(t, b.InstanceID)
	requestAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	if verdict == "gone" {
		if after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 || !requestAfter.Closed() || requestAfter.ProvenGone() || requestAfter.ProvenGoneHow != "" || requestAfter.Decision != "deny" || !requestAfter.DeliveredAt.IsZero() || requestAfter.PaneAnswered() {
			t.Errorf("not a single atomic missing close: before=%+v after=%+v request=%+v", before, after, requestAfter)
		}
	} else if protection != "none" {
		// The protection, not the gateway sample, decides the row: no
		// note, no write.
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requestBefore, requestAfter) {
			t.Errorf("protected pending row changed: before=%+v after=%+v", before, after)
		}
	} else {
		note := "probe_eacces"
		if verdict == "alive" {
			note = "unreported"
		}
		assertCursorRecoveryNote(t, before, after, note)
		if !reflect.DeepEqual(requestBefore, requestAfter) {
			t.Error("alive/unknown changed request")
		}
	}
	if after.AgentArgs != before.AgentArgs || after.LaunchToken != before.LaunchToken || after.LifeNumber != before.LifeNumber || after.PanePID != before.PanePID || after.PaneStarttime != before.PaneStarttime || after.SessionID != before.SessionID {
		t.Error("missing changed args or examined launch identity")
	}
	if _, err := e.st.ReadCursorRuntime(b.InstanceID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("read fabricated runtime binding: %v", err)
	}
}

func TestCursorStartupAbsentBindingFailures(t *testing.T) {
	// Serial: deadline-sensitive real-store setup runs outside the parallel API fixture wave.
	for _, verb := range []string{"Get", "Status", "List"} {
		for _, failure := range []string{"held writer", "request close"} {
			t.Run(verb+"/"+failure, func(t *testing.T) {
				setupStart := time.Now()
				e, c, b, token := cursorReadLaunch(t)
				t.Logf("launch setup start=%s elapsed=%v", setupStart.Format(time.RFC3339Nano), time.Since(setupStart))
				cursorReadSQL(t, e.dbPath, "DELETE FROM cursor_runtimes WHERE bound_instance_id=?", b.InstanceID)
				e.pc.Set(b.GatewayPID, procfix.Gone())
				before := e.columns(t, b.InstanceID)
				requestBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
				if err != nil {
					t.Fatal(err)
				}
				if failure == "held writer" {
					writer, err := sql.Open("sqlite", e.dbPath)
					if err != nil {
						t.Fatal(err)
					}
					defer writer.Close()
					writer.SetMaxOpenConns(1)
					if _, err = writer.Exec("BEGIN IMMEDIATE"); err != nil {
						t.Fatal(err)
					}
					defer writer.Exec("ROLLBACK")
				} else {
					storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailPermissionDecision, b.InstanceID)
				}
				callStart := time.Now()
				state, _, err := cursorStartupRead(c, verb, b.InstanceID)
				t.Logf("startup read start=%s elapsed=%v", callStart.Format(time.RFC3339Nano), time.Since(callStart))
				if failure == "held writer" && (err != nil || state != "missing") {
					t.Errorf("held writer state=%q error=%v want missing without error", state, err)
				}
				if failure == "request close" && (err == nil || !strings.Contains(err.Error(), "injected write failure: "+storefix.WriteFailPermissionDecision.String())) {
					t.Errorf("close failure error=%v want injected failure", err)
				}
				// The operation allowance excludes preceding reads and process probes.
				// Frozen held-SQL/trail tests cover its deadline; this checks rollback.
				if failure == "request close" && state != "" {
					t.Errorf("failed startup close exposed state=%q", state)
				}
				requestAfter, readErr := e.st.GetPermissionRequest(b.InstanceID, token)
				if readErr != nil || !reflect.DeepEqual(before, e.columns(t, b.InstanceID)) || !reflect.DeepEqual(requestBefore, requestAfter) {
					t.Errorf("failed startup close partially wrote: request=%+v err=%v", requestAfter, readErr)
				}
			})
		}
	}
}
