//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/cursortranscript"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

type rotationPrompt struct{ Session, Text string }

func rotationPrompts(c *cursorSession, t *testing.T) []rotationPrompt {
	t.Helper()
	var result []rotationPrompt
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) != `"session/prompt"` {
			continue
		}
		var p struct {
			Session string `json:"sessionId"`
			Prompt  []struct {
				Text string `json:"text"`
			} `json:"prompt"`
		}
		if err := json.Unmarshal(f["params"], &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Prompt) != 1 {
			t.Fatalf("unexpected prompt: %s", f["params"])
		}
		result = append(result, rotationPrompt{p.Session, p.Prompt[0].Text})
	}
	return result
}
func rotationRecordCount(c *cursorSession, t *testing.T, kind string) int {
	t.Helper()
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.JSONLPath == "" {
		return 0
	}
	raw, err := os.ReadFile(row.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	end := bytes.LastIndexByte(raw, '\n')
	if end < 0 {
		return 0
	}
	// Complete protected records only. Quotes inside user text are JSON-escaped;
	// counting this field avoids repeatedly decoding 8MiB while the original
	// cancellation deadline is running.
	encoded, _ := json.Marshal(kind)
	return bytes.Count(raw[:end], append([]byte(`"kind":`), encoded...))
}

func rotationNewCount(c *cursorSession, t *testing.T) int {
	t.Helper()
	n := 0
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/new"` {
			n++
		}
	}
	return n
}
func rotationRelease(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func rotationNoBytes(t *testing.T, c *cursorSession, text string) {
	t.Helper()
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, encoded) {
		t.Fatalf("refused/held text reached raw ACP pipe: %q", text)
	}
}
func rotationBarrier(t *testing.T, phase string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "SessionStart")
	t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", phase)
	t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
	t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600) })
	return dir
}
func rotationEntered(c *cursorSession, t *testing.T, dir string) {
	t.Helper()
	c.wait(t, "publication barrier", func() bool { _, e := os.Stat(filepath.Join(dir, "entered")); return e == nil })
}

// Real controller, PTY and guarded publication with a synthetic ACP peer.
func TestCursorRotationAliasesIdleAndActive(t *testing.T) {
	// Serial: Run and verified pane fixtures own process environment and signals.
	for _, alias := range []string{"/clear", "/new"} {
		for _, active := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/active=%v", alias, active), func(t *testing.T) {
				initial := ""
				if active {
					initial = "HOLD"
				}
				c := postBootstrapCursorSession(t, "off", initial)
				if active {
					c.wait(t, "old prompt received", func() bool { return len(rotationPrompts(c, t)) == 1 })
					c.keys(t, "old-queue-never-new\r")
					c.wait(t, "old queue accepted", func() bool { return rotationRecordCount(c, t, "submission_accepted") == 2 })
				}
				before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
				c.keys(t, alias+"\r")
				c.wait(t, "rotation requested", func() bool { return rotationRecordCount(c, t, "rotation_requested") == 1 })
				if active {
					if rotationNewCount(c, t) != 1 {
						t.Fatal("session/new preceded correlated active cancellation outcome")
					}
					rotationRelease(t, c.e.runtime.Release)
				}
				c.wait(t, "fresh request", func() bool { return rotationNewCount(c, t) == 2 })
				texts := []string{"第一\t🐝\nnext café", "second 🦋\nline two"}
				c.keys(t, "\x1b[200~"+texts[0]+"\x1b[201~\r\x1b[200~"+texts[1]+"\x1b[201~\r")
				c.wait(t, "held records", func() bool { return rotationRecordCount(c, t, "input_held") == 2 })
				for _, text := range texts {
					rotationNoBytes(t, c, text)
				}
				current, _ := c.e.s.GetSpawn(before.InstanceID)
				history, _ := c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
				if current.SessionID != before.SessionID || len(history) != 0 {
					t.Fatalf("unconfirmed publication: %+v %v", current, history)
				}
				rotationRelease(t, c.e.runtime.NewRelease)
				outcomes := len(texts)
				if active {
					outcomes++
				}
				c.wait(t, "held FIFO completion", func() bool { return rotationRecordCount(c, t, "turn_outcome") == outcomes })
				c.stop(t)
				rawReports, err := os.ReadFile(c.e.path + ".reports")
				if err != nil {
					t.Fatal(err)
				}
				confirmedPayloads := 0
				for _, line := range bytes.Split(bytes.TrimSpace(rawReports), []byte{'\n'}) {
					var payload struct {
						Event    string `json:"hook_event_name"`
						Session  string `json:"session_id"`
						Previous string `json:"cursor_previous_session"`
						Deadline string `json:"cursor_rotation_deadline"`
					}
					if err := json.Unmarshal(line, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.Previous != "" {
						confirmedPayloads++
						until, err := time.Parse(time.RFC3339Nano, payload.Deadline)
						if err != nil || payload.Event != "SessionStart" || payload.Previous != before.SessionID || payload.Session != "synthetic-session-2" || until.IsZero() {
							t.Fatalf("wrong actual publication payload: %s", line)
						}
					}
				}
				if confirmedPayloads != 1 {
					t.Fatalf("actual correlated rotation report count %d", confirmedPayloads)
				}
				var want []rotationPrompt
				if active {
					want = append(want, rotationPrompt{"synthetic-session-1", "HOLD"})
				}
				for _, text := range texts {
					want = append(want, rotationPrompt{"synthetic-session-2", text})
				}
				if got := rotationPrompts(c, t); !reflect.DeepEqual(got, want) {
					t.Fatalf("wrong conversation/order: %+v want %+v", got, want)
				}
				row, _ := c.e.s.GetSpawn(before.InstanceID)
				history, err = c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
				if err != nil || row.LifeNumber != before.LifeNumber || row.JSONLPath != before.JSONLPath || len(history) != 1 || history[0].SessionID != before.SessionID || history[0].JSONLPath != before.JSONLPath {
					t.Fatalf("same-life archive: row=%+v history=%+v %v", row, history, err)
				}
				held, accepted, started := map[string]string{}, map[string]string{}, map[string]bool{}
				published := false
				for _, r := range c.records(t) {
					switch r.Kind {
					case "input_held":
						if r.Session != "" {
							t.Fatal("held record invented conversation admission")
						}
						held[r.Data.ID] = r.Data.Text
					case "rotation_confirmed":
						if r.Session != before.SessionID {
							t.Fatal("protocol confirmation falsely published transcript identity")
						}
					case "rotation_published":
						published = true
						if r.Session != "synthetic-session-2" {
							t.Fatal("published record retained outgoing identity")
						}
					case "submission_accepted":
						if text, ok := held[r.Data.ID]; ok {
							if !published || r.Session != "synthetic-session-2" || r.Data.Text != text {
								t.Fatalf("false held admission: %+v", r)
							}
							accepted[r.Data.ID] = text
						}
					case "submission_started":
						if _, ok := held[r.Data.ID]; ok {
							started[r.Data.ID] = true
						}
					}
				}
				if len(held) != 2 || len(accepted) != 2 || len(started) != 2 {
					t.Fatalf("incomplete held lifecycle: %v %v %v", held, accepted, started)
				}
			})
		}
	}
}

func TestCursorRotationPublicationAndEveryHoldPrecedePromotion(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	barrier, opener := newRecordBarrier(t, "input_held")
	consumed := make(chan struct{}, 3)
	starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
		return terminalLoop(in, out, func(v terminalInput) {
			emit(v)
			switch v.text {
			case "held-until-all-proofs", "ordinary-must-be-refused", "still-refused":
				consumed <- struct{}{}
			}
		}, admit, choices...)
	}
	c := postBootstrapCursorSessionWithInput(t, "off", "", opener, starter, true)
	defer barrier.unblock()
	before, _ := c.e.s.GetSpawn(c.e.binding.InstanceID)
	b, _ := c.e.s.ReadCursorRuntime(before.InstanceID)
	gate := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	for i, tool := range []string{"Unknown", "Plan"} {
		_, a, err := c.e.s.InsertRelayRequest(b.InstanceID, gate, store.RelayRequest{RequestToken: fmt.Sprintf("aaaaaaaa-aaaa-4aaa-aaaa-%012d", i), ToolName: tool, ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait)
		if err != nil || !a.Applied {
			t.Fatalf("hold %+v %v", a, err)
		}
	}
	dir := rotationBarrier(t, "after")
	c.keys(t, "/new\r")
	rotationEntered(c, t, dir)
	c.keys(t, "held-until-all-proofs\r")
	// Input is consumed while the real hook has committed but its report is pending.
	c.wait(t, "provisional input consumed", func() bool { return len(consumed) == 1 })
	rotationNoBytes(t, c, "held-until-all-proofs")
	rotationRelease(t, filepath.Join(dir, "release"))
	c.wait(t, "publication settled with durable holds", func() bool {
		return rotationRecordCount(c, t, "rotation_published") == 1 && rotationRecordCount(c, t, "input_held") == 1
	})
	select {
	case <-barrier.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("held input record did not block")
	}
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" {
			t.Fatal("publication cleared independent guards")
		}
	}
	c.rotationFixtureSQL(t, nil, 2, `UPDATE permission_requests SET closed_at=?`, time.Now().UnixMilli())
	typed := time.Now()
	c.keys(t, "ordinary-must-be-refused\r")
	c.wait(t, "guarded input consumed", func() bool { return len(consumed) == 2 })
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Unknown'`, time.Now().UnixMilli())
	// A second actual control-input consumption is an observable check, without inferring admission from screen state.
	c.keys(t, "still-refused\r")
	c.wait(t, "remaining hold input consumed", func() bool { return len(consumed) == 3 })
	// b.bqd: both inputs were consumed while the controller could not record
	// their refusals. A record-write timestamp would fall after this cutoff.
	cutoff := time.Now()
	barrier.unblock()
	c.wait(t, "remaining hold refuses", func() bool { return rotationRecordCount(c, t, "submission_refused") == 2 })
	// b.bqd: each refusal names its discarded text by digest, and when it was
	// consumed, for send-keys.
	var digests []string
	for _, r := range c.records(t) {
		if r.Kind == "submission_refused" {
			if r.Data.ConsumedAt.Before(typed) || !r.Data.ConsumedAt.Before(cutoff) {
				t.Fatalf("submission_refused consumed_at = %s; want in [%s, %s) before recording resumed", r.Data.ConsumedAt, typed, cutoff)
			}
			digests = append(digests, r.Data.TextSHA256)
		}
	}
	if want := []string{cursortranscript.TextSHA256("ordinary-must-be-refused"), cursortranscript.TextSHA256("still-refused")}; !reflect.DeepEqual(digests, want) {
		t.Fatalf("refusal text_sha256 = %q; want %q", digests, want)
	}
	// The released record barrier lets one controller turn record the hold and
	// both refusals, then check the holds. End that turn before the last proof,
	// so no check already under way can release the held input.
	c.idleObservationBarrier(t, "rotation-refusals-recorded")
	rotationNoBytes(t, c, "held-until-all-proofs")
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Plan'`, time.Now().UnixMilli())
	c.idleObservationBarrier(t, "rotation-holds-independently-cleared")
	rotationNoBytes(t, c, "held-until-all-proofs")
	c.keys(t, "fresh-explicit-after-rotation-hold\r")
	c.wait(t, "all proofs release held before new input", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 2 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "held-until-all-proofs"}, {"synthetic-session-2", "fresh-explicit-after-rotation-hold"}}) {
		t.Fatalf("guard release %+v", got)
	}
}

// b.bqd: provisional input a control retires before the gateway records it
// held gets held_input_refused as its first verdict, naming its text by
// digest and its consumption time for send-keys: /exit retires it at
// consumption, an interrupt stops the gateway with it still in the mailbox.
func TestCursorUnrecordedHeldInputRefusalCarriesTextDigest(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	for _, tc := range []struct{ control, reason string }{
		{"/exit\r", "control retired provisional input"},
		{"\x03", "gateway stopped"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			consumed := make(chan struct{}, 2)
			starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
				return terminalLoop(in, out, func(v terminalInput) {
					emit(v)
					if v.text == "unrecorded-held" || v.text == "/exit" || v.interrupt {
						consumed <- struct{}{}
					}
				}, admit, choices...)
			}
			c := postBootstrapCursorSessionWithInput(t, "off", "", openTranscript, starter, true)
			dir := rotationBarrier(t, "after")
			c.keys(t, "/new\r")
			rotationEntered(c, t, dir)
			// Publication blocks the loop, so the control comes before the input is recorded.
			typed := time.Now()
			c.keys(t, "unrecorded-held\r"+tc.control)
			c.wait(t, "held input and control consumed", func() bool { return len(consumed) == 2 })
			cutoff := time.Now()
			rotationRelease(t, filepath.Join(dir, "release"))
			select {
			case c.endErr = <-c.done:
				c.ended = true
			case <-time.After(5 * time.Second):
				t.Fatal("control did not stop the gateway")
			}
			var refused []submissionRecord
			for _, r := range c.records(t) {
				if r.Kind == "input_held" {
					t.Fatalf("retired input recorded held: %+v", r)
				}
				if r.Kind == "held_input_refused" {
					refused = append(refused, r)
				}
			}
			if len(refused) != 1 || refused[0].Data.Reason != tc.reason || refused[0].Data.TextSHA256 != cursortranscript.TextSHA256("unrecorded-held") {
				t.Fatalf("held_input_refused = %+v; want one, %q, with the text's digest (end: %v)", refused, tc.reason, c.endErr)
			}
			if got := refused[0].Data.ConsumedAt; got.Before(typed) || !got.Before(cutoff) {
				t.Fatalf("held_input_refused consumed_at = %s; want in [%s, %s) before recording resumed", got, typed, cutoff)
			}
			// b.x9o: the refusal's durable capture is finished as refused.
			if v := c.capturedInput(t, refused[0]); v.State != store.CursorInputRefused || v.Text != "unrecorded-held" || !v.CapturedAt.Equal(refused[0].Data.ConsumedAt) {
				t.Fatalf("held refusal capture = %+v; want refused %q captured at %s", v, "unrecorded-held", refused[0].Data.ConsumedAt)
			}
			rotationNoBytes(t, c, "unrecorded-held")
		})
	}
}

// b.bqd: a held refusal that is not the text's first verdict, and the
// non-refusal input_observed, never gain a text digest, so send-keys cannot
// claim them; their other released fields stay. b.x9o: each input's durable
// capture is still finished as refused.
func TestCursorUnclaimableInputRecordsKeepCapture(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	t.Run("held retired after recording", func(t *testing.T) {
		// Recorded held, and kept held past publication by an unproven hold.
		c := postBootstrapCursorSession(t, "off", "")
		b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
		if err != nil {
			t.Fatal(err)
		}
		gate := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
		if _, a, err := c.e.s.InsertRelayRequest(b.InstanceID, gate, store.RelayRequest{RequestToken: "aaaaaaaa-aaaa-4aaa-aaaa-000000000077", ToolName: "Unknown", ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
			t.Fatalf("independent hold: %+v %v", a, err)
		}
		c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
		c.keys(t, "/new\r")
		c.wait(t, "rotation pending", func() bool { return rotationNewCount(c, t) == 2 })
		c.keys(t, "recorded-held\r")
		c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
		rotationRelease(t, c.e.runtime.NewRelease)
		c.wait(t, "published with the hold", func() bool {
			return rotationRecordCount(c, t, "rotation_published") == 1 && bytes.Contains(c.pane.bytes(), []byte("Cursor fresh conversation published"))
		})
		// The post-publication hold check has finished, so /exit reaches the
		// held-retirement loop from the controller's idle select.
		c.idleObservationBarrier(t, "published-hold-idle")
		c.stop(t)
		var held, refused []submissionRecord
		for _, r := range c.records(t) {
			switch r.Kind {
			case "input_held":
				held = append(held, r)
			case "held_input_refused":
				refused = append(refused, r)
			}
		}
		if len(held) != 1 || len(refused) != 1 || refused[0].Data.ID != held[0].Data.ID || refused[0].Data.Reason != "control retired provisional input" || refused[0].Data.TextSHA256 != "" {
			t.Fatalf("held_input_refused = %+v after %+v; want one by the control, without a text digest", refused, held)
		}
		if v := c.capturedInput(t, refused[0]); v.State != store.CursorInputRefused || v.Text != "recorded-held" {
			t.Fatalf("held refusal capture = %+v; want refused %q", v, "recorded-held")
		}
		rotationNoBytes(t, c, "recorded-held")
	})
	t.Run("observed at gateway stop", func(t *testing.T) {
		// Accepted input is still in the mailbox when the runtime dies during a report.
		c := startCursorSession(t, "")
		dir := t.TempDir()
		t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "UserPromptSubmit")
		t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "before")
		t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
		t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600) })
		c.keys(t, "before-wire\r")
		c.wait(t, "pending submission report", func() bool { _, err := os.Stat(filepath.Join(dir, "entered")); return err == nil })
		c.keys(t, "observed-at-stop\r~")
		c.wait(t, "input consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ~")) })
		raw, err := os.ReadFile(c.e.runtime.PID)
		if err != nil {
			t.Fatal(err)
		}
		var pid int
		fmt.Sscan(string(raw), &pid)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		c.awaitFailure(t, 5*time.Second)
		var observed []submissionRecord
		cancelled := map[string]bool{}
		for _, r := range c.records(t) {
			switch r.Kind {
			case "input_observed":
				observed = append(observed, r)
			case "submission_cancelled":
				cancelled[r.Data.ID] = true
			case "submission_refused", "held_input_refused":
				t.Fatalf("observed input became a refusal: %+v", r)
			}
		}
		if len(observed) != 1 || observed[0].Data.TextSHA256 != "" || observed[0].Data.Durable == nil || *observed[0].Data.Durable || observed[0].Data.Text != "observed-at-stop" || observed[0].Data.ConsumedAt.IsZero() || !cancelled[observed[0].Data.ID] {
			t.Fatalf("input_observed = %+v; want one with its text and consumption time, without a text digest or durable admission, then cancelled", observed)
		}
		if v := c.capturedInput(t, observed[0]); v.State != store.CursorInputRefused || v.Text != "observed-at-stop" {
			t.Fatalf("observed input capture = %+v; want refused %q", v, "observed-at-stop")
		}
		rotationNoBytes(t, c, "observed-at-stop")
	})
}

// Task t2.7tz.5c.8d, b.x9o: with no idle ticker, a control consumed by the
// held-promotion record's await, after the loop's control handling, must still
// be acted on before the controller blocks. The interrupt is recorded, the
// loop, not shutdown, cancels the promoted input still queued and retires the
// input still held, and the gateway recovers idle instead of timing out.
// Task t2.7tz.5c.at: /exit there ends at once as on main 443474a9, without
// recording its control, and shutdown disposes of both inputs after the end.
func TestCursorControlDuringHeldPromotionIsActedOn(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	for _, tc := range []struct {
		control, keys            string
		want                     []string
		queuedReason, heldReason string
	}{
		{"interrupt", "\x03", []string{"rotation", "interrupt", "exit"}, "interruption or shutdown", "control retired provisional input"},
		{"exit", "\x15/exit\r", []string{"rotation"}, "gateway stopped", "rotation stopped; neither conversation admitted this input"},
	} {
		t.Run(tc.control, func(t *testing.T) {
			barrier, opener := newMatchingRecordBarrier(t, "submission_accepted", func(raw []byte) bool {
				return bytes.Contains(raw, []byte(`"released_from_hold":true`)) && bytes.Contains(raw, []byte(`"queued-second"`))
			})
			consumed := make(chan struct{}, 2)
			starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
				return terminalLoop(in, out, func(v terminalInput) {
					emit(v)
					if v.interrupt || v.text == "/exit" {
						select {
						case consumed <- struct{}{}:
						default:
						}
					}
				}, admit, choices...)
			}
			c := postBootstrapCursorSessionWithInput(t, "off", "", opener, starter)
			defer barrier.unblock()
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			c.keys(t, "/new\r")
			c.wait(t, "rotation pending", func() bool { return rotationNewCount(c, t) == 2 })
			// The first is taken by submit, which cancels it before dispatch; the
			// second stays queued behind it; the third is never promoted.
			c.keys(t, "promoted-first\rqueued-second\rstill-held\r")
			c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 3 })
			rotationRelease(t, c.e.runtime.NewRelease)
			select {
			case <-barrier.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("promotion record did not block")
			}
			// The blocked record's await is the only reader of the control's wake.
			c.keys(t, tc.keys)
			c.wait(t, "control consumed", func() bool { return len(consumed) == 1 })
			barrier.unblock()
			if tc.control == "interrupt" {
				c.wait(t, "idle recovery", func() bool { return bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) })
				c.keys(t, "\x15/exit\r")
			}
			select {
			case c.endErr = <-c.done:
				c.ended = true
			case <-time.After(5 * time.Second):
				t.Fatal("exit did not stop the gateway")
			}
			if c.endErr != nil {
				t.Fatalf("gateway ended with %v; want a clean exit", c.endErr)
			}
			var controls []string
			held := map[string]string{}
			var refused []submissionRecord
			cancelled := map[string]string{}
			ended := false
			// The interrupt's loop disposes after its control record; /exit's
			// shutdown disposes only after the intentional end.
			disposed := func() bool {
				if tc.control == "exit" {
					return ended
				}
				return len(controls) >= 2
			}
			for _, r := range c.records(t) {
				switch r.Kind {
				case "input_control":
					controls = append(controls, r.Data.Control)
				case "session_end":
					ended = true
				case "input_held":
					held[r.Data.Text] = r.Data.ID
				case "held_input_refused":
					if !disposed() {
						t.Fatalf("held input refused before its %s owner acted: %+v", tc.control, r)
					}
					refused = append(refused, r)
				case "submission_cancelled":
					// submit itself cancels the first before the control is recorded.
					if r.Data.ID == held["queued-second"] && !disposed() {
						t.Fatalf("queued input cancelled before its %s owner acted: %+v", tc.control, r)
					}
					cancelled[r.Data.ID] = r.Data.Reason
				}
			}
			if !reflect.DeepEqual(controls, tc.want) {
				t.Fatalf("input_control records = %q; want %q", controls, tc.want)
			}
			if got := cancelled[held["queued-second"]]; got != tc.queuedReason {
				t.Fatalf("queued input cancellation = %q (all %v); want %q", got, cancelled, tc.queuedReason)
			}
			if len(refused) != 1 || refused[0].Data.ID != held["still-held"] || refused[0].Data.Reason != tc.heldReason {
				t.Fatalf("held_input_refused = %+v; want %q retired with %q", refused, held["still-held"], tc.heldReason)
			}
			for _, text := range []string{"promoted-first", "queued-second", "still-held"} {
				rotationNoBytes(t, c, text)
			}
		})
	}
}

// b.x9o, Task t2.7tz.ti.b9: a control put while the held promotion's own
// durable save blocks retires its epoch, so the store refuses the save before
// mutation. As on main 443474a9, which persists nothing at promotion, the
// input is accepted after fresh publication and submit cancels it before
// dispatch; the control then acts, and the input held behind it is refused.
// Any other failure of that save stays fatal.
func TestCursorHeldPromotionSaveControlCancelsAuthorityLossFails(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	for _, tc := range []struct{ control, keys string }{
		{"interrupt", "\x03"},
		{"exit", "\x15/exit\r"},
		{"authority loss", ""},
	} {
		t.Run(tc.control, func(t *testing.T) {
			save := newSaveBarrier(t, func(next store.CursorInput) bool {
				return next.State == store.CursorInputAdmitted && next.Text == "promotion-retired"
			})
			c := postBootstrapCursorSessionWithOptions(t, Options{beforeInputSave: save.before}, "off", "", openTranscript, save.start)
			defer save.open()
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			c.keys(t, "/new\r")
			c.wait(t, "rotation pending", func() bool { return rotationNewCount(c, t) == 2 })
			c.keys(t, "promotion-retired\rheld-behind\r")
			c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 2 })
			rotationRelease(t, c.e.runtime.NewRelease)
			if tc.keys == "" {
				save.fatal(t, c, "promotion-retired")
				return
			}
			window := save.control(t, c, tc.keys, "after-promotion-retired")
			held := map[string]submissionRecord{}
			for _, r := range c.records(t) {
				if r.Kind == "input_held" {
					held[r.Data.Text] = r
				}
			}
			first, second := held["promotion-retired"], held["held-behind"]
			want := []string{
				"submission_accepted consumed_at=* durable_admission=false input_epoch=* released_from_hold=true submission_id=promotion-retired text=promotion-retired",
				"user_input origin=main submission_id=promotion-retired text=promotion-retired",
				"submission_cancelled control_epoch=* reason=control consumed before dispatch started=false submission_id=promotion-retired submission_session_id=synthetic-session-2",
			}
			pane := []string{"Cursor submission " + first.Data.ID + " accepted after fresh publication", "Cursor submission " + first.Data.ID + " cancelled before dispatch: control consumed before dispatch"}
			refused := "Cursor held submission " + second.Data.ID + " refused; nothing sent to either conversation"
			secondDetail := "control retired provisional input"
			var prompts []rotationPrompt
			if tc.control == "interrupt" {
				want = append(want, "input_control consumed_at=* control=interrupt count=1 input_epoch=* input_id=*", "held_input_refused reason=control retired provisional input submission_id=held-behind")
				pane = append(pane, refused, "Cursor input cancelled; submit a new prompt to continue")
				prompts = []rotationPrompt{{"synthetic-session-2", "after-promotion-retired"}}
			} else {
				// Main records no input_control for this /exit; shutdown refuses the second.
				want = append(want, "session_end intentional=true runtime_gone=true", "held_input_refused reason=rotation stopped; neither conversation admitted this input submission_id=held-behind")
				pane = append(pane, "Cursor runtime terminated", refused)
				secondDetail = "rotation stopped; input not dispatched"
				if row, err := c.e.s.GetSpawn(c.e.binding.InstanceID); err != nil || row.State != store.StateEnded {
					t.Fatalf("row after /exit = %+v (%v); want ended", row, err)
				}
			}
			if !reflect.DeepEqual(window, want) {
				t.Fatalf("records from the promotion:\n%s\nwant:\n%s", strings.Join(window, "\n"), strings.Join(want, "\n"))
			}
			paneOrder(t, c, pane...)
			// Cancelled from its held row: never persisted admitted to a target.
			if v := c.capturedInput(t, first); v.State != store.CursorInputCancelled || v.Detail != "control consumed before dispatch" || v.TargetSession != "" {
				t.Fatalf("promoted input row = %+v; want cancelled from held", v)
			}
			if v := c.capturedInput(t, second); v.State != store.CursorInputRefused || v.Detail != secondDetail {
				t.Fatalf("second held input row = %+v; want refused, %q", v, secondDetail)
			}
			for _, text := range []string{"promotion-retired", "held-behind"} {
				rotationNoBytes(t, c, text)
			}
			if got := rotationPrompts(c, t); !reflect.DeepEqual(got, prompts) {
				t.Fatalf("prompts = %+v; want only %+v, once", got, prompts)
			}
		})
	}
}

// Task t2.7tz.5c.8d, b.x9o: a prompt retired before write, whose failure the
// interrupt's record await consumes, is still cancelled with nothing written,
// and the gateway recovers idle. The interrupt's session/cancel write also
// wakes the select, so this does not pin awaitConsumedWake's prompt trigger.
func TestCursorPromptRetiredDuringControlRecordIsActedOn(t *testing.T) {
	// Serial: protected runtime and reporter subprocess use process environment.
	start, startOpener := newRecordBarrier(t, "submission_started")
	control, _ := newRecordBarrier(t, "input_control")
	defer start.unblock()
	defer control.unblock()
	consumed := make(chan string, 4)
	options := Options{afterAwaitConsumedWake: func(kind string) { consumed <- kind }}
	c := beginCursorSessionWithOptions(t, options, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{}}, control.around(startOpener), terminalLoop)
	c.wait(t, "confirmed ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	c.keys(t, "retired-unwritten\r")
	select {
	case <-start.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start record did not block")
	}
	// The blocked start record's await consumes the interrupt; its write guard
	// still waits for that record.
	c.keys(t, "\x03")
	c.wait(t, "interrupt consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cancellation requested")) })
	start.unblock()
	select {
	case kind := <-consumed:
		if kind != "prompt retired" {
			t.Fatalf("await consumed %q; want the retired prompt", kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await did not consume the retired prompt")
	}
	control.unblock()
	c.wait(t, "idle recovery", func() bool { return bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) })
	c.stop(t)
	if len(consumed) != 0 {
		t.Fatalf("await consumed another wake: %q", <-consumed)
	}
	var controls []string
	var cancelled []submissionRecord
	id := ""
	for _, r := range c.records(t) {
		switch r.Kind {
		case "input_control":
			controls = append(controls, r.Data.Control)
		case "submission_started":
			id = r.Data.ID
		case "submission_cancelled":
			if len(controls) == 0 {
				t.Fatalf("prompt retired before its control was recorded: %+v", r)
			}
			cancelled = append(cancelled, r)
		case "submission_unconfirmed", "turn_outcome":
			t.Fatalf("retired prompt reached an outcome: %+v", r)
		}
	}
	if !reflect.DeepEqual(controls, []string{"interrupt", "exit"}) {
		t.Fatalf("input_control records = %q; want interrupt, exit", controls)
	}
	if len(cancelled) != 1 || cancelled[0].Data.ID != id || cancelled[0].Data.Reason != "retired before write" || cancelled[0].Data.Started ||
		cancelled[0].Data.Written == nil || *cancelled[0].Data.Written != 0 {
		t.Fatalf("submission_cancelled = %+v; want %q retired before write with 0 bytes written", cancelled, id)
	}
	if v := c.capturedInput(t, cancelled[0]); v.State != store.CursorInputCancelled || v.Detail != "retired before write" {
		t.Fatalf("retired prompt capture = %+v; want cancelled, retired before write", v)
	}
	if got := c.prompts(t); len(got) != 0 {
		t.Fatalf("retired prompt reached the runtime: %q", got)
	}
	rotationNoBytes(t, c, "retired-unwritten")
}

func TestCursorRotationFailureRefusesEveryHeldInput(t *testing.T) {
	// Serial: real runtime/report subprocess and fault injection own environment.
	for _, failure := range []string{"rejection", "missing ID", "malformed ID", "reused ID", "archive", "report after commit", "authority loss", "runtime death"} {
		t.Run(failure, func(t *testing.T) {
			c := postBootstrapCursorSession(t, "off", "", true)
			before, _ := c.e.s.GetSpawn(c.e.binding.InstanceID)
			scenario := cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true}
			switch failure {
			case "rejection":
				scenario.NewReplies = []json.RawMessage{json.RawMessage(`{"error":{"code":-32000,"message":"refused"}}`)}
			case "missing ID":
				scenario.NewReplies = []json.RawMessage{json.RawMessage(`{"result":{}}`)}
			case "malformed ID":
				scenario.NewReplies = []json.RawMessage{json.RawMessage(`{"result":{"sessionId":"bad\nidentity"}}`)}
			case "reused ID":
				scenario.NewReplies = []json.RawMessage{json.RawMessage(`{"result":{"sessionId":"synthetic-session-1"}}`)}
			case "archive":
				storefix.InjectWriteFailure(t, c.e.path, storefix.WriteFailReuseArchive, before.InstanceID)
			case "report after commit":
				t.Setenv("AD_TEST_CURSOR_REPORT_MODE", "rotation-error-after-commit")
			}
			c.e.runtime.Configure(t, scenario)
			c.keys(t, "/clear\r")
			c.wait(t, "pending new", func() bool { return rotationNewCount(c, t) == 2 })
			c.keys(t, "refuse-held-one\rrefuse-held-two\r")
			c.wait(t, "both provisional records", func() bool { return rotationRecordCount(c, t, "input_held") == 2 })
			switch failure {
			case "authority loss":
				c.e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
			case "runtime death":
				raw, _ := os.ReadFile(c.e.runtime.PID)
				var pid int
				fmt.Sscan(string(raw), &pid)
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			}
			rotationRelease(t, c.e.runtime.NewRelease)
			err := c.awaitFailure(t, 5*time.Second)
			t.Logf("causal failure: %v", err)
			for _, text := range []string{"refuse-held-one", "refuse-held-two"} {
				rotationNoBytes(t, c, text)
			}
			stopped := 0
			for _, r := range c.records(t) {
				if r.Kind == "input_held" && !bytes.Contains(c.pane.bytes(), []byte("held submission "+r.Data.ID+" refused; nothing sent to either conversation")) {
					t.Fatalf("missing per-input refusal: %+v pane=%s", r, c.pane.bytes())
				}
				if r.Kind == "held_input_refused" {
					stopped++
					// b.bqd: input recorded held already has its verdict, so its
					// rotation-stopped refusal stays unclaimable by send-keys.
					if r.Data.TextSHA256 != "" {
						t.Fatalf("rotation-stopped refusal gained a text digest: %+v", r)
					}
				}
				if r.Kind == "submission_started" || r.Kind == "submission_accepted" || r.Kind == "session_end" {
					t.Fatalf("false execution/end: %+v", r)
				}
			}
			if failure == "rejection" && stopped != 2 {
				t.Fatalf("rotation-stopped refusals = %d; want one per held input", stopped)
			}
			row, _ := c.e.s.GetSpawn(before.InstanceID)
			h, _ := c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
			if failure == "report after commit" {
				if !strings.Contains(err.Error(), "Cursor rotation report failed; publication may already be committed; held input refused") {
					t.Fatalf("committed report failure lost its exact advice: %v", err)
				}
				inspected, inspectErr := api.Get(c.e.s, api.RelayView{}, before.InstanceID)
				if inspectErr != nil || inspected.SessionID != "synthetic-session-2" || inspected.JSONLPath != before.JSONLPath || len(inspected.PriorSessions) != 1 || inspected.PriorSessions[0].SessionID != before.SessionID || inspected.PriorSessions[0].JSONLPath != before.JSONLPath {
					t.Fatalf("public inspection lost committed truth: %+v %v", inspected, inspectErr)
				}
				if row.SessionID != "synthetic-session-2" || len(h) != 1 {
					t.Fatalf("committed publication falsely rolled back: %+v %v", row, h)
				}
			} else if row.SessionID != before.SessionID || len(h) != 0 {
				t.Fatalf("failed publication changed identity/history: %+v %v", row, h)
			}
		})
	}
}

func TestCursorRotationRepeatedControlsPreserveFirstDeadline(t *testing.T) {
	// Serial: actual three-second controller cancellation deadline and isolated processes.
	for _, phase := range []string{"new", "mode"} {
		t.Run(phase, func(t *testing.T) {
			var c *cursorSession
			if phase == "mode" {
				c = beginCursorSessionWithOptions(t, Options{RequestedMode: "plan"}, cursorfix.Scenario{Modes: advertisedCursorModes()}, openTranscript, terminalLoop)
				c.wait(t, "configured startup ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
				c.e.runtime.Configure(t, cursorfix.Scenario{Modes: advertisedCursorModes(), WaitForMode: true})
			} else {
				c = postBootstrapCursorSession(t, "off", "")
				c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			}
			first := time.Now()
			c.keys(t, "/new\r")
			c.wait(t, "fresh request", func() bool { return rotationNewCount(c, t) == 2 })
			c.keys(t, "deadline-held\r")
			c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
			// Repeat aliases under finite ordinary-input pressure at half the original budget.
			timer := time.NewTimer(time.Until(first.Add(1500 * time.Millisecond)))
			<-timer.C
			c.keys(t, strings.Repeat("/clear\r/new\r", 20))
			c.awaitFailure(t, time.Until(first.Add(cancelWait+time.Second)))
			elapsed := time.Since(first)
			if elapsed > cancelWait+time.Second || elapsed < cancelWait-100*time.Millisecond {
				t.Fatalf("cancel bound changed: %s", elapsed)
			}
			if rotationNewCount(c, t) != 2 {
				t.Fatal("repeated controls issued extra session creation")
			}
			rotationNoBytes(t, c, "deadline-held")
			row, _ := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if row.SessionID != "synthetic-session-1" || row.State == store.StateEnded {
				t.Fatalf("late/unconfirmed creation fabricated publication/end: %+v", row)
			}
		})
	}
}

func TestCursorRotationAfterKnownZeroByteRetirementNeedsNoPromptOutcome(t *testing.T) {
	// Serial: protected start Sync, real guarded protocol writer and isolated runtime.
	for _, phase := range []string{"start Sync", "start report"} {
		t.Run(phase, func(t *testing.T) {
			kind := "submission_started"
			if phase == "start report" {
				kind = "not-armed"
			}
			barrier, opener := newRecordBarrier(t, kind)
			i := &completeInput{ready: make(chan struct{})}
			c := postBootstrapCursorSessionWithInput(t, "off", "", opener, i.start)
			<-i.ready
			func() {
				defer func() {
					if t.Failed() {
						rotationStartupFailure(t, c, barrier)
					}
				}()
				c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			}()
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			dir := ""
			if phase == "start report" {
				dir = t.TempDir()
				t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "UserPromptSubmit")
				t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "after")
				t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
				t.Cleanup(func() { os.WriteFile(filepath.Join(dir, "release"), nil, 0600) })
			}
			input := terminalInput{text: "zero-byte-old"}
			c.wait(t, "actual old input admission", func() bool { input.accepted = i.admit(); return input.accepted })
			i.emit(input)
			if phase == "start report" {
				rotationEntered(c, t, dir)
			} else {
				select {
				case <-barrier.entered:
				case <-time.After(time.Second):
					t.Fatal("real start record did not block")
				}
			}
			c.keys(t, "/clear\rzero-byte-held\r^")
			c.wait(t, "rotation and held input consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ^")) })
			barrier.unblock()
			if dir != "" {
				rotationRelease(t, filepath.Join(dir, "release"))
			}
			c.wait(t, "new without nonexistent prompt outcome", func() bool { return rotationNewCount(c, t) == 2 && rotationRecordCount(c, t, "input_held") == 1 })
			rotationNoBytes(t, c, "zero-byte-old")
			rotationRelease(t, c.e.runtime.NewRelease)
			c.wait(t, "new turn", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
			c.stop(t)
			if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "zero-byte-held"}}) {
				t.Fatalf("unwritten retirement replay/misrouting: %+v", got)
			}
		})
	}
}

func TestCursorDelayedOldAdmissionRetainsOutgoingSessionAfterPublication(t *testing.T) {
	// Serial: existing complete-input seam parks actual consumption before mailbox insertion.
	i := &completeInput{ready: make(chan struct{})}
	c := postBootstrapCursorSessionWithInput(t, "off", "", openTranscript, i.start)
	<-i.ready
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	old := terminalInput{text: "old-admitted-late-insertion", accepted: i.admit()}
	if !old.accepted {
		t.Fatal("old consumption was not eligible")
	}
	i.emit(terminalInput{text: "/new"})
	c.wait(t, "published new identity", func() bool {
		return rotationRecordCount(c, t, "rotation_published") == 1 && bytes.Contains(c.pane.bytes(), []byte("Cursor fresh conversation published"))
	})
	i.emit(old)
	c.wait(t, "old insertion cancelled", func() bool { return rotationRecordCount(c, t, "submission_cancelled") == 1 })
	rotationNoBytes(t, c, old.text)
	for _, r := range c.records(t) {
		if r.Data.Text == old.text || r.Kind == "submission_cancelled" {
			if r.Session != "synthetic-session-1" {
				t.Fatalf("delayed admission falsely attributed to fresh conversation: %+v", r)
			}
		}
	}
	i.submit(t, c, "fresh-explicit-after-delayed-old")
	c.wait(t, "fresh explicit complete", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "fresh-explicit-after-delayed-old"}}) {
		t.Fatalf("old consumption replayed: %+v", got)
	}
}

func TestCursorRotationAdmissionParkedAcrossPublicationRemainsProvisional(t *testing.T) {
	// Serial: parks the real admission callback; durable unknown proof owns release.
	i := &completeInput{ready: make(chan struct{})}
	c := postBootstrapCursorSessionWithInput(t, "off", "", openTranscript, i.start)
	<-i.ready
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	gate := store.HookGate{Event: "PermissionRequest", ParentPID: b.GatewayPID, ParentStart: b.GatewayStart, SessionID: b.SessionID, Cursor: &b}
	token := "aaaaaaaa-aaaa-4aaa-aaaa-000000000088"
	if _, a, err := c.e.s.InsertRelayRequest(b.InstanceID, gate, store.RelayRequest{RequestToken: token, ToolName: "Unknown", ToolInput: "{}", SettledAt: time.Now().Add(time.Minute)}, 0, store.DefaultLockWait); err != nil || !a.Applied {
		t.Fatalf("independent hold: %+v %v", a, err)
	}
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
	i.emit(terminalInput{text: "/new"})
	c.wait(t, "rotation pending", func() bool { return rotationNewCount(c, t) == 2 })
	parked := terminalInput{text: "parked-during-rotation-雪\nsecond line", accepted: i.admit()}
	if !parked.accepted {
		t.Fatal("rotation consumption lacked provisional authority")
	}
	// No second admit: it would replace the snapshot whose insertion is parked.
	rotationRelease(t, c.e.runtime.NewRelease)
	c.wait(t, "mailbox publication", func() bool {
		return rotationRecordCount(c, t, "rotation_published") == 1 && bytes.Contains(c.pane.bytes(), []byte("Cursor fresh conversation published"))
	})
	i.emit(parked)
	c.wait(t, "parked input remains provisional", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
	heldID := ""
	for _, r := range c.records(t) {
		if r.Kind == "input_held" {
			if r.Session != "" || r.Data.Text != parked.text {
				t.Fatalf("provisional input acquired a conversation: %+v", r)
			}
			heldID = r.Data.ID
		}
		if r.Kind == "submission_accepted" || r.Kind == "submission_started" {
			t.Fatalf("publication bypassed durable unknown hold: %+v", r)
		}
	}
	rotationNoBytes(t, c, parked.text)
	request := c.e.request(t, token)
	if request.ProvenGone() || request.MainTurnEndEligible || request.AgentID != "" {
		t.Fatalf("publication invented unknown-origin proof: %+v", request)
	}
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET closed_at=?`, time.Now().UnixMilli())
	// A closed unknown request still requires independent proof before promotion.
	c.keys(t, "ordinary-under-closed-hold\r")
	c.wait(t, "closed hold still refuses", func() bool { return rotationRecordCount(c, t, "submission_refused") == 1 })
	rotationNoBytes(t, c, parked.text)
	if rotationRecordCount(c, t, "submission_accepted") != 0 {
		t.Fatal("closed_at supplied unknown proof")
	}
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET proven_gone_at=? WHERE request_token=?`, time.Now().UnixMilli(), token)
	c.idleObservationBarrier(t, "parked-hold-independently-cleared")
	rotationNoBytes(t, c, parked.text)
	c.keys(t, "fresh-explicit-after-parked-hold\r")
	c.wait(t, "guarded fresh turns complete in order", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 2 })
	c.stop(t)
	accepted := 0
	row, _ := c.e.s.GetSpawn(b.InstanceID)
	raw, err := os.ReadFile(row.JSONLPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var r struct {
			Kind    string `json:"kind"`
			Session string `json:"session_id"`
			Data    struct {
				ID       string `json:"submission_id"`
				Released bool   `json:"released_from_hold"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind == "submission_accepted" && r.Data.ID == heldID {
			accepted++
			if r.Data.ID != heldID || !r.Data.Released || r.Session != "synthetic-session-2" {
				t.Fatalf("parked value bypassed guarded fresh promotion: %s", line)
			}
		}
	}
	if accepted != 1 || !reflect.DeepEqual(rotationPrompts(c, t), []rotationPrompt{{"synthetic-session-2", parked.text}, {"synthetic-session-2", "fresh-explicit-after-parked-hold"}}) {
		t.Fatalf("parked input lost/replayed/crossed sessions: accepted=%d prompts=%+v", accepted, rotationPrompts(c, t))
	}
}

func TestCursorRotationRetiredNumericResponsesKeepSessionBoundaries(t *testing.T) {
	// Serial: actual successive requests and retired responses, protected attribution.
	c := postBootstrapCursorSession(t, "off", "")
	origins := map[string]string{"1": "", "2": "synthetic-session-1"}
	for turn := 1; turn <= 2; turn++ {
		session := fmt.Sprintf("synthetic-session-%d", turn)
		text := fmt.Sprintf("boundary-turn-%d", turn)
		c.keys(t, text+"\r")
		c.wait(t, "turn outcome", func() bool { return rotationRecordCount(c, t, "turn_outcome") == turn })
		var replay []json.RawMessage
		for _, f := range c.e.runtime.Frames(t) {
			if len(f["method"]) == 0 || len(f["id"]) == 0 {
				continue
			}
			id := string(f["id"])
			if _, ok := origins[id]; !ok {
				origins[id] = session
			}
			replay = append(replay, json.RawMessage(fmt.Sprintf(`{"id":%s,"result":{}}`, id)))
		}
		prior := rotationRecordCount(c, t, "retired_response")
		c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", AfterNew: replay})
		c.keys(t, "/new\r")
		c.wait(t, "retired numeric responses", func() bool {
			return rotationRecordCount(c, t, "retired_response") == prior+len(replay) && rotationRecordCount(c, t, "rotation_published") == turn
		})
		for _, r := range c.records(t) {
			if r.Kind == "retired_response" {
				want, ok := origins[r.Data.Request]
				if !ok || r.Session != want {
					t.Errorf("retired response invented/crossed origin: %+v want=%q known=%v", r, want, ok)
				}
			}
		}
	}
	c.keys(t, "fresh-after-retired-numeric\r")
	c.wait(t, "fresh turn survives", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 3 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-1", "boundary-turn-1"}, {"synthetic-session-2", "boundary-turn-2"}, {"synthetic-session-3", "fresh-after-retired-numeric"}}) {
		t.Fatalf("retired responses acquired current authority: %+v", got)
	}
}

func TestCursorRepeatedRotationPathsKeepSeparatePublication(t *testing.T) {
	// Serial: real session, protocol and guarded publication subprocesses.
	c := postBootstrapCursorSession(t, "off", "")
	before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		c.e.runtime.Configure(t, cursorfix.Scenario{WaitForNew: true})
		c.keys(t, "/new\r")
		c.wait(t, "new rotation request", func() bool { return rotationNewCount(c, t) == n+1 })
		repeated := bytes.Count(c.pane.bytes(), []byte("repeated control ignored"))
		c.keys(t, "/clear\r/new\r")
		c.wait(t, "same pending rotation controls consumed", func() bool { return bytes.Count(c.pane.bytes(), []byte("repeated control ignored")) > repeated })
		row, _ := c.e.s.GetSpawn(before.InstanceID)
		if row.SessionID != fmt.Sprintf("synthetic-session-%d", n) || rotationNewCount(c, t) != n+1 {
			t.Fatal("repeat published or issued another session/new")
		}
		rotationRelease(t, c.e.runtime.NewRelease)
		c.wait(t, "new rotation published", func() bool {
			return rotationRecordCount(c, t, "rotation_published") == n && bytes.Count(c.pane.bytes(), []byte("Cursor fresh conversation published")) == n
		})
		if err := os.Remove(c.e.runtime.NewRelease); err != nil {
			t.Fatal(err)
		}
		row, err = c.e.s.GetSpawn(before.InstanceID)
		history, historyErr := c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
		if err != nil || historyErr != nil || row.SessionID != fmt.Sprintf("synthetic-session-%d", n+1) || row.LifeNumber != before.LifeNumber || len(history) != n {
			t.Fatalf("publication/history: %+v %+v %v %v", row, history, err, historyErr)
		}
		seen := map[string]bool{}
		for _, h := range history {
			if seen[h.SessionID] {
				t.Fatal("duplicate publication history")
			}
			seen[h.SessionID] = true
		}
		for i := 1; i <= n; i++ {
			if !seen[fmt.Sprintf("synthetic-session-%d", i)] {
				t.Fatal("new rotation lost old history")
			}
		}
	}
	c.keys(t, "fresh-after-two-rotations\r")
	c.wait(t, "positive fresh turn", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-3", "fresh-after-two-rotations"}}) {
		t.Fatalf("rotation replay/captured target: %+v", got)
	}
}
