// Package api: FindMissing verb method.
//
// Logger contract: c.logger is passed verbatim to internal/api.FindMissing,
// which uses it for the degraded-mode guard warning (0 probe IDs + ≥1 live
// rows). The Client method layer is logger-agnostic — it only owns the
// wire-through. CLI callers inject a recovery logger; MCP callers inject nil
// (discard); library callers set Options.Logger to their preferred sink.

package api

import (
	"context"

	internalapi "github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/probe"
)

// FindMissing reconciles DB state against live processes. ctx is the first
// argument per SRD §12 (preserving CLI ctx semantics).
func (c *Client) FindMissing(ctx context.Context) (FindMissingResult, error) {
	if err := c.checkClosed(); err != nil {
		return FindMissingResult{}, err
	}
	return internalapi.FindMissing(ctx, c.st, probe.New(), c.logger)
}
