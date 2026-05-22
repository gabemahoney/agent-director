package api

import internalapi "github.com/gabemahoney/agent-director/internal/api"

// MakeTemplate saves a reusable spawn preset to
// ~/.agent-director/templates/<name>.toml.
func (c *Client) MakeTemplate(params MakeTemplateParams) (MakeTemplateResult, error) {
	if err := c.checkClosed(); err != nil {
		return MakeTemplateResult{}, err
	}
	return internalapi.MakeTemplate(params)
}
