//go:build linux

package cursorhost

import (
	"bytes"
	"context"
	"database/sql"
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

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// beginCursorLoad drives the actual sole controller; no native Cursor capture is represented.
func beginCursorLoad(t *testing.T, scenario cursorfix.Scenario, initial string, starter inputStarter, before ...func(*requestEnv)) *cursorSession {
	t.Helper()
	return beginObservedCursorLoad(t, scenario, initial, starter, nil, before...)
}

func beginObservedCursorLoad(t *testing.T, scenario cursorfix.Scenario, initial string, starter inputStarter, observe func(context.Context, *requestEnv), before ...func(*requestEnv)) *cursorSession {
	return beginCursorLoadWithOptions(t, scenario, Options{InitialPrompt: initial}, starter, observe, before...)
}

func beginCursorLoadWithOptions(t *testing.T, scenario cursorfix.Scenario, options Options, starter inputStarter, observe func(context.Context, *requestEnv), before ...func(*requestEnv)) *cursorSession {
	t.Helper()
	e := newRequestEnv(t, "off", "synthetic-session-1")
	if setting := os.Getenv("AD_TEST_LOAD_DISABLE_ASK_USER"); setting != "" {
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "config.toml"), []byte("[defaults]\ndisable_ask_user = "+setting+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e.runtime.Configure(t, scenario)
	master, slave := e.prepareRunPTY(t)
	e.exec(t, `UPDATE spawns SET session_id=?,jsonl_path=? WHERE instance_id=?`, "synthetic-session-1", e.tr.path, e.binding.InstanceID)
	for _, fn := range before {
		fn(e)
	}
	c := &cursorSession{e: e, master: master, pane: &observedWire{}, done: make(chan error, 1)}
	options.Runtime = e.runtime.Program
	// The same behavioral test also runs against the pre-load controller via a source overlay.
	if field := reflect.ValueOf(&options).Elem().FieldByName("LoadSession"); field.IsValid() {
		field.SetString("synthetic-session-1")
	}
	go func() {
		if observe == nil {
			c.done <- runWithInput(options, slave, c.pane, openTranscript, starter)
			return
		}
		c.done <- runWithLoadPublicationObserver(options, slave, c.pane, openTranscript, starter, func(ctx context.Context) { observe(ctx, e) })
	}()
	t.Cleanup(func() {
		if !c.ended {
			master.Write([]byte("\x15/exit\r"))
			select {
			case <-c.done:
			case <-time.After(5 * time.Second):
				t.Error("load cleanup timed out")
			}
		}
	})
	return c
}

func TestCursorLoadSavedInitialWaitsForIndependentHoldOnce(t *testing.T) {
	var token string
	c := beginCursorLoadWithOptions(t, cursorfix.Scenario{LoadSupport: true, Modes: advertisedCursorModes()}, Options{InitialPrompt: "saved initial", RequestedMode: "plan"}, terminalLoop, nil, func(e *requestEnv) {
		p, err := apitest.SeedPermissionRequest(e.path, e.binding.InstanceID, "Bash")
		if err != nil {
			t.Fatal(err)
		}
		token = p.RequestToken
		e.exec(t, `UPDATE spawns SET state='pending' WHERE instance_id=?`, e.binding.InstanceID)
		if err := e.tr.record("submission_accepted", map[string]any{"submission_id": "lost-transient", "text": "transient queued before crash"}); err != nil {
			t.Fatal(err)
		}
		if err := e.tr.record("input_held", map[string]any{"text": "held text before crash"}); err != nil {
			t.Fatal(err)
		}
	})
	c.wait(t, "loaded with durable hold", func() bool { return bytes.Contains(c.pane.bytes(), []byte("conversation loaded")) })
	if len(c.prompts(t)) != 0 {
		t.Fatal("saved initial bypassed independent hold")
	}
	c.e.exec(t, `UPDATE permission_requests SET decision='deny',proven_gone_at=1 WHERE request_token=?`, token)
	c.wait(t, "saved initial after actual hold release", func() bool { return len(c.prompts(t)) == 1 })
	c.wait(t, "initial completed", func() bool {
		for _, r := range c.records(t) {
			if r.Kind == "turn_outcome" {
				return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete"))
			}
		}
		return false
	})
	if got := c.prompts(t); len(got) != 1 || got[0] != "saved initial" {
		t.Fatalf("initial replay: %v", got)
	}
	c.stop(t)
	var methods []string
	for _, f := range c.e.runtime.Frames(t) {
		if len(f["method"]) > 0 {
			methods = append(methods, string(f["method"]))
		}
	}
	if got := strings.Join(methods, ","); got != `"initialize","session/load","session/set_mode","session/prompt"` {
		t.Fatalf("saved initial mode/load order: %s", got)
	}
}

func TestCursorLoadQueuedControlPreventsPublicationAndSavedPrompt(t *testing.T) {
	for _, exit := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupt", true: "exit"}[exit], func(t *testing.T) {
			input := &completeInput{ready: make(chan struct{})}
			starter := func(in *os.File, out io.Writer, emit func(terminalInput), admit func() bool, choices ...func(rune) (string, string, bool)) func() {
				stop := input.start(in, out, emit, admit, choices...)
				v := terminalInput{interrupt: true}
				if exit {
					v = terminalInput{text: "/exit"}
				}
				emit(v)
				return stop
			}
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "never dispatch", starter)
			select {
			case <-c.done:
				c.ended = true
			case <-time.After(4 * time.Second):
				t.Fatal("pre-initialize control was not bounded")
			}
			for _, r := range c.records(t) {
				if r.Kind == "session_loaded" || r.Kind == "submission_dispatched" {
					t.Fatalf("retired load published: %+v", r)
				}
			}
			if len(c.prompts(t)) != 0 {
				t.Fatal("canceled load dispatched saved initial")
			}
		})
	}
}

func TestCursorLoadLatestThenRealPrompt(t *testing.T) {
	for _, reply := range []string{`{"result":{}}`, `{"result":{"sessionId":"synthetic-session-1"}}`} {
		t.Run(reply, func(t *testing.T) {
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true, LoadReply: json.RawMessage(reply)}, "", terminalLoop)
			c.wait(t, "loaded", func() bool { return bytes.Contains(c.pane.bytes(), []byte("conversation loaded")) })
			row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil || row.State != store.StatePending || row.SessionID != "synthetic-session-1" || row.JSONLPath != c.e.tr.path {
				t.Fatalf("loaded row: %+v %v", row, err)
			}
			frames := c.e.runtime.Frames(t)
			var methods []string
			for _, f := range frames {
				if len(f["method"]) > 0 {
					methods = append(methods, string(f["method"]))
				}
			}
			if len(methods) != 2 || methods[0] != `"initialize"` || methods[1] != `"session/load"` {
				t.Fatalf("initialize/load ordering: %v", methods)
			}
			var params struct {
				Session string `json:"sessionId"`
				CWD     string `json:"cwd"`
				MCP     []any  `json:"mcpServers"`
			}
			if err := json.Unmarshal(frames[1]["params"], &params); err != nil || params.Session != row.SessionID || params.CWD != row.CWD || params.MCP == nil || len(params.MCP) != 0 {
				t.Fatalf("load params: %+v %v", params, err)
			}
			for _, r := range c.records(t) {
				if r.Kind == "session_start" || r.Kind == "user_input" {
					t.Fatalf("load manufactured readiness/input: %+v", r)
				}
			}
			c.keys(t, "real prompt\r")
			c.wait(t, "real prompt dispatched", func() bool { return len(c.prompts(t)) == 1 })
			c.wait(t, "real prompt completed", func() bool {
				for _, r := range c.records(t) {
					if r.Kind == "turn_outcome" {
						return true
					}
				}
				return false
			})
			if got := c.prompts(t); len(got) != 1 || got[0] != "real prompt" {
				t.Fatalf("prompts: %v", got)
			}
			c.stop(t)
		})
	}
}

func TestCursorLoadFailureNeverCreatesFresh(t *testing.T) {
	for _, tc := range []struct {
		name      string
		supported bool
		reply     string
	}{
		{"unsupported", false, ``},
		{"explicitly disabled", false, ``},
		{"missing native", true, ``},
		{"null", true, `{"result":null}`},
		{"array", true, `{"result":[]}`},
		{"mismatched id", true, `{"result":{"sessionId":"older-session"}}`},
		{"invalid id", true, `{"result":{"sessionId":12}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var native cursorfix.NativeHistory
			var protected cursorfix.GatewayHistory
			var currentPrefix []byte
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: tc.supported, LoadReply: json.RawMessage(tc.reply)}, "must not dispatch", terminalLoop, func(e *requestEnv) {
				if tc.name == "explicitly disabled" {
					e.runtime.Configure(t, cursorfix.Scenario{InitializeReply: json.RawMessage(`{"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}}`)})
				}
				if tc.name != "missing native" {
					return
				}
				native = cursorfix.NewNativeHistory(t, os.Getenv("HOME"), "synthetic-session-1", "older-session")
				native.Latest.RemoveDatabase(t)
				protected = cursorfix.NewGatewayHistory(t, e.binding.InstanceID, e.binding.Life, "synthetic-session-1", "older-session")
				var err error
				currentPrefix, err = os.ReadFile(e.tr.path)
				if err != nil {
					t.Fatal(err)
				}
				e.runtime.Configure(t, cursorfix.Scenario{LoadSupport: true, NativeConfigRoot: native.ConfigRoot})
			})
			select {
			case err := <-c.done:
				c.ended = true
				if err == nil {
					t.Fatal("invalid load succeeded")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("load refusal unbounded")
			}
			for _, f := range c.e.runtime.Frames(t) {
				if string(f["method"]) == `"session/new"` || string(f["method"]) == `"session/prompt"` {
					t.Fatalf("failed load fell back/dispatched: %s", f["method"])
				}
			}
			for _, r := range c.records(t) {
				if r.Kind == "session_loaded" || r.Kind == "session_start" || r.Kind == "submission_dispatched" {
					t.Fatalf("failed load published: %+v", r)
				}
			}
			if tc.name == "missing native" {
				loads := 0
				for _, frame := range c.e.runtime.Frames(t) {
					if string(frame["method"]) == `"session/load"` {
						loads++
						var params struct {
							Session string `json:"sessionId"`
						}
						if err := json.Unmarshal(frame["params"], &params); err != nil || params.Session != native.Latest.ID {
							t.Fatalf("missing native did not load latest: %+v %v", params, err)
						}
					}
				}
				if loads != 1 {
					t.Fatalf("latest load attempts=%d", loads)
				}
				missingReply := false
				for _, frame := range c.e.runtime.SentFrames(t) {
					var failure struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					}
					if json.Unmarshal(frame["error"], &failure) == nil && failure.Code == -32603 && failure.Message == `Session "synthetic-session-1" not found` {
						missingReply = true
					}
				}
				if !missingReply {
					t.Fatal("actual native absence did not cause the source-derived synthetic session/load refusal")
				}
				current, err := os.ReadFile(c.e.tr.path)
				if err != nil || !bytes.HasPrefix(current, currentPrefix) {
					t.Fatalf("load refusal rewrote current protected transcript: %v", err)
				}
				if _, err := os.Stat(native.Latest.Database); !os.IsNotExist(err) {
					t.Fatalf("missing native database recreated: %v", err)
				}
				for path, want := range map[string][]byte{native.Older.Database: native.Older.DatabaseBytes, native.Older.Metadata: native.Older.MetadataBytes, native.Latest.Metadata: native.Latest.MetadataBytes, protected.Latest: protected.LatestBytes, protected.Older: protected.OlderBytes} {
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("load refusal changed retained artifact %s: %v", path, err)
					}
				}
			}
		})
	}
}

func TestCursorLoadWithheldReplyExpiresOriginalProtocolBound(t *testing.T) {
	if testing.Short() {
		t.Skip("retains actual 45-second protocol bound")
	}
	for _, phase := range []string{"load", "mode"} {
		t.Run(phase, func(t *testing.T) {
			scenario := cursorfix.Scenario{LoadSupport: true, WaitForLoad: true}
			options := Options{InitialPrompt: "never dispatch"}
			if phase == "mode" {
				scenario.Modes = advertisedCursorModes()
				scenario.WaitForMode = true
				options.RequestedMode = "plan"
			}
			c := beginCursorLoadWithOptions(t, scenario, options, terminalLoop, nil)
			c.wait(t, "load dispatched", func() bool {
				for _, f := range c.e.runtime.Frames(t) {
					if string(f["method"]) == `"session/load"` {
						return true
					}
				}
				return false
			})
			started := time.Now()
			if phase == "mode" {
				// Consume part of loadUntil before moving to configuration; a fresh
				// mode-relative 45s timer would exceed the unchanged outer bound below.
				<-time.After(2 * time.Second)
				rotationRelease(t, c.e.runtime.NewRelease)
				c.wait(t, "load mode dispatched", func() bool {
					for _, f := range c.e.runtime.Frames(t) {
						if string(f["method"]) == `"session/set_mode"` {
							return true
						}
					}
					return false
				})
			}
			select {
			case err := <-c.done:
				c.ended = true
				if err == nil {
					t.Fatal("withheld response succeeded")
				}
			case <-time.After(time.Until(started.Add(protocolWait + time.Second))):
				t.Fatal("load renewed original protocol deadline")
			}
			if time.Since(started) < protocolWait-time.Second {
				t.Fatal("withheld load failed before original bound")
			}
			for _, f := range c.e.runtime.Frames(t) {
				if string(f["method"]) == `"session/new"` || string(f["method"]) == `"session/prompt"` {
					t.Fatal("timed-out load created/dispatched")
				}
			}
		})
	}
}

func TestCursorLoadCanceledReplyCannotPublish(t *testing.T) {
	for _, phase := range []string{"load", "mode"} {
		t.Run(phase, func(t *testing.T) {
			for _, control := range []string{"interrupt", "exit", "runtime death"} {
				t.Run(control, func(t *testing.T) {
					input := &completeInput{ready: make(chan struct{})}
					scenario := cursorfix.Scenario{LoadSupport: true, WaitForLoad: true}
					options := Options{InitialPrompt: "never dispatch"}
					method := `"session/load"`
					if phase == "mode" {
						scenario.WaitForLoad = false
						scenario.Modes = advertisedCursorModes()
						scenario.WaitForMode = true
						options.RequestedMode = "plan"
						method = `"session/set_mode"`
					}
					c := beginCursorLoadWithOptions(t, scenario, options, input.start, nil)
					<-input.ready
					c.wait(t, "load before cancellation", func() bool {
						for _, f := range c.e.runtime.Frames(t) {
							if string(f["method"]) == method {
								return true
							}
						}
						return false
					})
					if control == "runtime death" {
						raw, err := os.ReadFile(c.e.runtime.PID)
						if err != nil {
							t.Fatal(err)
						}
						var pid int
						if _, err := fmt.Sscan(string(raw), &pid); err != nil {
							t.Fatal(err)
						}
						if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
							t.Fatal(err)
						}
					} else {
						v := terminalInput{interrupt: true}
						if control == "exit" {
							v = terminalInput{text: "/exit"}
						}
						input.emit(v)
					}
					// Epoch retirement happens synchronously in the mailbox before the peer is released.
					release := c.e.runtime.NewRelease
					if phase == "mode" {
						release = c.e.runtime.Release
					}
					if err := os.WriteFile(release, []byte("release after retirement"), 0600); err != nil {
						t.Fatal(err)
					}
					select {
					case <-c.done:
						c.ended = true
					case <-time.After(cancelWait + time.Second):
						t.Fatal("canceled load not bounded")
					}
					for _, r := range c.records(t) {
						if r.Kind == "session_loaded" || r.Kind == "submission_dispatched" {
							t.Fatalf("late response granted publication/admission: %+v", r)
						}
					}
					if len(c.prompts(t)) != 0 {
						t.Fatal("canceled load dispatched saved initial")
					}
				})
			}
		})
	}
}

type loadPublicationWriter struct {
	ctx     context.Context
	db      *sql.DB
	conn    *sql.Conn
	row     store.Spawn
	binding store.CursorRuntime
	prefix  []byte
	err     error
}

// holdLoadPublication acquires a real writer after the validated load reply and returns promptly.
// Its notification does not establish that the subsequent native BEGIN has attempted.
func holdLoadPublication(ready chan<- *loadPublicationWriter, change string) func(context.Context, *requestEnv) {
	return func(ctx context.Context, e *requestEnv) {
		w := &loadPublicationWriter{ctx: ctx}
		defer func() { ready <- w }()
		w.db, w.err = sql.Open("sqlite", e.path)
		if w.err != nil {
			return
		}
		w.conn, w.err = w.db.Conn(context.Background())
		if w.err != nil {
			return
		}
		if _, w.err = w.conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); w.err != nil {
			return
		}
		w.row, w.err = e.s.GetSpawn(e.binding.InstanceID)
		if w.err != nil {
			return
		}
		w.binding, w.err = e.s.ReadCursorRuntime(e.binding.InstanceID)
		if w.err != nil {
			return
		}
		w.prefix, w.err = os.ReadFile(e.tr.path)
		if w.err != nil {
			return
		}
		query := map[string]string{
			"session":    `UPDATE spawns SET session_id='replacement-session' WHERE instance_id=?`,
			"path":       `UPDATE spawns SET jsonl_path='replacement-path' WHERE instance_id=?`,
			"snapshot":   `UPDATE spawns SET row_version=row_version+1 WHERE instance_id=?`,
			"connection": `UPDATE cursor_runtimes SET bound_connection_id='replacement-connection' WHERE bound_instance_id=?`,
		}[change]
		if query != "" {
			_, w.err = w.conn.ExecContext(context.Background(), query, w.row.InstanceID)
		}
	}
}

func awaitLoadPublicationWriter(t *testing.T, ready <-chan *loadPublicationWriter) *loadPublicationWriter {
	t.Helper()
	select {
	case w := <-ready:
		t.Cleanup(func() {
			if w.conn != nil {
				w.conn.ExecContext(context.Background(), "ROLLBACK")
				w.conn.Close()
			}
			if w.db != nil {
				w.db.Close()
			}
		})
		if w.err != nil {
			t.Fatal(w.err)
		}
		if w.binding.SessionID != "" {
			t.Fatal("load was published before writer boundary")
		}
		return w
	case <-time.After(3 * time.Second):
		t.Fatal("validated load did not reach publication boundary")
		return nil
	}
}

func (c *cursorSession) assertLoadUnpublished(t *testing.T, input *completeInput, prefix []byte) {
	t.Helper()
	if input.admit() || bytes.Contains(c.pane.bytes(), []byte("conversation loaded")) {
		t.Fatal("retired load granted admission or claimed success")
	}
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/new"` || string(f["method"]) == `"session/prompt"` {
			t.Fatalf("retired load created or dispatched: %s", f["method"])
		}
	}
	raw, err := os.ReadFile(c.e.tr.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, prefix) {
		t.Fatal("retired load rewrote existing transcript")
	}
	for _, line := range bytes.Split(raw[len(prefix):], []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r submissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.Kind == "session_loaded" || r.Kind == "session_start" || r.Kind == "submission_dispatched" || r.Kind == "user_input" {
			t.Fatalf("retired load recorded success or input: %+v", r)
		}
	}
}

func TestCursorLoadSuccessfulReplyWriterControl(t *testing.T) {
	for _, control := range []string{"interrupt", "exit", "runtime death", "release"} {
		t.Run(control, func(t *testing.T) {
			input := &completeInput{ready: make(chan struct{})}
			ready := make(chan *loadPublicationWriter, 1)
			c := beginObservedCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "saved initial", input.start, holdLoadPublication(ready, ""))
			w := awaitLoadPublicationWriter(t, ready)
			<-input.ready
			if input.admit() || len(c.prompts(t)) != 0 {
				t.Fatal("uncommitted load admitted input")
			}
			started := time.Now()
			if control == "runtime death" {
				if err := syscall.Kill(w.binding.RuntimePID, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			} else if control != "release" {
				v := terminalInput{interrupt: true}
				if control == "exit" {
					v = terminalInput{text: "/exit"}
				}
				input.emit(v)
			}
			if control != "release" {
				select {
				case <-w.ctx.Done():
				case <-time.After(cancelWait + time.Second):
					t.Fatal("publication context ignored control or runtime death while writer held")
				}
			}
			if _, err := w.conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
				t.Fatal(err)
			}
			if control == "release" {
				c.wait(t, "load published after writer released", func() bool { return bytes.Contains(c.pane.bytes(), []byte("conversation loaded")) })
				c.wait(t, "saved initial dispatched after commit", func() bool { return len(c.prompts(t)) == 1 })
				c.wait(t, "saved initial completed", func() bool {
					for _, r := range c.records(t) {
						if r.Kind == "turn_outcome" {
							return true
						}
					}
					return false
				})
				if got := c.prompts(t); len(got) != 1 || got[0] != "saved initial" {
					t.Fatalf("saved initial replayed: %v", got)
				}
				c.stop(t)
			} else {
				c.awaitFailure(t, time.Until(started.Add(cancelWait+time.Second)))
				c.assertLoadUnpublished(t, input, w.prefix)
			}
			reader, err := store.Open(c.e.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			row, err := reader.GetSpawn(w.row.InstanceID)
			if err != nil || row.SessionID != w.row.SessionID || row.JSONLPath != w.row.JSONLPath || row.LifeNumber != w.row.LifeNumber || row.Identity != w.row.Identity {
				t.Fatalf("load changed durable saved identity: %+v %v", row, err)
			}
			binding, err := reader.ReadCursorRuntime(w.row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			want := w.binding
			want.Terminated = binding.Terminated // Cleanup may retire the exact failed runtime.
			if control == "release" {
				want.SessionID = w.row.SessionID
			}
			if !reflect.DeepEqual(binding, want) {
				t.Fatalf("durable publication: %+v want %+v", binding, want)
			}
		})
	}
}

func TestCursorLoadPublicationRejectsReplacementWhileWriterHeld(t *testing.T) {
	for _, change := range []string{"session", "path", "snapshot", "connection"} {
		t.Run(change, func(t *testing.T) {
			input := &completeInput{ready: make(chan struct{})}
			ready := make(chan *loadPublicationWriter, 1)
			c := beginObservedCursorLoad(t, cursorfix.Scenario{LoadSupport: true}, "never dispatch", input.start, holdLoadPublication(ready, change))
			w := awaitLoadPublicationWriter(t, ready)
			if _, err := w.conn.ExecContext(context.Background(), "COMMIT"); err != nil {
				t.Fatal(err)
			}
			c.awaitFailure(t, 3*time.Second)
			c.assertLoadUnpublished(t, input, w.prefix)
			reader, err := store.Open(c.e.path)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			row, err := reader.GetSpawn(w.row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			wantSession, wantPath := w.row.SessionID, w.row.JSONLPath
			if change == "session" {
				wantSession = "replacement-session"
			}
			if change == "path" {
				wantPath = "replacement-path"
			}
			if row.SessionID != wantSession || row.JSONLPath != wantPath || row.LifeNumber != w.row.LifeNumber || row.Identity != w.row.Identity || (change == "snapshot" && row.RowVersion <= w.row.RowVersion) {
				t.Fatalf("replacement overwritten: %+v", row)
			}
			binding, err := reader.ReadCursorRuntime(row.InstanceID)
			want := w.binding
			want.Terminated = binding.Terminated
			if change == "connection" {
				want.Connection = "replacement-connection"
			}
			if err != nil || !reflect.DeepEqual(binding, want) {
				t.Fatalf("stale load rebound replacement: %+v want %+v %v", binding, want, err)
			}
		})
	}
}

func TestCursorLoadUnsupportedReplayAuthorizationFailsClosed(t *testing.T) {
	c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true, BeforeLoadReply: []json.RawMessage{json.RawMessage(`{"id":"unknown-authorization","method":"cursor/authorize","params":{}}`)}, WaitForLoad: true}, "never dispatch", terminalLoop)
	select {
	case err := <-c.done:
		c.ended = true
		if err == nil {
			t.Fatal("unsupported replay authorization succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replay authorization was unbounded")
	}
	for _, f := range c.e.runtime.Frames(t) {
		if string(f["method"]) == `"session/new"` || string(f["method"]) == `"session/prompt"` {
			t.Fatal("unsupported replay created/admitted")
		}
	}
}

func TestCursorLoadReplayCannotAdmitOrPersistHistory(t *testing.T) {
	for _, disabled := range []string{"false", "true"} {
		t.Run("disable_ask_user="+disabled, func(t *testing.T) {
			t.Setenv("AD_TEST_LOAD_DISABLE_ASK_USER", disabled)
			input := &completeInput{ready: make(chan struct{})}
			var genuine string
			var originalHold store.PermissionRow
			events := []json.RawMessage{
				json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"historical user"}}}}`),
				json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"historical agent"}}}}`),
				json.RawMessage(`{"id":"old-permission","method":"session/request_permission","params":{"sessionId":"synthetic-session-1","toolCall":{"toolCallId":"old-tool","title":"Bash"},"options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"reject","kind":"reject_once"}]}}`),
				json.RawMessage(`{"id":"old-plan","method":"cursor/create_plan","params":{}}`),
				json.RawMessage(`{"method":"session/update","params":{"sessionId":"synthetic-session-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"old-tool","status":"completed"}}}`),
			}
			questions := cursorfix.SourceDerivedQuestions(t, "synthetic-session-1")
			for _, q := range questions {
				raw, err := json.Marshal(map[string]any{"id": q.ID, "method": q.Method, "params": json.RawMessage(q.Params)})
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, raw)
			}
			c := beginCursorLoad(t, cursorfix.Scenario{LoadSupport: true, BeforeLoadReply: events, WaitForLoad: true}, "", input.start, func(e *requestEnv) {
				p, err := apitest.SeedPermissionRequest(e.path, e.binding.InstanceID, "Bash")
				if err != nil {
					t.Fatal(err)
				}
				genuine = p.RequestToken
				originalHold, err = e.s.GetPermissionRequest(e.binding.InstanceID, genuine)
				if err != nil {
					t.Fatal(err)
				}
				e.exec(t, `UPDATE spawns SET state='pending' WHERE instance_id=?`, e.binding.InstanceID)
			})
			<-input.ready
			c.wait(t, "all replay requests denied", func() bool {
				n := 0
				for _, f := range c.e.runtime.Frames(t) {
					if len(f["id"]) > 0 && len(f["method"]) == 0 {
						var denied struct {
							Outcome struct {
								Option string `json:"optionId"`
							} `json:"outcome"`
						}
						if err := json.Unmarshal(f["result"], &denied); err != nil {
							t.Fatal(err)
						}
						if denied.Outcome.Option != "" && denied.Outcome.Option != "reject" && denied.Outcome.Option != "__ask_question_skip__" {
							t.Fatal("replay permission allowed")
						}
						n++
					}
				}
				return n == 2+len(questions)
			})
			if input.admit() {
				t.Fatal("replay created input admission")
			}
			row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
			if err != nil || row.State != store.StatePending {
				t.Fatalf("replay changed state %+v %v", row, err)
			}
			raw, err := os.ReadFile(row.JSONLPath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("historical")) || bytes.Contains(raw, []byte("old-permission")) {
				t.Fatalf("replay persisted into transcript: %s", raw)
			}
			if _, err := c.e.s.GetPermissionRequest(row.InstanceID, "old-permission"); err == nil {
				t.Fatal("replay admitted durable permission")
			}
			db, err := sql.Open("sqlite", c.e.path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM permission_requests WHERE instance_id=?`, row.InstanceID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("replay added durable request: count=%d %v", count, err)
			}
			history, err := c.e.s.ListSessionHistory(row.InstanceID, row.LifeNumber)
			if err != nil || len(history) != 0 {
				t.Fatalf("replay added history: %+v %v", history, err)
			}
			if err := os.WriteFile(c.e.runtime.NewRelease, []byte("release"), 0600); err != nil {
				t.Fatal(err)
			}
			c.wait(t, "load after replay", func() bool { return bytes.Contains(c.pane.bytes(), []byte("conversation loaded")) })
			if input.admit() {
				t.Fatal("replay denial released an independent durable hold")
			}
			hold, err := c.e.s.GetPermissionRequest(row.InstanceID, genuine)
			if err != nil || !hold.AwaitsAnswer() || hold.ProvenGone() || !reflect.DeepEqual(hold, originalHold) {
				t.Fatalf("replay changed genuine hold: %+v %v", hold, err)
			}
			if len(c.prompts(t)) != 0 {
				t.Fatal("historical input replayed")
			}
			c.stop(t)
		})
	}
}
