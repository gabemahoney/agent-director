package mcp_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestContextMCP(t *testing.T) {
	for _, name := range []string{"reviewer", "claude-parent", "claude-missing-capacity", "cursor-2026.10.01-e373342", "cursor-2026.10.12-d47ca18"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			sample := contextfix.Must(t, name)
			apitest.SeedContextSpawn(t, e.storePath, "context-row", false, sample)
			for _, verb := range []string{"status", "get"} {
				obj := callToolText(t, e.d, verb, paramJSON(t, map[string]any{"instance_id": "context-row"}))
				var got, want bytes.Buffer
				if err := json.Compact(&got, obj["context"]); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&want, sample.Expected); err != nil {
					t.Fatal(err)
				}
				if got.String() != want.String() {
					t.Errorf("%s context = %s; want %s", verb, &got, &want)
				}
			}
			if name == "claude-parent" {
				child := contextfix.Must(t, "claude-child")
				apitest.SeedContextChild(t, e.storePath, "context-row", child)
				obj := callToolText(t, e.d, "list-subagents", paramJSON(t, map[string]any{"instance_id": "context-row"}))
				var children []struct{ Context json.RawMessage }
				if err := json.Unmarshal(obj["subagents"], &children); err != nil {
					t.Fatal(err)
				}
				if len(children) != 1 {
					t.Fatalf("child visibility: %+v", obj)
				}
				var got, want bytes.Buffer
				if err := json.Compact(&got, children[0].Context); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&want, child.Expected); err != nil {
					t.Fatal(err)
				}
				if got.String() != want.String() {
					t.Errorf("child context=%s; want %s", &got, &want)
				}
			}
		})
	}
}
