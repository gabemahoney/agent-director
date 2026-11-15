package main

import (
	"log"
	"sync"
	"testing"
	"time"
)

type startupDiagnosticWriter func([]byte) (int, error)

func (write startupDiagnosticWriter) Write(p []byte) (int, error) { return write(p) }

// CR1 / t2.xis.oo.67: the hard deadline and timer cleanup must remain
// independent of both a blocked diagnostic sink and a held logger mutex.
func TestClaudeStartupWakeDeadlineIgnoresBlockedDiagnostic(t *testing.T) {
	for _, heldMutex := range []bool{false, true} {
		name := "blocked sink"
		if heldMutex {
			name = "held logger mutex"
		}
		t.Run(name, func(t *testing.T) {
			release, entered := make(chan struct{}), make(chan struct{})
			var once sync.Once
			logger := log.New(startupDiagnosticWriter(func(p []byte) (int, error) {
				once.Do(func() { close(entered) })
				<-release
				return len(p), nil
			}), "", 0)
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatal("deadline callback or cleanup waited for the blocked diagnostic")
				}
			}
			var holderDone chan struct{}
			if heldMutex {
				holderDone = make(chan struct{})
				go func() { defer close(holderDone); logger.Print("another log call") }()
			}
			started, finished := make(chan struct{}), make(chan struct{})
			var stop func()
			t.Cleanup(func() {
				close(release)
				if stop != nil {
					stop()
					wait(finished)
				}
				if holderDone != nil {
					wait(holderDone)
				}
			})
			if heldMutex {
				wait(entered) // the earlier log call owns the mutex and is in Write
			}
			exit := make(chan int, 1)
			stop = armClaudeStartupWakeDeadline(time.Now().Add(500*time.Millisecond), func() {
				close(started)
				logger.Print("startup job deadline")
				close(finished)
			}, func(code int) { exit <- code })
			wait(started)
			if !heldMutex {
				wait(entered)
			}
			select {
			case code := <-exit:
				if code != hookExitCode {
					t.Errorf("deadline exit = %d; want %d", code, hookExitCode)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("hard deadline waited for diagnostic I/O or the logger mutex")
			}
			select {
			case <-finished:
				t.Fatal("diagnostic finished before its sink was released")
			default:
			}
			stopped := make(chan struct{})
			go func() { stop(); close(stopped) }()
			wait(stopped) // stop must not join a callback still blocked in Print
		})
	}
}
