package api_test

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func cursorStartupRead(c *api.Client, verb, id string) (string, error) {
	switch verb {
	case "Get":
		r, err := c.Get(id)
		return r.State, err
	case "Status":
		r, err := c.Status(id)
		return r.State, err
	default:
		r, err := c.List(api.ListParams{})
		if err != nil {
			return "", err
		}
		for _, row := range r.Spawns {
			if row.InstanceID == id {
				return row.State, nil
			}
		}
		return "", nil
	}
}

func TestCursorStartupAbsentBindingReads(t *testing.T) {
	// Serial: deadline-sensitive real-store setup runs outside the parallel API fixture wave.
	for _, verb := range []string{"Get", "Status", "List"} {
		for _, verdict := range []string{"gone", "alive", "unknown"} {
			t.Run(verb+"/"+verdict, func(t *testing.T) {
				setupStart := time.Now()
				e, c, b, token := cursorReadLaunch(t)
				t.Logf("launch setup start=%s elapsed=%v", setupStart.Format(time.RFC3339Nano), time.Since(setupStart))
				cursorReadSQL(t, e.dbPath, "DELETE FROM cursor_runtimes WHERE bound_instance_id=?", b.InstanceID)
				cursorReadSQL(t, e.dbPath, "UPDATE spawns SET state='pending',settled_state='pending',agent_args=? WHERE instance_id=?", `["--model","auto"]`, b.InstanceID)
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
					state, err := cursorStartupRead(c, verb, b.InstanceID)
					t.Logf("startup read start=%s elapsed=%v", callStart.Format(time.RFC3339Nano), time.Since(callStart))
					if err != nil || state != want {
						t.Errorf("%s gateway=%s no runtime binding: state=%q err=%v want=%s", verb, verdict, state, err, want)
					}
				}
				after := e.columns(t, b.InstanceID)
				requestAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
				if err != nil {
					t.Fatal(err)
				}
				if verdict == "gone" {
					if after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 || !requestAfter.Closed() || !requestAfter.ProvenGone() || requestAfter.Decision != "deny" || !requestAfter.DeliveredAt.IsZero() || requestAfter.PaneAnswered() {
						t.Errorf("not a single atomic missing close: before=%+v after=%+v request=%+v", before, after, requestAfter)
					}
				} else if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(requestBefore, requestAfter) {
					t.Error("alive/unknown no-binding read mutated raw row/request")
				}
				if after.AgentArgs != before.AgentArgs || after.LaunchToken != before.LaunchToken || after.LifeNumber != before.LifeNumber || after.PanePID != before.PanePID || after.PaneStarttime != before.PaneStarttime || after.SessionID != before.SessionID {
					t.Error("missing changed args or examined launch identity")
				}
				if _, err := e.st.ReadCursorRuntime(b.InstanceID); !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("read fabricated runtime binding: %v", err)
				}
			})
		}
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
				state, err := cursorStartupRead(c, verb, b.InstanceID)
				t.Logf("startup read start=%s elapsed=%v", callStart.Format(time.RFC3339Nano), time.Since(callStart))
				if failure == "held writer" && !errors.Is(err, api.ErrStoreBusy) {
					t.Errorf("held writer error=%v want ErrStoreBusy", err)
				}
				if failure == "request close" && (err == nil || !strings.Contains(err.Error(), "injected write failure: "+storefix.WriteFailPermissionDecision.String())) {
					t.Errorf("close failure error=%v want injected failure", err)
				}
				// The operation allowance excludes preceding reads and process probes.
				// Frozen held-SQL/trail tests cover its deadline; this checks rollback.
				if state != "" {
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
