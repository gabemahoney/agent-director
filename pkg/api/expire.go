package api

import (
	"sort"
	"time"
)

// ExpireStore is the narrow store surface Expire needs.
type ExpireStore interface {
	DeleteTerminalOlderThan(older time.Duration) (int, []string, error)
}

// ExpireResult is the typed return shape — count of rows removed plus
// the sorted IDs. The slice is non-nil so JSON encodes as `[]` even on
// a no-op sweep.
type ExpireResult struct {
	// Count is the number of terminal rows removed.
	Count int `json:"count"`
	// IDs is the sorted slice of instance ids that were removed. Always
	// non-nil — encodes as [] when no rows were expired.
	IDs []string `json:"ids"`
}

// ExpireLogger is the narrow log surface Expire writes per-row
// warnings to. *log.Logger satisfies it.
type ExpireLogger interface {
	Printf(format string, v ...any)
}

// Expire removes terminal-state rows older than the retention window
// (SRD §12). Behavior:
//
//   - olderThan nil → uses retentionDays to compute the cutoff
//     (interpreted as days; 0 means "reap all terminal rows").
//   - olderThan non-nil → exact duration. A zero or negative duration
//     reaps every terminal row, irrespective of ended_at.
//   - Per-row failures inside DeleteTerminalOlderThan are propagated
//     as the outer error; partial deletes are surfaced via the
//     IDs slice. (SQLite's RETURNING clause inside a DELETE is
//     atomic per row, so a partial state only matters under a
//     transient I/O failure mid-iteration.)
//
// Live-state rows are untouched.
func Expire(s ExpireStore, retentionDays int, olderThan *time.Duration, lg ExpireLogger) (ExpireResult, error) {
	d := time.Duration(retentionDays) * 24 * time.Hour
	if olderThan != nil {
		d = *olderThan
	}

	count, ids, err := s.DeleteTerminalOlderThan(d)
	if err != nil {
		if lg != nil {
			lg.Printf("expire: DeleteTerminalOlderThan: %v", err)
		}
		return ExpireResult{}, err
	}

	sort.Strings(ids)
	return ExpireResult{Count: count, IDs: ids}, nil
}

// Expire removes terminal-state rows (ended/missing) whose ended_at is older
// than the retention window. When olderThan is nil the window comes from
// defaults.expire_retention_days in config.toml; a non-nil value overrides it.
// Passing a zero or negative duration reaps every terminal row. Live-state rows
// and rows with NULL ended_at are never touched. Does not affect tmux sessions
// or JSONL transcripts.
//
// CLI: agent-director expire
//
// Errors: none (verb-level errors are reported per-row in ExpireResult.IDs).
//
// Nondeterminism: none.
func (c *Client) Expire(olderThan *time.Duration) (ExpireResult, error) {
	if err := c.checkClosed(); err != nil {
		return ExpireResult{}, err
	}
	return Expire(c.st, c.cfg.Defaults.ExpireRetentionDays, olderThan, c.logger)
}
