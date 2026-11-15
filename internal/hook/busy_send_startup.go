package hook

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

const (
	claudeStartupWakeBudget  = 5 * time.Second
	claudeStartupWatchSettle = 250 * time.Millisecond
	claudeStartupExitPoll    = 25 * time.Millisecond
)

// ClaudeStartupWake is the private SessionStart job, containing only its
// Launch binding and finite deadline, never a Message or permission answer.
// The executable adds its own hook pid and actual store path when detaching.
type ClaudeStartupWake struct {
	InstanceID, LaunchToken, SessionID string
	ParentPID                          int
	ParentStart                        string
	Deadline                           time.Time
}

// RunClaudeStartupWake waits only for the scheduling SessionStart's exit,
// then allows Claude to install watchPaths before making one touch. It is
// finite work already in flight, never idle polling or delivery authority.
// The detached executable supplies hookExited by observing reparenting;
// tests supply a barrier and a virtual PollClock/Now. Failure keeps custody
// in SQLite; the executable logs the error and exits without retrying.
func RunClaudeStartupWake(ctx context.Context, st HookStore, job ClaudeStartupWake, hookExited func() bool,
	clock PollClock, now func() time.Time, logger *log.Logger) error {
	if now == nil {
		now = time.Now
	}
	if clock == nil {
		clock = DefaultPollClock()
	}
	deadline := job.Deadline
	if cap := now().Add(claudeStartupWakeBudget); deadline.After(cap) {
		deadline = cap
	}
	// Use a duration so injected clocks need not share wall time with the
	// process, while store reads still get a real cancellation deadline.
	ctx, cancel := context.WithTimeout(ctx, max(0, deadline.Sub(now())))
	defer cancel()
	remaining := func() bool { return ctx.Err() == nil && now().Before(deadline) }
	if hookExited == nil {
		return fmt.Errorf("startup wake has no hook-exit observer")
	}
	for remaining() && !hookExited() {
		clock.Sleep(ctx, min(claudeStartupExitPoll, deadline.Sub(now())))
	}
	if !remaining() || deadline.Sub(now()) <= claudeStartupWatchSettle {
		return fmt.Errorf("startup wake budget exhausted waiting for SessionStart exit")
	}
	clock.Sleep(ctx, claudeStartupWatchSettle)
	if !remaining() {
		return fmt.Errorf("startup wake budget exhausted waiting for watch registration")
	}
	gate := store.HookGate{ParentPID: job.ParentPID, ParentStart: job.ParentStart}
	res := ClassifyResult{Agent: store.AgentClaude, EventName: "SessionStart", SessionID: job.SessionID}
	f, ok := claudeRouteFacts(ctx, st, job.InstanceID, gate, res, logger)
	if !ok || f.Destination.LaunchToken != job.LaunchToken || f.State == store.StateEnded || f.State == store.StateMissing {
		return nil // changed, ended or uncovered: this job no longer has a wake
	}
	if !remaining() {
		return fmt.Errorf("startup wake budget exhausted reading Launch facts")
	}
	if err := store.TouchBusySendWake(f.WakePath); err != nil {
		return fmt.Errorf("touch startup wake: %w", err)
	}
	return nil
}
