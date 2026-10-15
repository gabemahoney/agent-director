package cursorhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/internal/version"
)

type Options struct {
	Runtime       string
	RuntimeArgs   []string
	InitialPrompt string
}
type lockedWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(p)
}

// Run owns one fresh Cursor conversation. It performs no authentication,
// session restoration or queued follow-up admission.
func Run(options Options, in *os.File, out io.Writer) (err error) {
	ownedID := ""
	defer func() {
		if err != nil && ownedID != "" {
			_ = trail.Emit(context.Background(), "ad.cursor.failed", map[string]any{"instance_id": ownedID, "reason": err.Error(), "source": "ad_cursor_host"})
		}
	}()
	if runtime.GOOS != "linux" {
		return fmt.Errorf("Cursor protected lifetime is unsupported on this platform")
	}
	cfg, err := config.Load(layout.DisplayConfigPath)
	if err != nil {
		return fmt.Errorf("Cursor gateway config unavailable")
	}
	path, err := cfg.Store.EffectiveDbPath()
	if ownedPath := os.Getenv("AGENT_DIRECTOR_CURSOR_STORE_PATH"); ownedPath != "" {
		path = ownedPath
	}
	if err != nil {
		return fmt.Errorf("Cursor gateway store unavailable")
	}
	s, err := store.OpenWithBusyTimeout(path, cfg.Store.EffectiveBusyTimeoutMs())
	if err != nil {
		return fmt.Errorf("Cursor gateway store unavailable")
	}
	defer s.Close()
	s.SetSettleMs(int64(cfg.State.EffectiveSettleMs()))
	row, err := waitForOwnedPane(s, cfg)
	if err != nil {
		return err
	}
	ownedID = row.InstanceID
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("Cursor reporting executable unavailable")
	}
	p, err := startProcess(options.Runtime, append(options.RuntimeArgs, "acp"), row.CWD)
	if err != nil {
		return err
	}
	// Only positive child exit evidence permits intentional end reporting.
	intentional := false
	defer func() {
		stopErr := p.terminate()
		if stopErr != nil && err == nil {
			err = stopErr
		}
		p.input.Close()
		p.output.Close()
	}()
	connection, err := randomID()
	if err != nil {
		return err
	}
	inputPipe, err := processPipe(p.pid, 0)
	if err != nil {
		return err
	}
	outputPipe, err := processPipe(p.pid, 1)
	if err != nil {
		return err
	}
	b := store.CursorRuntime{InstanceID: row.InstanceID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: os.Getpid(), GatewayStart: row.Identity.PaneStarttime, RuntimePID: p.pid, RuntimeStart: p.start, Connection: connection, InputPipe: inputPipe, OutputPipe: outputPipe, InputFD: int(p.input.Fd()), OutputFD: int(p.output.Fd())}
	if err = VerifyRuntime(b, false); err != nil {
		return err
	}
	applied, err := s.RegisterCursorRuntime(row, b)
	if err != nil || !applied {
		return fmt.Errorf("Cursor runtime binding was refused")
	}
	tr, err := openTranscript(os.Getenv("HOME"), b)
	if err != nil {
		return err
	}
	defer tr.file.Close()
	rep := &reporter{store: s, cfg: cfg, binding: b, exe: exe, path: tr.path}
	protocolInput, err := p.protocolInput()
	if err != nil {
		return err
	}
	protocol := newProtocol(protocolInput, p.output)
	defer protocol.close()
	writer := &lockedWriter{out: out}
	inputs := make(chan terminalInput, 32)
	requests := newRequestController(rep, protocol, writer)
	defer requests.cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var admission promptAdmission
	defer admission.disable()
	restore := terminalInputLoop(in, writer, inputs, admission.take, requests.key)
	defer restore()
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	defer signal.Stop(signals)
	fmt.Fprint(writer, "Cursor gateway: starting a fresh conversation\r\n")
	if _, err = protocol.request("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": map[string]any{"name": "agent-director", "version": version.Version}}); err != nil {
		return err
	}
	session := ""
	phase := "initialize"
	busy := false
	requestDeadlineHeld := false
	cancelled := false
	deadline := time.NewTimer(protocolWait)
	defer deadline.Stop()
	resetDeadline := func(d time.Duration) {
		if !deadline.Stop() {
			select {
			case <-deadline.C:
			default:
			}
		}
		deadline.Reset(d)
	}
	stopDeadline := func() {
		if !deadline.Stop() {
			select {
			case <-deadline.C:
			default:
			}
		}
	}
	submit := func(text string) error {
		admission.disable()
		if len(text) > maxFrameBytes/2 {
			return fmt.Errorf("Cursor input exceeds limit")
		}
		if e := VerifyRuntime(rep.binding, false); e != nil {
			return e
		}
		if e := tr.record("user_input", map[string]any{"text": text, "origin": "main"}); e != nil {
			return e
		}
		if e := rep.report("UserPromptSubmit"); e != nil {
			return e
		}
		if _, e := protocol.request("session/prompt", map[string]any{"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": text}}}); e != nil {
			return e
		}
		busy = true
		phase = "prompt"
		resetDeadline(30 * time.Minute)
		return nil
	}
	for {
		select {
		case childErr := <-p.done:
			if childErr != nil {
				return childErr
			}
			return fmt.Errorf("Cursor runtime exited unexpectedly")
		case <-signals:
			requests.cancel()
			admission.disable()
			intentional = true
		case <-ticker.C:
			if err = requests.tick(); err != nil {
				return err
			}
			if requestDeadlineHeld && !requests.open() {
				requestDeadlineHeld = false
				// Cancellation owns its fixed deadline even when it closes
				// the authorization requests that paused prompt timing.
				if busy && !cancelled {
					resetDeadline(30 * time.Minute)
				}
			}
		case <-deadline.C:
			requests.cancel()
			return fmt.Errorf("Cursor ACP operation timed out")
		case input := <-inputs:
			if input.err != nil {
				return fmt.Errorf("Cursor pane input closed")
			}
			if input.choiceToken != "" {
				requests.choose(input.choiceToken, input.choiceOption)
				if err = requests.tick(); err != nil {
					return err
				}
				continue
			}
			if input.interrupt {
				requests.cancel()
				if busy && !cancelled {
					if err = protocol.notify("session/cancel", map[string]any{"sessionId": session}); err != nil {
						return err
					}
					cancelled = true
					resetDeadline(cancelWait)
					fmt.Fprint(writer, "Cancellation requested\r\n")
				} else if !busy {
					fmt.Fprint(writer, "> ")
				}
				continue
			}
			text := input.text
			if text == "/exit" {
				intentional = true
				break
			}
			if text == "/new" || text == "/clear" {
				fmt.Fprint(writer, "Unsupported Cursor conversation control; nothing sent\r\n> ")
				continue
			}
			if text == "" {
				fmt.Fprint(writer, "> ")
				continue
			}
			if !input.accepted || session == "" || busy || requests.open() {
				fmt.Fprint(writer, "Cursor input refused while starting or busy; nothing sent\r\n")
				continue
			}
			if err = submit(text); err != nil {
				return err
			}
		case writeErr := <-protocol.writes:
			if writeErr != nil {
				return writeErr
			}
		case next := <-protocol.frames:
			if next.err != nil {
				return fmt.Errorf("Cursor ACP transport failed; child termination must be confirmed separately")
			}
			f := next.frame
			if err = VerifyRuntime(rep.binding, false); err != nil {
				return err
			}
			if f.Method != "" {
				if len(f.ID) > 0 {
					if cancelled {
						return fmt.Errorf("Cursor authorization after cancellation; no response sent")
					}
					if session != "" && !cancelled && busy {
						if e := requests.admit(f, session); e == nil {
							// Authorization has its own finite immutable bounds;
							// time waiting on it is not prompt generation time.
							stopDeadline()
							requestDeadlineHeld = true
							if e = tr.record("authorization_admitted", map[string]any{"method": f.Method, "origin": "unknown"}); e != nil {
								return e
							}
							continue
						} else {
							var policy policyRejection
							if !errors.As(e, &policy) {
								return fmt.Errorf("Cursor authorization admission failed; no response sent: %w", e)
							}
							fmt.Fprintf(writer, "Cursor authorization unavailable: %s\r\n", safeText(e.Error()))
						}
					}
					// Policy/unsupported failures are not relay responses. Their
					// explicit reject/cancel is separate from admitted requests.
					cancel, e := protocol.deny(f)
					if e != nil {
						return e
					}
					if e = tr.record("authorization_denied", map[string]any{"method": f.Method, "cancelled": cancel}); e != nil {
						return e
					}
					fmt.Fprint(writer, "Cursor authorization denied\r\n")
					if cancel {
						if session == "" {
							return fmt.Errorf("unsupported authorization during Cursor startup")
						}
						if !cancelled {
							requests.cancel()
							if err = protocol.notify("session/cancel", map[string]any{"sessionId": session}); err != nil {
								return err
							}
							cancelled = true
							resetDeadline(cancelWait)
						}
					}
					continue
				}
				if f.Method == "session/update" {
					if !busy || session == "" {
						// Session creation/config notifications carry no turn proof. They
						// are launch-attributed metadata, never reported as working/waiting.
						if err = tr.record("startup_or_idle_update", map[string]any{"origin": "unknown"}); err != nil {
							return err
						}
						continue
					}
					var update struct {
						SessionID string `json:"sessionId"`
						Update    struct {
							Kind    string `json:"sessionUpdate"`
							Content struct {
								Type string `json:"type"`
								Text string `json:"text"`
							} `json:"content"`
							AgentID string `json:"agentId"`
						} `json:"update"`
					}
					if json.Unmarshal(f.Params, &update) != nil || update.SessionID != session {
						return fmt.Errorf("Cursor update session mismatch")
					}
					// Unknown/child provenance is preserved without supplying lifecycle
					// proof. Only correlated main text is rendered as main output.
					origin := "unknown"
					if update.Update.AgentID == "" && (update.Update.Kind == "agent_message_chunk" || update.Update.Kind == "agent_thought_chunk") {
						origin = "launch"
					}
					if err = tr.record("update", map[string]any{"origin": origin, "update": json.RawMessage(f.Params)}); err != nil {
						return err
					}
					if err = requests.execution(f.Params); err != nil {
						return err
					}
					if update.Update.Content.Type == "text" {
						fmt.Fprint(writer, safeText(update.Update.Content.Text))
					} else if update.Update.Kind == "tool_call" || update.Update.Kind == "tool_call_update" {
						fmt.Fprint(writer, "\r\nCursor tool activity\r\n")
					}
				}
				continue
			}
			raw, e := protocol.response(f)
			if e != nil {
				return e
			}
			switch phase {
			case "initialize":
				var init struct {
					Version      int             `json:"protocolVersion"`
					Capabilities json.RawMessage `json:"agentCapabilities"`
				}
				if json.Unmarshal(raw, &init) != nil || init.Version != 1 || len(init.Capabilities) == 0 {
					return fmt.Errorf("Cursor runtime does not support ACP protocol 1")
				}
				if _, err = protocol.request("session/new", map[string]any{"cwd": row.CWD, "mcpServers": []any{}}); err != nil {
					return err
				}
				phase = "new"
				resetDeadline(protocolWait)
			case "new":
				var created struct {
					ID string `json:"sessionId"`
				}
				if json.Unmarshal(raw, &created) != nil || created.ID == "" || len(created.ID) > 512 {
					return fmt.Errorf("Cursor did not confirm a fresh session")
				}
				session = created.ID
				b.SessionID = session
				rep.binding = b
				tr.binding = b
				applied, e := s.SetCursorRuntimeSession(b, session)
				if e != nil || !applied {
					return fmt.Errorf("Cursor session binding changed")
				}
				if err = tr.record("session_start", map[string]any{"origin": "main"}); err != nil {
					return err
				}
				var waiting func(func() bool)
				if options.InitialPrompt == "" {
					waiting = admission.waiting
				}
				if err = rep.reportWaiting("SessionStart", waiting); err != nil {
					return err
				}
				stopDeadline()
				fmt.Fprint(writer, "Cursor ready\r\n> ")
				if options.InitialPrompt == "" {
					admission.confirm()
				}
				if options.InitialPrompt != "" {
					text := options.InitialPrompt
					options.InitialPrompt = ""
					if err = submit(text); err != nil {
						return err
					}
				}
			case "prompt":
				var completed struct {
					Reason string `json:"stopReason"`
				}
				if json.Unmarshal(raw, &completed) != nil {
					return fmt.Errorf("invalid Cursor completion")
				}
				switch completed.Reason {
				case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
				default:
					return fmt.Errorf("Cursor completion reason unavailable")
				}
				if err = tr.record("turn_outcome", map[string]any{"stop_reason": completed.Reason, "cancellation_requested": cancelled, "origin": "main"}); err != nil {
					return err
				}
				if completed.Reason == "cancelled" {
					requests.cancel()
				}
				busy = false
				requestDeadlineHeld = false
				cancelled = false
				phase = "idle"
				stopDeadline()
				if err = rep.reportWaiting("Stop", admission.waiting); err != nil {
					return err
				}
				fmt.Fprint(writer, "\r\nCursor turn complete\r\n> ")
				admission.confirm()
			default:
				return fmt.Errorf("unexpected Cursor response")
			}
		}
		if intentional {
			requests.cancel()
			admission.disable()
			if busy {
				_ = protocol.notify("session/cancel", map[string]any{"sessionId": session})
			}
			if err = p.terminate(); err != nil {
				return err
			}
			if err = VerifyRuntime(rep.binding, true); err != nil {
				return err
			}
			if err = tr.record("session_end", map[string]any{"intentional": true, "runtime_gone": true}); err != nil {
				return err
			}
			if session != "" {
				if err = rep.report("SessionEnd"); err != nil {
					return err
				}
			}
			fmt.Fprint(writer, "Cursor runtime terminated\r\n")
			return nil
		}
	}
}
