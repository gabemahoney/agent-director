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

// nowFunc is the wall-clock seam Poll uses for its deadline math.
// Held as a package var so tests can inject a virtual clock that
// advances only when the fake sleeper is invoked — see
// internal/hook/export_test.go. Defaults to time.Now in production.
var nowFunc = time.Now

// pollMaxReadRetries is the upper bound on consecutive SQL read
// failures the polling loop tolerates before giving up. Each retry
// pays the floor sleep so a flapping DB doesn't burn CPU. Past the
// budget the loop fails closed (deny envelope).
const pollMaxReadRetries = 5

// PollResult captures the outcome of one polling-loop run. Decision
// is the empty string when the loop exited without a definitive
// answer (timeout / ctx / preemption / exhaustion); the handler
// translates that into a deny envelope at the boundary.
//
// CreatedAt is populated from the decided row's created_at column in
// the happy-path (Decision != ""). It is the zero time.Time on all
// timeout/error paths — runRelay does one additional GetPermissionRequest
// to recover it when needed.
type PollResult struct {
	Decision  string
	Reason    string
	Why       string    // human-readable reason for diagnostics; not echoed to Claude.
	CreatedAt time.Time // zero on timeout/error paths; non-zero on decided path.
}

// PollStore is the narrow surface the loop reads. *store.Store
// satisfies it via GetPermissionRequest.
type PollStore interface {
	GetPermissionRequest(instanceID, requestToken string) (store.PermissionRow, error)
}

// PollClock is the sleeper seam. Production uses time.NewTimer to
// honor ctx.Done; tests can inject a fast variant.
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

// Poll runs the SRD §6.2 relay polling loop. requestToken narrows the read to
// the specific permission_requests row minted for this relay invocation (the
// UUIDv4 token minted by mintRequestToken in runRelay). The pair
// (instanceID, requestToken) uniquely identifies the row per SRD §6.2.
//
// Behavior per iteration:
//
//  1. Read the permission_requests row by (instanceID, requestToken).
//     - sql.ErrNoRows → another hook event preempted; fail-closed deny.
//     - any other error → bounded retry (pollMaxReadRetries); if the
//       retry budget is exhausted, fail-closed deny.
//     - decision still NULL → sleep and loop.
//     - decision populated → return it.
//  2. The per-iteration sleep is `max(pollFloor, cfg.PollBaseMs + uniform(0, cfg.PollJitterMs))`.
//     ctx.Done preempts the sleep (the realPollClock uses a Timer +
//     select).
//  3. The overall loop is capped by cfg.TimeoutSeconds. On expiry the
//     loop returns a fail-closed deny without making one more poll
//     (the timeout boundary is the answer the operator agreed to).
//
// The polling loop NEVER writes to permission_requests — SRD §6.2
// invariant. Only decide() owns the decision columns.
func Poll(ctx context.Context, s PollStore, clock PollClock, cfg config.Relay, instanceID, requestToken string, rng *rand.Rand) PollResult {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		// Match config.Default().Relay.TimeoutSeconds (86400s / 1 day);
		// guard against a config that pinned 0 or negative. See b.p48.
		timeout = 86400 * time.Second
	}
	deadline := nowFunc().Add(timeout)

	readFails := 0
	for {
		// Honor ctx before each iteration so a fast cancel doesn't
		// pay a full sleep.
		if err := ctx.Err(); err != nil {
			return PollResult{Why: "ctx cancelled: " + err.Error()}
		}
		// `!Before` (i.e. now >= deadline) handles the virtual-clock
		// edge case where Sleep clamps sleep=remaining and leaves
		// nowFunc() == deadline exactly; `After` alone would spin in
		// that case (real wall-clock time would have stepped past in
		// production, masking the issue).
		if !nowFunc().Before(deadline) {
			return PollResult{Why: "polling timeout exceeded"}
		}

		row, err := s.GetPermissionRequest(instanceID, requestToken)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Row is gone — the (instanceID, requestToken) pair no
			// longer exists. This should be rare in v2 (rows are
			// INSERT-only and only removed by ON DELETE CASCADE), but
			// a cascade from a spawn delete is possible. Fail closed.
			return PollResult{Why: "row preempted (deleted) during poll"}
		case err != nil:
			readFails++
			if readFails > pollMaxReadRetries {
				return PollResult{Why: "exceeded read-retry budget: " + err.Error()}
			}
			clock.Sleep(ctx, pollFloor)
			continue
		}

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
		if remaining := deadline.Sub(nowFunc()); remaining < sleep {
			sleep = remaining
		}
		if sleep > 0 {
			clock.Sleep(ctx, sleep)
		}
	}
}
