package mcp_test

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestDisplayedStateContractMCP: b.zpv regression. The status, get and list
// tools keep apitest.CheckDisplayedStateContract: state is the real state,
// an idle_screen row's Displayed State is only in displayed_state, and the
// list state filter matches the real or the displayed state, before limit.
func TestDisplayedStateContractMCP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := apitest.SeedDisplayedStateContract(path); err != nil {
		t.Fatal(err)
	}
	d := settleDispatcher(t, path, &apitest.SettleOnConfig)
	apitest.CheckDisplayedStateContract(t, apitest.DisplayedStateSurface{
		Name: "mcp",
		Read: func(verb, id string) []byte {
			return []byte(toolText(t, callTool(t, d, verb, paramJSON(t, map[string]any{"instance_id": id}))))
		},
		List: func(states []string, limit int) []byte {
			args := map[string]any{}
			if len(states) > 0 {
				args["state"] = states
			}
			if limit > 0 {
				args["limit"] = limit
			}
			return []byte(toolText(t, callTool(t, d, "list", paramJSON(t, args))))
		},
	})
}
