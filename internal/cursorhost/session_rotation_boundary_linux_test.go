//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorRotationHeldCapacityIsProvisional(t *testing.T) {
	// Serial: real admission/mailbox and protected transcript, isolated runtime.
	for _, capacity := range []string{"entries", "bytes", "individual"} {
		t.Run(capacity, func(t *testing.T) {
			i := &completeInput{ready: make(chan struct{})}
			c := postBootstrapCursorSessionWithInput(t, "", openTranscript, i.start)
			c.waitFailureDiagnostics = true
			<-i.ready
			c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			i.emit(terminalInput{text: "/new"})
			c.wait(t, "pending new", func() bool { return rotationNewCount(c, t) == 2 })
			var texts []string
			switch capacity {
			case "entries":
				for n := 0; n < 32; n++ {
					texts = append(texts, fmt.Sprintf("held-entry-%02d", n))
				}
			case "bytes":
				for n := 0; n < 8; n++ {
					texts = append(texts, byteBoundaryText(n, 1024*1024))
				}
			case "individual":
				texts = []string{byteBoundaryText(0, 1024*1024)}
			}
			for _, text := range texts {
				i.submit(t, text)
			}
			excess := "over-capacity-never-transmitted"
			if capacity == "individual" {
				excess = byteBoundaryText(9, 1024*1024+1)
			}
			i.submit(t, excess)
			c.wait(t, "provisional capacity accounted", func() bool {
				return rotationRecordCount(c, t, "input_held") == len(texts) && rotationRecordCount(c, t, "submission_refused") == 1
			})
			c.wait(t, "exact capacity refusal notice", func() bool {
				return bytes.Contains(c.pane.bytes(), []byte("Cursor input refused while starting or busy (capacity); nothing sent"))
			})
			if rotationRecordCount(c, t, "submission_accepted") != 0 || len(rotationPrompts(c, t)) != 0 {
				t.Fatal("provisional reservations became old-conversation admissions")
			}
			raw, err := os.ReadFile(c.e.runtime.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("session/prompt")) {
				t.Fatal("held capacity reached peer before confirmation")
			}
			rotationRelease(t, c.e.runtime.NewRelease)
			for n := range texts {
				c.wait(t, fmt.Sprintf("fresh capacity turn %d", n+1), func() bool { return rotationRecordCount(c, t, "turn_outcome") >= n+1 })
			}
			var want []rotationPrompt
			for _, text := range texts {
				want = append(want, rotationPrompt{"synthetic-session-2", text})
			}
			if !reflect.DeepEqual(rotationPrompts(c, t), want) {
				t.Fatal("provisional capacity lost exact FIFO text or replayed overflow")
			}
			if capacity != "individual" {
				// Follow the capacity advice: after publication and completed turns,
				// only a later explicit submission delivers the refused text.
				rotationNoBytes(t, c, excess)
				input := terminalInput{text: excess}
				c.wait(t, "explicit capacity submission authority", func() bool {
					input.accepted = i.admit()
					return input.accepted
				})
				i.emit(input)
				c.wait(t, "explicit capacity retry completes", func() bool { return rotationRecordCount(c, t, "turn_outcome") == len(texts)+1 })
				want = append(want, rotationPrompt{"synthetic-session-2", excess})
				if !reflect.DeepEqual(rotationPrompts(c, t), want) {
					t.Fatal("explicit retry failed or overflow was replayed")
				}
			}
			c.stop(t)
		})
	}
}

func TestCursorRotationRetiredTrafficCannotAcquireCurrentAuthority(t *testing.T) {
	// Serial: actual protocol/controller, kernel evidence and protected records.
	for _, traffic := range []string{"known old", "unattributed", "foreign", "owned death with old traffic"} {
		t.Run(traffic, func(t *testing.T) {
			c := postBootstrapCursorSession(t, "", true)
			old := json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old-output-not-current"}}}}`)
			frames := []json.RawMessage{old, json.RawMessage(`{"id":2,"result":{"sessionId":"synthetic-session-1"}}`), json.RawMessage(`{"id":"old-child-auth","method":"session/request_permission","params":{"sessionId":"synthetic-session-1","agentId":"child","toolCall":{"toolCallId":"old","title":"Bash"},"options":[{"optionId":"yes","kind":"allow_once"}]}}`)}
			if traffic == "unattributed" {
				frames = []json.RawMessage{json.RawMessage(`{"method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"unattributed"}}}}`)}
			}
			if traffic == "foreign" {
				frames = []json.RawMessage{json.RawMessage(`{"method":"session/update","params":{"sessionId":"never-observed","update":{}}}`)}
			}
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true, AfterNew: frames})
			c.keys(t, "/clear\r")
			c.wait(t, "new pending", func() bool { return rotationNewCount(c, t) == 2 })
			if traffic == "owned death with old traffic" {
				raw, _ := os.ReadFile(c.e.runtime.PID)
				var pid int
				fmt.Sscan(string(raw), &pid)
				syscall.Kill(pid, syscall.SIGKILL)
			}
			rotationRelease(t, c.e.runtime.NewRelease)
			if traffic != "known old" {
				err := c.awaitFailure(t, 5*time.Second)
				t.Logf("fail-closed traffic: %v", err)
				return
			}
			c.wait(t, "old frames inspected", func() bool {
				return rotationRecordCount(c, t, "retired_event") == 2 && rotationRecordCount(c, t, "retired_response") == 1
			})
			for _, record := range c.records(t) {
				if record.Kind == "retired_event" && record.Session != "synthetic-session-1" {
					t.Errorf("late outgoing event falsely attributed to fresh conversation in protected record: %+v", record)
				}
			}
			if rotationRecordCount(c, t, "authorization_admitted") != 0 || bytes.Contains(c.pane.bytes(), []byte("old-output-not-current")) {
				t.Fatal("retired traffic acquired current output/authorization authority")
			}
			c.keys(t, "current-survives-old-traffic\r")
			c.wait(t, "current turn complete", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 1 })
			c.stop(t)
			if got := rotationPrompts(c, t); !reflect.DeepEqual(got, []rotationPrompt{{"synthetic-session-2", "current-survives-old-traffic"}}) {
				t.Fatalf("stale response redirected/ended current: %+v", got)
			}
			for _, f := range c.e.runtime.Frames(t) {
				if string(f["id"]) == `"old-child-auth"` && len(f["method"]) == 0 {
					t.Fatal("retired authorization received an answer")
				}
			}
		})
	}
}

func TestCursorRotationExitInterruptAndEligiblePausePreemptPhases(t *testing.T) {
	// Serial: real Run reports, PTY and public pause using verified synthetic pane.
	for _, phase := range []string{"cancellation", "new pending", "confirmation sync", "publication before", "publication after", "published sync"} {
		for _, control := range []string{"exit", "interrupt", "pause"} {
			if phase == "cancellation" && control == "pause" {
				continue
			} // Working pause refusal has its existing public owner.
			t.Run(phase+"/"+control, func(t *testing.T) {
				exitConsumed := make(chan struct{})
				var once sync.Once
				starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
					return terminalLoop(in, out, func(v terminalInput) {
						emit(v)
						if v.text == "/exit" {
							once.Do(func() { close(exitConsumed) })
						}
					}, admit, choices...)
				}
				kind := "not-armed"
				if phase == "confirmation sync" {
					kind = "rotation_confirmed"
				}
				if phase == "published sync" {
					kind = "rotation_published"
				}
				protectedPhase := phase == "confirmation sync" || phase == "published sync"
				controls := make(chan []byte, 2)
				barrier, opener := newMatchingRecordBarrier(t, kind, func(raw []byte) bool {
					if protectedPhase {
						var r submissionRecord
						if json.Unmarshal(raw, &r) == nil && r.Kind == "input_control" {
							select {
							case controls <- append([]byte(nil), raw...):
							default: // Two retained records already establish a duplicate.
							}
						}
					}
					return true
				})
				opened := make(chan store.CursorRuntime, 1)
				if protectedPhase {
					original := opener
					opener = func(home string, b store.CursorRuntime) (*transcript, error) {
						tr, err := original(home, b)
						if err == nil {
							opened <- b
						}
						return tr, err
					}
				}
				initial := ""
				if phase == "cancellation" {
					initial = "HOLD"
				}
				c := postBootstrapCursorSessionWithInput(t, initial, opener, starter, phase == "confirmation sync")
				defer barrier.unblock()
				diagnosed := false
				defer func() {
					if protectedPhase && t.Failed() && !diagnosed {
						rotationFailureDiagnostics(t, c, barrier, []string{"spawns", "cursor_runtimes", "permission_requests"})
					}
				}()
				failPhase := func(reason string) {
					t.Helper()
					t.Errorf("protected rotation phase setup: %s", reason)
					diagnosed = true
					rotationFailureDiagnostics(t, c, barrier, []string{"spawns", "cursor_runtimes", "permission_requests"})
					t.FailNow()
				}
				var firstBinding store.CursorRuntime
				if protectedPhase {
					select {
					case firstBinding = <-opened:
					default:
						failPhase("missing actual transcript launch binding")
					}
				}
				c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
				c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
				dir := ""
				if strings.HasPrefix(phase, "publication ") {
					dir = rotationBarrier(t, strings.TrimPrefix(phase, "publication "))
				}
				firstSend := time.Now()
				c.keys(t, "/new\r")
				if phase == "cancellation" {
					c.wait(t, "active cancellation", func() bool { return rotationRecordCount(c, t, "rotation_requested") == 1 })
				} else {
					c.wait(t, "new pending", func() bool { return rotationNewCount(c, t) == 2 })
				}
				c.keys(t, "preempt-held\r")
				c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
				var phaseDeadline time.Time
				if protectedPhase {
					var raw []byte
					select {
					case raw = <-controls:
					default:
						failPhase("missing first protected rotation input_control")
					}
					select {
					case <-controls:
						failPhase("duplicate protected input_control before phase entry")
					default:
					}
					var r submissionRecord
					if err := json.Unmarshal(raw, &r); err != nil {
						failPhase(fmt.Sprintf("decode first protected control: %v", err))
					}
					if r.Kind != "input_control" || r.Sequence == 0 || r.Instance != firstBinding.InstanceID || r.Token != firstBinding.Token || r.Life != firstBinding.Life || r.Connection != firstBinding.Connection || r.Session != "synthetic-session-1" || r.Data.Control != "rotation" || r.Data.InputID != "1" || r.Data.InputEpoch == nil || *r.Data.InputEpoch != 0 || r.Data.Count != 1 || r.Data.ConsumedAt.IsZero() || r.Data.ConsumedAt.Before(firstSend) || r.Data.ConsumedAt.After(time.Now()) {
						failPhase(fmt.Sprintf("first rotation control correlation changed: %+v", r))
					}
					// This is the mailbox timestamp used by cancelAt, never a new allowance.
					phaseDeadline = r.Data.ConsumedAt.Add(cancelWait)
				}
				checkPhase := func() {
					t.Helper()
					if !protectedPhase {
						return
					}
					select {
					case c.endErr = <-c.done:
						c.ended = true
						failPhase(fmt.Sprintf("Run ended before control injection: %v", c.endErr))
					default:
					}
					if !time.Now().Before(phaseDeadline) {
						failPhase(fmt.Sprintf("original first-control deadline expired: %s", phaseDeadline))
					}
					select {
					case <-controls:
						failPhase("duplicate protected input_control before control injection")
					default:
					}
				}
				checkPhase()
				release := c.e.runtime.NewRelease
				if phase != "new pending" && phase != "cancellation" {
					rotationRelease(t, c.e.runtime.NewRelease)
					if dir != "" {
						rotationEntered(c, t, dir)
						release = filepath.Join(dir, "release")
					} else {
						timer := time.NewTimer(time.Until(phaseDeadline))
						defer timer.Stop()
						select {
						case <-barrier.entered:
						case c.endErr = <-c.done:
							c.ended = true
							failPhase(fmt.Sprintf("Run ended before %s Sync entry: %v", phase, c.endErr))
						case <-timer.C:
							failPhase(fmt.Sprintf("%s Sync not reached by original first-control deadline %s", phase, phaseDeadline))
						}
						checkPhase()
					}
				}
				// Pause must be eligible from actual durable waiting state; no unresolved dialog is hidden.
				if control == "pause" {
					row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
					if err != nil || row.State != store.StateWaiting {
						t.Fatalf("pause setup ineligible: %+v %v", row, err)
					}
					tc := tmux.New("", tmux.Timeouts{Query: time.Second, Action: time.Second, Create: time.Second, WaitDelay: time.Second})
					send := pausePTY{PauseTmux: tc, c: c, t: t, release: release, exitConsumed: exitConsumed}
					checkPhase()
					if phase == "confirmation sync" || phase == "published sync" {
						go func() { <-exitConsumed; barrier.unblock() }()
					}
					_, err = api.Pause(context.Background(), c.e.s, send, api.PauseEnv{Relay: api.RelayView{Procs: probe.NewProcChecker()}}, 5, api.PauseParams{InstanceID: row.InstanceID})
					if err != nil {
						t.Errorf("eligible pause failed: %v", err)
					}
				} else {
					keys := "/exit\r"
					if control == "interrupt" {
						keys = "\x03"
					}
					checkPhase()
					c.keys(t, keys)
					// Input callback observation proves consumption even while rendering or Sync is held.
					if control == "exit" {
						select {
						case <-exitConsumed:
						case <-time.After(time.Second):
							t.Fatal("exit not consumed")
						}
					}
					if control == "interrupt" {
						c.keys(t, "^")
						c.wait(t, "interrupt consumed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ^")) })
					}
					rotationRelease(t, release)
					barrier.unblock()
				}
				select {
				case err := <-c.done:
					c.ended = true
					if control != "interrupt" && err != nil {
						t.Errorf("intentional shutdown failed: %v", err)
					}
					if control == "interrupt" && err == nil {
						t.Error("rotation interruption silently resumed")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("control did not terminate gateway")
				}
				rotationNoBytes(t, c, "preempt-held")
				rawPID, _ := os.ReadFile(c.e.runtime.PID)
				var pid int
				fmt.Sscan(string(rawPID), &pid)
				if syscall.Kill(pid, 0) == nil {
					t.Fatal("gateway returned while owned runtime remained live")
				}
				row, _ := c.e.s.GetSpawn(c.e.binding.InstanceID)
				if control != "interrupt" && row.State != store.StateEnded {
					t.Errorf("real intentional termination left false-live row: %+v", row)
				}
				if control == "interrupt" && row.State == store.StateEnded {
					t.Error("failed rotation fabricated intentional end")
				}
			})
		}
	}
}

// This is a genuine reporter boundary, not a callback-only record-format test.
func TestCursorRotationConfirmedAndPublishedSyncFailureRefusesHeld(t *testing.T) {
	// Serial: real controller and protected transcript file fault injection.
	for _, kind := range []string{"rotation_confirmed", "rotation_published", "submission_accepted", "rotation_published_write"} {
		t.Run(kind, func(t *testing.T) {
			recordKind := kind
			writeFailure := kind == "rotation_published_write"
			if writeFailure {
				recordKind = "rotation_published"
			}
			f := &rotationSyncFailure{kind: recordKind, writeFailure: writeFailure}
			opener := func(home string, b store.CursorRuntime) (*transcript, error) {
				tr, err := openTranscript(home, b)
				if err == nil {
					f.transcriptFile = tr.file
					tr.file = f
				}
				return tr, err
			}
			c := postBootstrapCursorSessionWithInput(t, "", opener, terminalLoop, true)
			c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			c.keys(t, "/new\r")
			c.wait(t, "new pending", func() bool { return rotationNewCount(c, t) == 2 })
			c.keys(t, "sync-failed-held\r")
			c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
			rotationRelease(t, c.e.runtime.NewRelease)
			err := c.awaitFailure(t, 5*time.Second)
			expectedFailure := "persistence"
			if writeFailure {
				expectedFailure = "write failed"
			}
			if !strings.Contains(err.Error(), expectedFailure) {
				t.Fatalf("wrong protected failure: %v", err)
			}
			rotationNoBytes(t, c, "sync-failed-held")
			for _, record := range c.records(t) {
				if record.Kind == "input_held" && !bytes.Contains(c.pane.bytes(), []byte("held submission "+record.Data.ID+" refused; nothing sent to either conversation")) && !bytes.Contains(c.pane.bytes(), []byte("submission "+record.Data.ID+" cancelled before dispatch")) {
					t.Errorf("unrecordable admission lost per-held visible refusal for %s; pane=%s", record.Data.ID, c.pane.bytes())
				}
			}
			row, _ := c.e.s.GetSpawn(c.e.binding.InstanceID)
			h, _ := c.e.s.ListSessionHistory(row.InstanceID, row.LifeNumber)
			expected := "synthetic-session-2"
			archives := 1
			if kind == "rotation_confirmed" {
				expected = "synthetic-session-1"
				archives = 0
			}
			if row.SessionID != expected || len(h) != archives || row.State == store.StateEnded {
				t.Fatalf("false rollback/history/end after Sync failure: %+v %+v", row, h)
			}
		})
	}
}

type rotationSyncFailure struct {
	transcriptFile
	kind, current string
	writeFailure  bool
}

func (f *rotationSyncFailure) Write(raw []byte) (int, error) {
	var r struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, err
	}
	f.current = r.Kind
	if f.writeFailure && f.current == f.kind {
		return 0, fmt.Errorf("injected protected Write failure")
	}
	return f.transcriptFile.Write(raw)
}
func (f *rotationSyncFailure) Sync() error {
	if f.kind == f.current {
		return fmt.Errorf("injected protected Sync failure")
	}
	return f.transcriptFile.Sync()
}

func TestCursorRotationProvisionalCapacityDoesNotBorrowOldQueue(t *testing.T) {
	// Serial: real bounded input mailbox and independently blocked active ACP turn.
	i := &completeInput{ready: make(chan struct{})}
	c := postBootstrapCursorSessionWithInput(t, "HOLD", openTranscript, i.start)
	<-i.ready
	c.wait(t, "active receipt", func() bool { return len(rotationPrompts(c, t)) == 1 })
	for n := 0; n < 32; n++ {
		i.submit(t, fmt.Sprintf("old-full-%02d", n))
	}
	c.wait(t, "full old queue", func() bool { return rotationRecordCount(c, t, "submission_accepted") == 33 })
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
	i.emit(terminalInput{text: "/clear"})
	for n := 0; n < 32; n++ {
		i.submit(t, fmt.Sprintf("fresh-full-%02d", n))
	}
	c.wait(t, "old canceled and new held independently", func() bool {
		return rotationRecordCount(c, t, "submission_cancelled") == 32 && rotationRecordCount(c, t, "input_held") == 32
	})
	if rotationRecordCount(c, t, "submission_refused") != 0 {
		t.Fatal("provisional capacity borrowed old reservations")
	}
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "new pending", func() bool { return rotationNewCount(c, t) == 2 })
	rotationRelease(t, c.e.runtime.NewRelease)
	for n := 0; n < 32; n++ {
		c.wait(t, fmt.Sprintf("fresh full-queue turn %d", n+1), func() bool { return rotationRecordCount(c, t, "turn_outcome") >= n+2 })
	}
	c.stop(t)
	want := []rotationPrompt{{"synthetic-session-1", "HOLD"}}
	for n := 0; n < 32; n++ {
		want = append(want, rotationPrompt{"synthetic-session-2", fmt.Sprintf("fresh-full-%02d", n)})
	}
	if !reflect.DeepEqual(rotationPrompts(c, t), want) {
		t.Fatal("old full queue migrated or held reservations lost order")
	}
}

func TestCursorRotationRetiresPermissionPlanCallbacksAndChoices(t *testing.T) {
	// Serial: real Run requests, durable holds and terminal menu keys, synthetic peer.
	barrier, opener := newMatchingRecordBarrier(t, "retired_event", func(raw []byte) bool {
		var r struct {
			Data struct {
				Params struct {
					Session string `json:"sessionId"`
					Update  struct {
						Kind   string `json:"sessionUpdate"`
						Tool   string `json:"toolCallId"`
						Status string `json:"status"`
					} `json:"update"`
				} `json:"params"`
			} `json:"data"`
		}
		return json.Unmarshal(raw, &r) == nil && r.Data.Params.Session == "synthetic-session-1" && r.Data.Params.Update.Kind == "tool_call_update" && r.Data.Params.Update.Tool == "old-shell" && r.Data.Params.Update.Status == "completed"
	})
	c := postBootstrapCursorSessionWithInput(t, "", opener, terminalLoop)
	c.diagnosticBarrier = barrier
	defer barrier.unblock()
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	permission, _ := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"old-permission"`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-1", "old-shell", "execute")})
	planParams := json.RawMessage(`{"toolCallId":"old-plan-tool","name":"Synthetic plan","overview":"Safe plan","plan":"one step","todos":[],"isProject":false,"phases":[]}`)
	plan, _ := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"old-plan"`), Method: "cursor/create_plan", Params: planParams})
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{permission, plan}, CompleteBeforeAnswers: true, WaitForRelease: true})
	c.keys(t, "old-request-turn\r")
	c.wait(t, "two genuine pending requests", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 2 })
	oldKeys := regexp.MustCompile(`\[([^]]+)\] Allow once`).FindAllSubmatch(c.pane.bytes(), -1)
	if len(oldKeys) != 1 {
		t.Fatalf("missing old genuine choice: %s", c.pane.bytes())
	}
	oldKey := string(oldKeys[0][1])
	// Same request IDs and the plan's same tool under a fresh callback ID all remain retired.
	newPlan, _ := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"delayed-old-plan"`), Method: "cursor/create_plan", Params: planParams})
	staleExecution, _ := json.Marshal(frame{JSONRPC: "2.0", Method: "session/update", Params: executionUpdate("synthetic-session-1", "old-shell", "completed", `{"exitCode":0,"stdout":"done","stderr":""}`)})
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true, AfterNew: []json.RawMessage{permission, plan, newPlan, staleExecution}})
	c.keys(t, "/new\rheld-after-old-requests\r")
	c.wait(t, "held new input", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
	rotationRelease(t, c.e.runtime.Release)
	c.wait(t, "new request", func() bool { return rotationNewCount(c, t) == 2 })
	rotationRelease(t, c.e.runtime.NewRelease)
	c.wait(t, "retired callbacks inspected", func() bool {
		select {
		case <-barrier.entered:
			return rotationRecordCount(c, t, "retired_authorization") == 3 && rotationRecordCount(c, t, "retired_event") >= 1
		default:
			return false
		}
	})
	for _, record := range c.records(t) {
		if record.Kind == "retired_authorization" && record.Session != "synthetic-session-1" {
			t.Errorf("retired permission/plan callback falsely attributed to fresh conversation: %+v", record)
		}
	}
	rows, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("retired callbacks created requests: %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.ProvenGone() || r.MainTurnEndEligible || r.Decision != "" {
			t.Fatalf("fresh identity/old completion fabricated hold proof: %+v", r)
		}
	}
	rotationNoBytes(t, c, "held-after-old-requests")
	current, _ := json.Marshal(frame{JSONRPC: "2.0", ID: json.RawMessage(`"current-permission"`), Method: "session/request_permission", Params: cursorfix.Permission("synthetic-session-2", "fresh-shell", "execute")})
	c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", Requests: []json.RawMessage{current}})
	// Sync parks the controller after its previous eligibility transaction and
	// before its next one; this mutation supplies synthetic fixture proof only.
	c.rotationFixtureSQL(t, barrier, 2, `UPDATE permission_requests SET proven_gone_at=? WHERE instance_id=? AND request_token IN (?,?)`, time.Now().UnixMilli(), c.e.binding.InstanceID, rows[0].RequestToken, rows[1].RequestToken)
	barrier.unblock()
	c.wait(t, "current genuine request", func() bool { return rotationRecordCount(c, t, "authorization_admitted") == 3 })
	keys := regexp.MustCompile(`\[([^]]+)\] Allow once`).FindAllSubmatch(c.pane.bytes(), -1)
	if len(keys) != 2 || string(keys[1][1]) == oldKey {
		t.Fatalf("retired key reused for current: %v", keys)
	}
	c.keys(t, oldKey+"^")
	c.wait(t, "old key consumed as draft", func() bool { return bytes.Contains(c.pane.bytes(), []byte("> "+oldKey+"^")) })
	for _, f := range c.e.runtime.Frames(t) {
		if len(f["method"]) == 0 && len(f["id"]) > 0 {
			t.Fatalf("retired choice/callback answered a request: %s", f["id"])
		}
	}
	c.keys(t, "\x15"+string(keys[1][1]))
	c.wait(t, "current request completes", func() bool { return rotationRecordCount(c, t, "turn_outcome") == 2 })
	c.stop(t)
	replies := 0
	for _, f := range c.e.runtime.Frames(t) {
		if len(f["method"]) == 0 && len(f["id"]) > 0 {
			replies++
			if string(f["id"]) != `"current-permission"` {
				t.Fatalf("retired authorization answered: %s", f["id"])
			}
		}
	}
	if replies != 1 {
		t.Fatalf("current choice answer count %d", replies)
	}
}

func TestCursorRotationCommittedHookStallDoesNotLoseShutdownIdentity(t *testing.T) {
	// Serial: actual after-COMMIT subprocess stays blocked through Run's return.
	for _, control := range []string{"exit", "pause", "interrupt", "timeout", "death"} {
		t.Run(control, func(t *testing.T) {
			i := &completeInput{ready: make(chan struct{})}
			consumed := make(chan struct{})
			var exitOnce sync.Once
			starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
				return i.start(in, out, func(v terminalInput) {
					emit(v)
					if v.text == "/exit" {
						exitOnce.Do(func() { close(consumed) })
					}
				}, admit, choices...)
			}
			c := postBootstrapCursorSessionWithInput(t, "", openTranscript, starter)
			<-i.ready
			c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			c.e.runtime.Configure(t, cursorfix.Scenario{SessionID: "synthetic-session-1", WaitForNew: true})
			dir := rotationBarrier(t, "after")
			first := time.Now()
			i.emit(terminalInput{text: "/new"})
			c.wait(t, "new pending", func() bool { return rotationNewCount(c, t) == 2 })
			i.submit(t, "unreleased-hook-held")
			c.wait(t, "held", func() bool { return rotationRecordCount(c, t, "input_held") == 1 })
			rotationRelease(t, c.e.runtime.NewRelease)
			rotationEntered(c, t, dir)
			// Independent committed SQL, never payload/screen/hook exit, establishes publication.
			bound, err := c.e.s.ReadCursorRuntime(before.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			row, err := c.e.s.GetSpawn(before.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			h, err := c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
			if err != nil || row.SessionID != "synthetic-session-2" || bound.SessionID != row.SessionID || row.JSONLPath != before.JSONLPath || len(h) != 1 || h[0].SessionID != before.SessionID || h[0].JSONLPath != before.JSONLPath {
				t.Fatalf("no committed atomic publication: row=%+v runtime=%+v history=%+v err=%v", row, bound, h, err)
			}
			if err := VerifyRuntime(bound, false); err != nil {
				t.Fatal(err)
			}
			switch control {
			case "exit":
				i.emit(terminalInput{text: "/exit"})
			case "interrupt":
				i.emit(terminalInput{interrupt: true})
			case "death":
				if err := syscall.Kill(bound.RuntimePID, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			case "pause":
				if row.State != store.StateWaiting {
					t.Fatalf("pause setup ineligible: %+v", row)
				}
				tc := tmux.New("", tmux.Timeouts{Query: time.Second, Action: time.Second, Create: time.Second, WaitDelay: time.Second})
				// The established Pause sender writes a separate harmless release file;
				// the actual SessionStart barrier remains unreleased throughout shutdown.
				send := pausePTY{PauseTmux: tc, c: c, t: t, release: filepath.Join(t.TempDir(), "pause-sent"), exitConsumed: consumed}
				_, err = api.Pause(context.Background(), c.e.s, send, api.PauseEnv{Relay: api.RelayView{Procs: probe.NewProcChecker()}}, 5, api.PauseParams{InstanceID: row.InstanceID})
				if err != nil {
					t.Errorf("eligible pause after committed stalled hook: %v", err)
				}
			}
			select {
			case err := <-c.done:
				c.ended = true
				intentional := control == "exit" || control == "pause"
				if (err == nil) != intentional {
					t.Errorf("shutdown outcome %s: %v", control, err)
				}
				t.Logf("first-control elapsed=%s; outcome=%v", time.Since(first), err)
			case <-time.After(time.Until(first.Add(cancelWait + time.Second))):
				t.Fatal("stalled hook extended original first-control bound")
			}
			if time.Since(first) > cancelWait+time.Second {
				t.Error("shutdown exceeded original observer allowance")
			}
			if control == "timeout" && time.Since(first) < cancelWait-100*time.Millisecond {
				t.Error("timeout did not retain original budget")
			}
			if _, err := os.Stat(filepath.Join(dir, "release")); !os.IsNotExist(err) {
				t.Fatalf("report barrier released before Run exited: %v", err)
			}
			if syscall.Kill(bound.RuntimePID, 0) == nil {
				t.Fatal("Run returned before actual owned runtime death")
			}
			rotationNoBytes(t, c, "unreleased-hook-held")
			row, err = c.e.s.GetSpawn(before.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			h, err = c.e.s.ListSessionHistory(before.InstanceID, before.LifeNumber)
			if err != nil || row.SessionID != bound.SessionID || row.JSONLPath != before.JSONLPath || row.LifeNumber != before.LifeNumber || len(h) != 1 || h[0].SessionID != before.SessionID {
				t.Fatalf("committed identity/history rolled back: %+v %+v %v", row, h, err)
			}
			intentional := control == "exit" || control == "pause"
			if (row.State == store.StateEnded) != intentional {
				t.Errorf("false end/live state: %+v", row)
			}
			ends := 0
			for _, r := range c.records(t) {
				if r.Kind == "input_held" && !bytes.Contains(c.pane.bytes(), []byte("held submission "+r.Data.ID+" refused; nothing sent to either conversation")) {
					t.Errorf("held owner lacks visible refusal: %+v", r)
				}
				if r.Kind == "session_end" {
					ends++
					if r.Session != bound.SessionID {
						t.Errorf("committed fresh end misattributed: %+v", r)
					}
				}
				if r.Kind == "submission_accepted" || r.Kind == "submission_started" {
					t.Errorf("stalled publication admitted held input: %+v", r)
				}
			}
			if intentional && ends != 1 || !intentional && ends != 0 {
				t.Errorf("protected intentional ends=%d control=%s", ends, control)
			}
		})
	}
}
