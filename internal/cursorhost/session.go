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
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/internal/version"
)

var errPromptRetired = errors.New("Cursor prompt retired before write")

type Options struct {
	Runtime       string
	RuntimeArgs   []string
	InitialPrompt string
}

// Run owns one fresh Cursor conversation without authentication or restoration. Follow-ups remain transient and conversation-bound.
func Run(options Options, in *os.File, out io.Writer) error {
	return runWithTranscript(options, in, out, openTranscript)
}

// The opener retains protected file creation in production and permits a
// recorder with controlled I/O in tests of persistence supervision.
func runWithTranscript(options Options, in *os.File, out io.Writer, open func(string, store.CursorRuntime) (*transcript, error)) (err error) {
	return runWithInput(options, in, out, open, terminalLoop)
}

// Input starters receive the actual admission, mailbox and choice callbacks.
// Production always uses terminalLoop; tests can control completed-input timing
// without replacing the controller or decoding megabytes one rune at a time.
type inputStarter func(*os.File, io.Writer, func(terminalInput), func() bool, ...func(rune) (string, string, bool)) func()

// afterRecoveryReport optionally observes successful recovery reporting before
// epoch-owned finalization. It runs on the session goroutine, outside mailbox
// locks, so tests can order a real control after await has selected completion.
func runWithInput(options Options, in *os.File, out io.Writer, open func(string, store.CursorRuntime) (*transcript, error), startInput inputStarter, afterRecoveryReport ...func()) (err error) {
	if len(options.InitialPrompt) > maxFrameBytes/2 {
		return fmt.Errorf("Cursor input exceeds limit")
	}
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
	tr, err := open(os.Getenv("HOME"), b)
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
	writer := newPaneDisplay(out)
	defer writer.stop()
	inputs := newInputMailbox()
	requests := newRequestController(rep, protocol, writer)
	defer requests.cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var admission promptAdmission
	defer admission.disable()
	inputs.onControl = func() { admission.disable(); requests.generation.Add(1) }
	// Capture cancellation authority before the potentially slow admission read.
	// Insertion must never grant an old admission the epoch of a later control.
	var admissionEpoch uint64
	restore := startInput(in, writer, func(v terminalInput) {
		if v.accepted {
			v.epoch, v.epochBound = admissionEpoch, true
		}
		inputs.put(v)
	}, func() bool {
		admissionEpoch = inputs.epochValue()
		return admission.take()
	}, requests.key)
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
	var queue []terminalInput
	var active *terminalInput
	activeRequest := ""
	promptFailures := make(chan error, 1)
	activeRetiredBeforeWrite := false
	persistenceFailed := false
	var cancelAt time.Time
	runtimeEnded := false
	activeCancellation := false
	idleControl := false
	var pendingControl map[string]any
	handleControl := func(input terminalInput) error {
		if runtimeEnded {
			return nil
		}
		if input.submission == "" {
			inputs.put(input)
			if v := inputs.takeControl(); v != nil {
				input = *v
			}
		}
		kind := "interrupt"
		if input.text == "/exit" {
			kind = "exit"
		}
		if input.err != nil {
			kind = "input_closed"
		}
		if pendingControl == nil {
			pendingControl = map[string]any{"control": kind, "input_id": input.submission, "input_epoch": input.epoch, "consumed_at": input.at, "count": 1}
			if active != nil {
				pendingControl["submission_id"] = active.submission
				pendingControl["request_id"] = activeRequest
			}
		} else {
			pendingControl["count"] = pendingControl["count"].(int) + 1
			pendingControl["last_input_id"] = input.submission
		}

		admission.disable()
		requests.cancel()
		if !busy {
			idleControl = true
		}
		if session == "" && options.InitialPrompt != "" {
			options.InitialPrompt = ""
			pendingControl["initial_prompt_cancelled"] = true
			fmt.Fprint(writer, "Cursor initial prompt cancelled before session confirmation\r\n")
		}
		if input.err != nil {
			return fmt.Errorf("Cursor pane input closed")
		}
		if input.text == "/exit" {
			intentional = true
		}
		if cancelAt.IsZero() {
			cancelAt = input.at
			if cancelAt.IsZero() {
				cancelAt = time.Now()
			}
			resetDeadline(time.Until(cancelAt.Add(cancelWait)))
		}
		if busy && !cancelled {
			activeCancellation = true
			cancelled = true
			if e := protocol.notify("session/cancel", map[string]any{"sessionId": session}); e != nil {
				return e
			}
			fmt.Fprint(writer, "Cancellation requested\r\n")
		}
		return nil
	}
	// Slow persistence and hook completion run serially, but controls and the
	// original cancellation deadline remain supervised while they finish.
	await := func(operation func() error) error {
		if !cancelAt.IsZero() && !time.Now().Before(cancelAt.Add(cancelWait)) {
			return fmt.Errorf("Cursor cancellation timed out")
		}
		result := make(chan error, 1)
		go func() { result <- operation() }()
		for {
			childDone := p.done
			if runtimeEnded {
				childDone = nil
			}
			select {
			case e := <-result:
				return e
			case <-inputs.controlWake:
				if v := inputs.takeControl(); v != nil {
					if e := handleControl(*v); e != nil {
						return e
					}
				}
			case <-signals:
				if e := handleControl(terminalInput{text: "/exit", at: time.Now()}); e != nil {
					return e
				}
			case e := <-promptFailures:
				if errors.Is(e, errPromptRetired) {
					activeRetiredBeforeWrite = true
				} else if !intentional && !runtimeEnded {
					return e
				}
			case <-deadline.C:
				return fmt.Errorf("Cursor ACP operation timed out")
			case <-childDone:
				if intentional {
					runtimeEnded = true
					continue
				}
				return fmt.Errorf("Cursor runtime exited during persistence or reporting")
			}
		}
	}
	record := func(kind string, data any) error {
		if persistenceFailed {
			return fmt.Errorf("Cursor transcript unavailable; no further admission")
		}
		e := await(func() error { return tr.record(kind, data) })
		if e != nil {
			persistenceFailed = true
			admission.disable()
		}
		return e
	}
	report := func(event string) error { return await(func() error { return rep.report(event) }) }
	reportWaiting := func(event string, check func(func() bool)) error {
		return await(func() error { return rep.reportWaiting(event, check) })
	}
	cancelSubmission := func(v terminalInput, reason string) error {
		inputs.release(v)
		fmt.Fprintf(writer, "Cursor submission %s cancelled before dispatch: %s\r\n", v.submission, reason)
		return record("submission_cancelled", map[string]any{"submission_id": v.submission, "reason": reason, "started": false, "control_epoch": inputs.epochValue()})
	}
	defer func() {
		admission.disable()
		requests.cancel()
		if err != nil {
			protocol.close()
			_ = p.terminate()
		}
		select {
		case e := <-promptFailures:
			activeRetiredBeforeWrite = errors.Is(e, errPromptRetired)
		default:
		}

		// Failure cleanup records disposition where the protected recorder still
		// works; it never recovers or replays the in-memory queue.
		if persistenceFailed {
			return
		}
		finalDeadline := time.Now().Add(cancelWait)
		if !cancelAt.IsZero() {
			finalDeadline = cancelAt.Add(cancelWait)
		}
		finalRecord := func(kind string, data any) bool {
			remaining := time.Until(finalDeadline)
			var e error
			if remaining <= 0 {
				e = fmt.Errorf("Cursor transcript finalization timed out")
			} else {
				result := make(chan error, 1)
				go func() { result <- tr.record(kind, data) }()
				timer := time.NewTimer(remaining)
				select {
				case e = <-result:
				case <-timer.C:
					e = fmt.Errorf("Cursor transcript finalization timed out")
				}
				timer.Stop()
			}
			if e != nil {
				err = errors.Join(err, fmt.Errorf("Cursor transcript finalization failed: %w", e))
				fmt.Fprint(writer, "Cursor transcript finalization failed; queued work will not be recovered\r\n")
				return false
			}
			return true
		}
		for _, v := range queue {
			if !finalRecord("submission_cancelled", map[string]any{"submission_id": v.submission, "reason": "gateway stopped", "started": false}) {
				return
			}
		}
		for {
			v, ok := inputs.take()
			if !ok {
				break
			}
			if v.accepted {
				if !finalRecord("submission_accepted", map[string]any{"submission_id": v.submission, "text": v.text, "consumed_at": v.at, "input_epoch": v.epoch}) {
					return
				}
				if !finalRecord("submission_cancelled", map[string]any{"submission_id": v.submission, "reason": "gateway stopped", "started": false}) {
					return
				}
			}
		}
		if active != nil && activeRetiredBeforeWrite {
			finalRecord("submission_cancelled", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "reason": "retired before write", "started": false, "bytes_written": 0})
		} else if active != nil {
			finalRecord("submission_unconfirmed", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "reason": "gateway stopped; delivery or completion unconfirmed"})
		}
	}()
	submit := func(v terminalInput) error {
		if e := VerifyRuntime(rep.binding, false); e != nil {
			return e
		}
		if e := record("user_input", map[string]any{"text": v.text, "submission_id": v.submission, "origin": "main"}); e != nil {
			return e
		}
		if e := report("UserPromptSubmit"); e != nil {
			return e
		}
		if intentional || !inputs.current(v.epoch) {
			return cancelSubmission(v, "control consumed before dispatch")
		}
		boundReporter := *rep
		recorded := make(chan struct{})
		writeDeadline := time.Now().Add(transportWriteWait)
		guard := func() error {
			if !inputs.current(v.epoch) {
				return errPromptRetired
			}
			// Allocate the actual RPC before recording its start, but grant no output
			// authority until that record is synchronized. A slow recorder cannot
			// extend this frame's existing transport bound.
			timer := time.NewTimer(time.Until(writeDeadline))
			defer timer.Stop()
			select {
			case <-recorded:
			case <-protocol.closed:
				return fmt.Errorf("Cursor prompt transport closed before start record")
			case <-timer.C:
				return fmt.Errorf("Cursor prompt start record timed out")
			}
			if !inputs.current(v.epoch) {
				return errPromptRetired
			}
			ok, e := boundReporter.promptEligible()
			if e != nil {
				return e
			}
			if !ok {
				return fmt.Errorf("Cursor prompt held by outstanding decision")
			}
			if !inputs.current(v.epoch) {
				return errPromptRetired
			}
			return nil
		}
		id, e := protocol.requestGuarded("session/prompt", map[string]any{"sessionId": session, "prompt": []any{map[string]any{"type": "text", "text": v.text}}}, guard, func(e error) {
			if e != nil {
				promptFailures <- e
			}
		})
		if e != nil {
			return e
		}
		inputs.release(v)
		active = &v
		activeRetiredBeforeWrite = false
		activeRequest = id
		busy = true
		phase = "prompt"
		resetDeadline(30 * time.Minute)
		if e = record("submission_started", map[string]any{"submission_id": v.submission, "request_id": id, "delivery": "unconfirmed"}); e != nil {
			return e
		}
		close(recorded)
		return nil
	}

	for {
		if v := inputs.takeControl(); v != nil {
			if err = handleControl(*v); err != nil {
				return err
			}
		}
		if activeRetiredBeforeWrite && active != nil {
			busy = false
			cancelled = false
			phase = "idle"
			protocol.pendingID = ""
			protocol.pendingMethod = ""
			if err = record("submission_cancelled", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "reason": "retired before write", "started": false, "bytes_written": 0}); err != nil {
				return err
			}
			active = nil
			activeRequest = ""
			activeRetiredBeforeWrite = false
			activeCancellation = false
			idleControl = true
		}
		if pendingControl != nil {
			data := pendingControl
			pendingControl = nil
			if err = record("input_control", data); err != nil {
				return err
			}
		}
		if activeCancellation && active != nil {
			if err = record("submission_cancel_requested", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "delivery": "unconfirmed"}); err != nil {
				return err
			}
			activeCancellation = false
		}
		for n := 0; n < 4*maxQueuedSubmissions; n++ {
			input, ok := inputs.take()
			if !ok {
				break
			}
			if input.choiceToken != "" {
				requests.choose(input.choiceToken, input.choiceOption)
				continue
			}
			if input.text == "/new" || input.text == "/clear" {
				if err = record("submission_refused", map[string]any{"submission_id": input.submission, "reason": "unsupported conversation control"}); err != nil {
					return err
				}
				fmt.Fprint(writer, "Unsupported Cursor conversation control; nothing sent\r\n> ")
				continue
			}
			if input.text == "" && input.dropped == 0 && input.refusal == "" {
				continue
			}
			if !input.accepted || session == "" {
				reason := input.refusal
				if reason == "" {
					reason = "starting or cancellation"
				}
				if err = record("submission_refused", map[string]any{"submission_id": input.submission, "reason": reason, "count": input.dropped}); err != nil {
					return err
				}
				fmt.Fprintf(writer, "Cursor input refused while starting or busy (%s); nothing sent\r\n", reason)
				continue
			}
			if err = record("submission_accepted", map[string]any{"submission_id": input.submission, "text": input.text, "input_epoch": input.epoch, "consumed_at": input.at, "origin": "main"}); err != nil {
				return err
			}
			fmt.Fprintf(writer, "Cursor submission %s accepted\r\n", input.submission)
			queue = append(queue, input)
		}
		for len(queue) > 0 && (intentional || !inputs.current(queue[0].epoch)) {
			v := queue[0]
			queue = queue[1:]
			if err = cancelSubmission(v, "interruption or shutdown"); err != nil {
				return err
			}
		}
		if idleControl && !busy && !intentional && session != "" {
			idleControl = false
			epoch := inputs.epochValue()
			if err = reportWaiting("Stop", func(check func() bool) { inputs.ifCurrent(epoch, func() { admission.waiting(check) }) }); err != nil {
				return err
			}
			if len(afterRecoveryReport) > 0 && afterRecoveryReport[0] != nil {
				afterRecoveryReport[0]()
			}
			recovered := false
			if pendingControl == nil {
				// Report completion can win await's select while a newer control
				// is already in the mailbox. Only this epoch may finish recovery;
				// otherwise that control retains the original cancellation bound.
				inputs.ifCurrent(epoch, func() {
					stopDeadline()
					cancelAt = time.Time{}
					admission.enableQueue()
					recovered = true
				})
			}
			if !recovered {
				idleControl = true
				continue
			}
			fmt.Fprint(writer, "Cursor input cancelled; submit a new prompt to continue\r\n> ")
		}
		if !intentional && !busy && len(queue) > 0 && !requests.open() {
			eligible := false
			e := await(func() (e error) { eligible, e = rep.promptEligible(); return e })
			if e != nil {
				return e
			}
			if eligible {
				v := queue[0]
				if err = submit(v); err != nil {
					if active != nil && active.submission == v.submission {
						queue = queue[1:]
					}
					return err
				}
				queue = queue[1:]
			}
		}
		if intentional {
			break
		}
		select {
		case childErr := <-p.done:
			if childErr != nil {
				return childErr
			}
			return fmt.Errorf("Cursor runtime exited unexpectedly")
		case <-signals:
			if err = handleControl(terminalInput{text: "/exit", at: time.Now()}); err != nil {
				return err
			}
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
		case <-inputs.controlWake:
			if v := inputs.takeControl(); v != nil {
				if err = handleControl(*v); err != nil {
					return err
				}
			}
		case <-inputs.wake:
			// The next loop drains observed complete submissions in terminal order.

		case e := <-promptFailures:
			if errors.Is(e, errPromptRetired) {
				activeRetiredBeforeWrite = true
			} else {
				return e
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
							if e = record("authorization_admitted", map[string]any{"method": f.Method, "origin": "unknown", "submission_id": active.submission, "prompt_request_id": activeRequest, "authorization_id": json.RawMessage(f.ID), "params": json.RawMessage(f.Params)}); e != nil {
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
					if e = record("authorization_denied", map[string]any{"method": f.Method, "cancelled": cancel}); e != nil {
						return e
					}
					fmt.Fprint(writer, "Cursor authorization denied\r\n")
					if cancel {
						if session == "" {
							return fmt.Errorf("unsupported authorization during Cursor startup")
						}
						if !cancelled {
							inputs.put(terminalInput{interrupt: true})
							if v := inputs.takeControl(); v != nil {
								if err = handleControl(*v); err != nil {
									return err
								}
							}
						}
					}
					continue
				}
				if f.Method == "session/update" {
					if !busy || session == "" {
						// Session creation/config notifications carry no turn proof. They
						// are launch-attributed metadata, never reported as working/waiting.
						if err = record("startup_or_idle_update", map[string]any{"origin": "unknown", "update": json.RawMessage(f.Params)}); err != nil {
							return err
						}
						// Late exact tool proof may discharge its own hold after the prompt
						// response. Idle/main completion itself still proves nothing.
						if session != "" {
							if err = requests.execution(f.Params); err != nil {
								return err
							}
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
					if err = record("update", map[string]any{"origin": origin, "update": json.RawMessage(f.Params), "submission_id": active.submission, "request_id": activeRequest}); err != nil {
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
				admission.mu.Lock()
				admissionReporter := *rep
				admission.validate = func() bool { ok, e := admissionReporter.promptEligible(); return e == nil && ok }
				admission.mu.Unlock()
				applied, e := s.SetCursorRuntimeSession(b, session)
				if e != nil || !applied {
					return fmt.Errorf("Cursor session binding changed")
				}
				waitingEpoch := inputs.epochValue()
				if err = record("session_start", map[string]any{"origin": "main"}); err != nil {
					return err
				}
				var waiting func(func() bool)
				if options.InitialPrompt == "" {
					waiting = func(check func() bool) { inputs.ifCurrent(waitingEpoch, func() { admission.waiting(check) }) }
				}
				if err = reportWaiting("SessionStart", waiting); err != nil {
					return err
				}
				if cancelAt.IsZero() {
					stopDeadline()
				}
				fmt.Fprint(writer, "Cursor ready\r\n> ")
				if options.InitialPrompt == "" {
					if !intentional {
						inputs.ifCurrent(waitingEpoch, admission.enableQueue)
					}
				}
				if options.InitialPrompt != "" {
					text := options.InitialPrompt
					options.InitialPrompt = ""
					v := terminalInput{text: text, accepted: true, submission: "initial", at: time.Now()}
					inputs.mu.Lock()
					v.epoch = waitingEpoch
					inputs.count++
					inputs.bytes += len(text)
					inputs.mu.Unlock()
					if err = record("submission_accepted", map[string]any{"submission_id": v.submission, "text": text, "origin": "main"}); err != nil {
						return err
					}
					queue = append(queue, v)
					if err = submit(v); err != nil {
						if active != nil && active.submission == v.submission {
							queue = queue[1:]
						}
						return err
					}
					queue = queue[1:]
				}
				if !intentional {
					inputs.ifCurrent(waitingEpoch, admission.enableQueue)
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
				if err = record("turn_outcome", map[string]any{"stop_reason": completed.Reason, "cancellation_requested": cancelled, "origin": "main", "submission_id": active.submission, "request_id": activeRequest}); err != nil {
					return err
				}
				if completed.Reason == "cancelled" {
					requests.cancel()
				}
				active = nil
				activeRequest = ""
				activeCancellation = false
				busy = false
				requestDeadlineHeld = false
				cancelled = false
				phase = "idle"
				if cancelAt.IsZero() {
					stopDeadline()
				} else {
					idleControl = true
				}
				waitingEpoch := inputs.epochValue()
				if err = reportWaiting("Stop", func(check func() bool) { inputs.ifCurrent(waitingEpoch, func() { admission.waiting(check) }) }); err != nil {
					return err
				}
				fmt.Fprint(writer, "\r\nCursor turn complete\r\n> ")
				if !intentional {
					inputs.ifCurrent(waitingEpoch, admission.enableQueue)
				}
			default:
				return fmt.Errorf("unexpected Cursor response")
			}
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
		runtimeEnded = true
		if active != nil {
			if err = record("submission_terminated", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "runtime_gone": true, "completion": "unconfirmed"}); err != nil {
				return err
			}
			active = nil
		}
		if err = VerifyRuntime(rep.binding, true); err != nil {
			return err
		}
		if err = record("session_end", map[string]any{"intentional": true, "runtime_gone": true}); err != nil {
			return err
		}
		if session != "" {
			if err = report("SessionEnd"); err != nil {
				return err
			}
		}
		fmt.Fprint(writer, "Cursor runtime terminated\r\n")
		return nil
	}
	return fmt.Errorf("Cursor gateway stopped without termination evidence")
}
