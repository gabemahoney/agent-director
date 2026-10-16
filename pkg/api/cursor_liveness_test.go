package api_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Synthetic persisted bindings isolate public read behavior from protocol execution.
func cursorReadLaunch(t *testing.T) (*killEnv, *api.Client, store.CursorRuntime, string) {
	t.Helper()
	e := newKillEnv(t)
	id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, "", "on", "", false,
		apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: trailToken, PanePID: apitest.TestPanePID, PaneStarttime: apitest.LinuxProcStarttime}))
	if err != nil {
		t.Fatal(err)
	}
	row, err := e.st.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	b := store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, RuntimePID: 12345, RuntimeStart: apitest.LinuxProcStarttime, Connection: "read-connection", InputPipe: "pipe:[1001]", OutputPipe: "pipe:[1002]", InputFD: 11, OutputFD: 12}
	if ok, err := e.st.RegisterCursorRuntime(row, b); err != nil || !ok {
		t.Fatalf("register: %v %v", ok, err)
	}
	if ok, err := e.st.SetCursorRuntimeSession(b, "read-session"); err != nil || !ok {
		t.Fatalf("session: %v %v", ok, err)
	}
	b.SessionID = "read-session"
	gate := store.HookGate{Event: "SessionStart", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b, SessionStart: true, Examined: row.Snapshot}
	if a, _, err := e.st.RecordSessionStartIdentity(id, gate, "synthetic-transcript", true); err != nil || !a.Applied {
		t.Fatalf("start: %+v %v", a, err)
	}
	gate.Event = "PreToolUse"
	gate.SessionStart = false
	req := store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)}
	if _, a, err := e.st.InsertRelayRequest(id, gate, req, 0, store.DefaultLockWait); err != nil || !a.Applied {
		t.Fatalf("request: %+v %v", a, err)
	}
	c := e.settleClient(t)
	if err := apitest.SetSettle(e.dbPath, id, store.StateWorking, e.clock.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return e, c, b, req.RequestToken
}

func cursorReadSQL(t *testing.T, path, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestCursorReadGatewayVerdicts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		gateway procfix.Process
		missing bool
	}{
		{"same start alive", procfix.Alive(apitest.LinuxProcStarttime), false},
		{"unknown", procfix.Unreadable(), false},
		{"recycled PID", procfix.Alive(apitest.DarwinProcStarttime), true},
		{"gone with live runtime", procfix.Gone(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			e.pc.Set(b.GatewayPID, tc.gateway)
			e.pc.Set(b.RuntimePID, procfix.Alive(b.RuntimeStart))
			sibling, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "", "", false, apitest.WithAgent(store.AgentCursor))
			if err != nil {
				t.Fatal(err)
			}
			sentinel, err := apitest.SeedSpawn(e.dbPath, "", store.StateWorking, "", "", "", false)
			if err != nil {
				t.Fatal(err)
			}
			before := e.columns(t, b.InstanceID)
			siblingBefore := e.columns(t, sibling)
			sentinelBefore := e.columns(t, sentinel)
			for i := 0; i < 2; i++ {
				status, err := c.Status(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				row, err := c.Get(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				want := store.StateWorking
				if tc.missing {
					want = store.StateMissing
				}
				if status.State != want || row.State != want {
					t.Fatalf("status/get: %s/%s want %s", status.State, row.State, want)
				}
			}
			after := e.columns(t, b.InstanceID)
			pr, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if tc.missing {
				if after.RowVersion != before.RowVersion.(int64)+1 || pr.AwaitsAnswer() {
					t.Fatalf("missing not a single atomic close: %+v %+v", after, pr)
				}
			} else if !reflect.DeepEqual(before, after) || !pr.AwaitsAnswer() {
				t.Fatal("alive/unknown mutated launch or request")
			}
			if !reflect.DeepEqual(siblingBefore, e.columns(t, sibling)) || !reflect.DeepEqual(sentinelBefore, e.columns(t, sentinel)) {
				t.Fatal("read swept another row")
			}
			calls := len(e.pc.StartTimeCalls())
			if tc.missing {
				if _, err := c.Get(b.InstanceID); err != nil {
					t.Fatal(err)
				}
				if len(e.pc.StartTimeCalls()) != calls {
					t.Fatal("terminal read probed again")
				}
			}
		})
	}
}

func TestCursorReadLostGatewayRefusesUnsafeSuccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query  string
		closeFailure bool
	}{
		{"stale session", "UPDATE cursor_runtimes SET bound_session_id='old-session' WHERE bound_instance_id=?", false},
		{"stale life", "UPDATE cursor_runtimes SET bound_life_number=bound_life_number+1 WHERE bound_instance_id=?", false},
		{name: "request close fails", closeFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			if tc.closeFailure {
				storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailPermissionDecision, b.InstanceID)
			} else {
				cursorReadSQL(t, e.dbPath, tc.query, b.InstanceID)
			}
			before := e.columns(t, b.InstanceID)
			prBefore, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			for _, read := range []func() error{func() error { _, err := c.Get(b.InstanceID); return err }, func() error { _, err := c.Status(b.InstanceID); return err }} {
				if err := read(); err == nil {
					t.Fatal("known-dead unsafe binding reported success")
				} else if tc.closeFailure && !strings.Contains(err.Error(), "injected write failure") {
					t.Fatal(err)
				}
			}
			prAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, e.columns(t, b.InstanceID)) || !reflect.DeepEqual(prBefore, prAfter) {
				t.Fatal("failed reconciliation partially committed")
			}
		})
	}
}

type cursorReadBarrier struct {
	pc   *procfix.Checker
	once func()
}

func (p *cursorReadBarrier) StartTime(pid int) (string, bool, bool) {
	if p.once != nil {
		f := p.once
		p.once = nil
		f()
	}
	return p.pc.StartTime(pid)
}

func TestCursorReadConcurrentLaunchChange(t *testing.T) {
	t.Parallel()
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "same launch snapshot changed", true: "replacement launch"}[replace], func(t *testing.T) {
			e, c, b, token := cursorReadLaunch(t)
			var committed apitest.SpawnColumns
			api.SetProcCheckerForTest(c, &cursorReadBarrier{pc: e.pc, once: func() {
				query := "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?"
				if replace {
					query = "UPDATE spawns SET launch_token='replacement', life_number=life_number+1, row_version=row_version+1 WHERE instance_id=?"
				}
				cursorReadSQL(t, e.dbPath, query, b.InstanceID)
				committed = e.columns(t, b.InstanceID)
			}})
			row, err := c.Get(b.InstanceID)
			if replace {
				if err != nil || row.State != store.StateWorking {
					t.Fatalf("replacement read: %+v %v", row, err)
				}
			} else if err == nil {
				t.Fatal("same dead launch conflict reported success")
			}
			if !reflect.DeepEqual(committed, e.columns(t, b.InstanceID)) {
				t.Fatal("reconciliation changed raced launch")
			}
			pr, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil || !pr.AwaitsAnswer() {
				t.Fatalf("raced request closed: %+v %v", pr, err)
			}
		})
	}
}
