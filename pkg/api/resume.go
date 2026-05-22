package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// Resume brings a terminated (ended/missing) Spawn back to life via
// `claude --resume`. The same claude_instance_id is preserved across
// the resurrection.
func (c *Client) Resume(params ResumeParams) (ResumeResult, error) {
	if err := c.checkClosed(); err != nil {
		return ResumeResult{}, err
	}
	return internalapi.Resume(c.st, c.tmuxClient, c.cfg, params)
}
