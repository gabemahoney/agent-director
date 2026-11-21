package qualification

import (
	"context"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type commandResult struct {
	command runner.Command
	err     error
}

type commandCall struct {
	cancel context.CancelFunc
	done   chan commandResult
	joined bool
}

func startCommand(ctx context.Context, run func(context.Context) commandResult) *commandCall {
	ctx, cancel := context.WithCancel(ctx)
	c := &commandCall{cancel: cancel, done: make(chan commandResult, 1)}
	go func() {
		// Also publish a failure if a test seam exits its goroutine with Goexit.
		v := commandResult{err: errors.New("background command ended without a result")}
		defer func() { c.done <- v }()
		v = run(ctx)
	}()
	return c
}

func (c *commandCall) wait(ctx context.Context) commandResult {
	// Prefer a result already available even when cancellation arrived with it.
	select {
	case v := <-c.done:
		c.joined = true
		return v
	default:
	}
	select {
	case v := <-c.done:
		c.joined = true
		return v
	case <-ctx.Done():
		return commandResult{err: ctx.Err()}
	}
}

func (c *commandCall) stop(ctx context.Context) error {
	c.cancel()
	if c.joined {
		return nil
	}
	// An early caller error must not wait out the phase's remaining minutes.
	// A broken Execute seam must not turn cancellation into an unbounded join.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return c.wait(ctx).err
}
