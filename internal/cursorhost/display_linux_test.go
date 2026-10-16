//go:build linux

package cursorhost

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPaneDisplayReportsLossWithoutAnotherProducer(t *testing.T) {
	for _, pressure := range []string{"full queue before drain", "oversized with empty queue"} {
		t.Run(pressure, func(t *testing.T) {
			entered, release, warned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce, warningOnce, stopOnce sync.Once
			out := &observedWire{observe: func(raw []byte) {
				if string(raw) == "blocked;" {
					close(entered)
					<-release
				}
				if bytes.Contains(raw, []byte("Cursor pane output omitted under pressure; inspect the protected transcript")) {
					warningOnce.Do(func() { close(warned) })
				}
			}}
			d := newPaneDisplay(out)
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			stop := func() { stopOnce.Do(d.stop) }
			t.Cleanup(func() { unblock(); stop() })
			write := func(raw string) {
				t.Helper()
				if n, err := d.Write([]byte(raw)); n != len(raw) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if pressure == "full queue before drain" {
				write("blocked;")
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("consumer did not reach blocked output")
				}
				// Fill the bounded queue while its sole consumer is held, then make
				// the final producer lose a frame before any drain can occur.
				for n := 0; n < 64; n++ {
					write("queued;")
				}
				write("lost-final;")
				unblock()
			} else {
				write(strings.Repeat("x", maxFrameBytes+1))
			}
			select {
			case <-warned:
			case <-time.After(time.Second):
				t.Fatal("pending rendering loss was not reported without another producer Write")
			}
			// Stop must drain accepted frames and finish the consumer, including
			// when the last observed physical write was the pressure warning.
			stop()
			select {
			case <-d.done:
			case <-time.After(time.Second):
				t.Fatal("display did not finish draining")
			}
			raw := string(out.bytes())
			if strings.Count(raw, "Cursor pane output omitted under pressure") != 1 {
				t.Fatalf("loss warning count in %q", raw)
			}
			if pressure == "full queue before drain" {
				if strings.Count(raw, "queued;") != 64 || !strings.HasPrefix(raw, "blocked;") || strings.Contains(raw, "lost-final;") {
					t.Fatalf("accepted frames were lost or dropped frame rendered: %q", raw)
				}
			} else if strings.Contains(raw, "xxxx") {
				t.Fatal("oversized frame reached output")
			}
		})
	}
}
