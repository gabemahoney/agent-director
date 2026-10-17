package cursorhost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const maxFrameBytes = 2 * 1024 * 1024
const protocolWait = 45 * time.Second
const cancelWait = 3 * time.Second
const transportWriteWait = 3 * time.Second

var errMalformedACPFrame = errors.New("malformed ACP frame")
var errACPOperationUnsupported = errors.New("operation unsupported by the selected runtime")

type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}
type incoming struct {
	frame frame
	err   error
}
type outgoingFrame struct {
	raw      []byte
	deadline time.Time
	guard    func() error
	done     func(error)
}

type protocol struct {
	input          io.Writer
	outgoing       chan outgoingFrame
	writes         chan error
	closed         chan struct{}
	outputMu       sync.Mutex // serializes enqueue with transport retirement
	closeOnce      sync.Once
	writeFailed    atomic.Bool
	frames         chan incoming
	next           uint64
	pendingID      string
	pendingMethod  string
	retiredThrough uint64
}

func newProtocol(in io.Writer, out io.Reader) *protocol {
	p := &protocol{input: in, outgoing: make(chan outgoingFrame, 32), writes: make(chan error, 32), closed: make(chan struct{}), frames: make(chan incoming, 32)}
	go p.writeLoop()
	go func() {
		r := bufio.NewReaderSize(out, 64*1024)
		for {
			var raw []byte
			for {
				part, e := r.ReadSlice('\n')
				if len(raw)+len(part) > maxFrameBytes {
					p.deliver(incoming{err: errMalformedACPFrame})
					return
				}
				raw = append(raw, part...)
				if e == bufio.ErrBufferFull {
					continue
				}
				if e != nil {
					if len(raw) > 0 {
						e = errMalformedACPFrame
					}
					p.deliver(incoming{err: e})
					return
				}
				break
			}
			var f frame
			if e := json.Unmarshal(raw, &f); e != nil || uniqueJSON(raw) != nil || f.JSONRPC != "2.0" {
				p.deliver(incoming{err: errMalformedACPFrame})
				return
			}
			if len(f.ID) > 0 {
				var id any
				decoder := json.NewDecoder(bytes.NewReader(f.ID))
				decoder.UseNumber()
				if e := decoder.Decode(&id); e != nil {
					p.deliver(incoming{err: errMalformedACPFrame})
					return
				}
				switch id.(type) {
				case string, json.Number:
				default:
					p.deliver(incoming{err: errMalformedACPFrame})
					return
				}
			}
			if f.Method == "" && (len(f.ID) == 0 || ((len(f.Result) == 0) == (len(f.Error) == 0))) {
				p.deliver(incoming{err: errMalformedACPFrame})
				return
			}
			p.deliver(incoming{frame: f})
		}
	}()
	return p
}
func (p *protocol) send(value any) error {
	return p.sendGuarded(value, time.Now().Add(transportWriteWait), nil, nil)
}

func (p *protocol) sendGuarded(value any, deadline time.Time, guard func() error, done func(error)) error {
	raw, e := json.Marshal(value)
	if e != nil {
		return e
	}
	if len(raw) > maxFrameBytes {
		return fmt.Errorf("ACP output exceeds limit")
	}
	p.outputMu.Lock()
	defer p.outputMu.Unlock()
	if p.writeFailed.Load() {
		return fmt.Errorf("ACP transport write failed")
	}
	select {
	case <-p.closed:
		return fmt.Errorf("ACP transport closed")
	default:
	}
	select {
	case p.outgoing <- outgoingFrame{raw: append(raw, '\n'), deadline: deadline, guard: guard, done: done}:
	default:
		return fmt.Errorf("ACP output queue exceeds limit")
	}
	return nil
}

// close releases pipe I/O without treating transport closure as process death.
func (p *protocol) close() {
	p.closeOnce.Do(func() {
		p.outputMu.Lock()
		close(p.closed)
		p.outputMu.Unlock()
		if closer, ok := p.input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
}

func (p *protocol) deliver(next incoming) {
	select {
	case p.frames <- next:
	case <-p.closed:
	}
}

// One worker serializes writes. A timeout makes delivery uncertain and fatal;
// no pending frame is replayed, and the main loop remains free to stop the child.
func (p *protocol) writeLoop() {
	// Once accepted, a frame belongs to this worker even if close or a
	// preceding write failure stops it. Retire admission before draining so
	// no successful enqueue can be stranded behind the stopped worker.
	defer p.failQueued()
	for {
		var next outgoingFrame
		select {
		case <-p.closed:
			return
		case next = <-p.outgoing:
		}
		// Retired request frames are discarded, including denials. Guard failure
		// is a request failure, not an uncertain pipe write and never a retry.
		if next.guard != nil {
			if err := next.guard(); err != nil {
				if next.done != nil {
					next.done(err)
				}
				continue
			}
		}
		type writeResult struct {
			err      error
			prewrite bool
		}
		result := make(chan writeResult, 1)
		go func() {
			// Frames which expired in the queue must never reach the pipe.
			if !time.Now().Before(next.deadline) {
				result <- writeResult{err: fmt.Errorf("ACP transport write timed out")}
				return
			}
			if next.guard != nil {
				if err := next.guard(); err != nil {
					result <- writeResult{err: err, prewrite: errors.Is(err, errPromptRetired)}
					return
				}
			}
			// Authority checks may wait on tmux/kernel/store evidence. They
			// cannot turn an expired transport frame into a late pipe write.
			if !time.Now().Before(next.deadline) || p.writeFailed.Load() {
				result <- writeResult{err: fmt.Errorf("ACP response expired before write")}
				return
			}
			select {
			case <-p.closed:
				result <- writeResult{err: fmt.Errorf("ACP transport closed")}
				return
			default:
			}
			n, err := p.input.Write(next.raw)
			if err == nil && n != len(next.raw) {
				err = io.ErrShortWrite
			}
			result <- writeResult{err: err}
		}()
		timer := time.NewTimer(time.Until(next.deadline))
		var err error
		prewrite := false
		select {
		case written := <-result:
			err, prewrite = written.err, written.prewrite
		case <-timer.C:
			err = fmt.Errorf("ACP transport write timed out")
		case <-p.closed:
			err = fmt.Errorf("ACP transport closed")
		}
		timer.Stop()
		if prewrite {
			// A retired prompt is known unwritten at the final guard too.
			// Other final authority failures retain their fail-closed behavior.
			if next.done != nil {
				next.done(err)
			}
			continue
		}
		if err != nil {
			p.writeFailed.Store(true)
			// Closing an owned pipe releases a stalled writer. The supervisor,
			// not this transport worker, must still confirm process termination.
			if closer, ok := p.input.(io.Closer); ok {
				_ = closer.Close()
			}
			err = fmt.Errorf("ACP transport write failed; delivery is uncertain")
		}
		if next.done != nil {
			next.done(err)
		}
		select {
		case p.writes <- err:
		case <-p.closed:
			return
		}
		if err != nil {
			return
		}
	}
}

func (p *protocol) failQueued() {
	p.outputMu.Lock()
	p.writeFailed.Store(true)
	var pending []outgoingFrame
	for {
		select {
		case next := <-p.outgoing:
			pending = append(pending, next)
		default:
			p.outputMu.Unlock()
			for _, next := range pending {
				if next.done != nil {
					next.done(fmt.Errorf("ACP transport stopped before response completion"))
				}
			}
			return
		}
	}
}

func (p *protocol) request(method string, params any) (string, error) {
	return p.requestGuarded(method, params, nil)
}

func (p *protocol) requestGuarded(method string, params any, guard func() error, completions ...func(error)) (string, error) {
	var done func(error)
	if len(completions) > 0 {
		done = completions[0]
	}
	if p.pendingID != "" {
		return "", fmt.Errorf("ACP request already pending")
	}
	p.next++
	id := fmt.Sprint(p.next)
	if err := p.sendGuarded(map[string]any{"jsonrpc": "2.0", "id": p.next, "method": method, "params": params}, time.Now().Add(transportWriteWait), guard, done); err != nil {
		return "", err
	}
	p.pendingID = id
	p.pendingMethod = method
	return id, nil
}
func (p *protocol) notify(method string, params any) error {
	return p.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (p *protocol) response(f frame) (json.RawMessage, error) {
	if f.Method != "" || string(bytes.TrimSpace(f.ID)) != p.pendingID || p.pendingID == "" {
		return nil, fmt.Errorf("uncorrelated ACP response")
	}
	method := p.pendingMethod
	p.retiredThrough = p.next
	p.pendingID = ""
	p.pendingMethod = ""
	if len(f.Error) > 0 {
		return nil, acpResponseFailure(method, f.Error)
	}
	return f.Result, nil
}

// Only documented ACP error codes supply causes. Message and data are untrusted
// runtime diagnostics and must never reach the pane or ad.cursor.failed trail.
// https://agentclientprotocol.com/protocol/v1/schema#errorcode
func acpResponseFailure(method string, raw json.RawMessage) error {
	method = acpOperation(method)
	// Case variants are unknown properties, never replacements for the exact
	// required keys or evidence for a different structured failure cause.
	var failure map[string]json.RawMessage
	var code *int32
	var message *string
	if json.Unmarshal(raw, &failure) != nil || json.Unmarshal(failure["code"], &code) != nil || code == nil ||
		json.Unmarshal(failure["message"], &message) != nil || message == nil {
		return fmt.Errorf("Cursor ACP %s failed: malformed error response", method)
	}
	cause := "runtime rejected the request; cause unknown"
	switch *code {
	case -32601:
		return fmt.Errorf("Cursor ACP %s failed: %w", method, errACPOperationUnsupported)
	case -32700, -32600, -32602:
		cause = "runtime rejected the request format or parameters"
	case -32603:
		cause = "runtime internal error; cause unknown"
	case -32800:
		cause = "runtime cancelled the request"
	case -32000:
		cause = "authentication required; check the selected runtime's existing login"
	case -32002:
		cause = "requested resource not found"
	}
	return fmt.Errorf("Cursor ACP %s failed: %s", method, cause)
}

func acpOperation(method string) string {
	// Operation names come only from this host. Bound future internal callers
	// too, so no runtime-supplied method or arbitrary label can be rendered.
	switch method {
	case "initialize", "session/new", "session/load", "session/set_mode", "session/prompt":
		return method
	default:
		return "request"
	}
}

func (p *protocol) retiredResponse(f frame) bool {
	raw := string(bytes.TrimSpace(f.ID))
	id, err := strconv.ParseUint(raw, 10, 64)
	return f.Method == "" && err == nil && id != 0 && strconv.FormatUint(id, 10) == raw && id <= p.retiredThrough
}

func (p *protocol) retirePending() {
	if p.pendingID != "" {
		p.retiredThrough = p.next
	}
	p.pendingID, p.pendingMethod = "", ""
}
func (p *protocol) deny(f frame) (bool, error) {
	if len(f.ID) == 0 {
		return false, nil
	}
	var outcome any
	cancel := false
	switch f.Method {
	case "cursor/ask_question":
		// Installed-source-supported cancellation is a complete question
		// rejection; it does not cancel independent genuine requests.
		outcome = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
	case "cursor/create_plan":
		outcome = map[string]any{"outcome": map[string]any{"outcome": "rejected"}}
	case "session/request_permission":
		var params struct {
			Options []struct {
				ID   string `json:"optionId"`
				Kind string `json:"kind"`
			} `json:"options"`
		}
		valid := json.Unmarshal(f.Params, &params) == nil
		// Pre-admission policy rejection, including fallback questions:
		// only a genuine reject option may be selected. Otherwise cancel the turn.
		seen := make(map[string]bool, len(params.Options))
		rejectID := ""
		for _, o := range params.Options {
			if o.ID == "" || seen[o.ID] {
				valid = false
				break
			}
			seen[o.ID] = true
			switch o.Kind {
			case "allow_once", "allow_always", "reject_always":
			case "reject_once":
				if rejectID == "" {
					rejectID = o.ID
				}
			default:
				valid = false
			}
		}
		if valid && rejectID != "" {
			outcome = map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": rejectID}}
		}
		if outcome == nil {
			outcome = map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
			cancel = true
		}
	default:
		if err := p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(f.ID), "error": map[string]any{"code": -32601, "message": "Unsupported client request; turn cancelled"}}); err != nil {
			return true, err
		}
		return true, nil
	}
	return cancel, p.send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(f.ID), "result": outcome})
}
