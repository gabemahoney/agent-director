package api_test

import (
	"context"
	"database/sql"
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
				before := cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)
				got, err := c.Get(row.InstanceID)
				after := cursorResumeRawSnapshot(t, e.dbPath, row.InstanceID)
				t.Logf("public state=%s error=%v; raw before=%+v after=%+v", got.State, err, before, after)
				if err != nil {
					t.Fatal(err)
				}
				want := store.StatePending
				if verdict == "dead" {
					want = store.StateMissing
				}
				if got.State != want {
					t.Fatalf("read current gateway: got %s want %s", got.State, want)
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
						for column, value := range before["spawns"] {
							switch column {
							case "state", "settled_state", "state_changed_ms", "ended_at", "last_seen_at", "liveness_unverified_since", "liveness_note", "launch_started_at", "row_version":
								continue
							}
							if !reflect.DeepEqual(value, after["spawns"][column]) {
								t.Fatalf("terminal close changed saved launch column %s: %+v", column, after)
							}
						}
					} else if !reflect.DeepEqual(before, after) {
						t.Fatalf("best-effort missing projection partially mutated launch/binding/request: before=%+v after=%+v", before, after)
					}
				} else if !reflect.DeepEqual(before, after) {
					t.Fatal("unknown/living gateway changed row")
				}
			})
		}
	}
}

// Read all three tables in one independent, read-only SQLite snapshot without reconciliation.
func cursorResumeRawSnapshot(t *testing.T, path, id string) map[string]map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snapshot := make(map[string]map[string]any)
	for _, table := range []string{"spawns", "cursor_runtimes", "permission_requests"} {
		key := "instance_id"
		if table == "cursor_runtimes" {
			key = "bound_instance_id"
		}
		rows, err := tx.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
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
			t.Fatal(err)
		}
		if !rows.Next() {
			rows.Close()
			t.Fatalf("raw %s snapshot missing: %v", table, rows.Err())
		}
		if err := rows.Scan(pointers...); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		if rows.Next() || rows.Err() != nil {
			rows.Close()
			t.Fatalf("raw %s snapshot must contain one row: %v", table, rows.Err())
		}
		rows.Close()
		snapshot[table] = row
	}
	return snapshot
}
