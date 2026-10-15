package cursorhost

import (
	"fmt"
	"sync"
	"time"
)

type terminalInput struct {
	submission   string
	epoch        uint64
	epochBound   bool
	at           time.Time
	refusal      string
	dropped      uint64
	choiceToken  string
	choiceOption string
	accepted     bool
	text         string
	interrupt    bool
	err          error
}

const maxQueuedSubmissions = 32
const maxQueuedBytes = 8 * 1024 * 1024

// The terminal never waits for ordinary work to drain before consuming a
// control. Accepted reservations include work transferred to the session queue.
type inputMailbox struct {
	mu           sync.Mutex
	wake         chan struct{}
	controlWake  chan struct{}
	items        []terminalInput
	control      *terminalInput
	next         uint64
	count, bytes int
	refused      uint64
	epoch        uint64
	onControl    func()
}

func newInputMailbox() *inputMailbox {
	return &inputMailbox{wake: make(chan struct{}, 1), controlWake: make(chan struct{}, 1)}
}

func (m *inputMailbox) put(v terminalInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	v.submission = fmt.Sprint(m.next)
	if !v.epochBound {
		v.epoch = m.epoch
	}
	v.at = time.Now()
	if v.interrupt || v.text == "/exit" || v.err != nil {
		m.epoch++
		if m.onControl != nil {
			m.onControl()
		}
		// Repeated interrupts coalesce without moving the first deadline;
		// exit/input loss can only strengthen the pending control.
		if m.control == nil {
			m.control = &v
		} else if v.text == "/exit" || v.err != nil {
			v.at = m.control.at
			m.control = &v
		}
		select {
		case m.controlWake <- struct{}{}:
		default:
		}
		return
	}
	if v.accepted {
		if m.count >= maxQueuedSubmissions || len(v.text) > maxFrameBytes/2 || m.bytes+len(v.text) > maxQueuedBytes {
			v.accepted = false
			v.refusal = "capacity"
		} else {
			m.count++
			m.bytes += len(v.text)
		}
	}
	if !v.accepted && v.choiceToken == "" && v.text != "" && v.text != "/new" && v.text != "/clear" {
		if v.refusal == "" {
			v.refusal = "starting, held, stale or cancellation"
		}
		v.text = ""
	}
	// Refusals and menu keys are bounded too. Accepted reservations are never
	// replaced. Overflow is an explicit aggregated refusal, never future work.
	if !v.accepted && len(m.items) >= 2*maxQueuedSubmissions {
		m.refused++
	} else {
		m.items = append(m.items, v)
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *inputMailbox) takeControl() *terminalInput {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.control
	m.control = nil
	return v
}

func (m *inputMailbox) take() (terminalInput, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.items) == 0 {
		if m.refused == 0 {
			return terminalInput{}, false
		}
		v := terminalInput{refusal: "input capacity", dropped: m.refused}
		m.refused = 0
		return v, true
	}
	v := m.items[0]
	m.items[0] = terminalInput{}
	m.items = m.items[1:]
	return v, true
}

func (m *inputMailbox) release(v terminalInput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count--
	m.bytes -= len(v.text)
}

func (m *inputMailbox) current(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch == epoch
}

func (m *inputMailbox) epochValue() uint64 { m.mu.Lock(); defer m.mu.Unlock(); return m.epoch }

func (m *inputMailbox) ifCurrent(epoch uint64, action func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch == epoch {
		action()
	}
}
