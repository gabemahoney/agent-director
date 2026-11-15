package hook_test

// busy_send_activation_test.go — Task t2.xis.oo.2o's single backlog
// regression for activated Claude Code coverage: a 1,000-Message backlog
// through real SQLite admission and the Claude route's own hooks (the wake
// fixture of busy_send_claude_test.go). The partition and limit arithmetic
// itself is E4's; this checks the whole drain functionally against the one
// published capability table.

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/store"
)

// backlogBody is Message i of the backlog: exactly 1,900 UTF-8 bytes,
// multiline, with Unicode and generic tags (none Claude escapes), no blank
// line, and its index in its first and last lines.
func backlogBody(i int) string {
	head := fmt.Sprintf("<msg n=%04d>\nΔ backlog ✓ 多 🦋 <b>%04d</b>\n<note>tabs\tand <i>tags</i></note>\n", i, i)
	tail := fmt.Sprintf("\n</msg n=%04d>", i)
	return head + strings.Repeat("x", 1900-len(head)-len(tail)) + tail
}

// TestClaudeWakeDrainsBacklog (SR-1.6, SR-2.2 to SR-2.5, SR-3.3 to SR-3.5;
// Plan b.xis decisions 12 and 13): 1,000 Messages of 1,900 bytes each, every
// third one confirmed, are admitted while the agent works and stay Held,
// within the published held limits. Each turn end (the parent's Stop) then
// re-signals the wake, whose FileChanged writes one whole prompt, and the
// agent's exact sighting of it starts the next turn: the prompts carry every
// body whole, once, in accepted order, with exact separators, each the
// longest prefix of the rest that fits the published complete-prompt limit,
// until none is Held. Each confirmed Message ends sent_unconfirmed
// (wait_turn_raced): hook output is no proof the model took it.
func TestClaudeWakeDrainsBacklog(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the 1,000-Message backlog in short mode")
	}
	if raceDetector {
		// One goroutine drives the drain, so the detector checks nothing here;
		// it would only slow SQLite down by an order of magnitude.
		t.Skip("skipping the single-goroutine 1,000-Message backlog under the race detector")
	}
	e := newWakeEnv(t, store.StateWorking)
	caps := store.BusySendCapabilitiesFor(store.AgentClaude)
	limits, ok := caps.Mode(store.BusySendWaitTurn)
	if !ok {
		t.Fatal("claude offers no wait_turn")
	}
	const n = 1000
	bodies := make([]string, n)
	for i := range bodies {
		bodies[i] = backlogBody(i)
		if len(bodies[i]) != 1900 || !utf8.ValidString(bodies[i]) || strings.Contains(bodies[i], "\n\n") {
			t.Fatalf("body %d: %d bytes, valid %t; want 1,900 valid UTF-8 bytes with no blank line", i, len(bodies[i]), utf8.ValidString(bodies[i]))
		}
	}
	request := func(i int) string { return fmt.Sprintf("backlog-%04d", i) }
	for i, body := range bodies {
		if i%3 == 0 {
			e.admit(t, request(i), body)
			continue
		}
		if _, err := e.st.AdmitBusySend(store.BusySendAdmissionRequest{InstanceID: e.id, Text: []byte(body)}); err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
	}
	if o := e.observe(t); o.HeldCount != n || o.HeldBytes != n*1900 || n > caps.HeldCount || o.HeldBytes > caps.HeldBytes {
		t.Fatalf("while working: Held %d (%d bytes); want all %d (%d bytes), within %d and %d", o.HeldCount, o.HeldBytes, n, n*1900,
			caps.HeldCount, caps.HeldBytes)
	}
	e.assertHeld(t, request(0), store.BusySendWaitTurnRunning)

	next := 0 // the first body no prompt has carried yet
	for prompts := 0; next < n; prompts++ {
		if prompts == n {
			t.Fatalf("%d prompts carried %d of %d bodies; want the backlog drained", prompts, next, n)
		}
		e.fire(t, e.payload(t, "stop.json", nil)) // turn end: re-signals the wake
		got := e.wake(t)
		m := 0
		first, _, _ := strings.Cut(got, "\n")
		if _, err := fmt.Sscanf(first, "[agent-director: message 1 of %d]", &m); err != nil || m < 1 || next+m > n {
			t.Fatalf("prompt %d starts %.40q (%v); want message 1 of m with %d bodies left", prompts, got, err, n-next)
		}
		parts := make([]string, m)
		for k := range parts {
			parts[k] = fmt.Sprintf("[agent-director: message %d of %d]\n", k+1, m) + bodies[next+k]
		}
		if want := strings.Join(parts, "\n\n"); got != want {
			t.Fatalf("prompt %d (%d bytes) differs from bodies %d..%d, whole and in order (%d bytes)", prompts, len(got), next, next+m-1, len(want))
		}
		if size := limits.Measure([]byte(got)); size > limits.PromptBytes {
			t.Fatalf("prompt %d measures %d bytes; want at most the published %d", prompts, size, limits.PromptBytes)
		}
		if next+m < n {
			texts := make([][]byte, m+1)
			for k := range texts {
				texts[k] = []byte(bodies[next+k])
			}
			if size := limits.Measure(store.RenderBusySendPromptBody(texts)); size <= limits.PromptBytes {
				t.Fatalf("prompt %d carries %d bodies, but the next fits too (%d bytes); want the fewest prompts", prompts, m, size)
			}
		}
		e.fire(t, e.payload(t, "user-prompt-submit.json", map[string]any{"prompt": claudeWake(got)}))
		next += m
	}
	if o := e.observe(t); o.HeldCount != 0 || o.AwaitingTurnStart != 0 {
		t.Errorf("after the drain: Held %d, awaiting %d; want 0, 0", o.HeldCount, o.AwaitingTurnStart)
	}
	for i := 0; i < n; i += 3 {
		e.assertRaced(t, request(i))
	}
}
