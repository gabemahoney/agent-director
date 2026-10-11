package mcp_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// settleDispatcher is a live dispatcher over a Client on storePath with
// config (a whole config.toml; nil: no file).
func settleDispatcher(t *testing.T, storePath string, config *string) mcp.Dispatcher {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.SettleVariant{Config: config}.WriteConfig(t, cfgPath)
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, TmuxClient: tmuxfix.NewRecorder()})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client)
}

// TestSettleOnMCP: with the largest settle time, the status, get and list
// tools return each settle-off golden with only the state read as settled
// (b.3ie): no new field and no new state value; list filters on it.
func TestSettleOnMCP(t *testing.T) {
	cases := apitest.LoadReadSurfaceCases(t, filepath.Join("..", "..", "cmd", "agent-director", "testdata", "settle_off"))
	storePath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedReadSurfaceStore(storePath); err != nil {
		t.Fatalf("SeedReadSurfaceStore: %v", err)
	}
	d := settleDispatcher(t, storePath, &apitest.SettleOnConfig)
	for _, c := range cases.Cases {
		cases.CheckSettleOn(t, c, toolText(t, callTool(t, d, c.Verb, paramJSON(t, c.Params))))
	}
}

// TestSettleListStateMCP: the list tool's state filter matches the settled
// state with a settle time, ahead of its limit, and the raw state with none,
// as the CLI's list --state does (b.3ie).
func TestSettleListStateMCP(t *testing.T) {
	for _, settled := range []bool{false, true} {
		t.Run(fmt.Sprintf("settle time %v", settled), func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "state.db")
			rows, err := apitest.SeedSettleFilterRows(storePath, time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			var cfg *string
			if settled {
				cfg = &apitest.SettleOnConfig
			}
			d := settleDispatcher(t, storePath, cfg)
			for _, tc := range []struct {
				state string
				limit int
			}{{store.StateWaiting, 0}, {store.StateWorking, 0}, {store.StateWaiting, 1}, {store.StateWorking, 1}} {
				params := map[string]any{"state": []string{tc.state}}
				if tc.limit > 0 {
					params["limit"] = tc.limit
				}
				var got []struct {
					ID string `json:"instance_id"`
				}
				if err := json.Unmarshal(callToolText(t, d, "list", paramJSON(t, params))["spawns"], &got); err != nil {
					t.Fatalf("parse list spawns: %v", err)
				}
				ids := []string{}
				for _, r := range got {
					ids = append(ids, r.ID)
				}
				if want := apitest.SettleFilterIDs(rows, settled, tc.limit, tc.state); !slices.Equal(ids, want) {
					t.Errorf("list %v = %v; want %v", params, ids, want)
				}
			}
		})
	}
}
