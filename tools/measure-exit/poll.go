package main

import (
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

// pollInterval is the SRD method's polling interval for both exit pollers
// (RN-6: the start-time reader; RN-2: the session listing).
const pollInterval = 100 * time.Millisecond

// clock is the time source of the pollers and the run log. time.Now's
// reading carries Go's monotonic clock, so elapsed times are immune to wall
// clock steps. Tests inject a virtual clock whose sleep advances its time.
type clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// realClock is the production clock.
type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }

// goneProbe answers one poll: whether the watched thing is gone. An error
// means the poll could not tell; the poller records it and never reads it as
// gone or present.
type goneProbe func() (gone bool, err error)

// outcome is how a sample ended.
type outcome string

const (
	// outcomeCompleted: the exit was observed; the sample has a time.
	outcomeCompleted outcome = "completed"
	// outcomeDidNotExit: the ceiling passed with the thing still present.
	outcomeDidNotExit outcome = "did_not_exit"
	// outcomeProbeError: the ceiling passed and the last poll could not
	// tell (an unreadable process, a failed listing).
	outcomeProbeError outcome = "probe_error"
	// outcomeNotReady: the agent did not report in (or start its turn)
	// within the ready timeout ("did not reach its prompt").
	outcomeNotReady outcome = "did_not_reach_prompt"
	// outcomeNoEndedAt: RN-2 only, the row never got ended_at (no
	// SessionEnd applied).
	outcomeNoEndedAt outcome = "no_ended_at"
	// outcomeFailed: the sample could not be set up or its action failed.
	outcomeFailed outcome = "failed"
)

// pollResult is one poller run.
type pollResult struct {
	Outcome outcome
	// Elapsed is from the caller's start instant to the poll that saw the
	// thing gone (or to the last poll, when it never went).
	Elapsed time.Duration
	// ObservedAt is that poll's instant (wall and monotonic).
	ObservedAt time.Time
	Polls      int
	// ProbeErrors counts polls that could not tell; LastError is the most
	// recent one's text.
	ProbeErrors int
	LastError   string
}

// pollUntilGone polls p every pollInterval, the first poll at once, until it
// answers gone or ceiling has passed since start. Polls that error are
// counted and polling continues; when the ceiling passes, the outcome is
// outcomeProbeError if the last poll errored, else outcomeDidNotExit.
func pollUntilGone(clk clock, start time.Time, ceiling time.Duration, p goneProbe) pollResult {
	var r pollResult
	lastErrored := false
	for {
		gone, err := p()
		now := clk.Now()
		r.Polls++
		r.ObservedAt = now
		r.Elapsed = now.Sub(start)
		lastErrored = err != nil
		if lastErrored {
			r.ProbeErrors++
			r.LastError = err.Error()
		} else if gone {
			r.Outcome = outcomeCompleted
			return r
		}
		if r.Elapsed >= ceiling {
			r.Outcome = outcomeDidNotExit
			if lastErrored {
				r.Outcome = outcomeProbeError
			}
			return r
		}
		clk.Sleep(pollInterval)
	}
}

// processGone is RN-6's probe: the agent process with this start time is
// gone. A zombie reads as gone (the start-time reader's contract), and so
// does the pid alive with another start time (the pid was reused). An
// unreadable process is an error, never gone.
func processGone(pc probe.ProcChecker, pid int, start string) goneProbe {
	return func() (bool, error) {
		got, alive, known := pc.StartTime(pid)
		switch {
		case !known:
			return false, fmt.Errorf("start time of pid %d unreadable", pid)
		case !alive:
			return true, nil
		default:
			return got != start, nil
		}
	}
}
