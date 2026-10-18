package cursorhost

import (
	"context"
	"errors"
	"time"

	"github.com/gabemahoney/agent-director/internal/tmux"
)

type paneUncertain struct {
	cause       error
	interrupted bool
}

func (e *paneUncertain) Error() string { return "Cursor pane ownership evidence unavailable" }
func (e *paneUncertain) Unwrap() error { return e.cause }

// VerifyGuidanceOwnership bounds private guidance admission by its original
// setup deadline. verify must only read evidence: a late worker cannot admit,
// acknowledge or publish anything. A zero until permits one verification for a
// replacement server after publication, without granting another setup budget.
func VerifyGuidanceOwnership(ctx context.Context, until time.Time, verify func() error) error {
	if !until.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, until)
		defer cancel()
	}
	for {
		if !until.IsZero() && !time.Now().Before(until) {
			return context.DeadlineExceeded
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result := make(chan error, 1)
		go func() { result <- verify() }()
		var err error
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-result:
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !until.IsZero() && !time.Now().Before(until) {
			return context.DeadlineExceeded
		}
		var uncertain *paneUncertain
		if err == nil || until.IsZero() || !errors.As(err, &uncertain) || !uncertain.interrupted {
			return err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func interruptedPane(cause error) *paneUncertain {
	var call *tmux.CallError
	return &paneUncertain{cause: cause, interrupted: errors.As(cause, &call) && call.QueryInterrupted()}
}
