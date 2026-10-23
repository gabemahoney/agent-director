//go:build linux

package realtmux_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.o89: an allowed shell tool's completion arriving while an independent
// writer briefly holds the store's write lock (well under its 10 s busy
// timeout) must not kill the Cursor runtime and gateway; the proof is stored
// once the writer releases.
func TestCursorProofSurvivesBriefWriterContention(t *testing.T) {
	// Serial: the shared real-pane fixture changes HOME, PATH and tmux environment.
	const session = "busy-proof-session"
	update := json.RawMessage(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"busy-proof-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"proof-tool","status":"completed","rawOutput":{"exitCode":0,"stdout":"b.o89 contended proof","stderr":""}}}}`)
	script := cursorfix.Scenario{SessionID: session, WaitForAfterAnswersRelease: true, Requests: []json.RawMessage{relayFrame("proof", "session/request_permission", cursorfix.Permission(session, "proof-tool", "execute"))}, AfterAnswers: []json.RawMessage{update}}
	f, r, launch := newCursorPaneScenario(t, false, "", cursorPaneOptions{Script: &script, Relay: "on", Timeout: 30})
	st, e := store.Open(f.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	binding, e := st.ReadCursorRuntime(launch.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.API.SendKeys(api.SendKeysParams{InstanceID: launch.ID, Text: "perform allowed tool"}); e != nil {
		t.Fatal(e)
	}
	request := relayRequests(t, f, launch.ID, 1)[0]
	if _, e = f.API.Decide(api.DecideParams{InstanceID: launch.ID, RequestToken: request.RequestToken, Decision: "allow"}); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "ACP allow answer received", func() bool { return len(relayReplies(t, r)) == 1 }, func() string { return fmt.Sprint(relayReplies(t, r)) })
	if delivered := relayRequests(t, f, launch.ID, 1)[0]; delivered.Decision != "allow" || delivered.DeliveredAt.IsZero() || delivered.ProvenGone() {
		t.Fatalf("allow not delivered before the tool completed: %+v", delivered)
	}
	transcript := readRow(t, f.DBPath, launch.ID).JSONLPath.(string)
	alive := func() {
		t.Helper()
		if pidGone(binding.GatewayPID) || pidGone(binding.RuntimePID) {
			t.Fatalf("Cursor session died under brief writer contention: gateway=%s runtime=%s failures=%v", procState(binding.GatewayPID), procState(binding.RuntimePID), hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID))
		}
	}

	release := apitest.HoldWriteLock(t, f.DBPath, time.Minute)
	if e = os.WriteFile(r.Release, nil, 0600); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "gateway receives the completed tool update while the lock is held", func() bool {
		raw, e := os.ReadFile(transcript)
		return e == nil && strings.Contains(string(raw), "b.o89 contended proof")
	}, nil)
	heldUntil := time.Now().Add(time.Second)
	waitForObserved(t, 2*time.Second, "session stays alive while the writer holds the lock", func() bool {
		alive()
		return !time.Now().Before(heldUntil)
	}, nil)
	release()

	waitFor(t, "proof stored and row waiting after the writer releases", func() bool {
		alive()
		got := relayRequests(t, f, launch.ID, 1)[0]
		return got.ProvenGoneHow == store.ProvenGoneToolRan && readRow(t, f.DBPath, launch.ID).State == store.StateWaiting
	}, func() string {
		return fmt.Sprint(relayRequests(t, f, launch.ID, 1), readRow(t, f.DBPath, launch.ID).State)
	})
	alive()
	got := relayRequests(t, f, launch.ID, 1)[0]
	if got.Decision != "allow" || got.DecisionReason != "tool_ran" || got.PaneAnswer != store.PaneAnswerNone || got.DeliveredAt.IsZero() {
		t.Fatalf("wrong proof after contention: %+v", got)
	}
	if row, e := f.API.Get(launch.ID); e != nil || row.State != store.StateWaiting {
		t.Fatalf("get after contention: %+v %v", row, e)
	}
	if failures := hookTrailRecords(t, os.Getenv("HOME"), "ad.cursor.failed", launch.ID); len(failures) != 0 {
		t.Fatalf("brief contention recorded a Cursor failure: %v", failures)
	}
}
