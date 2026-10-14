package mcp_test

import (
	"github.com/gabemahoney/agent-director/internal/mcp"
	"testing"
)

func TestCursorHostCannotBeDiscoveredOrDispatched(t *testing.T) {
	if mcp.ExposedVerb("cursor-host") {
		t.Fatal("internal cursor-host exposed")
	}
	d := mcp.NewLiveDispatcher(nil)
	for _, name := range []string{"cursor_host", "cursor-host"} {
		resp := callTool(t, d, name, `{"runtime":"must-not-run"}`)
		if resp == nil || resp.Error == nil {
			t.Fatalf("internal gateway dispatched: %+v", resp)
		}
	}
}
