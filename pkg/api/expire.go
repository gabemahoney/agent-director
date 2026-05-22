// Package api: Expire verb method.
//
// Logger contract: c.logger is passed verbatim to internal/api.Expire,
// which uses it for per-row deletion log lines. The Client method layer is
// logger-agnostic — it only owns the wire-through.

package api

import (
	"time"

	internalapi "github.com/gabemahoney/agent-director/internal/api"
)

// Expire removes terminal-state rows (ended/missing) whose ended_at is older
// than the retention window. olderThan overrides the config default when
// non-nil. Does NOT touch tmux sessions or JSONL transcripts.
func (c *Client) Expire(olderThan *time.Duration) (ExpireResult, error) {
	if err := c.checkClosed(); err != nil {
		return ExpireResult{}, err
	}
	return internalapi.Expire(c.st, c.cfg, olderThan, c.logger)
}
