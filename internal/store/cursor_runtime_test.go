package store_test

import (
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"reflect"
	"testing"
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
		{"stale token", func(g *store.HookGate) { g.Cursor.Token += "old" }},
		{"old life", func(g *store.HookGate) { g.Cursor.Life-- }},
		{"runtime", func(g *store.HookGate) { g.Cursor.RuntimePID++ }},
		{"runtime start", func(g *store.HookGate) { g.Cursor.RuntimeStart += "old" }},
		{"wrong connection", func(g *store.HookGate) { g.Cursor.Connection += "other" }},
		{"wrong pipe", func(g *store.HookGate) { g.Cursor.InputPipe = "pipe:[9999]" }},
		{"wrong FD", func(g *store.HookGate) { g.Cursor.OutputFD++ }},
		{"wrong session", func(g *store.HookGate) { g.SessionID = "other" }},
		{"false termination", func(g *store.HookGate) { g.Cursor.Terminated = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, row, b := cursorRowBinding(t)
			if ok, err := f.s.SetCursorRuntimeSession(b, "synthetic-session"); err != nil || !ok {
				t.Fatalf("session bind %v %v", ok, err)
			}
			b.SessionID = "synthetic-session"
			g := store.HookGate{Event: "UserPromptSubmit", ParentPID: row.Identity.PanePID, ParentStart: row.Identity.PaneStarttime, SessionID: b.SessionID, Cursor: &b}
			tc.change(&g)
			before := hgColumns(t, f, row.InstanceID)
			_, applied, err := f.s.ApplyHookTransitionResult(row.InstanceID, g, store.StateWorking, false, "UserPromptSubmit", "", false)
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
