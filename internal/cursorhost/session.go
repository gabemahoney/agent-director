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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/internal/version"
)

var errPromptRetired = errors.New("Cursor prompt retired before write")
var errACPOperationTimeout = errors.New("Cursor ACP operation timed out")

type Options struct {
	Runtime       string
	RuntimeArgs   []string
	InitialPrompt string
	LoadSession   string
	RequestedMode string

	// verifyModeRuntime replaces only the mode writer's runtime verification.
	// Tests can delay a real VerifyRuntime result to order a control during its
	// verification window. Production leaves it nil and uses VerifyRuntime.
	verifyModeRuntime func(store.CursorRuntime, bool) error

	// afterStartupQueueEnabled observes successful epoch-owned startup admission.
	// It runs once on the session goroutine, outside mailbox locks, and must
	// return promptly. Production leaves it nil.
	afterStartupQueueEnabled func()

	// beforeControllerCleanup observes the controller loop's result before any
	// deferred cleanup can delay or amend it. Setup returns before loop entry are
	// not observed. It runs on the session goroutine, outside locks, and must
	// return promptly. Production leaves it nil.
	beforeControllerCleanup func(error)

	// afterRuntimeRetired observes the durable retirement before the reporting
	// worker updates its local proof. Production leaves it nil.
	afterRuntimeRetired func()
}

// Run owns a fresh or latest-loaded Cursor conversation. Follow-ups remain
// transient and conversation-bound; load replay never restores a local queue.
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
	return runWithLoadPublicationObserver(options, in, out, open, startInput, nil, afterRecoveryReport...)
}

// beforeLoadPublication is a per-run test observer of an accepted load response.
// It runs in await's worker immediately before binding, outside mailbox locks,
// with the publication's original context/deadline and control supervision active.
// Tests may acquire a real SQL writer here and observe cancellation through ctx;
// they must return promptly so the actual binding operation can proceed.
// Production always passes nil.
func runWithLoadPublicationObserver(options Options, in *os.File, out io.Writer, open func(string, store.CursorRuntime) (*transcript, error), startInput inputStarter, beforeLoadPublication func(context.Context), afterRecoveryReport ...func()) (err error) {
	if len(options.InitialPrompt) > maxFrameBytes/2 {
		return fmt.Errorf("Cursor input exceeds limit")
	}
	signals := make(chan os.Signal, 2)
	// Keep repeated terminal signals captured through runtime/store cleanup and
	// the final failure event, including after the terminal restores ISIG.
	defer signal.Stop(signals)
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
	if options.LoadSession != "" {
		if row.SessionID != options.LoadSession || len(options.LoadSession) > 512 || strings.TrimSpace(options.LoadSession) != options.LoadSession || strings.ContainsAny(options.LoadSession, "\x00\r\n") || row.JSONLPath == "" {
			return fmt.Errorf("Cursor latest conversation changed")
		}
		if err = VerifyTranscriptPath(row.JSONLPath); err != nil {
			return err
		}
	}
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
	var cleanupBinding *store.CursorRuntime
	defer func() {
		stopErr := p.terminate()
		if stopErr != nil {
			err = errors.Join(err, stopErr)
		}
		if err != nil && cleanupBinding != nil {
			if proofErr := VerifyRuntime(*cleanupBinding, true); proofErr != nil {
				err = errors.Join(err, proofErr)
			} else if closeErr := closeFailedLaunch(s, *cleanupBinding); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
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
	cleanupBinding = &b
	var tr *transcript
	if options.LoadSession != "" {
		tr, err = appendTranscript(row.JSONLPath, b)
	} else {
		tr, err = open(os.Getenv("HOME"), b)
	}
	if err != nil {
		return err
	}
	defer tr.file.Close()
	rep := &reporter{store: s, cfg: cfg, binding: b, exe: exe, path: tr.path, afterRuntimeRetired: options.afterRuntimeRetired}
	cleanupBinding = &rep.binding
	protocolInput, err := p.protocolInput()
	if err != nil {
		return err
	}
	protocol := newProtocol(protocolInput, p.output)
	defer protocol.close()
	writer := newPaneDisplay(out)
	defer writer.stop()
	inputs := newInputMailbox()
	// Initialization/load keeps its original authority even if a control is
	// queued before the controller consumes it or before publication starts.
	loadEpoch := inputs.epochValue()
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
	var admissionHeld bool
	var admissionIdentity atomic.Value
	admissionIdentity.Store("")
	admissionSession := ""
	restore := startInput(in, writer, func(v terminalInput) {
		if v.accepted {
			v.epoch, v.epochBound = admissionEpoch, true
			v.sessionID = admissionSession
			v.held = admissionHeld
		}
		inputs.put(v)
	}, func() bool {
		inputs.mu.Lock()
		admissionEpoch = inputs.epoch
		admissionSession = admissionIdentity.Load().(string)
		admissionHeld = inputs.rotating
		inputs.mu.Unlock()
		if admissionHeld {
			return true
		}
		return admission.take()
	}, requests.key)
	defer restore()
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	if options.LoadSession != "" {
		fmt.Fprint(writer, "Cursor gateway: loading the latest conversation\r\n")
	} else {
		fmt.Fprint(writer, "Cursor gateway: starting a fresh conversation\r\n")
	}
	if _, err = protocol.request("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false}, "clientInfo": map[string]any{"name": "agent-director", "version": version.Version}}); err != nil {
		return err
	}
	session := ""
	phase := "initialize"
	var loadUntil time.Time
	var sessionUntil time.Time
	var modeResponse json.RawMessage
	var modePhase, modeSession string
	var modeEpoch uint64
	var modeUntil time.Time
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
	var held []terminalInput
	rotating := false
	rotationEpoch := uint64(0)
	rotationOld := ""
	confirmedSessions := map[string]bool{}
	retiredSessions := map[string]bool{}
	var active *terminalInput
	activeRequest := ""
	type responseOrigin struct {
		first   uint64
		session string
	}
	// Session boundaries bound retirement attribution by the same conversation
	// cap as confirmedSessions, regardless of the number of completed prompts.
	var responseOrigins []responseOrigin
	promptFailures := make(chan error, 1)
	activeRetiredBeforeWrite := false
	persistenceFailed := false
	var cancelAt time.Time
	runtimeEnded := false
	activeCancellation := false
	idleControl := false
	var pendingControl map[string]any
	var stopPublication context.CancelFunc
	verifyModeRuntime := VerifyRuntime
	if options.verifyModeRuntime != nil {
		verifyModeRuntime = options.verifyModeRuntime
	}
	configureMode := func(raw json.RawMessage, target string) error {
		if err := requestedSessionMode(raw, options.RequestedMode); err != nil {
			return err
		}
		modeResponse, modePhase, modeSession = raw, phase, target
		modeEpoch, modeUntil = loadEpoch, sessionUntil
		if phase == "load" {
			modeUntil = loadUntil
		}
		if phase == "rotate_new" {
			modeEpoch, modeUntil = rotationEpoch, cancelAt.Add(cancelWait)
		}
		epoch, until := modeEpoch, modeUntil
		if _, err := protocol.requestGuarded("session/set_mode", map[string]any{"sessionId": target, "modeId": options.RequestedMode}, func() error {
			if !inputs.current(epoch) || !time.Now().Before(until) {
				return errPromptRetired
			}
			if err := verifyModeRuntime(rep.binding, false); err != nil {
				return err
			}
			// Kernel reads cannot renew authority retired during verification.
			if !inputs.current(epoch) || !time.Now().Before(until) {
				return errPromptRetired
			}
			return nil
		}); err != nil {
			return err
		}
		phase = modePhase + "_mode"
		return nil
	}
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
		rotationControl := input.text == "/new" || input.text == "/clear"
		if rotationControl {
			kind = "rotation"
			if rotating || input.repeatOnly {
				fmt.Fprint(writer, "Cursor rotation already pending; repeated control ignored\r\n")
				if pendingControl == nil {
					pendingControl = map[string]any{"control": "rotation_repeated", "input_id": input.submission, "count": 1 + input.repeats}
				}
				return nil
			}
			if session == "" {
				return fmt.Errorf("Cursor rotation refused before session confirmation")
			}
			rotating, rotationOld, rotationEpoch = true, session, inputs.epochValue()
			fmt.Fprint(writer, "Cursor rotation requested; new input is held until publication\r\n")
		} else if rotating && input.text != "/exit" {
			return fmt.Errorf("Cursor rotation interrupted; held input refused to both conversations")
		}
		if input.text == "/exit" {
			kind = "exit"
		}
		if input.err != nil {
			kind = "input_closed"
		}
		if pendingControl == nil {
			pendingControl = map[string]any{"control": kind, "input_id": input.submission, "input_epoch": input.epoch, "consumed_at": input.at, "count": 1 + input.repeats}
			if active != nil {
				pendingControl["submission_id"] = active.submission
				pendingControl["request_id"] = activeRequest
			}
		} else {
			pendingControl["count"] = pendingControl["count"].(uint64) + 1
			pendingControl["last_input_id"] = input.submission
		}

		admission.disable()
		requests.cancel()
		if options.LoadSession != "" && stopPublication != nil {
			stopPublication()
		}
		if !busy && !rotating {
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
			if stopPublication != nil {
				stopPublication()
			}
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
				return errACPOperationTimeout
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
	report := func(event string) error {
		e := await(func() error { return rep.report(event) })
		if errors.Is(e, errACPOperationTimeout) {
			return fmt.Errorf("%w (phase=%s, report=%s)", e, phase, event)
		}
		return e
	}
	reportWaiting := func(event string, check func(func() bool)) error {
		return await(func() error { return rep.reportWaiting(event, check) })
	}
	cancelSubmission := func(v terminalInput, reason string) error {
		inputs.release(v)
		fmt.Fprintf(writer, "Cursor submission %s cancelled before dispatch: %s\r\n", v.submission, reason)
		return record("submission_cancelled", map[string]any{"submission_id": v.submission, "submission_session_id": v.sessionID, "reason": reason, "started": false, "control_epoch": inputs.epochValue()})
	}
	defer func() {
		admission.disable()
		inputs.stop()
		requests.cancel()
		if rotating {
			fmt.Fprint(writer, "Cursor rotation failed or stopped; held input refused to both conversations; no automatic replay\r\n")
		}
		for _, v := range queue {
			fmt.Fprintf(writer, "Cursor submission %s cancelled before dispatch: gateway stopped\r\n", v.submission)
		}
		for _, v := range held {
			fmt.Fprintf(writer, "Cursor held submission %s refused; nothing sent to either conversation\r\n", v.submission)
		}
		var pending []terminalInput
		for {
			v, ok := inputs.take()
			if !ok {
				break
			}
			pending = append(pending, v)
			if v.accepted && v.held {
				fmt.Fprintf(writer, "Cursor held submission %s refused; nothing sent to either conversation\r\n", v.submission)
			}
		}
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
			if !finalRecord("submission_cancelled", map[string]any{"submission_id": v.submission, "submission_session_id": v.sessionID, "reason": "gateway stopped", "started": false}) {
				return
			}
		}
		for _, v := range held {
			if !finalRecord("held_input_refused", map[string]any{"submission_id": v.submission, "reason": "rotation stopped; neither conversation admitted this input"}) {
				return
			}
		}
		if rotating {
			if !finalRecord("rotation_failed", map[string]any{"previous_session_id": rotationOld, "last_observed_session_id": rep.binding.SessionID, "reason": "gateway stopped; publication may already have committed; no rollback or replay"}) {
				return
			}
		}
		for _, v := range pending {
			if v.accepted {
				if v.held {
					if !finalRecord("held_input_refused", map[string]any{"submission_id": v.submission, "text": v.text, "reason": "gateway stopped"}) {
						return
					}
					continue
				}
				if !finalRecord("submission_accepted", map[string]any{"submission_id": v.submission, "submission_session_id": v.sessionID, "text": v.text, "consumed_at": v.at, "input_epoch": v.epoch}) {
					return
				}
				if !finalRecord("submission_cancelled", map[string]any{"submission_id": v.submission, "submission_session_id": v.sessionID, "reason": "gateway stopped", "started": false}) {
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
		if v.sessionID != session {
			return cancelSubmission(v, "conversation changed before dispatch")
		}
		if e := VerifyRuntime(rep.binding, false); e != nil {
			return e
		}
		if e := record("user_input", map[string]any{"text": v.text, "submission_id": v.submission, "origin": "main"}); e != nil {
			return e
		}
		if e := report("UserPromptSubmit"); e != nil {
			return e
		}
		if intentional || !inputs.current(v.epoch) || v.sessionID != session {
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

	// Register after all cleanup defers so the loop result is observed first.
	if options.beforeControllerCleanup != nil {
		defer func() { options.beforeControllerCleanup(err) }()
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
			protocol.retirePending()
			if err = record("submission_cancelled", map[string]any{"submission_id": active.submission, "request_id": activeRequest, "reason": "retired before write", "started": false, "bytes_written": 0}); err != nil {
				return err
			}
			active = nil
			activeRequest = ""
			activeRetiredBeforeWrite = false
			activeCancellation = false
			idleControl = !rotating
		}
		if pendingControl != nil {
			data := pendingControl
			pendingControl = nil
			if err = record("input_control", data); err != nil {
				return err
			}
			if data["control"] == "rotation" {
				if err = record("rotation_requested", map[string]any{"previous_session_id": rotationOld, "input_epoch": rotationEpoch}); err != nil {
					return err
				}
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
			if input.held {
				if rotating && inputs.current(input.epoch) && input.epoch == rotationEpoch {
					held = append(held, input)
					if err = record("input_held", map[string]any{"submission_id": input.submission, "text": input.text, "input_epoch": input.epoch, "consumed_at": input.at}); err != nil {
						return err
					}
					fmt.Fprintf(writer, "Cursor submission %s held provisionally\r\n", input.submission)
					continue
				}
				if !inputs.current(input.epoch) || intentional {
					inputs.release(input)
					fmt.Fprintf(writer, "Cursor held submission %s refused; nothing sent to either conversation\r\n", input.submission)
					if err = record("held_input_refused", map[string]any{"submission_id": input.submission, "reason": "control retired provisional input"}); err != nil {
						return err
					}
					continue
				}
				held = append(held, input)
				if err = record("input_held", map[string]any{"submission_id": input.submission, "text": input.text, "input_epoch": input.epoch, "consumed_at": input.at}); err != nil {
					return err
				}
				continue
			}
			if err = record("submission_accepted", map[string]any{"submission_id": input.submission, "submission_session_id": input.sessionID, "text": input.text, "input_epoch": input.epoch, "consumed_at": input.at, "origin": "main"}); err != nil {
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
		for len(held) > 0 && (intentional || !inputs.current(held[0].epoch)) {
			v := held[0]
			inputs.release(v)
			fmt.Fprintf(writer, "Cursor held submission %s refused; nothing sent to either conversation\r\n", v.submission)
			if err = record("held_input_refused", map[string]any{"submission_id": v.submission, "reason": "control retired provisional input"}); err != nil {
				return err
			}
			held = held[1:]
		}
		if rotating && !busy && !intentional && phase != "rotate_new" && phase != "rotate_new_mode" {
			if !inputs.current(rotationEpoch) {
				return fmt.Errorf("Cursor rotation authority retired")
			}
			rotationUntil := cancelAt.Add(cancelWait)
			if !time.Now().Before(rotationUntil) {
				return fmt.Errorf("Cursor rotation cancellation deadline passed")
			}
			requestEpoch := rotationEpoch
			if _, err = protocol.requestGuarded("session/new", map[string]any{"cwd": row.CWD, "mcpServers": []any{}}, func() error {
				if !inputs.current(requestEpoch) || !time.Now().Before(rotationUntil) {
					return errPromptRetired
				}
				return nil
			}); err != nil {
				return err
			}
			phase = "rotate_new"
			// The first control's existing fixed bound covers creation and publication.
		}
		if idleControl && !rotating && !busy && !intentional && session != "" {
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
		if !rotating && !intentional && !busy && (len(queue) > 0 || len(held) > 0) && !requests.open() {
			eligible := false
			e := await(func() (e error) { eligible, e = rep.promptEligible(); return e })
			if e != nil {
				return e
			}
			if eligible {
				for len(held) > 0 && inputs.current(held[0].epoch) && !intentional {
					v := held[0]
					var promoted bool
					v, promoted = inputs.promote(v)
					if !promoted {
						break
					}
					v.sessionID = session
					held = held[1:]
					queue = insertSubmission(queue, v)
					if err = record("submission_accepted", map[string]any{"submission_id": v.submission, "text": v.text, "input_epoch": v.epoch, "consumed_at": v.at, "released_from_hold": true}); err != nil {
						return err
					}
					fmt.Fprintf(writer, "Cursor submission %s accepted after fresh publication\r\n", v.submission)
				}
				if len(queue) == 0 {
					continue
				}
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
			return errACPOperationTimeout
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
				return fmt.Errorf("Cursor ACP %s transport write failed; delivery unconfirmed", acpOperation(protocol.pendingMethod))
			}
		case next := <-protocol.frames:
			if next.err != nil {
				if errors.Is(next.err, errMalformedACPFrame) {
					return fmt.Errorf("Cursor ACP %s received a malformed frame", acpOperation(protocol.pendingMethod))
				}
				return fmt.Errorf("Cursor ACP %s transport failed; child termination must be confirmed separately", acpOperation(protocol.pendingMethod))
			}
			f := next.frame
			if err = VerifyRuntime(rep.binding, false); err != nil {
				return err
			}
			if modeResponse != nil && f.Method != "" {
				var attribution struct {
					Session string `json:"sessionId"`
				}
				if json.Unmarshal(f.Params, &attribution) != nil {
					return fmt.Errorf("invalid Cursor mode event attribution")
				}
				if attribution.Session == modeSession {
					if f.Method == "session/update" {
						if err = modeUpdate(f.Params, modeSession, options.RequestedMode); err != nil {
							return err
						}
					}
					if len(f.ID) > 0 {
						cancel, e := protocol.deny(f)
						if e != nil {
							return e
						}
						if cancel {
							return fmt.Errorf("unsupported authorization during Cursor mode configuration")
						}
					}
					if err = record("session_configuration_update", map[string]any{"origin": "unknown", "params": json.RawMessage(f.Params)}); err != nil {
						return err
					}
					continue
				}
			}
			// session/load replays history before its response. Historical frames
			// never reach recording, request admission, execution proof or state.
			if phase == "load" && f.Method != "" {
				if len(f.ID) > 0 {
					cancel, e := protocol.deny(f)
					if e != nil {
						return e
					}
					if cancel {
						return fmt.Errorf("unsupported authorization during Cursor load")
					}
				}
				continue
			}
			if protocol.retiredResponse(f) {
				id, _ := strconv.ParseUint(strings.TrimSpace(string(f.ID)), 10, 64)
				origin := ""
				for i := len(responseOrigins) - 1; i >= 0; i-- {
					if id >= responseOrigins[i].first {
						origin = responseOrigins[i].session
						break
					}
				}
				if err = record("retired_response", map[string]any{"request_id": json.RawMessage(f.ID), "submission_session_id": origin}); err != nil {
					return err
				}
				continue
			}
			if f.Method != "" {
				retiring := ""
				if rotating {
					retiring = rotationOld
				}
				if origin := requests.retiredAuthorization(f, session, retiring); len(f.ID) > 0 && origin != "" {
					if err = record("retired_authorization", map[string]any{"authorization_id": json.RawMessage(f.ID), "method": f.Method, "submission_session_id": origin}); err != nil {
						return err
					}
					continue
				}
				// Session-attributed retired traffic is inspectable but grants no
				// current lifecycle, answer or hold-release authority.
				var attribution struct {
					Session string `json:"sessionId"`
				}
				if json.Unmarshal(f.Params, &attribution) != nil {
					return fmt.Errorf("invalid Cursor event attribution")
				}
				if retiredSessions[attribution.Session] || (rotating && attribution.Session == rotationOld) {
					if err = record("retired_event", map[string]any{"session_id": attribution.Session, "submission_session_id": attribution.Session, "method": f.Method, "params": json.RawMessage(f.Params)}); err != nil {
						return err
					}
					if !retiredSessions[attribution.Session] && f.Method == "session/update" {
						if err = requests.execution(f.Params); err != nil {
							return err
						}
					}
					continue
				}
				if attribution.Session != "" && attribution.Session != session {
					return fmt.Errorf("Cursor event session mismatch")
				}
				if f.Method == "session/update" && session != "" && attribution.Session == "" {
					return fmt.Errorf("Cursor update lacks session attribution")
				}
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
							Kind    string          `json:"sessionUpdate"`
							Content json.RawMessage `json:"content"`
							AgentID string          `json:"agentId"`
						} `json:"update"`
					}
					if e := json.Unmarshal(f.Params, &update); e != nil {
						return fmt.Errorf("invalid Cursor update schema: %w", e)
					}
					if update.SessionID != session {
						return fmt.Errorf("Cursor update session mismatch")
					}
					var text struct {
						Type string `json:"type"`
						Text string `json:"text"`
					}
					textChunk := update.Update.Kind == "agent_message_chunk" || update.Update.Kind == "agent_thought_chunk"
					if textChunk {
						if e := json.Unmarshal(update.Update.Content, &text); e != nil {
							return fmt.Errorf("invalid Cursor text update schema: %w", e)
						}
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
					if textChunk && update.Update.AgentID == "" && text.Type == "text" {
						fmt.Fprint(writer, safeText(text.Text))
					} else if update.Update.Kind == "tool_call" || update.Update.Kind == "tool_call_update" {
						fmt.Fprint(writer, "\r\nCursor tool activity\r\n")
					}
				}
				continue
			}
			raw, e := protocol.response(f)
			if e != nil {
				if modeResponse != nil {
					return fmt.Errorf("Cursor requested mode configuration failed; remove --plan, or --mode and its value, then retry: %w", e)
				}
				return e
			}
			modeConfirmed := false
			if modeResponse != nil {
				if !inputs.current(modeEpoch) || intentional || !time.Now().Before(modeUntil) {
					return fmt.Errorf("Cursor mode configuration authority retired; no prompt admitted")
				}
				if err = confirmSessionMode(raw, modeSession, options.RequestedMode); err != nil {
					return err
				}
				if err = record("session_mode_configured", map[string]any{"configured_session_id": modeSession, "mode_id": options.RequestedMode, "request_id": json.RawMessage(f.ID)}); err != nil {
					return err
				}
				if !inputs.current(modeEpoch) || intentional || !time.Now().Before(modeUntil) {
					return fmt.Errorf("Cursor mode configuration authority retired; no prompt admitted")
				}
				raw, phase, modeResponse = modeResponse, modePhase, nil
				modeConfirmed = true
			}
			switch phase {
			case "initialize":
				if options.LoadSession != "" && (!cancelAt.IsZero() || !inputs.current(loadEpoch)) {
					return fmt.Errorf("Cursor initialization retired by control")
				}
				loadSupported, e := initializeCapabilities(raw)
				if e != nil {
					return e
				}
				method := "session/new"
				params := map[string]any{"cwd": row.CWD, "mcpServers": []any{}}
				phase = "new"
				if options.LoadSession != "" {
					if !loadSupported {
						return fmt.Errorf("Cursor runtime does not support session/load")
					}
					method, phase = "session/load", "load"
					params["sessionId"] = options.LoadSession
					loadUntil = time.Now().Add(protocolWait)
				}
				if _, err = protocol.request(method, params); err != nil {
					return err
				}
				if phase == "load" {
					resetDeadline(time.Until(loadUntil))
				} else {
					sessionUntil = time.Now().Add(protocolWait)
					resetDeadline(protocolWait)
				}
			case "rotate_new":
				var created struct {
					ID string `json:"sessionId"`
				}
				if json.Unmarshal(raw, &created) != nil || created.ID == "" || strings.TrimSpace(created.ID) != created.ID || len(created.ID) > 512 || strings.ContainsAny(created.ID, "\x00\r\n") || confirmedSessions[created.ID] || len(confirmedSessions) >= maxConnectionRequests {
					return fmt.Errorf("Cursor did not confirm a genuinely fresh session; held input refused")
				}
				if options.RequestedMode != "" && !modeConfirmed {
					if err = configureMode(raw, created.ID); err != nil {
						return err
					}
					continue
				}
				if err = record("rotation_confirmed", map[string]any{"previous_session_id": rotationOld, "confirmed_session_id": created.ID}); err != nil {
					return err
				}
				if intentional {
					continue
				}
				if !inputs.current(rotationEpoch) {
					return fmt.Errorf("Cursor rotation confirmation retired")
				}
				publication := *rep
				publicationUntil := cancelAt.Add(cancelWait)
				publicationContext, cancelPublication := context.WithDeadline(context.Background(), publicationUntil)
				publication.rotationContext = publicationContext
				stopPublication = cancelPublication
				var reconciled atomic.Bool
				publication.rotationReconciled = func() { reconciled.Store(true) }
				publicationFinished := make(chan struct{})
				var committed atomic.Pointer[store.CursorRuntime]
				publication.rotationObserved = func(bound store.CursorRuntime) { committed.Store(&bound) }
				err = await(func() error {
					defer close(publicationFinished)
					return publication.reportIdentity("SessionStart", nil, created.ID, publicationUntil)
				})
				cancelPublication()
				stopPublication = nil
				// An interrupted await can finish before the canceled hook. Join
				// only within the original bound; its final snapshot follows exit.
				join := time.NewTimer(time.Until(publicationUntil))
				select {
				case <-publicationFinished:
				case <-join.C:
				}
				join.Stop()
				// Committed observations do not depend on hook completion. Neither
				// observation nor joining can extend the first cancellation deadline.
				expected := b
				expected.SessionID = created.ID
				var observed store.CursorRuntime
				if bound := committed.Load(); bound != nil {
					observed = *bound
				}
				if observed == expected {
					b, rep.binding, tr.binding, session = expected, expected, expected, created.ID
					retiredSessions[rotationOld] = true
					confirmedSessions[session] = true
					responseOrigins = append(responseOrigins, responseOrigin{protocol.next, session})
				}
				if intentional && reconciled.Load() {
					err = nil
					continue
				}
				if err != nil {
					return fmt.Errorf("Cursor rotation report failed; publication may already be committed; held input refused: %w", err)
				}
				if observed != expected {
					return fmt.Errorf("Cursor rotation publication was not committed")
				}
				if err = record("rotation_published", map[string]any{"previous_session_id": rotationOld, "session_id": session}); err != nil {
					return err
				}
				if intentional || !inputs.current(rotationEpoch) {
					continue
				}
				admission.mu.Lock()
				admissionReporter := *rep
				admission.validate = func() bool { ok, e := admissionReporter.promptEligible(); return e == nil && ok }
				admission.mu.Unlock()
				if !inputs.publish(rotationEpoch, func() { admissionIdentity.Store(session); admission.enableQueue() }) {
					continue
				}
				rotating, cancelled, idleControl, requestDeadlineHeld = false, false, false, false
				phase = "idle"
				stopDeadline()
				cancelAt = time.Time{}
				fmt.Fprint(writer, "Cursor fresh conversation published\r\n> ")
			case "new", "load":
				loaded := phase == "load"
				if loaded && (!cancelAt.IsZero() || !inputs.current(loadEpoch)) {
					return fmt.Errorf("Cursor load retired by control")
				}
				var created struct {
					ID string `json:"sessionId"`
				}
				if loaded {
					var result map[string]json.RawMessage
					if json.Unmarshal(raw, &result) != nil || result == nil {
						return fmt.Errorf("invalid Cursor load response")
					}
					if supplied, ok := result["sessionId"]; ok {
						if json.Unmarshal(supplied, &created.ID) != nil || created.ID != options.LoadSession {
							return fmt.Errorf("Cursor loaded session mismatch")
						}
					}
					created.ID = options.LoadSession
				}
				if (!loaded && json.Unmarshal(raw, &created) != nil) || created.ID == "" || len(created.ID) > 512 || strings.TrimSpace(created.ID) != created.ID || strings.ContainsAny(created.ID, "\x00\r\n") {
					return fmt.Errorf("Cursor did not confirm a fresh session")
				}
				if options.RequestedMode != "" && !modeConfirmed {
					if err = configureMode(raw, created.ID); err != nil {
						return err
					}
					continue
				}
				waitingEpoch := inputs.epochValue()
				expected := b
				expected.SessionID = created.ID
				var applied bool
				var e error
				if loaded {
					waitingEpoch = loadEpoch
					publicationContext, cancelPublication := context.WithDeadline(context.Background(), loadUntil)
					stopPublication = cancelPublication
					// await supervises controls/death; SQL, commit and cleanup use
					// the same cancelable context and original load deadline.
					e = await(func() error {
						if beforeLoadPublication != nil {
							beforeLoadPublication(publicationContext)
						}
						ok, bindErr := s.BindLoadedCursorSession(publicationContext, row, expected, tr.path, loadUntil, func() bool {
							return inputs.current(loadEpoch) && VerifyRuntime(expected, false) == nil
						})
						if bindErr == nil && !ok {
							return fmt.Errorf("Cursor session binding changed")
						}
						return bindErr
					})
					cancelPublication()
					stopPublication = nil
					if e != nil {
						return fmt.Errorf("Cursor load publication failed; commit may be unconfirmed; no prompt admitted: %w", e)
					}
					applied = true
				} else {
					applied, e = s.SetCursorRuntimeSession(expected, created.ID)
				}
				if e != nil || !applied {
					return fmt.Errorf("Cursor session binding changed")
				}
				b, rep.binding, tr.binding, session = expected, expected, expected, created.ID
				if loaded && (!cancelAt.IsZero() || intentional || !inputs.current(loadEpoch) || !time.Now().Before(loadUntil)) {
					return fmt.Errorf("Cursor load publication committed but authority retired; no prompt admitted")
				}
				admissionIdentity.Store(session)
				confirmedSessions[session] = true
				responseOrigins = append(responseOrigins, responseOrigin{protocol.next, session})
				rep.loaded = loaded
				admission.mu.Lock()
				admissionReporter := *rep
				admission.validate = func() bool { ok, e := admissionReporter.promptEligible(); return e == nil && ok }
				admission.mu.Unlock()
				kind := "session_start"
				if loaded {
					kind = "session_loaded"
				}
				if err = record(kind, map[string]any{"origin": "main"}); err != nil {
					return err
				}
				if loaded && (!cancelAt.IsZero() || intentional || !inputs.current(loadEpoch) || !time.Now().Before(loadUntil)) {
					return fmt.Errorf("Cursor loaded binding retained but authority retired; no prompt admitted")
				}
				var waiting func(func() bool)
				if options.InitialPrompt == "" {
					waiting = func(check func() bool) { inputs.ifCurrent(waitingEpoch, func() { admission.waiting(check) }) }
				}
				if !loaded {
					if err = reportWaiting("SessionStart", waiting); err != nil {
						return err
					}
				}
				if cancelAt.IsZero() {
					stopDeadline()
				}
				if loaded {
					fmt.Fprint(writer, "Cursor conversation loaded; awaiting a prompt\r\n> ")
				} else {
					fmt.Fprint(writer, "Cursor ready\r\n> ")
				}
				phase = "idle"
				if options.InitialPrompt == "" {
					if !intentional {
						inputs.ifCurrent(waitingEpoch, admission.enableQueue)
					}
				}
				if options.InitialPrompt != "" && !intentional && cancelAt.IsZero() && inputs.current(waitingEpoch) {
					text := options.InitialPrompt
					options.InitialPrompt = ""
					v := terminalInput{text: text, sessionID: session, accepted: true, submission: "initial", at: time.Now()}
					inputs.mu.Lock()
					v.epoch = waitingEpoch
					inputs.count++
					inputs.bytes += len(text)
					inputs.mu.Unlock()
					if err = record("submission_accepted", map[string]any{"submission_id": v.submission, "text": text, "origin": "main"}); err != nil {
						return err
					}
					queue = append(queue, v)
					if !loaded {
						if err = submit(v); err != nil {
							if active != nil && active.submission == v.submission {
								queue = queue[1:]
							}
							return err
						}
						queue = queue[1:]
					}
				}
				if !intentional {
					queueEnabled := false
					inputs.ifCurrent(waitingEpoch, func() {
						admission.enableQueue()
						queueEnabled = true
					})
					if queueEnabled && options.afterStartupQueueEnabled != nil {
						options.afterStartupQueueEnabled()
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
				if rotating {
					continue
				}
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
			rep.terminationUntil = cancelAt.Add(cancelWait)
			if err = report("SessionEnd"); err != nil {
				return err
			}
		}
		fmt.Fprint(writer, "Cursor runtime terminated\r\n")
		return nil
	}
	return fmt.Errorf("Cursor gateway stopped without termination evidence")
}

// A completed Run has relinquished admission even though its hosting process
// may still be alive. Only independently proven runtime death permits this close.
func closeFailedLaunch(s *store.Store, b store.CursorRuntime) error {
	row, err := s.GetSpawn(b.InstanceID)
	if err != nil {
		return err
	}
	_, result, err := s.MarkFailedCursorRuntimeMissing(row, b)
	if err != nil {
		return err
	}
	if result == store.CondChanged {
		current, e := s.GetSpawn(b.InstanceID)
		if e != nil {
			return e
		}
		if current.State != store.StateEnded && current.State != store.StateMissing && current.Identity.Token == b.Token && current.LifeNumber == b.Life {
			return fmt.Errorf("Cursor failed launch changed during close")
		}
	}
	return nil
}
