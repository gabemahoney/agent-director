//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

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
				c := postBootstrapCursorSession(t, initial)
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
	c := postBootstrapCursorSession(t, "")
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
	c.wait(t, "provisional input consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("held-until-all-proofs")) })
	rotationNoBytes(t, c, "held-until-all-proofs")
	rotationRelease(t, filepath.Join(dir, "release"))
	c.wait(t, "publication settled with durable holds", func() bool {
		return rotationRecordCount(c, t, "rotation_published") == 1 && rotationRecordCount(c, t, "input_held") == 1
	})
	for _, r := range c.records(t) {
		if r.Kind == "submission_accepted" {
			t.Fatal("publication cleared independent guards")
		}
	}
	c.rotationFixtureSQL(t, nil, 2, `UPDATE permission_requests SET closed_at=?`, time.Now().UnixMilli())
	c.keys(t, "ordinary-must-be-refused\r")
	c.wait(t, "guarded refusal", func() bool { return rotationRecordCount(c, t, "submission_refused") == 1 })
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Unknown'`, time.Now().UnixMilli())
	// A second actual control-input consumption is an observable check, without inferring admission from screen state.
	c.keys(t, "still-refused\r")
	c.wait(t, "remaining hold refuses", func() bool { return rotationRecordCount(c, t, "submission_refused") == 2 })
	rotationNoBytes(t, c, "held-until-all-proofs")
	c.rotationFixtureSQL(t, nil, 1, `UPDATE permission_requests SET proven_gone_at=? WHERE tool_name='Plan'`, time.Now().UnixMilli())
	c.wait(t, "all proofs release held", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "held-until-all-proofs"}}) {
		t.Fatalf("guard release %+v", got)
	}
}

func TestCursorRotationFailureRefusesEveryHeldInput(t *testing.T) {
	// Serial: real runtime/report subprocess and fault injection own environment.
	for _, failure := range []string{"rejection", "missing ID", "malformed ID", "reused ID", "archive", "report after commit", "authority loss", "runtime death"} {
		t.Run(failure, func(t *testing.T) {
			c := postBootstrapCursorSession(t, "")
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
			for _, r := range c.records(t) {
				if r.Kind == "input_held" && !bytes.Contains(c.pane.bytes(), []byte("held submission "+r.Data.ID+" refused; nothing sent to either conversation")) {
					t.Fatalf("missing per-input refusal: %+v pane=%s", r, c.pane.bytes())
				}
				if r.Kind == "submission_started" || r.Kind == "submission_accepted" || r.Kind == "session_end" {
					t.Fatalf("false execution/end: %+v", r)
				}
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
	c := postBootstrapCursorSession(t, "")
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
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
			c := postBootstrapCursorSessionWithInput(t, "", opener, i.start)
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
	c := postBootstrapCursorSessionWithInput(t, "", openTranscript, i.start)
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
	i.submit(t, "fresh-explicit-after-delayed-old")
	c.wait(t, "fresh explicit complete", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
	c.stop(t)
	if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "fresh-explicit-after-delayed-old"}}) {
		t.Fatalf("old consumption replayed: %+v", got)
	}
}

func TestCursorRotationAdmissionParkedAcrossPublicationRemainsProvisional(t *testing.T) {
	// Serial: parks the real admission callback; durable unknown proof owns release.
	i := &completeInput{ready: make(chan struct{})}
	c := postBootstrapCursorSessionWithInput(t, "", openTranscript, i.start)
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
	c.wait(t, "guarded fresh turn completes", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
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
		if r.Kind == "submission_accepted" {
			accepted++
			if r.Data.ID != heldID || !r.Data.Released || r.Session != "synthetic-session-2" {
				t.Fatalf("parked value bypassed guarded fresh promotion: %s", line)
			}
		}
	}
	if accepted != 1 || !reflect.DeepEqual(rotationPrompts(c, t), []rotationPrompt{{"synthetic-session-2", parked.text}}) {
		t.Fatalf("parked input lost/replayed/crossed sessions: accepted=%d prompts=%+v", accepted, rotationPrompts(c, t))
	}
}

func TestCursorRotationRetiredNumericResponsesKeepSessionBoundaries(t *testing.T) {
	// Serial: actual successive requests and retired responses, protected attribution.
	c := postBootstrapCursorSession(t, "")
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
