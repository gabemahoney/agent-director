package store_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorReuseRetiresOldBindingAndPublicationDefeatsRestore(t *testing.T) {
	for _, agents := range [][]string{{"cursor"}, {"claude", "cursor"}, {"codex", "cursor"}} {
		t.Run(agents[0], func(t *testing.T) {
			f, old, binding := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for i, agent := range agents {
				// A finished synthetic launch qualifies through the real read/reset transaction.
				if _, err := db.Exec("UPDATE spawns SET state='ended' WHERE instance_id=?", old.InstanceID); err != nil {
					t.Fatal(err)
				}
				fresh := reuseFresh("", false)
				fresh.Agent = agent
				fresh.Identity.Token = []string{"1111111111111111", "2222222222222222"}[i]
				r := rrDoReset(t, f, old.InstanceID, fresh)
				if got, err := f.s.RecordLaunchIdentity(old.InstanceID, r.version, fresh.Identity.Token, fresh.Identity); err != nil || got != store.CondApplied {
					t.Fatalf("publish pane: %v %v", got, err)
				}
				row, err := f.s.GetSpawn(old.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				before := f.rawColumns(old.InstanceID)
				for _, event := range []string{"UserPromptSubmit", "Stop", "SessionEnd"} {
					g := store.HookGate{Event: event, ParentPID: binding.GatewayPID, ParentStart: binding.GatewayStart, SessionID: binding.SessionID, Cursor: &binding, Examined: old.Snapshot}
					if _, a, err := f.s.ApplyHookTransitionResult(old.InstanceID, g, store.StateEnded, false, event, "", false); err != nil || a.Applied {
						t.Fatalf("old %s applied: %+v %v", event, a, err)
					}
				}
				if ok, err := f.s.MarkCursorRuntimeGone(binding); err != nil || ok {
					t.Fatalf("old gateway retirement: %v %v", ok, err)
				}
				if ok, err := f.s.SetCursorRuntimeSession(binding, "stale-session"); err != nil || ok {
					t.Fatalf("old connection publication: %v %v", ok, err)
				}
				if !reflect.DeepEqual(before, f.rawColumns(old.InstanceID)) {
					t.Fatal("old binding changed reused row")
				}
				if agent == "cursor" {
					current := binding
					current.Token, current.Life = row.Identity.Token, row.LifeNumber
					current.GatewayPID, current.GatewayStart = row.Identity.PanePID, row.Identity.PaneStarttime
					current.Connection, current.SessionID = "replacement-connection", ""
					if ok, err := f.s.RegisterCursorRuntime(row, current); err != nil || !ok {
						t.Fatalf("replacement register: %v %v", ok, err)
					}
					if ok, err := f.s.SetCursorRuntimeSession(current, "replacement-session"); err != nil || !ok {
						t.Fatalf("replacement report binding: %v %v", ok, err)
					}
					current.SessionID = "replacement-session"
					gate := store.HookGate{Event: "SessionStart", ParentPID: current.GatewayPID, ParentStart: current.GatewayStart, SessionID: current.SessionID, Cursor: &current, SessionStart: true, Examined: row.Snapshot}
					if applied, _, err := f.s.RecordSessionStartIdentity(row.InstanceID, gate, "/synthetic/reused-launch.jsonl", true); err != nil || !applied.Applied {
						t.Fatalf("replacement report: %+v %v", applied, err)
					}
					row, err = f.s.GetSpawn(row.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
					if ok, err := f.s.RotateCursorSession(row, gate, "replacement-rotated", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err != nil || !ok {
						t.Fatalf("reused rotation: %v %v", ok, err)
					}
					rotated := f.rawColumns(row.InstanceID)
					gate.Event, gate.SessionStart = "SessionEnd", false
					if _, applied, err := f.s.ApplyHookTransitionResult(row.InstanceID, gate, store.StateEnded, false, "SessionEnd", "", false); err != nil || applied.Applied {
						t.Fatalf("retired conversation ended reused launch: %+v %v", applied, err)
					}
					if !reflect.DeepEqual(rotated, f.rawColumns(row.InstanceID)) {
						t.Fatal("old conversation changed rotated reused row")
					}
					live, err := f.s.ReadCursorRuntime(row.InstanceID)
					if err != nil || live.SessionID != "replacement-rotated" || live.Terminated {
						t.Fatalf("reused rotated binding: %+v %v", live, err)
					}
					// Real death of this continuing gateway still retires the binding after rotation.
					if ok, err := f.s.MarkCursorRuntimeGone(live); err != nil || !ok {
						t.Fatalf("reused rotated runtime death: %v %v", ok, err)
					}

				}
				published := f.rawColumns(old.InstanceID)
				if got, err := f.s.RestoreAfterFailedReuse(old.InstanceID, r.version, r.prior, time.Now()); err != nil || got != store.CondChanged {
					t.Fatalf("stale restore after publication: %v %v", got, err)
				}
				if !reflect.DeepEqual(published, f.rawColumns(old.InstanceID)) {
					t.Fatal("stale restore changed published launch")
				}
			}
		})
	}
}

func TestCursorMissingRefusesReplacedAuthority(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		for _, tc := range []struct{ name, query string }{
			{"connection rotated", "UPDATE cursor_runtimes SET bound_connection_id='replacement' WHERE bound_instance_id=?"},
			{"session rotated", "UPDATE cursor_runtimes SET bound_session_id='replacement' WHERE bound_instance_id=?"},
			{"runtime PID replaced", "UPDATE cursor_runtimes SET bound_runtime_pid=bound_runtime_pid+1 WHERE bound_instance_id=?"},
			{"runtime start replaced", "UPDATE cursor_runtimes SET bound_runtime_start='replacement' WHERE bound_instance_id=?"},
			{"runtime termination changed", "UPDATE cursor_runtimes SET bound_terminated=1 WHERE bound_instance_id=?"},
			{"launch replaced", "UPDATE spawns SET launch_token='replacement',life_number=life_number+1 WHERE instance_id=?"},
			{"row refreshed", "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?"},
			{"token replaced", "UPDATE spawns SET launch_token='replacement' WHERE instance_id=?"},
			{"life replaced", "UPDATE spawns SET life_number=life_number+1 WHERE instance_id=?"},
			{"gateway replaced", "UPDATE spawns SET pane_pid=pane_pid+1 WHERE instance_id=?"},
			{"gateway start replaced", "UPDATE spawns SET pane_starttime='replacement' WHERE instance_id=?"},
			{"input pipe replaced", "UPDATE cursor_runtimes SET bound_input_pipe='replacement' WHERE bound_instance_id=?"},
			{"output pipe replaced", "UPDATE cursor_runtimes SET bound_output_pipe='replacement' WHERE bound_instance_id=?"},
			{"input fd replaced", "UPDATE cursor_runtimes SET bound_input_fd=bound_input_fd+1 WHERE bound_instance_id=?"},
			{"output fd replaced", "UPDATE cursor_runtimes SET bound_output_fd=bound_output_fd+1 WHERE bound_instance_id=?"},
			{"pane replaced", "UPDATE spawns SET pane_id='%replacement' WHERE instance_id=?"},
			{"socket replaced", "UPDATE spawns SET tmux_socket='replacement' WHERE instance_id=?"},
			{"server pid replaced", "UPDATE spawns SET tmux_server_pid=COALESCE(tmux_server_pid,0)+1 WHERE instance_id=?"},
			{"server start replaced", "UPDATE spawns SET tmux_server_started=COALESCE(tmux_server_started,0)+1 WHERE instance_id=?"},
			{"server starttime replaced", "UPDATE spawns SET tmux_server_starttime='replacement' WHERE instance_id=?"},
			{"transcript replaced", "UPDATE spawns SET jsonl_path='replacement' WHERE instance_id=?"},
			{"row session replaced", "UPDATE spawns SET session_id='replacement' WHERE instance_id=?"},
			{"descriptor token replaced", "UPDATE cursor_runtimes SET bound_launch_token='replacement' WHERE bound_instance_id=?"},
			{"descriptor life replaced", "UPDATE cursor_runtimes SET bound_life_number=bound_life_number+1 WHERE bound_instance_id=?"},
			{"descriptor gateway replaced", "UPDATE cursor_runtimes SET bound_gateway_pid=bound_gateway_pid+1 WHERE bound_instance_id=?"},
			{"descriptor gateway start replaced", "UPDATE cursor_runtimes SET bound_gateway_start='replacement' WHERE bound_instance_id=?"},
		} {
			if fatal && tc.name == "runtime termination changed" {
				continue
			}
			name := map[bool]string{false: "strict/", true: "fatal/"}[fatal] + tc.name
			t.Run(name, func(t *testing.T) {
				f, row, b := cursorRowBinding(t)
				mix := f.seedCloseMix(row.InstanceID)
				if _, err := apitest.SeedPermissionRequest(f.path, row.InstanceID, "Bash"); err != nil {
					t.Fatal(err)
				}
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
				requestsBefore, err := f.s.PermissionRequestsForSpawn(row.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				closeMissing := f.s.MarkCursorMissingIfSameLaunch
				if fatal {
					closeMissing = f.s.MarkFailedCursorRuntimeMissing
				}
				prior, result, err := closeMissing(row, b)
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
				requestsAfter, err := f.s.PermissionRequestsForSpawn(row.InstanceID)
				if err != nil || !reflect.DeepEqual(requestsBefore, requestsAfter) {
					t.Fatalf("refused close changed requests: %v", err)
				}
			})
		}
	}
}

func TestFailedCursorRuntimeMissingTerminationAndTerminalGuards(t *testing.T) {
	for _, state := range []string{store.StatePending, store.StateWaiting, store.StateEnded, store.StateMissing} {
		for _, bits := range []struct {
			name           string
			durable, proof bool
		}{
			{"unretired", false, false}, {"retirement advanced", true, false}, {"retired", true, true}, {"proof ahead of durable", false, true},
		} {
			t.Run(state+"/"+bits.name, func(t *testing.T) {
				f, row, b := cursorRowBinding(t)
				db, err := sql.Open("sqlite", f.path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err = db.Exec("UPDATE spawns SET state=? WHERE instance_id=?", state, row.InstanceID); err != nil {
					t.Fatal(err)
				}
				if bits.durable {
					if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || !ok {
						t.Fatalf("retire %v %v", ok, err)
					}
				}
				row, err = f.s.GetSpawn(row.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				before := f.rawColumns(row.InstanceID)
				b.Terminated = bits.proof
				prior, result, err := f.s.MarkFailedCursorRuntimeMissing(row, b)
				if store.IsLiveState(state) && (!bits.proof || bits.durable) {
					after, readErr := f.s.GetSpawn(row.InstanceID)
					if err != nil || result != store.CondApplied || prior != state || readErr != nil || after.State != store.StateMissing || after.RowVersion != row.RowVersion+1 {
						t.Fatalf("fatal close: prior=%s result=%v err=%v after=%+v read=%v", prior, result, err, after, readErr)
					}
				} else if err != nil || result != store.CondChanged || prior != "" || !reflect.DeepEqual(before, f.rawColumns(row.InstanceID)) {
					t.Fatalf("terminal no-op: %s %v %v", prior, result, err)
				}
			})
		}
	}
}

func TestFailedCursorRuntimeCannotCloseRetainedObsoleteDescriptor(t *testing.T) {
	for _, query := range []string{
		"UPDATE spawns SET launch_token='replacement' WHERE instance_id=?",
		"UPDATE spawns SET life_number=life_number+1 WHERE instance_id=?",
		"UPDATE spawns SET pane_pid=pane_pid+1 WHERE instance_id=?",
		"UPDATE spawns SET pane_starttime='replacement' WHERE instance_id=?",
	} {
		t.Run(query, func(t *testing.T) {
			f, row, b := cursorRowBinding(t)
			db, err := sql.Open("sqlite", f.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(query, row.InstanceID); err != nil {
				t.Fatal(err)
			}
			row, err = f.s.GetSpawn(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			before := f.rawColumns(row.InstanceID)
			if prior, result, err := f.s.MarkFailedCursorRuntimeMissing(row, b); err != nil || result != store.CondChanged || prior != "" {
				t.Fatalf("old runtime closed replacement: %s %v %v", prior, result, err)
			}
			if !reflect.DeepEqual(before, f.rawColumns(row.InstanceID)) {
				t.Fatal("old runtime changed replacement")
			}
		})
	}
}

func TestFailedCursorRuntimeMissingDoesNotWaitForWriter(t *testing.T) {
	e := newCursorTrailEnv(t)
	e.insert(t, "held")
	row, err := e.f.s.GetSpawn(e.b.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	before := e.f.rawColumns(row.InstanceID)
	request := e.read(t, "held")
	db, err := sql.Open("sqlite", e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	start := time.Now()
	prior, result, err := e.f.s.MarkFailedCursorRuntimeMissing(row, e.b)
	if !errors.Is(err, store.ErrStoreBusy) || prior != "" || result == store.CondApplied || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("no-wait close: %s %v %v elapsed=%s", prior, result, err, time.Since(start))
	}
	view := e.f.s.WithCursorMissingReads([]store.CursorMissingProof{{Row: row, Binding: e.b}})
	projected, err := view.GetSpawn(row.InstanceID)
	if err != nil || projected.State != store.StateMissing {
		t.Fatalf("busy read projection: %+v %v", projected, err)
	}
	if !reflect.DeepEqual(before, e.f.rawColumns(row.InstanceID)) || !reflect.DeepEqual(request, e.read(t, "held")) {
		t.Fatal("busy close mutated row/request")
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
