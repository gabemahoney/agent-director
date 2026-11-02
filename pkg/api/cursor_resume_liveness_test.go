package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorResumedGatewayReadUsesCurrentPaneWithRetainedDescriptor(t *testing.T) {
	t.Parallel()
	for _, registered := range []bool{false, true} {
		for _, verdict := range []string{"dead", "unknown", "alive"} {
			t.Run(verdict+map[bool]string{true: "/registered", false: "/before registration"}[registered], func(t *testing.T) {
				e, c, old, token := cursorReadLaunch(t)
				pid := old.GatewayPID + 100
				cursorReadSQL(t, e.dbPath, `UPDATE spawns SET state='pending',launch_token='2222222222222222',pane_pid=?,launch_started_at=?,row_version=row_version+1 WHERE instance_id=?`, pid, e.clock.Now().UnixMilli(), old.InstanceID)
				if err := apitest.SetSettle(e.dbPath, old.InstanceID, store.StatePending, e.clock.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
				row, err := e.st.GetSpawn(old.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if registered {
					current := old
					current.Token = row.Identity.Token
					current.GatewayPID = pid
					current.Connection = "resumed-connection"
					current.SessionID = ""
					if ok, err := e.st.RegisterCursorRuntime(row, current); err != nil || !ok {
						t.Fatalf("register resumed: %v %v", ok, err)
					}
				}
				e.pc.Set(old.GatewayPID, procfix.Alive(old.GatewayStart))
				e.pc.Set(old.RuntimePID, procfix.Alive(old.RuntimeStart))
				process := procfix.Gone()
				if verdict == "unknown" {
					process = procfix.Unreadable()
				}
				if verdict == "alive" {
					process = procfix.Alive(row.Identity.PaneStarttime)
				}
				e.pc.Set(pid, process)
				if err := apitest.SetFamilyHistory(e.dbPath, row.InstanceID, store.StateWorking, store.StateWorking, 0); err != nil {
					t.Fatal(err)
				}
				before := cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)
				if verdict != "dead" {
					// b.x9o: shared checks preserve pending grace for an alive or
					// unknown gateway. Advancing the clock alone performs no recovery.
					grace, err := c.Get(row.InstanceID)
					if err != nil || grace.State != store.StatePending || !reflect.DeepEqual(before, cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)) {
						t.Fatalf("pending grace mutated current launch: %+v %v", grace, err)
					}
					e.clock.Advance(time.Minute + time.Second)
					if !reflect.DeepEqual(before, cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)) {
						t.Fatal("time alone performed recovery")
					}
				}
				// b.x9o (MI Q2A): an exact gateway loss outranks pending grace, so
				// a dead gateway's first read, inside grace, reports missing.
				getStarted := time.Now()
				got, err := c.Get(row.InstanceID)
				getFinished := time.Now()
				t.Logf("public Get elapsed=%s state=%s error=%v", time.Since(getStarted), got.State, err)
				if err != nil {
					t.Fatal(err)
				}
				after := cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)
				t.Logf("raw before=%+v after=%+v", before, after)
				want := store.StatePending
				if verdict == "dead" {
					want = store.StateMissing
				}
				if got.State != want {
					t.Fatalf("read current gateway: got %s want %s", got.State, want)
				}
				// b.x9o: the projected loss is this read's own still-owned
				// attempt, its deferred write not yet durable; the Helper's
				// gateway is never evidence about the runtime process.
				if r := got.Confirmations.State; verdict == "dead" && (r.Status != store.ConfirmationPending || r.ConfirmedAt != nil ||
					r.Source != nil || r.CheckedAt == nil || r.Reason == nil || *r.Reason != store.ReasonOperationPending) {
					t.Fatalf("projected missing state record %s; want pending, operation_pending, no confirmation", conf(got.Confirmations.State))
				}
				if got.ProcessLiveness != store.ProcessUnknown || conf(got.Confirmations.ProcessLiveness) != unknownRecord(store.ReasonUnsupported) {
					t.Fatalf("Cursor process_liveness %s %s; want unknown, unsupported", got.ProcessLiveness, conf(got.Confirmations.ProcessLiveness))
				}
				if got.InstanceID != row.InstanceID || got.Agent != store.AgentCursor || got.SessionID != row.SessionID || got.JSONLPath != row.JSONLPath {
					t.Fatalf("read changed current launch identity: %+v", got)
				}
				request := after["permission_requests"]
				if request["request_token"] != token {
					t.Fatalf("wrong request snapshot: %+v", request)
				}
				if request["proven_gone_at"] != nil || request["proven_gone_how"] != nil {
					t.Fatalf("gateway death manufactured request proof: %+v", request)
				}
				if verdict == "dead" {
					if got.EndedAt != nil || len(got.PermissionRequests) != 0 || len(got.UnprovenRequests) != 0 {
						t.Fatalf("missing projection invented terminal facts or displayed permissions: %+v", got)
					}
					if after["spawns"]["state"] == store.StateMissing {
						if request["closed_at"] == nil || request["decision"] != "deny" || request["decision_reason"] != store.DecisionReasonFindMissing || request["decided_at"] == nil || request["delivered_at"] != nil || request["pane_answer"] != "none" {
							t.Fatalf("durable missing did not atomically close request: %+v", after)
						}
						if after["spawns"]["row_version"] != before["spawns"]["row_version"].(int64)+1 || after["spawns"]["ended_at"] == nil || !reflect.DeepEqual(before["cursor_runtimes"], after["cursor_runtimes"]) {
							t.Fatalf("terminal persistence changed binding or lacked terminal facts: %+v", after)
						}
						if after["spawns"]["permission_epoch"] != before["spawns"]["permission_epoch"].(int64)+1 || after["spawns"]["observation_version"] != before["spawns"]["observation_version"].(int64)+1 || after["spawns"]["display_observation"] != nil {
							t.Fatalf("pending to missing did not invalidate permission ownership and reset the display observation exactly once: before=%+v after=%+v", before["spawns"], after["spawns"])
						}
						changed := after["spawns"]["family_changed_ms"].(int64)
						if after["spawns"]["family_state"] != store.StateWaiting || after["spawns"]["family_settled_state"] != store.StateWorking || changed < getStarted.UnixMilli() || changed > getFinished.UnixMilli() {
							t.Fatalf("terminal persistence did not record the family transition independently: %+v", after["spawns"])
						}
						// The exact gateway loss is the missing value's state record
						// (process), for the resumed launch; the read stamps it.
						var evidence struct {
							V, Token, Src string
							Life, Epoch   int64
							At, Checked   int64
						}
						if raw, ok := after["spawns"]["state_evidence"].(string); !ok || json.Unmarshal([]byte(raw), &evidence) != nil ||
							evidence.V != store.StateMissing || evidence.Src != store.EvidenceProcess || evidence.Life != row.LifeNumber ||
							evidence.Token != row.Identity.Token || evidence.Epoch != after["spawns"]["permission_epoch"] ||
							evidence.At < getStarted.UnixMilli() || evidence.At > getFinished.UnixMilli() || evidence.Checked != evidence.At {
							t.Fatalf("terminal close state record = %v; want missing from process for life %d, launch %s, its epoch, stamped by the read",
								after["spawns"]["state_evidence"], row.LifeNumber, row.Identity.Token)
						}
						// The immediate write's settled value is missing itself, so no
						// earlier value's settled record survives it.
						if after["spawns"]["settled_evidence"] != nil {
							t.Fatalf("terminal close kept settled record %v", after["spawns"]["settled_evidence"])
						}
						for column, value := range before["spawns"] {
							switch column {
							case "state", "settled_state", "state_changed_ms", "ended_at", "last_seen_at", "liveness_unverified_since", "liveness_note", "launch_started_at", "row_version", "family_state", "family_settled_state", "family_changed_ms", "state_evidence", "settled_evidence":
								continue
							case "permission_epoch", "observation_version", "display_observation":
								// Exact invalidation/reset expectations are checked above.
								continue
							}
							if !reflect.DeepEqual(value, after["spawns"][column]) {
								t.Fatalf("terminal close changed saved launch column %s: %+v", column, after)
							}
						}
					} else if !reflect.DeepEqual(before, after) {
						t.Fatalf("best-effort missing projection partially mutated launch/binding/request: before=%+v after=%+v", before, after)
					}
				} else {
					note := "unreported"
					if verdict == "unknown" {
						note = "probe_eacces"
					}
					current := after["spawns"]
					if current["liveness_note"] != note || current["liveness_unverified_since"] == nil || current["row_version"] != before["spawns"]["row_version"].(int64)+1 {
						t.Fatalf("wrong pending note: %+v", current)
					}
					before["spawns"]["liveness_note"], before["spawns"]["liveness_unverified_since"], before["spawns"]["row_version"] = note, current["liveness_unverified_since"], current["row_version"]
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("pending note changed unrelated row/request/runtime: before=%+v after=%+v", before, after)
					}
				}
			})
		}
	}
}

// Read all three tables in one independent, read-only SQLite snapshot without reconciliation.
func cursorResumeRawSnapshot(t *testing.T, path, id string) map[string]map[string]any {
	t.Helper()
	started := time.Now()
	// This independent verification allowance is not a product/API deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	phase, table, phaseStarted := "open", "", started
	nextPhase := func(next string) {
		t.Logf("raw snapshot phase=%s table=%s duration=%s elapsed=%s", phase, table, time.Since(phaseStarted), time.Since(started))
		phase, phaseStarted = next, time.Now()
	}
	fail := func(err error) {
		t.Helper()
		t.Fatalf("raw snapshot phase=%s table=%s elapsed=%s: %v", phase, table, time.Since(started), err)
	}
	defer func() { t.Logf("raw snapshot total elapsed=%s", time.Since(started)) }()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		fail(err)
	}
	defer func() {
		cleanupStarted := time.Now()
		err := db.Close()
		t.Logf("raw snapshot pool cleanup duration=%s error=%v", time.Since(cleanupStarted), err)
	}()
	nextPhase("begin (including connection initialization)")
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		fail(err)
	}
	defer func() {
		cleanupStarted := time.Now()
		err := tx.Rollback()
		t.Logf("raw snapshot transaction cleanup duration=%s error=%v", time.Since(cleanupStarted), err)
	}()
	snapshot := make(map[string]map[string]any)
	for _, name := range []string{"spawns", "cursor_runtimes", "permission_requests"} {
		nextPhase("schema")
		table = name
		key := "instance_id"
		if table == "cursor_runtimes" {
			key = "bound_instance_id"
		}
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if err != nil {
			fail(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			fail(err)
		}
		nextPhase("data query")
		expressions := make([]string, len(columns))
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i, column := range columns {
			// Unary + preserves raw timestamp storage instead of driver time decoding.
			expressions[i] = `+"` + strings.ReplaceAll(column, `"`, `""`) + `"`
			pointers[i] = &values[i]
		}
		rows, err = tx.QueryContext(ctx, "SELECT "+strings.Join(expressions, ",")+" FROM "+table+" WHERE "+key+"=?", id)
		if err != nil {
			fail(err)
		}
		nextPhase("scan")
		if !rows.Next() {
			rows.Close()
			t.Fatalf("raw snapshot phase=%s table=%s elapsed=%s missing: %v", phase, table, time.Since(started), rows.Err())
		}
		if err := rows.Scan(pointers...); err != nil {
			rows.Close()
			fail(err)
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		if rows.Next() || rows.Err() != nil {
			rows.Close()
			t.Fatalf("raw snapshot phase=%s table=%s elapsed=%s must contain one row: %v", phase, table, time.Since(started), rows.Err())
		}
		rows.Close()
		snapshot[table] = row
	}
	nextPhase("complete")
	return snapshot
}
