package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CheckCursorPrompt verifies the current owned conversation and every durable
// dialog hold under the same bounded authority transaction as Cursor answers.
// Acknowledgement, a native claim and local request retirement do not prove a
// dialog gone. This grants no authority to replay an earlier submission.
func (s *Store) CheckCursorPrompt(b CursorRuntime, until time.Time, check CursorCheck) (bool, error) {
	eligible := false
	err := s.inWriteTx(max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		if _, err := s.cursorAuthority(ctx, conn, b, until, check); err != nil {
			return err
		}
		var held int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM permission_requests WHERE instance_id=? AND proven_gone_at IS NULL`, b.InstanceID).Scan(&held); err != nil {
			return err
		}
		if !time.Now().Before(until) {
			return fmt.Errorf("store: Cursor prompt verification expired")
		}
		eligible = held == 0
		return nil
	})
	return eligible && err == nil, err
}
