package store_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

func TestCursorNativeReturningRowsCannotPublishLateAck(t *testing.T) {
	e := newCursorTrailEnv(t)
	e.insert(t, "late-ack")
	if written, err := e.f.s.DecideRelayRequest(e.b.InstanceID, "late-ack", "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !written {
		t.Fatalf("seed decision=%v,%v", written, err)
	}
	before := e.read(t, "late-ack")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	timer := time.AfterFunc(3*time.Second, unblock)
	defer func() { unblock(); timer.Stop() }()
	var enter sync.Once
	stopObserving := store.ObserveReturningRowsForTest(t, e.f.s, e.f.path, func(_ context.Context, q string, _ error) {
		if strings.Contains(q, "UPDATE permission_requests") {
			enter.Do(func() { close(entered); <-release })
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	type result struct {
		decision string
		acked    bool
		err      error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		d, a, err := e.f.s.WithContext(ctx).AckCursorDecision(e.b, "late-ack", false, e.until, e.until, 5*time.Second, cursorTrailCheck)
		done <- result{d, a, err}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("actual Cursor ack returning row was not reached")
	}
	select {
	case got := <-done:
		if got.decision != "" || got.acked || !errors.Is(got.err, store.ErrStoreBusy) {
			t.Errorf("late native Cursor row leaked ack: %+v", got)
		}
	case <-time.After(1500*time.Millisecond - time.Since(start)):
		t.Error("Cursor native row blocked past original 500ms + 1s")
	}
	unblock()
	stopObserving()
	if after := e.read(t, "late-ack"); !reflect.DeepEqual(before, after) {
		t.Errorf("late Cursor row fabricated durable ack: before=%+v after=%+v", before, after)
	}
}

// These store tests exercise persisted authority; controller tests own runtime proof validation.
type cursorTrailEnv struct {
	f     *v5Store
	b     store.CursorRuntime
	until time.Time
}

func newCursorTrailEnv(t *testing.T) *cursorTrailEnv {
	t.Helper()
	f, _, b := cursorRowBinding(t)
	if ok, err := f.s.SetCursorRuntimeSession(b, "trail-session"); err != nil || !ok {
		t.Fatalf("session: %v %v", ok, err)
	}
	b.SessionID = "trail-session"
	g := store.HookGate{Event: "SessionStart", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	if a, err := f.s.ApplyHookTransition(b.InstanceID, g, store.StateWorking, false, "SessionStart", "", false); err != nil || !a.Applied {
		t.Fatalf("start: %+v %v", a, err)
	}
	return &cursorTrailEnv{f: f, b: b, until: time.UnixMilli(time.Now().Add(time.Hour).UnixMilli())}
}

func cursorTrailCheck(store.Spawn) error { return nil }

func (e *cursorTrailEnv) requestInput(token string) store.RelayRequest {
	return store.RelayRequest{RequestToken: token, ToolUseID: "tool-" + token, ToolName: "Bash", ToolInput: `{"command":"private-payload"}`, SettledAt: e.until}
}

func (e *cursorTrailEnv) insert(t *testing.T, token string) {
	t.Helper()
	r := e.requestInput(token)
	if err := e.f.s.InsertCursorRequest(e.b, r, 0, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil {
		t.Fatal(err)
	}
}

func (e *cursorTrailEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *cursorTrailEnv) read(t *testing.T, token string) store.PermissionRow {
	t.Helper()
	// A second store connection sees only committed values.
	s, err := store.Open(e.f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.GetPermissionRequest(e.b.InstanceID, token)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *cursorTrailEnv) events(t *testing.T, mark int) []map[string]any {
	t.Helper()
	return store.TrailEventsSince(t, mark, "ad.row_mutation.committed", e.b.InstanceID)
}

func (e *cursorTrailEnv) beforeCommit(t *testing.T, mark int) *bool {
	t.Helper()
	seen := new(bool)
	store.BeforeCursorTrailExecForTest(t, e.f.s, e.f.path, func(query string) {
		if query == "COMMIT" && !*seen {
			*seen = true
			if events := e.events(t, mark); len(events) != 0 {
				t.Errorf("event before COMMIT: %v", events)
			}
		}
	})
	return seen
}

func (e *cursorTrailEnv) assertEvent(t *testing.T, mark int, token, kind, decision, reason string) store.PermissionRow {
	t.Helper()
	events := e.events(t, mark)
	if len(events) != 1 {
		t.Fatalf("mutation events = %v; want exactly one", events)
	}
	r := e.read(t, token)
	want := map[string]any{"source": "ad_store", "writer_process": "cursor_host", "mutation_kind": kind, "instance_id": e.b.InstanceID, "request_token": token, "request_id": float64(r.RequestID), "tool_name": r.ToolName, "decision": nil, "decision_reason": nil}
	if decision != "" {
		want["decision"] = decision
	}
	if reason != "" {
		want["decision_reason"] = reason
	}
	for k, v := range want {
		if got, exists := events[0][k]; !exists || got != v {
			t.Errorf("event %s = %v; want %v", k, got, v)
		}
	}
	for _, k := range []string{"tool_input", "rawOutput", "params", "options"} {
		if _, exists := events[0][k]; exists {
			t.Errorf("event exposes %s", k)
		}
	}
	if r.Decision != decision || r.DecisionReason != reason {
		t.Fatalf("independent committed row disagrees: %+v", r)
	}
	return r
}

func (e *cursorTrailEnv) sent(t *testing.T, token, decision string, complete bool) {
	t.Helper()
	i, ok, err := e.f.s.RecordPaneIntent(e.b.InstanceID, token, decision, store.ProcessIdentity{}, time.Now, store.DefaultLockWait, nil)
	if err != nil || !ok {
		t.Fatalf("intent: %v %v", ok, err)
	}
	if complete {
		if ok, err := e.f.s.RecordPaneSent(e.b.InstanceID, token, i, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("sent: %v %v", ok, err)
		}
	}
}

func TestCursorTrailAdmissionAndTimeout(t *testing.T) {
	e := newCursorTrailEnv(t)
	mark := store.TrailMark(t)
	seen := e.beforeCommit(t, mark)
	e.insert(t, "admission")
	r := e.assertEvent(t, mark, "admission", "insert", "", "")
	if !*seen || r.Hook != (store.ProcessIdentity{}) || r.MainTurnEndEligible {
		t.Fatal("admission lost transaction/evidence contract")
	}
	transitions := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", e.b.InstanceID)
	if len(transitions) != 1 || transitions[0]["source"] != "ad_spawn_store" || transitions[0]["prior_state"] != "working" || transitions[0]["new_state"] != "check_permission" || transitions[0]["triggering_event_name"] != "PermissionRequest" || transitions[0]["soft_refresh"] != false {
		t.Fatalf("admission transition: %v", transitions)
	}
	mark = store.TrailMark(t)
	seen = e.beforeCommit(t, mark)
	d, ack, err := e.f.s.AckCursorDecision(e.b, "admission", true, e.until, e.until, store.DefaultLockWait, cursorTrailCheck)
	if err != nil || !ack || d != "deny" || !*seen {
		t.Fatalf("timeout: %q %v %v", d, ack, err)
	}
	r = e.assertEvent(t, mark, "admission", "update", "deny", "timeout")
	if r.DeliveredAt.IsZero() {
		t.Fatal("timeout event without committed ack")
	}
	mark = store.TrailMark(t)
	if _, ack, err = e.f.s.AckCursorDecision(e.b, "admission", false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || ack || len(e.events(t, mark)) != 0 {
		t.Fatalf("duplicate ack: %v %v", ack, err)
	}
}

func TestCursorTrailNativeClaim(t *testing.T) {
	for _, mode := range []string{"direct allow", "direct deny", "consume sent"} {
		t.Run(mode, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			e.insert(t, "independent")
			decision := "allow"
			if mode == "direct deny" {
				decision = "deny"
			}
			if mode == "consume sent" {
				e.sent(t, "target", decision, true)
			}
			mark := store.TrailMark(t)
			seen := e.beforeCommit(t, mark)
			ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, "target", decision, e.until, store.DefaultLockWait, cursorTrailCheck)
			if err != nil || !ok || !*seen {
				t.Fatalf("claim: %v %v", ok, err)
			}
			r := e.assertEvent(t, mark, "target", "update", decision, "pane")
			if r.PaneAnswer != "sent" || r.PaneAs != decision || r.ProvenGone() || !r.DeliveredAt.IsZero() {
				t.Fatalf("native invented ack/proof: %+v", r)
			}
			if other := e.read(t, "independent"); other.PaneAnswered() || other.ProvenGone() || other.Decision != "" {
				t.Fatal("native changed independent hold")
			}
		})
	}
}

func TestCursorTrailProofFinalValuesAndDuplicate(t *testing.T) {
	for _, prior := range []string{"undecided", "API allow", "operator deny", "timeout deny", "native pane", "outside unknown", "incomplete proof"} {
		t.Run(prior, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			e.insert(t, "independent")
			decision, reason := "allow", "tool_ran"
			switch prior {
			case "API allow", "operator deny":
				d, why := "allow", ""
				if prior == "operator deny" {
					d, why, decision, reason = "deny", "operator", "deny", "operator"
				}
				if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, "target", d, why, store.WriterProcessDecide); err != nil || !ok {
					t.Fatalf("decide: %v %v", ok, err)
				}
			case "timeout deny":
				decision, reason = "deny", "timeout"
				if _, ok, err := e.f.s.AckCursorDecision(e.b, "target", true, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
					t.Fatalf("timeout: %v %v", ok, err)
				}
			case "native pane":
				decision, reason = "deny", "pane"
				if ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, "target", decision, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
					t.Fatalf("native: %v %v", ok, err)
				}
			case "outside unknown":
				reason = "pane_outside"
				if ok, err := e.f.s.RecordPaneOutside(e.b.InstanceID, "target", "unknown", store.DefaultLockWait, nil); err != nil || !ok {
					t.Fatalf("outside: %v %v", ok, err)
				}
			case "incomplete proof":
				e.exec(t, `UPDATE permission_requests SET proven_gone_at=1234,proven_gone_how='tool_ran',pane_answer='tool_ran',decision='allow',decided_at='2026-01-01 00:00:00' WHERE request_token='target'`)
			}
			before := e.read(t, "target")
			mark := store.TrailMark(t)
			seen := e.beforeCommit(t, mark)
			if err := e.f.s.ProveCursorToolRan(e.b, "target", "tool-target", true, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			r := e.assertEvent(t, mark, "target", "update", decision, reason)
			if !*seen || !r.ProvenGone() || r.ProvenGoneHow != "tool_ran" || r.MainTurnEndEligible || r.AgentID != "" {
				t.Fatalf("proof contract: %+v", r)
			}
			if (!before.ProvenGoneAt.IsZero() && !r.ProvenGoneAt.Equal(before.ProvenGoneAt)) || (!before.DecidedAt.IsZero() && !r.DecidedAt.Equal(before.DecidedAt)) {
				t.Fatal("proof rewrote existing timestamps")
			}
			wantPane := "tool_ran"
			if before.PaneAnswered() || !before.DeliveredAt.IsZero() {
				wantPane = before.PaneAnswer
			}
			if r.PaneAnswer != wantPane || !r.DeliveredAt.Equal(before.DeliveredAt) || r.PaneAs != before.PaneAs {
				t.Fatalf("proof changed prior claim/ack: before %+v after %+v", before, r)
			}
			if other := e.read(t, "independent"); other.ProvenGone() || other.Decision != "" {
				t.Fatal("proof released independent hold")
			}
			mark = store.TrailMark(t)
			e.rejectCommitOnUpdate(t)
			if err := e.f.s.ProveCursorToolRan(e.b, "target", "tool-target", true, cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			if len(e.events(t, mark)) != 0 || !reflect.DeepEqual(r, e.read(t, "target")) {
				t.Fatal("duplicate proof mutated row/timestamps or emitted")
			}
		})
	}
}

func TestCursorTrailAcknowledgementAddsNoMutation(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, "target", decision, "", store.WriterProcessDecide); err != nil || !ok {
				t.Fatalf("decide: %v %v", ok, err)
			}
			mark := store.TrailMark(t)
			d, ack, err := e.f.s.AckCursorDecision(e.b, "target", false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck)
			if err != nil || !ack || d != decision || len(e.events(t, mark)) != 0 || e.read(t, "target").DeliveredAt.IsZero() {
				t.Fatalf("pure ack: %s %v %v", d, ack, err)
			}
		})
	}
}

// b.eoe: a committed proof is the Cursor row's PostToolUse (or
// PostToolUseFailure); while a turn is open it moves a check_permission row no
// request still holds to working, as that hook would, and otherwise leaves the
// row alone. A delivered deny moves nothing: the row keeps check_permission
// until the turn's Stop.
func TestCursorTrailProofReleasesRowToWorking(t *testing.T) {
	for _, ran := range []bool{true, false} {
		event := "PostToolUse"
		if !ran {
			event = "PostToolUseFailure"
		}
		for _, tc := range []string{"released", "still held", "repeat proof", "waiting after Stop", "turn closed", "delivered deny"} {
			if (!ran && tc != "released" && tc != "delivered deny") || (ran && tc == "delivered deny") {
				continue
			}
			t.Run(event+"/"+tc, func(t *testing.T) {
				e := newCursorTrailEnv(t)
				e.insert(t, "target")
				decision := "allow"
				if tc == "delivered deny" {
					decision = "deny"
				}
				if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, "target", decision, "", store.WriterProcessDecide); err != nil || !ok {
					t.Fatalf("decide: %v %v", ok, err)
				}
				if d, ok, err := e.f.s.AckCursorDecision(e.b, "target", false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok || d != decision {
					t.Fatalf("ack: %q %v %v", d, ok, err)
				}
				prove := func(turnOpen bool) {
					t.Helper()
					fn := e.f.s.ProveCursorToolRan
					if !ran {
						fn = e.f.s.ProveCursorToolFailed
					}
					if err := fn(e.b, "target", "tool-target", turnOpen, cursorTrailCheck); err != nil {
						t.Fatal(err)
					}
				}
				want := store.StateCheckPermission
				switch tc {
				case "released":
					want = store.StateWorking
				case "still held":
					e.insert(t, "independent")
				case "repeat proof":
					e.insert(t, "independent")
					prove(true)
					e.exec(t, `UPDATE permission_requests SET proven_gone_at=1234,proven_gone_how='tool_ran' WHERE request_token='independent'`)
				case "waiting after Stop":
					g := store.HookGate{Event: "Stop", ParentPID: e.b.GatewayPID, ParentStart: e.b.GatewayStart, SessionID: e.b.SessionID, Cursor: &e.b}
					if a, err := e.f.s.ApplyHookTransition(e.b.InstanceID, g, store.StateWaiting, false, "Stop", "synthetic-path", true); err != nil || !a.Applied {
						t.Fatalf("stop: %+v %v", a, err)
					}
					want = store.StateWaiting
				}
				e.exec(t, `UPDATE spawns SET idle_since='2026-01-01 00:00:00',liveness_unverified_since='2026-01-01T00:00:00Z',liveness_note='note',state_changed_ms=1 WHERE instance_id=?`, e.b.InstanceID)
				wantBefore := store.StateCheckPermission
				if tc == "waiting after Stop" {
					wantBefore = store.StateWaiting
				}
				before, err := e.f.s.GetSpawn(e.b.InstanceID)
				if err != nil || before.State != wantBefore {
					t.Fatalf("before proof: state %q, want %q (%v)", before.State, wantBefore, err)
				}
				mark := store.TrailMark(t)
				prove(tc != "turn closed")
				if got := e.read(t, "target"); got.ProvenGoneHow != store.ProvenGoneToolRan {
					t.Fatalf("proof not written: %+v", got)
				}
				after, err := e.f.s.GetSpawn(e.b.InstanceID)
				if err != nil || after.State != want {
					t.Fatalf("after proof: state %q, want %q (%v)", after.State, want, err)
				}
				transitions := store.TrailEventsSince(t, mark, "ad.spawn.state_transition", e.b.InstanceID)
				if want != store.StateWorking {
					if len(transitions) != 0 || after.RowVersion != before.RowVersion || after.StateChangedMillis != before.StateChangedMillis || after.IdleSince != before.IdleSince || after.LivenessNote != before.LivenessNote || after.LivenessUnverifiedSince != before.LivenessUnverifiedSince {
						t.Fatalf("proof without a move changed the row: transitions=%v before=%+v after=%+v", transitions, before, after)
					}
					return
				}
				if after.RowVersion <= before.RowVersion || after.StateChangedMillis <= before.StateChangedMillis || after.IdleSince != "" || after.LivenessNote != "" || after.LivenessUnverifiedSince != "" {
					t.Fatalf("move to working skipped hook columns: before=%+v after=%+v", before, after)
				}
				if len(transitions) != 1 || transitions[0]["source"] != "ad_spawn_store" || transitions[0]["prior_state"] != store.StateCheckPermission || transitions[0]["new_state"] != store.StateWorking || transitions[0]["triggering_event_name"] != event || transitions[0]["soft_refresh"] != false {
					t.Fatalf("proof transition: %v", transitions)
				}
			})
		}
	}
}
