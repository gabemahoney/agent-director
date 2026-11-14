package store

import (
	"context"
	"database/sql"
	"time"
)

// EndBusySendLaunchForTest records launchID's Launch End and its custody
// disposition (applyBusySendLaunchEnd) in one transaction, as Task 4 will.
func EndBusySendLaunchForTest(s *Store, launchID string, at time.Time) error {
	return s.inDurableWriteTxContext(context.Background(), DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		if err := busySendChange(ctx, conn, `UPDATE busy_send_launches SET ended_at_ms = ?, end_cause = 'process_exited',
		    revision = revision + 1 WHERE launch_id = ? AND ended_at_ms IS NULL`, at.UnixMilli(), launchID); err != nil {
			return err
		}
		return applyBusySendLaunchEnd(ctx, conn, s.StoreID(), launchID, at.UnixMilli())
	})
}

// ExitBusySendLaunchForTest lifts launchID's fences (applyBusySendLaunchExit)
// in one transaction, as exit evidence will.
func ExitBusySendLaunchForTest(s *Store, launchID string, at time.Time) error {
	return s.inDurableWriteTxContext(context.Background(), DefaultLockWait, func(ctx context.Context, conn *sql.Conn) error {
		return applyBusySendLaunchExit(ctx, conn, s.StoreID(), launchID, at.UnixMilli())
	})
}
