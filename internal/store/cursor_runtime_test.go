package store_test

import (
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"reflect"
	"testing"
	"time"
)

func cursorRowBinding(t *testing.T) (*v5Store, store.Spawn, store.CursorRuntime) {
	t.Helper()
	f := newV5Store(t)
	id := hgSeed(t, f, hgRow{state: store.StatePending, opts: []apitest.SpawnOption{apitest.WithAgent(store.AgentCursor), apitest.WithLifeNumber(2)}})
	row, err := f.s.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	b := store.CursorRuntime{InstanceID: id, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, RuntimePID: 12345, RuntimeStart: "synthetic-runtime-start", Connection: "synthetic-connection", InputPipe: "pipe:[1001]", OutputPipe: "pipe:[1002]", InputFD: 11, OutputFD: 12}
	ok, err := f.s.RegisterCursorRuntime(row, b)
	if err != nil || !ok {
		t.Fatalf("binding register = %v %v", ok, err)
	}
	return f, row, b
}
func TestCursorHookRequiresCurrentRuntimeConnectionAndImmediateParent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*store.HookGate)
	}{
		{"current", func(*store.HookGate) {}},
		{"no independent binding", func(g *store.HookGate) { g.Cursor = nil }},
		{"controller", func(g *store.HookGate) { g.ParentPID++ }},
		{"unreadable parent", func(g *store.HookGate) { g.ParentStart = "" }},
		{"wrong instance", func(g *store.HookGate) { g.Cursor.InstanceID += "other" }},
		{"wrong gateway", func(g *store.HookGate) { g.Cursor.GatewayPID++ }},
		{"gateway reused", func(g *store.HookGate) { g.Cursor.GatewayStart += "old" }},
		{"wrong output pipe", func(g *store.HookGate) { g.Cursor.OutputPipe = "pipe:[9999]" }},
		{"wrong input FD", func(g *store.HookGate) { g.Cursor.InputFD++ }},
		{"stale token", func(g *store.HookGate) { g.Cursor.Token += "old" }},
		{"old life", func(g *store.HookGate) { g.Cursor.Life-- }},
		{"runtime", func(g *store.HookGate) { g.Cursor.RuntimePID++ }},
		{"runtime start", func(g *store.HookGate) { g.Cursor.RuntimeStart += "old" }},
		{"wrong connection", func(g *store.HookGate) { g.Cursor.Connection += "other" }},
		{"wrong pipe", func(g *store.HookGate) { g.Cursor.InputPipe = "pipe:[9999]" }},
		{"wrong FD", func(g *store.HookGate) { g.Cursor.OutputFD++ }},
		{"wrong session", func(g *store.HookGate) { g.SessionID = "other" }},
		{"false termination", func(g *store.HookGate) { g.Cursor.Terminated = !g.Cursor.Terminated }},
	} {
		for _, event := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
			t.Run(tc.name+"/"+event, func(t *testing.T) {
				f, row, b := cursorRowBinding(t)
				if ok, err := f.s.SetCursorRuntimeSession(b, "synthetic-session"); err != nil || !ok {
					t.Fatalf("session bind %v %v", ok, err)
				}
				b.SessionID = "synthetic-session"
				if event == "SessionEnd" {
					if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || !ok {
						t.Fatalf("retire %v %v", ok, err)
					}
					b.Terminated = true
				}
				g := store.HookGate{Event: event, ParentPID: row.Identity.PanePID, ParentStart: row.Identity.PaneStarttime, SessionID: b.SessionID, Cursor: &b, SessionStart: event == "SessionStart", Examined: row.Snapshot}
				tc.change(&g)
				before := hgColumns(t, f, row.InstanceID)
				target := store.StateWorking
				if event == "SessionStart" {
					target = store.StateWaiting
				}
				if event == "SessionEnd" {
					target = store.StateEnded
				}
				var applied store.HookApplied
				var err error
				if event == "SessionStart" {
					applied, _, err = f.s.RecordSessionStartIdentity(row.InstanceID, g, "synthetic-transcript", true)
				} else {
					_, applied, err = f.s.ApplyHookTransitionResult(row.InstanceID, g, target, false, event, "", false)
				}
				if err != nil {
					t.Fatal(err)
				}
				if tc.name == "current" {
					if !applied.Applied {
						t.Fatalf("current binding refused %+v", applied)
					}
					return
				}
				if applied.Applied {
					t.Fatal("foreign/stale hook applied")
				}
				if after := hgColumns(t, f, row.InstanceID); !reflect.DeepEqual(before, after) {
					t.Fatal("refused hook changed row")
				}
			})
		}
	}
}
func TestCursorRuntimeStaleSessionAndReplacementRefused(t *testing.T) {
	f, row, b := cursorRowBinding(t)
	if ok, err := f.s.RegisterCursorRuntime(row, b); err != nil || ok {
		t.Fatalf("same launch registration replaced %v %v", ok, err)
	}
	if ok, err := f.s.SetCursorRuntimeSession(b, "s1"); err != nil || !ok {
		t.Fatalf("fresh session %v %v", ok, err)
	}
	if ok, err := f.s.SetCursorRuntimeSession(b, "s2"); err != nil || ok {
		t.Fatalf("fresh session rotated %v %v", ok, err)
	}
	stale := b
	stale.Token += "stale"
	if ok, err := f.s.MarkCursorRuntimeGone(stale); err != nil || ok {
		t.Fatalf("stale retirement %v %v", ok, err)
	}
	got, err := f.s.ReadCursorRuntime(row.InstanceID)
	if err != nil || got.SessionID != "s1" || got.Terminated {
		t.Fatalf("binding changed %+v %v", got, err)
	}
}

func TestCursorStaleReportCannotMutateReplacementLaunch(t *testing.T) {
	f, row, b := cursorRowBinding(t)
	if ok, err := f.s.SetCursorRuntimeSession(b, "old-session"); err != nil || !ok {
		t.Fatalf("session bind %v %v", ok, err)
	}
	b.SessionID = "old-session"
	if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || !ok {
		t.Fatalf("termination bind %v %v", ok, err)
	}
	b.Terminated = true
	gate := store.HookGate{Event: "SessionEnd", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	if _, applied, err := f.s.ApplyHookTransitionResult(row.InstanceID, gate, store.StateEnded, false, "SessionEnd", "", false); err != nil || !applied.Applied {
		t.Fatalf("close old life %+v %v", applied, err)
	}
	old, _, err := f.s.ReadForReuse(row.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := reuseFresh("", true)
	fresh.Agent = store.AgentCursor
	if applied, _, _, err := f.s.ResetForReuse(row.InstanceID, old.Snapshot, store.AgentCursor, fresh); err != nil || applied != store.CondApplied {
		t.Fatalf("replacement write %v %v", applied, err)
	}
	before := hgColumns(t, f, row.InstanceID)
	gate.Event = "UserPromptSubmit"
	if _, applied, err := f.s.ApplyHookTransitionResult(row.InstanceID, gate, store.StateWorking, false, "UserPromptSubmit", "", false); err != nil || applied.Applied {
		t.Fatalf("stale report after replacement %+v %v", applied, err)
	}
	if after := hgColumns(t, f, row.InstanceID); !reflect.DeepEqual(before, after) {
		t.Fatal("stale report mutated replacement")
	}
	if ok, err := f.s.MarkCursorRuntimeGone(b); err != nil || ok {
		t.Fatalf("old retirement crossed replacement %v %v", ok, err)
	}
}

// Seeded adversaries exercise fresh registration/session/retirement guards, not resume or rotation.
func TestCursorRuntimeConditionalBindingWrites(t *testing.T) {
	for _, field := range []string{"InstanceID", "Token", "Life", "GatewayPID", "GatewayStart", "RuntimePID", "RuntimeStart", "Connection", "InputPipe", "OutputPipe", "InputFD", "OutputFD"} {
		for _, operation := range []string{"session", "retirement"} {
			t.Run(field+"/"+operation, func(t *testing.T) {
				f, row, b := cursorRowBinding(t)
				stale := b
				v := reflect.ValueOf(&stale).Elem().FieldByName(field)
				if v.Kind() == reflect.String {
					v.SetString(v.String() + "other")
				} else {
					v.SetInt(v.Int() + 1)
				}
				var applied bool
				var err error
				if operation == "session" {
					applied, err = f.s.SetCursorRuntimeSession(stale, "foreign-session")
				} else {
					applied, err = f.s.MarkCursorRuntimeGone(stale)
				}
				if err != nil || applied {
					t.Fatalf("stale %s accepted %v %v", operation, applied, err)
				}
				got, err := f.s.ReadCursorRuntime(row.InstanceID)
				if err != nil || !reflect.DeepEqual(got, b) {
					t.Fatalf("refused operation mutated current binding %+v %v", got, err)
				}
			})
		}
	}
}

func TestCursorBindingDatabaseFailureIsObservable(t *testing.T) {
	for _, operation := range []string{"registration", "session", "retirement"} {
		t.Run(operation, func(t *testing.T) {
			f, row, b := cursorRowBinding(t)
			if err := f.s.Close(); err != nil {
				t.Fatal(err)
			}
			var applied bool
			var err error
			switch operation {
			case "registration":
				applied, err = f.s.RegisterCursorRuntime(row, b)
			case "session":
				applied, err = f.s.SetCursorRuntimeSession(b, "session")
			case "retirement":
				applied, err = f.s.MarkCursorRuntimeGone(b)
			}
			if err == nil || applied {
				t.Fatalf("database failure reported success: %v %v", applied, err)
			}
		})
	}
}

// Source-derived synthetic runtime bindings exercise the persisted hold, not ACP admission.
func TestCursorLifecycleHoldsConcurrentRequests(t *testing.T) {
	for _, event := range []string{"Stop", "Interrupt", "UserPromptSubmit"} {
		for _, timing := range []string{"before read", "between read and update"} {
			t.Run(event+"/"+timing, func(t *testing.T) {
				f, row, b := cursorRowBinding(t)
				if ok, err := f.s.SetCursorRuntimeSession(b, "held-session"); err != nil || !ok {
					t.Fatalf("session %v %v", ok, err)
				}
				b.SessionID = "held-session"
				gate := store.HookGate{Event: event, ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
				seed := func() {
					req := store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", ToolName: "Bash", ToolInput: "{}", SettledAt: time.Now().Add(time.Hour)}
					if _, a, err := f.s.InsertRelayRequest(row.InstanceID, gate, req, 0, store.DefaultLockWait); err != nil || !a.Applied {
						t.Fatalf("insert %+v %v", a, err)
					}
				}
				if timing == "before read" {
					seed()
				} else {
					store.SetHookBeforeUpdateForTest(f.s, func() { store.SetHookBeforeUpdateForTest(f.s, nil); seed() })
					t.Cleanup(func() { store.SetHookBeforeUpdateForTest(f.s, nil) })
				}
				before, err := f.s.GetSpawn(row.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				target := store.StateWaiting
				if event == "UserPromptSubmit" {
					target = store.StateWorking
				}
				_, a, err := f.s.ApplyHookTransitionResult(row.InstanceID, gate, target, false, event, "synthetic-path", true)
				if err != nil || !a.Applied {
					t.Fatalf("held refresh %+v %v", a, err)
				}
				after, err := f.s.GetSpawn(row.InstanceID)
				if err != nil || after.State != store.StateCheckPermission || after.RowVersion <= before.RowVersion || after.SessionID != b.SessionID {
					t.Fatalf("held row %+v %v", after, err)
				}
				pr, err := f.s.GetPermissionRequest(row.InstanceID, "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa")
				if err != nil || pr.MainTurnEndEligible || pr.ProvenGone() || !pr.AwaitsAnswer() {
					t.Fatalf("unknown request %+v %v", pr, err)
				}

			})
		}
	}
}
