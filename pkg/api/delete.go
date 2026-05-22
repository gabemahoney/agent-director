package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Delete is admin batch removal by claude_instance_id. It bypasses all
// guards and never touches tmux sessions or JSONL transcripts. Per-row
// errors are recorded in DeleteResult.Results rather than aborting the batch.
func (c *Client) Delete(claudeInstanceIDs []string) (DeleteResult, error) {
	if err := c.checkClosed(); err != nil {
		return DeleteResult{}, err
	}
	return internalapi.Delete(c.st, claudeInstanceIDs)
}
