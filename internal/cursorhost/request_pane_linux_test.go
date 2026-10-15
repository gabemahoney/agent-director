//go:build linux

package cursorhost

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func displayedKey(t *testing.T, c *requestController, token, option string) rune {
	t.Helper()
	c.menuMu.Lock()
	defer c.menuMu.Unlock()
	for key, v := range c.menu {
		if v.token == token && v.option == option {
			return key
		}
	}
	t.Fatal("displayed option unavailable")
	return 0
}

func TestCursorImmediateNativeKeyRoutesOnlyItsRequest(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, alphabet := range []string{"ASCII", "Unicode"} {
		t.Run(alphabet, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			var pane bytes.Buffer
			c := e.controller(t, wire, &pane)
			if alphabet == "Unicode" {
				c.nextKey = 61
			}
			first := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			second := e.admit(t, c, `"1"`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "second", "execute"))
			key := displayedKey(t, c, second.token, "allow-always")
			if !strings.Contains(pane.String(), fmt.Sprintf("[%c] Allow always", key)) {
				t.Fatal("routing key was not displayed")
			}
			master, slave := terminalPair(t)
			events := make(chan terminalInput, 8)
			restore := terminalInputLoop(slave, io.Discard, events, func() bool { return false }, c.key)
			defer restore()
			if _, err := master.Write([]byte(string(key))); err != nil {
				t.Fatal(err)
			}
			event := terminalEvent(t, events)
			if event.choiceToken != second.token || event.choiceOption != "allow-always" || event.text != "" {
				t.Fatalf("single key routing: %+v", event)
			}
			c.choose(event.choiceToken, event.choiceOption)
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(wire.bytes(), []byte(`"id":"1"`)) || !bytes.Contains(wire.bytes(), []byte(`"optionId":"allow-always"`)) {
				t.Fatalf("native persistent option wrong: %s", wire.bytes())
			}
			if pr := e.request(t, first.token); pr.Decision != "" || pr.PaneAnswered() || pr.ProvenGone() {
				t.Fatal("other menu was answered")
			}
			if _, _, ok := c.key(key); ok {
				t.Fatal("retired key reused")
			}
			third := e.admit(t, c, `3`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "third", "execute"))
			if key == displayedKey(t, c, third.token, "allow-always") {
				t.Fatal("old key migrated to new menu")
			}
			if pr := e.request(t, second.token); !pr.DeliveredAt.IsZero() || pr.ProvenGone() || pr.DecisionReason != store.DecisionReasonPane {
				t.Fatal("native choice invented ack/proof")
			}
		})
	}
}

func TestCursorNativeFallbackAndRetirement(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, mode := range []string{"open fallback", "unacknowledged API allow", "expired key", "native timeout", "cancelled", "disconnected"} {
		t.Run(mode, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			wire := &observedWire{}
			var pane bytes.Buffer
			c := e.controller(t, wire, &pane)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			switch mode {
			case "unacknowledged API allow":
				e.decide(t, r.token, "allow")
			case "expired key":
				c.choose(r.token, "allow-always")
			case "native timeout":
				r.nativeUntil = time.Now().Add(-time.Second)
			case "cancelled":
				c.cancel()
			case "disconnected":
				c.protocol.close()
			}
			r.settle = time.UnixMilli(time.Now().Add(-time.Millisecond).UnixMilli())
			r.timeout = r.settle.Add(-requestReserve)
			e.exec(t, `UPDATE permission_requests SET settled_at=? WHERE request_token=?`, r.settle.UnixMilli(), r.token)
			err := c.tick()
			if mode == "native timeout" {
				if err == nil {
					t.Fatal("native timeout not reported")
				}
				c.choose(r.token, "allow-once")
				c.tick()
				if len(wire.bytes()) != 0 {
					t.Fatal("timed out revived")
				}
				return
			}
			if mode == "cancelled" {
				c.choose(r.token, "allow-once")
				c.tick()
				if len(wire.bytes()) != 0 {
					t.Fatal("cancelled revived")
				}
				return
			}
			if mode == "expired key" {
				if r.queued || len(wire.bytes()) != 0 {
					t.Fatal("expired selection became fallback approval")
				}
				return
			}
			c.choose(r.token, "reject-once")
			err = c.tick()
			if mode == "disconnected" {
				if err == nil {
					t.Fatal("disconnected native choice reported success")
				}
				if len(wire.bytes()) != 0 {
					t.Fatal("closed transport wrote")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(wire.bytes(), []byte(`"optionId":"reject-once"`)) {
				t.Fatalf("fresh fallback choice unavailable: %s", wire.bytes())
			}
			if pr := e.request(t, r.token); !pr.DeliveredAt.IsZero() || pr.ProvenGone() {
				t.Fatal("fallback invented ack/proof")
			}
		})
	}
}

func TestCursorPaneClaimsAreNotApprovalAuthority(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, claim := range []string{"intent", "sent", "outside", "other menu intent", "other menu sent"} {
		t.Run(claim, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			other := e.admit(t, c, `2`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "second", "execute"))
			target := r
			if strings.HasPrefix(claim, "other menu") {
				target = other
			}
			check := func(store.Spawn, []store.PermissionRow) error { return nil }
			if claim == "outside" {
				ok, err := e.s.RecordPaneOutside(e.binding.InstanceID, r.token, "allow", 0, check)
				if err != nil || !ok {
					t.Fatalf("outside %v %v", ok, err)
				}
			} else {
				intent, ok, err := e.s.RecordPaneIntent(e.binding.InstanceID, target.token, "allow", store.ProcessIdentity{}, time.Now, 0, check)
				if err != nil || !ok {
					t.Fatalf("intent %v %v", ok, err)
				}
				if strings.HasSuffix(claim, "sent") {
					ok, err = e.s.RecordPaneSent(e.binding.InstanceID, target.token, intent, 0)
					if err != nil || !ok {
						t.Fatalf("sent %v %v", ok, err)
					}
				}
			}
			c.choose(r.token, "allow-once")
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if claim == "sent" {
				if err := completedWrite(t, c.protocol); err != nil {
					t.Fatal(err)
				}
				if len(wire.bytes()) == 0 {
					t.Fatal("completed matching send claim cannot resolve native request")
				}
			} else if len(wire.bytes()) != 0 || r.queued {
				t.Fatal("outside/unsent/mismatched claim authorized ACP")
			}
			if e.request(t, r.token).ProvenGone() || e.request(t, other.token).ProvenGone() {
				t.Fatal("claim supplied execution proof")
			}
		})
	}
}

func TestCursorCallerRestartKeepsFiniteDelivery(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, relay := range []string{"on", "off"} {
		t.Run(relay, func(t *testing.T) {
			e := newRequestEnv(t, relay, "synthetic-session")
			c := e.controller(t, &observedWire{}, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			now := time.Now()
			view := api.RelayView{Now: func() time.Time { return now }}
			get := func(s *store.Store) api.GetPermissionResult {
				v, err := api.GetPermission(s, view, api.GetPermissionParams{RequestToken: r.token})
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
			before := get(e.s)
			want := api.DeliveryNotConfirmed
			if relay == "off" {
				want = api.DeliveryFallenBack
			}
			if before.Delivery != want || before.HookAlive != nil || !before.ConfirmBy.Equal(r.settle) {
				t.Fatalf("finite delivery: %+v", before)
			}
			if relay == "on" {
				e.decide(t, r.token, "allow")
			}
			// The gateway still lives, but the accepting runtime is gone; NULL request evidence remains finite.
			if err := e.process.terminate(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(e.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			after := get(reopened)
			if !after.ConfirmBy.Equal(before.ConfirmBy) || after.HookAlive != nil || after.Delivery != want {
				t.Fatalf("restart rearmed evidence: %+v", after)
			}
			now = r.settle.Add(time.Millisecond)
			after = get(reopened)
			if after.Delivery != api.DeliveryFallenBack || !after.ConfirmBy.Equal(before.ConfirmBy) {
				t.Fatalf("unacknowledged deadline failed: %+v", after)
			}
			row, err := reopened.GetPermissionRequest(e.binding.InstanceID, r.token)
			if err != nil {
				t.Fatal(err)
			}
			if row.MainTurnEndEligible || row.AgentID != "" || row.Hook != (store.ProcessIdentity{}) || row.ProvenGone() {
				t.Fatalf("reopen invented identity/proof %+v", row)
			}
		})
	}
}
