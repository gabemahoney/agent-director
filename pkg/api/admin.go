package api

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/adminapi"
)

// init sets internal/adminapi's hooks to the operator-only actions this
// package keeps unexported (b.vqr): kill's finished-row opt-in
// (Client.killFinished) and delete (Client.deleteRows). Only
// agent-director-admin calls them.
func init() {
	adminapi.KillFinished = func(c any, claudeInstanceID string) (adminapi.KillResult, error) {
		client, err := adminClient(c)
		if err != nil {
			return adminapi.KillResult{}, err
		}
		res, err := client.killFinished(claudeInstanceID)
		return adminapi.KillResult{KillSent: res.KillSent}, err
	}
	adminapi.Delete = func(c any, claudeInstanceIDs []string) (adminapi.DeleteResult, error) {
		client, err := adminClient(c)
		if err != nil {
			// Results is never nil, as adminapi.DeleteResult documents (b.4nt).
			return adminapi.DeleteResult{Results: map[string]string{}}, err
		}
		return client.deleteRows(claudeInstanceIDs)
	}
}

// adminClient returns c as the *Client an adminapi hook runs on; any other
// value, a nil *Client included, is an error.
func adminClient(c any) (*Client, error) {
	client, ok := c.(*Client)
	if !ok || client == nil {
		return nil, fmt.Errorf("adminapi: want a non-nil *api.Client, got %T", c)
	}
	return client, nil
}
