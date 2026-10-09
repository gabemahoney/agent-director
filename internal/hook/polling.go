package hook

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// pollFloor is the minimum per-iteration sleep. SRD §6.2: a
// misconfigured `poll_base_ms=0, poll_jitter_ms=0` must not pin the
// CPU; this floor guarantees we sleep at least 50ms per loop even on
// the most aggressive setting.
const pollFloor = 50 * time.Millisecond

// pollMaxReadRetries is the upper bound on consecutive SQL read
// failures the polling loop tolerates before giving up. Each retry
// pays the floor sleep so a flapping DB doesn't burn CPU. Past the
// budget the loop gives up, and the relay hook exits with no answer
// (b.146 rule 3).
const pollMaxReadRetries = 5

// PollResult captures the outcome of one polling-loop run. Decision
// is the empty string when the loop exited without a decision read:
// TimedOut is then true when it reached its deadline (the relay hook
// makes its timeout deny), and false when it gave up otherwise
// (ctx cancelled, the request gone, or the read-retry budget spent;
// the relay hook then exits with no answer).
//
// CreatedAt is the request's created_at from the last read that returned
// the row; the zero time.Time when no read did.
type PollResult struct {
	Decision  string
	Reason    string
	Why       string // human-readable reason for diagnostics; not echoed to Claude.
	TimedOut  bool
	CreatedAt time.Time
}

// PollStore is the narrow surface the loop reads. *store.Store
// satisfies it via GetPermissionRequest.
type PollStore interface {
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
}

// PollClock is the sleeper seam of the relay polling loop and of
// SessionStart's bounded wait for its launch's identity write (SR-22.9;
// HandleConfig.Clock, with HandleConfig.Now as the clock it advances).
// Production uses time.NewTimer to honor ctx.Done; tests can inject a fast
// variant.
type PollClock interface {
	Sleep(ctx context.Context, d time.Duration)
}

// realPollClock is the production sleeper. It uses time.NewTimer so
// ctx.Done preempts the sleep cleanly.
type realPollClock struct{}

func (realPollClock) Sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// DefaultPollClock returns the production sleeper. Tests inject their
// own.
func DefaultPollClock() PollClock { return realPollClock{} }

// Poll runs the SRD §6.2 relay polling loop until deadline on the clock now.
// requestToken narrows the read to the specific permission_requests row
// minted for this relay invocation (the UUIDv4 token minted by
// mintRequestToken in runRelay). The pair (instanceID, requestToken) uniquely
// identifies the row per SRD §6.2.
//
// Behavior per iteration:
//
//  1. Read the permission_requests row by (instanceID, requestToken).
//     - sql.ErrNoRows → the row was deleted (a cascade from a spawn delete);
//     give up.
//     - any other error → bounded retry (pollMaxReadRetries), then give up.
//     - decision still NULL → sleep and loop.
//     - decision populated → return it.
//  2. The per-iteration sleep is `max(pollFloor, cfg.PollBaseMs + uniform(0, cfg.PollJitterMs))`,
//     never past deadline. ctx.Done preempts the sleep (the realPollClock
//     uses a Timer + select).
//  3. At deadline the loop returns TimedOut without making one more poll.
//
// runRelay passes the relay hook's own deadline, counted from its start and
// the timeout spawn wrote for it (b.146 rule 4): its kill instant less the
// reserve its ack keeps less the lead its timeout deny may wait for the
// store's write lock.
//
// The polling loop NEVER writes to permission_requests — SRD §6.2
// invariant. decide() owns the verdict; the relay hook's ack and timeout
// deny are runRelay's.
func Poll(ctx context.Context, s PollStore, clock PollClock, now func() time.Time, deadline time.Time, cfg config.Relay, instanceID, requestToken string, rng *rand.Rand) PollResult {
	readFails := 0
	var createdAt time.Time
	for {
		// Honor ctx before each iteration so a fast cancel doesn't
		// pay a full sleep.
		if err := ctx.Err(); err != nil {
			return PollResult{Why: "ctx cancelled: " + err.Error(), CreatedAt: createdAt}
		}
		// `!Before` (i.e. now >= deadline) handles the virtual-clock
		// edge case where Sleep clamps sleep=remaining and leaves
		// now() == deadline exactly; `After` alone would spin in
		// that case (real wall-clock time would have stepped past in
		// production, masking the issue).
		if !now().Before(deadline) {
			return PollResult{Why: "polling timeout exceeded", TimedOut: true, CreatedAt: createdAt}
		}

		row, err := s.GetPermissionRequest(instanceID, requestToken)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Row is gone — the (instanceID, requestToken) pair no
			// longer exists. Rows are removed only by ON DELETE CASCADE
			// from a spawn delete.
			return PollResult{Why: "row preempted (deleted) during poll", CreatedAt: createdAt}
		case err != nil:
			readFails++
			if readFails > pollMaxReadRetries {
				return PollResult{Why: "exceeded read-retry budget: " + err.Error(), CreatedAt: createdAt}
			}
			clock.Sleep(ctx, pollFloor)
			continue
		}
		createdAt = row.CreatedAt

		if row.Decision != "" {
			return PollResult{
				Decision:  row.Decision,
				Reason:    row.DecisionReason,
				Why:       "decided",
				CreatedAt: row.CreatedAt,
			}
		}

		// Still pending → sleep and loop.
		readFails = 0
		sleep := time.Duration(cfg.PollBaseMs) * time.Millisecond
		if cfg.PollJitterMs > 0 {
			sleep += time.Duration(rng.Intn(cfg.PollJitterMs)) * time.Millisecond
		}
		if sleep < pollFloor {
			sleep = pollFloor
		}
		// Never sleep past the deadline.
		if remaining := deadline.Sub(now()); remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			clock.Sleep(ctx, sleep)
		}
	}
}
