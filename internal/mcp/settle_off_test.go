package mcp_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			first := make(map[string]string)
			for _, c := range cases.Cases {
				from := time.Now()
				got := toolText(t, callTool(t, d, c.Verb, paramJSON(t, c.Params))) + "\n"
				got = apitest.NormalizeReadSurfaceProbes(t, got, from, time.Now(), first, c)
				if want := cases.Golden(t, c.Name); got != want {
					t.Errorf("%s: text result differs from its golden\n got: %s\nwant: %s", c.Name, got, want)
				}
			}
			var ids, goldens []string
			for _, c := range cases.Cases {
				if c.Verb == "get" {
					ids = append(ids, c.Params["instance_id"].(string))
					goldens = append(goldens, strings.TrimSuffix(cases.Golden(t, c.Name), "\n"))
				}
			}
			ids = append(ids, "get-many-unknown")
			from := time.Now()
			got := toolText(t, callTool(t, d, "get_many", paramJSON(t, map[string]any{"instance_ids": ids}))) + "\n"
			got = apitest.NormalizeReadSurfaceProbes(t, got, from, time.Now(), first, apitest.ReadSurfaceCase{Verb: "get-many"})
			want := `{"spawns":[` + strings.Join(goldens, ",") + `],"not_found":["get-many-unknown"]}` + "\n"
			if got != want {
				t.Fatalf("get-many differs from get goldens\ngot: %s\nwant: %s", got, want)
			}

		})
	}
}
