package api_test

// readpane_ceiling_test.go proves read-pane's SR-13.2 ceiling in virtual time
// (the Recorder charges every call its full timeout; SR-7.5): the longest
// path is 3Q + A, a capture timeout 2Q + A. Nothing waits in real time.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestReadPaneCeilingVirtualTime: a failed capture whose follow-up finds Ours
// charges 3Q + A in four calls, a capture timeout 2Q + A in three, at the
// defaults and with Q raised above 2A.
func TestReadPaneCeilingVirtualTime(t *testing.T) {
	t.Parallel()
	q, a, _ := ceilDefaults()
	if got := 3*q + a; got != 6500*time.Millisecond {
		t.Fatalf("3Q + A at the defaults = %v; SR-7.5 says 6.5 s", got)
	}
	raised := 2*a + q // above 2A and above the default Q
	paths := []struct {
		name    string
		failure tmux.Failure
		calls   []tmux.Call
		want    func(q time.Duration) time.Duration
		desc    apitest.DescCase
	}{
		{"capture fails, follow-up finds Ours", tmux.FailUnrecognized, withFollowUp(paneReadCalls),
			func(q time.Duration) time.Duration { return 3*q + a }, apitest.DescUnrecognisedReply(tmux.CallCapture, "")},
		{"capture times out", tmux.FailTimeout, paneReadCalls,
			func(q time.Duration) time.Duration { return 2*q + a }, apitest.DescCallTimeout(tmux.CallCapture, a)},
	}
	for _, qc := range []struct {
		name string
		q    time.Duration
	}{{"default Q", q}, {"Q above 2A", raised}} {
		for _, pc := range paths {
			t.Run(qc.name+"/"+pc.name, func(t *testing.T) {
				e := newKillEnv(t)
				e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: qc.q})
				r := e.seedRow(t, killRowSpec{})
				e.rec.Script(r.Socket, tmuxfix.Script{Failure: pc.failure}, tmux.CallCapture)
				start := e.clock.Now()
				res, err := e.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: r.ID})
				if elapsed, want := e.clock.Now().Sub(start), pc.want(qc.q); elapsed != want {
					t.Errorf("virtual time = %v; want %v", elapsed, want)
				}
				if !errors.Is(err, api.ErrTmuxUnresponsive) {
					t.Fatalf("ReadPane = %+v, %v; want ErrTmuxUnresponsive", res, err)
				}
				apitest.AssertDescription(t, err.Error(), pc.desc)
				e.assertPaneCalls(t, pc.calls...)
				e.assertCaptured(t, r.Spawn.Identity.PaneID, api.DefaultReadPaneLines, false)
			})
		}
	}
}
