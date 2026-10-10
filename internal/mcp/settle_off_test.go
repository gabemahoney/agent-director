package mcp_test

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSettleOffMCP: with [state] settle_ms unset or 0, the status, get and
// list tools return exactly the CLI's settle-off goldens (b.3ie) as their
// text result, over the same fixed store.
func TestSettleOffMCP(t *testing.T) {
	cases := apitest.LoadReadSurfaceCases(t, filepath.Join("..", "..", "cmd", "agent-director", "testdata", "settle_off"))
	for _, v := range cases.Variants {
		t.Run(v.Name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "state.db")
			if _, err := apitest.SeedReadSurfaceStore(storePath); err != nil {
				t.Fatalf("SeedReadSurfaceStore: %v", err)
			}
			d := settleDispatcher(t, storePath, v.Config)
			for _, c := range cases.Cases {
				got := toolText(t, callTool(t, d, c.Verb, paramJSON(t, c.Params))) + "\n"
				if want := cases.Golden(t, c.Name); got != want {
					t.Errorf("%s: text result differs from its golden\n got: %s\nwant: %s", c.Name, got, want)
				}
			}
		})
	}
}
