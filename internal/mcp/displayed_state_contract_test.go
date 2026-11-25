package mcp_test

import (
	"encoding/json"
	"github.com/gabemahoney/agent-director/pkg/api"
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

// b.915: both MCP spellings omit finished rows while retaining a live row.
func TestSweepChecksMCP(t *testing.T) {
	for _, verb := range []string{"sweep", "find_missing"} {
		t.Run(verb, func(t *testing.T) {
			for _, live := range []bool{false, true} {
				path := filepath.Join(t.TempDir(), "state.db")
				for _, state := range []string{"ended", "missing"} {
					if _, err := apitest.SeedSpawn(path, state, state, "/tmp", "off", "", true, apitest.WithNoPane()); err != nil {
						t.Fatal(err)
					}
				}
				wantChecks := 0
				if live {
					wantChecks = 1
					if _, err := apitest.SeedSpawn(path, "pending", "pending", "/tmp", "off", "", false, apitest.WithNoPane()); err != nil {
						t.Fatal(err)
					}
				}
				d := settleDispatcher(t, path, nil)
				raw := toolText(t, callTool(t, d, verb, `{}`))
				apitest.AssertConfirmationsShape(t, raw)
				var got api.SweepResult
				if err := json.Unmarshal([]byte(raw), &got); err != nil {
					t.Fatal(err)
				}
				if got.Checks == nil || len(got.Checks) != wantChecks || got.Count != 0 || got.Unverified != 0 || got.IDs == nil || len(got.IDs) != 0 || got.UnverifiedIDs == nil || len(got.UnverifiedIDs) != 0 {
					t.Fatalf("result = %s", raw)
				}
				if live {
					row := got.Checks[0]
					if row.InstanceID != "pending" || row.State != "pending" || row.Completion != "complete" || row.Reason != nil || row.Confirmations.ProcessLiveness.Status != "pending" {
						t.Errorf("live check = %+v", row)
					}
				}
			}
		})
	}
}
