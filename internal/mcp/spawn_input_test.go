package mcp_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestToolsCallSpawnRejectsControlCharacterID (SR-9.1, AC-SPN-02): a spawn
// whose id holds a control character is ErrInvalidFlags with the Client's
// description, which never echoes the id, and nothing is created. Each control
// character, and the reuse opt-in alongside, is pkg/api's
// TestSpawnRejectsControlCharacterInstanceID.
func TestToolsCallSpawnRejectsControlCharacterID(t *testing.T) {
	const id = "mcp-head\nmcp-tail"
	e, c := newReuseParamEnv(t)

	resp := callTool(t, e.d, "spawn", spawnArgs(t, t.TempDir(), c, map[string]any{"claude_instance_id": id}))

	data := toolErrorData(t, resp)
	if data.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", data.ErrName)
	}
	apitest.AssertDescription(t, data.ErrDescription, apitest.DescInstanceIDControlChar(id))
	for _, text := range []string{data.ErrDescription, resp.Error.Message} {
		if strings.Contains(text, "mcp-head") || strings.Contains(text, "mcp-tail") {
			t.Errorf("error text %q contains the instance id", text)
		}
	}
	assertNothingCreated(t, e.d, e.rec)
}
