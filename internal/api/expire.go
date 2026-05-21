package api

import (
	"sort"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// ExpireStore is the narrow store surface Expire needs.
type ExpireStore interface {
	DeleteTerminalOlderThan(older time.Duration) (int, []string, error)
}

// ExpireResult is the typed return shape — count of rows removed plus
// the sorted IDs. The slice is non-nil so JSON encodes as `[]` even on
// a no-op sweep.
type ExpireResult struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// ExpireLogger is the narrow log surface Expire writes per-row
// warnings to. *log.Logger satisfies it.
type ExpireLogger interface {
	Printf(format string, v ...any)
}

// Expire removes terminal-state rows older than the retention window
// (SRD §12). Behavior:
//
//   - olderThan nil → uses cfg.Defaults.ExpireRetentionDays
//     (interpreted as days).
//   - olderThan non-nil → exact duration. A zero or negative duration
//     reaps every terminal row, irrespective of ended_at.
//   - Per-row failures inside DeleteTerminalOlderThan are propagated
//     as the outer error; partial deletes are surfaced via the
//     IDs slice. (SQLite's RETURNING clause inside a DELETE is
//     atomic per row, so a partial state only matters under a
//     transient I/O failure mid-iteration.)
//
// Live-state rows are untouched.
func Expire(s ExpireStore, cfg config.Config, olderThan *time.Duration, lg ExpireLogger) (ExpireResult, error) {
	d := time.Duration(cfg.Defaults.ExpireRetentionDays) * 24 * time.Hour
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
