package mcp_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestMCPGetManyRefusalsAndLoadedCap(t *testing.T) {
	e := newEnv(t)
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = fmt.Sprintf("many-%02d", i)
	}
	for _, id := range ids[:2] {
		seedFinished(t, e, id, store.StateEnded)
	}
	for _, args := range []string{`{}`, `{"instance_ids":null}`, `{"instance_ids":[]}`, `{"instance_ids":["many-00",""]}`, `{"instance_ids":["many-00","many-00"]}`} {
		data := toolErrorData(t, callTool(t, e.d, "get_many", args))
		if data.ErrName != "ErrInvalidFlags" {
			t.Fatalf("%s: %+v", args, data)
		}
	}
	data := toolErrorData(t, callTool(t, e.d, "get_many", paramJSON(t, map[string]any{"instance_ids": ids})))
	if data.ErrName != "ErrTooManyIds" || !reflect.DeepEqual(data.ErrDetails, map[string]any{"id_cap": float64(20)}) {
		t.Fatalf("default cap: %+v", data)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, cap := range []int{2, 1} {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("[get_many]\nid_cap = %d\n", cap)), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := api.New(api.Options{StorePath: e.storePath, ConfigPath: path, TmuxClient: e.rec})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		d := mcp.NewLiveDispatcher(c)
		data := toolErrorData(t, callTool(t, d, "get_many", paramJSON(t, map[string]any{"instance_ids": ids[:cap+1]})))
		if data.ErrName != "ErrTooManyIds" || !reflect.DeepEqual(data.ErrDetails, map[string]any{"id_cap": float64(cap)}) {
			t.Fatalf("loaded cap %d: %+v", cap, data)
		}
		// d keeps its loaded cap after the rewrite (2 in the first iteration).
		// e.d's all-found call checks that not_found is [], never null.
		if err := os.WriteFile(path, []byte("[get_many]\nid_cap = 1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			d     mcp.Dispatcher
			ids   []string
			found int
		}{{e.d, ids[:2], 2}, {d, ids[:cap], cap}, {d, []string{"unknown"}, 0}} {
			text := toolText(t, callTool(t, tc.d, "get_many", paramJSON(t, map[string]any{"instance_ids": tc.ids})))
			var got api.GetManyResult
			if err := json.Unmarshal([]byte(text), &got); err != nil || len(got.Spawns) != tc.found || got.Spawns == nil || got.NotFound == nil || len(got.NotFound) != len(tc.ids)-tc.found {
				t.Fatalf("result=%s err=%v", text, err)
			}
		}
	}
}
