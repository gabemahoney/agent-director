package cursorhost

import (
	"io"
	"sync"
	"time"
)

// Pane rendering must not stop terminal control consumption. The protected
// transcript remains the complete record when a slow terminal loses rendering.
type paneDisplay struct {
	mu     sync.Mutex
	queue  chan []byte
	loss   chan struct{}
	closed chan struct{}
	done   chan struct{}
	lost   bool
	bytes  int
}

func newPaneDisplay(out io.Writer) *paneDisplay {
	d := &paneDisplay{queue: make(chan []byte, 64), loss: make(chan struct{}, 1), closed: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		write := func(raw []byte) { d.mu.Lock(); d.bytes -= len(raw); d.mu.Unlock(); _, _ = out.Write(raw) }
		writeLoss := func() {
			d.mu.Lock()
			lost := d.lost
			d.lost = false
			d.mu.Unlock()
			if lost {
				_, _ = io.WriteString(out, "\r\nCursor pane output omitted under pressure; inspect the protected transcript\r\n")
			}
		}
		for {
			writeLoss()
			select {
			case raw := <-d.queue:
				write(raw)
			case <-d.loss:
			case <-d.closed:
				for {
					writeLoss()
					select {
					case raw := <-d.queue:
						write(raw)
					default:
						return
					}
				}
			}
		}
	}()
	return d
}

func (d *paneDisplay) Write(raw []byte) (int, error) {
	d.mu.Lock()
	defer func() {
		if d.lost {
			// Loss must wake an idle consumer even when no frame was queued.
			select {
			case d.loss <- struct{}{}:
			default:
			}
		}
		d.mu.Unlock()
	}()
	if len(raw) > maxFrameBytes || d.bytes+len(raw) > maxQueuedBytes {
		d.lost = true
		return len(raw), nil
	}
	select {
	case d.queue <- append([]byte(nil), raw...):
		d.bytes += len(raw)
	default:
		d.lost = true
	}
	return len(raw), nil
}

func (d *paneDisplay) stop() {
	close(d.closed)
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-d.done:
	case <-timer.C:
	}
}
