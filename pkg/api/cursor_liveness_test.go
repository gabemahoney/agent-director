package api_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/sqliteobserve"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Synthetic persisted bindings isolate public read behavior from protocol execution.
func cursorReadLaunch(t *testing.T, existing ...*killEnv) (*killEnv, *api.Client, store.CursorRuntime, string) {
	t.Helper()
	var e *killEnv
	pid := apitest.TestPanePID
	if len(existing) > 0 {
		e = existing[0]
		pid = e.newPID()
	} else {
		e = newKillEnv(t)
	}
	id, err := apitest.SeedSpawn(e.dbPath, "", store.StatePending, "", "on", "", false,
		apitest.WithAgent(store.AgentCursor), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: strings.ReplaceAll(uuid.NewString(), "-", "")[:16], PanePID: pid, PaneStarttime: apitest.LinuxProcStarttime}))
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
	req := store.RelayRequest{RequestToken: uuid.NewString(), ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)}
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
			} else if tc.name == "unknown" {
				assertCursorRecoveryNote(t, before, after, "probe_eacces")
				if !pr.AwaitsAnswer() {
					t.Fatal("unknown closed request")
				}
			} else if !reflect.DeepEqual(before, after) || !pr.AwaitsAnswer() {
				t.Fatal("alive mutated launch or request")
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

func TestCursorReadLostGatewayRetainedDescriptorAndFailure(t *testing.T) {
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
				if err := read(); !tc.closeFailure {
					if err != nil {
						t.Fatalf("examined obsolete descriptor blocked current gateway death: %v", err)
					}
				} else if err == nil {
					t.Fatal("known-dead unsafe binding reported success")
				} else if tc.closeFailure && !strings.Contains(err.Error(), "injected write failure") {
					t.Fatal(err)
				}
			}
			prAfter, err := e.st.GetPermissionRequest(b.InstanceID, token)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.closeFailure {
				after := e.columns(t, b.InstanceID)
				if after.State != store.StateMissing || after.RowVersion != before.RowVersion.(int64)+1 || !prAfter.Closed() || prAfter.ProvenGone() || prAfter.ProvenGoneHow != "" {
					t.Fatalf("retained descriptor close not atomic/request-safe: %+v %+v", after, prAfter)
				}
			} else if !reflect.DeepEqual(before, e.columns(t, b.InstanceID)) || !reflect.DeepEqual(prBefore, prAfter) {
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

// b.x9o: a stale sample for A must not suppress B's independent result or
// guarded write. Failed A retains applicable stored facts without a death claim.
func TestCursorReadIndependentRecoveryAfterStaleSample(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"list", "find-missing"} {
		t.Run(surface, func(t *testing.T) {
			e, c, a, tokenA := cursorReadLaunch(t)
			_, _, b, tokenB := cursorReadLaunch(t, e)
			e.pc.Set(a.GatewayPID, procfix.Gone())
			e.pc.Set(b.GatewayPID, procfix.Gone())
			beforeA, beforeB := e.columns(t, a.InstanceID), e.columns(t, b.InstanceID)
			api.SetProcCheckerForTest(c, &cursorReadBarrier{pc: e.pc, once: func() {
				cursorReadSQL(t, e.dbPath, "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?", a.InstanceID)
			}})
			if surface == "list" {
				got, err := c.List(api.ListParams{})
				if err != nil || len(got.Spawns) != 2 {
					t.Fatalf("independent list lost selected results: %+v %v", got, err)
				}
				seen := map[string]bool{}
				for _, row := range got.Spawns {
					if seen[row.InstanceID] {
						t.Fatalf("duplicate selected row: %+v", got)
					}
					seen[row.InstanceID] = true
					switch row.InstanceID {
					case a.InstanceID:
						if row.State != store.StateWorking || len(row.PermissionRequests) != 1 || row.PermissionRequests[0].RequestToken != tokenA {
							t.Fatalf("stale A lost its applicable stored projection: %+v", row)
						}
					case b.InstanceID:
						if row.State != store.StateMissing || len(row.PermissionRequests) != 0 {
							t.Fatalf("independent B result suppressed: %+v", row)
						}
					default:
						t.Fatalf("list added unselected row: %+v", row)
					}
				}
			} else {
				got, err := c.FindMissing(context.Background())
				if err != nil || got.Count != 1 || len(got.IDs) != 1 || got.IDs[0] != b.InstanceID {
					t.Fatalf("independent sweep=%+v %v", got, err)
				}
			}
			afterA, afterB := e.columns(t, a.InstanceID), e.columns(t, b.InstanceID)
			reqA, err := e.st.GetPermissionRequest(a.InstanceID, tokenA)
			if err != nil {
				t.Fatal(err)
			}
			reqB, err := e.st.GetPermissionRequest(b.InstanceID, tokenB)
			if err != nil {
				t.Fatal(err)
			}
			if afterA.State != beforeA.State || afterA.RowVersion != beforeA.RowVersion.(int64)+1 || !reqA.AwaitsAnswer() {
				t.Fatalf("stale A changed: %+v %+v", afterA, reqA)
			}
			if afterB.State != store.StateMissing || afterB.RowVersion != beforeB.RowVersion.(int64)+1 || !reqB.Closed() || !reqB.DeliveredAt.IsZero() {
				t.Fatalf("independent B did not recover atomically: %+v %+v", afterB, reqB)
			}
		})
	}
}

// b.x9o code-review03: the late race is after guarded projection, immediately
// before the first real deferred BEGIN. It must neither claim A missing nor
// suppress B. The single-row surfaces retain their own conflict error.
func TestCursorReadIndependentRecoveryAfterDeferredWriteRejection(t *testing.T) {
	t.Parallel()
	for _, surface := range []string{"list", "find-missing", "get", "status"} {
		t.Run(surface, func(t *testing.T) {
			e, c, a, tokenA := cursorReadLaunch(t)
			_, _, b, tokenB := cursorReadLaunch(t, e)
			if err := apitest.SeedParentChild(e.dbPath, a.InstanceID, b.InstanceID); err != nil {
				t.Fatal(err)
			}
			familyBefore := readFamilyHistories(t, e.dbPath, []string{a.InstanceID, b.InstanceID})
			if familyBefore[a.InstanceID].state != store.StateWorking || familyBefore[b.InstanceID].state != store.StateWorking {
				t.Fatalf("fixture must retain both live family contributions: %+v", familyBefore)
			}
			e.pc.Set(a.GatewayPID, procfix.Gone())
			e.pc.Set(b.GatewayPID, procfix.Gone())
			beforeA, beforeB := e.columns(t, a.InstanceID), e.columns(t, b.InstanceID)
			requestA, err := e.st.GetPermissionRequest(a.InstanceID, tokenA)
			if err != nil {
				t.Fatal(err)
			}
			requestB, err := e.st.GetPermissionRequest(b.InstanceID, tokenB)
			if err != nil {
				t.Fatal(err)
			}
			multi := surface == "list" || surface == "find-missing"
			wantProjected := int64(1)
			if multi {
				wantProjected = 2
			}
			writer, err := sql.Open("sqlite", e.dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			var projected, begins atomic.Int64
			var raced atomic.Bool
			stop, err := api.ObserveClientSQLiteForTest(c, func(d driver.Driver) driver.Driver {
				return sqliteobserve.Wrap(d, sqliteobserve.Hooks{
					AfterNext: func(_ context.Context, q string, err error) {
						if err == nil && strings.Contains(q, "AS cursor_missing_proof") {
							projected.Add(1)
						}
					},
					Before: func(ctx context.Context, q string) error {
						if q != "BEGIN IMMEDIATE" {
							return nil
						}
						begins.Add(1)
						if !raced.CompareAndSwap(false, true) {
							return nil
						}
						if projected.Load() < wantProjected {
							return fmt.Errorf("deferred write preceded guarded projection: got %d rows, want >= %d", projected.Load(), wantProjected)
						}
						_, err := writer.ExecContext(ctx, "UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?", a.InstanceID)
						return err
					},
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := stop(ctx); err != nil {
					t.Errorf("stop SQLite observer: %v", err)
				}
			}()
			// B's write stamps the store's own SQL clock, so its record time is
			// checked against wall-clock bounds around the read.
			from := time.Now().UnixMilli()
			switch surface {
			case "list":
				// b.x9o: A's reported (settled) value has no stored record, so the
				// rejected write's attempt shows alone.
				settle := store.SettleRead{SettleMs: settleMax, NowMs: e.clock.Now().UnixMilli()}
				if sp, err := e.st.GetSpawnSettled(a.InstanceID, settle); err != nil || sp.StateConfirmation(settle) != store.UnknownConfirmation(store.ReasonNoEvidence) {
					t.Fatalf("fixture: A's stored state record %+v, %v; want none", sp.StateConfirmation(settle), err)
				}
				got, err := c.List(api.ListParams{})
				if err != nil || len(got.Spawns) != 2 {
					t.Fatalf("late refusal lost selected results: %+v %v", got, err)
				}
				seen := map[string]bool{}
				for _, row := range got.Spawns {
					if seen[row.InstanceID] {
						t.Fatalf("duplicate selected row: %+v", got)
					}
					seen[row.InstanceID] = true
					// Family comes from the common stored snapshot, before either
					// deferred write; no refused missing proof may remove A.
					if row.FamilyState != store.StateWorking || row.DisplayedState != "" || row.IdleScreenAvailability != "unavailable" {
						t.Fatalf("incoherent stored family/Cursor display: %+v", row)
					}
					switch row.InstanceID {
					case a.InstanceID:
						if row.State != store.StateWorking || row.ParentID != "" || len(row.PermissionRequests) != 1 {
							t.Fatalf("refused A retained speculative terminal facts: %+v", row)
						}
						// b.x9o: the rejected write is this read's latest attempt for
						// the stored value list falls back to, never for missing.
						if want := "not_confirmed|-|-|" + e.clock.Now().UTC().Format(displayTS) + "|stale_observation"; conf(row.Confirmations.State) != want {
							t.Fatalf("refused A state record %s; want %s", conf(row.Confirmations.State), want)
						}
						want := api.PermissionRequestInfo{RequestID: requestA.RequestID, RequestToken: tokenA, ToolName: requestA.ToolName, ToolInput: requestA.ToolInput, RequestedAt: requestA.CreatedAt,
							RequestDelivery: api.RequestDelivery{Delivery: api.DeliveryNotConfirmed, ConfirmBy: requestA.SettledAt, PaneAnswer: "none"}}
						gotJSON, err := json.Marshal(row.PermissionRequests[0])
						if err != nil {
							t.Fatal(err)
						}
						wantJSON, err := json.Marshal(want)
						if err != nil || string(gotJSON) != string(wantJSON) {
							t.Fatalf("fallback request lost coherent undecided facts: got=%s want=%s err=%v", gotJSON, wantJSON, err)
						}
					case b.InstanceID:
						if row.State != store.StateMissing || row.ParentID != a.InstanceID || len(row.PermissionRequests) != 0 {
							t.Fatalf("independent B result lost: %+v", row)
						}
						// b.x9o: list projects B before its deferred write commits.
						if want := "pending|-|-|" + e.clock.Now().UTC().Format(displayTS) + "|operation_pending"; conf(row.Confirmations.State) != want {
							t.Fatalf("projected B state record %s; want %s", conf(row.Confirmations.State), want)
						}
					default:
						t.Fatalf("unselected row added: %+v", row)
					}
				}
			case "find-missing":
				got, err := c.FindMissing(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				assertLists(t, got, []string{b.InstanceID}, nil)
			case "get":
				if _, err := c.Get(a.InstanceID); err == nil || !strings.Contains(err.Error(), "changed during reconciliation") {
					t.Fatalf("direct Get lost its stale error: %v", err)
				}
			case "status":
				if _, err := c.Status(a.InstanceID); err == nil || !strings.Contains(err.Error(), "changed during reconciliation") {
					t.Fatalf("direct Status lost its stale error: %v", err)
				}
			}
			if !raced.Load() || begins.Load() != wantProjected {
				t.Fatalf("race/bounded writes not reached: raced=%v BEGIN=%d want=%d", raced.Load(), begins.Load(), wantProjected)
			}
			beforeA.RowVersion = beforeA.RowVersion.(int64) + 1
			afterA, afterB := e.columns(t, a.InstanceID), e.columns(t, b.InstanceID)
			afterRequestA, err := e.st.GetPermissionRequest(a.InstanceID, tokenA)
			if err != nil || !reflect.DeepEqual(beforeA, afterA) || afterRequestA != requestA {
				t.Fatalf("refused A changed row or request authority: row=%+v request=%+v err=%v", afterA, afterRequestA, err)
			}
			afterRequestB, err := e.st.GetPermissionRequest(b.InstanceID, tokenB)
			if err != nil {
				t.Fatal(err)
			}
			if multi {
				if afterB.State != store.StateMissing || afterB.RowVersion != beforeB.RowVersion.(int64)+1 || !afterRequestB.Closed() || !afterRequestB.DeliveredAt.IsZero() || afterRequestB.ProvenGone() {
					t.Fatalf("independent B did not close without delivery/proof: %+v %+v", afterB, afterRequestB)
				}
				// b.x9o: the exact gateway loss B's write acted on is its durable
				// missing record, confirmed and checked at the write's own time.
				to := time.Now().UnixMilli()
				sp, err := e.st.GetSpawn(b.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				rec := sp.StateConfirmation(store.SettleRead{})
				if want := (store.Confirmation{Status: store.ConfirmationConfirmed, ConfirmedAtMillis: rec.ConfirmedAtMillis, Source: store.EvidenceProcess,
					CheckedAtMillis: rec.ConfirmedAtMillis}); rec != want || rec.ConfirmedAtMillis < from || rec.ConfirmedAtMillis > to {
					t.Fatalf("B's stored state record %+v; want missing confirmed by process at one time in [%d, %d], no reason", rec, from, to)
				}
			} else if !reflect.DeepEqual(beforeB, afterB) || requestB != afterRequestB {
				t.Fatal("single-row read changed excluded B")
			}
			familyAfter := readFamilyHistories(t, e.dbPath, []string{a.InstanceID, b.InstanceID})
			if familyAfter[a.InstanceID] != familyBefore[a.InstanceID] {
				t.Fatalf("refused A changed its family history: before=%+v after=%+v", familyBefore, familyAfter)
			}
			if multi && familyAfter[b.InstanceID].state != store.StateWaiting || !multi && familyAfter[b.InstanceID] != familyBefore[b.InstanceID] {
				t.Fatalf("independent B family history: before=%+v after=%+v", familyBefore, familyAfter)
			}
			if calls := e.rec.SocketCalls(); len(calls) != 0 {
				t.Errorf("recovery dispatched tmux calls: %+v", calls)
			}
		})
	}
}

// A liveness note is the only permitted new fact for an uncertain live row.
func assertCursorRecoveryNote(t *testing.T, before, after apitest.SpawnColumns, note string) {
	t.Helper()
	if after.LivenessNote != note || after.LivenessUnverifiedSince == nil || after.RowVersion != before.RowVersion.(int64)+1 {
		t.Fatalf("note=%+v want %s once after %+v", after, note, before)
	}
	after.LivenessNote, after.LivenessUnverifiedSince, after.RowVersion = before.LivenessNote, before.LivenessUnverifiedSince, before.RowVersion
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("note changed unrelated columns: before=%+v after=%+v", before, after)
	}
}
