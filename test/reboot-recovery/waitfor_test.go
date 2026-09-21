package rebootrecovery_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// recordingTB is a minimal testing.TB stand-in that captures the first Fatalf
// message instead of aborting the outer test. Fatalf mirrors the real
// testing.T contract by calling runtime.Goexit, so callers must run it in its
// own goroutine (as the tests below do). Only the methods waitForObserved
// touches are implemented; the rest satisfy the interface but are unused.
type recordingTB struct {
	testing.TB
	fatal string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// runToTimeout drives waitForObserved with a never-true condition and a short
// budget in a goroutine, returning the Fatalf message it produced. The
// goroutine is required because Fatalf calls runtime.Goexit.
func runToTimeout(msg string, observe func() string) string {
	rec := &recordingTB{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForObserved(rec, 20*time.Millisecond, msg, func() bool { return false }, observe)
	}()
	<-done
	return rec.fatal
}

// TestWaitForObservedTimeoutDumpsObservedState anchors b.129: a waitFor timeout
// must report the state ACTUALLY observed at expiry, not only the wanted
// condition. Before the fix waitFor emitted only the "waiting for" message. The
// with-observer case asserts the injected observe() output rides along in the
// Fatalf; the nil-observer case proves the hook is optional and leaves no
// dangling "observed at expiry" clause.
func TestWaitForObservedTimeoutDumpsObservedState(t *testing.T) {
	t.Run("with observer", func(t *testing.T) {
		observed := "row abc: state=pending pid=NULL proc_starttime=NULL jsonl_path=NULL"
		got := runToTimeout("SessionStart persists identity", func() string { return observed })

		if !strings.Contains(got, "timed out after 20ms") {
			t.Errorf("timeout message missing budget; got %q", got)
		}
		if !strings.Contains(got, "waiting for: SessionStart persists identity") {
			t.Errorf("timeout message missing wanted condition; got %q", got)
		}
		if !strings.Contains(got, "observed at expiry: "+observed) {
			t.Errorf("timeout message missing observed-state dump; got %q", got)
		}
	})

	t.Run("nil observer omits dump", func(t *testing.T) {
		got := runToTimeout("some condition", nil)

		if !strings.Contains(got, "waiting for: some condition") {
			t.Errorf("timeout message missing wanted condition; got %q", got)
		}
		if strings.Contains(got, "observed at expiry") {
			t.Errorf("nil observer should not emit an observed clause; got %q", got)
		}
	})
}
