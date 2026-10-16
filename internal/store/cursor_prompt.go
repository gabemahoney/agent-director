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
	return s.checkCursorPrompt(b, until, check, "")
}

// CheckLoadedCursorPrompt permits the first real prompt while a confirmed
// latest load remains pending. Only this prompt path admits pending, and only
// for the exact saved transcript and current connection; all holds still apply.
func (s *Store) CheckLoadedCursorPrompt(b CursorRuntime, path string, until time.Time, check CursorCheck) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("store: loaded Cursor transcript required")
	}
	return s.checkCursorPrompt(b, until, check, path)
}

func (s *Store) checkCursorPrompt(b CursorRuntime, until time.Time, check CursorCheck, pendingPath string) (bool, error) {
	eligible := false
	err := s.inWriteTx(max(time.Until(until), 0), func(ctx context.Context, conn *sql.Conn) error {
		if _, err := s.cursorAuthorityForPrompt(ctx, conn, b, until, check, pendingPath); err != nil {
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
	if err != nil {
		return false, err
	}
	return eligible, nil
}
