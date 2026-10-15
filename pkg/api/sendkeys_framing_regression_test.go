package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysUnsafeFramingBeforeInput(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"hello\x1b[A", "hello\x00world", "hello\tworld", " /exit", "\n!shell", "hello\xff"} {
		t.Run(text, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: store.StateWaiting})
			_, err := e.sendKeys(api.SendKeysParams{InstanceID: r.ID, Text: text})
			if err == nil || !strings.Contains(err.Error(), "no input was attempted") {
				t.Errorf("unsafe framing was not refused before input: %v", err)
			}
			e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter, tmux.CallSendKey)
		})
	}
}

func TestSendKeysUnreadyLaunchNeverReceivesInput(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{State: store.StateWaiting})
	_, err := api.SendKeys(e.store, e.rec, api.SendKeysEnv{
		Relay: api.RelayView{Procs: e.pc, Now: e.clock.Now}, Sleep: func(d time.Duration) { e.clock.Advance(d) },
	}, api.SendKeysParams{InstanceID: r.ID, Text: "first ordinary prompt"})
	if err == nil || !strings.Contains(err.Error(), "no input was attempted") {
		t.Fatalf("unready launch accepted ordinary input: %v", err)
	}
	e.assertNoCalls(t, tmux.CallSendText, tmux.CallSendEnter, tmux.CallSendKey)
}
