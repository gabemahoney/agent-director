package mcp_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
)

func TestInternalVerbsCannotBeDispatched(t *testing.T) {
	for _, verb := range []string{"cursor-host", "tool-guard"} {
		t.Run(verb, func(t *testing.T) {
			if mcp.ExposedVerb(verb) {
				t.Fatalf("internal verb %s exposed", verb)
			}
			// No client is available: internal routes must refuse before execution.
			d := mcp.NewLiveDispatcher(nil)
			for _, name := range []string{verb, strings.ReplaceAll(verb, "-", "_")} {
				t.Run(name, func(t *testing.T) {
					resp := callTool(t, d, name, `{}`)
					data := toolErrorData(t, resp)
					want := "ErrUnknownTool: " + name
					if resp.Error.Code != -32000 || resp.Error.Message != want ||
						data.ErrName != "ErrUnknownTool" || data.ErrDescription != want ||
						data.ErrDetails != nil || resp.Result != nil {
						t.Errorf("internal verb response = %+v, data=%+v; want only unknown-tool refusal %q", resp, data, want)
					}
				})
			}
		})
	}
}
