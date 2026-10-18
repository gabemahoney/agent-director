//go:build linux

package realtmux_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/helptext"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorGuidanceTwoStoresTwoSocketsAndOwnedMCPRetirement(t *testing.T) {
	if testing.Short() {
		t.Skip("real tmux composition")
	}
	plain, err := helptext.Plain()
	if err != nil {
		t.Fatal(err)
	}
	scenario := cursorfix.Scenario{SessionID: "guidance-owned", MCPExpectedInstructions: plain, MCPCalls: []map[string]any{{"name": "list", "arguments": map[string]any{}}}}
	first, firstRuntime, firstLaunch := newCursorPaneScenario(t, false, "", cursorPaneOptions{InjectHelp: true, Script: &scenario})
	second, secondRuntime, secondLaunch := newCursorPaneScenario(t, false, "", cursorPaneOptions{InjectHelp: true, Script: &scenario})
	type process struct {
		pid   int
		start string
	}
	owned := func(r cursorfix.Runtime, own, foreign string) process {
		var child process
		waitFor(t, "scoped MCP list reaches only intended store", func() bool {
			raw, err := os.ReadFile(r.MCPTrace)
			if err != nil {
				return false
			}
			seen := false
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				var event struct {
					Event    string          `json:"event"`
					PID      int             `json:"pid"`
					Response json.RawMessage `json:"response"`
				}
				if json.Unmarshal([]byte(line), &event) != nil {
					return false
				}
				if event.Event == "started" {
					child.pid = event.PID
				}
				if event.Event == "tool_response" {
					var reply struct {
						Result struct {
							IsError bool `json:"isError"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
					}
					if json.Unmarshal(event.Response, &reply) != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
						t.Fatalf("MCP list response: %s", event.Response)
					}
					text := reply.Result.Content[0].Text
					seen = strings.Contains(text, own) && !strings.Contains(text, foreign)
				}
			}
			return child.pid > 0 && seen
		}, func() string { raw, _ := os.ReadFile(r.MCPTrace); return string(raw) })
		var alive, known bool
		child.start, alive, known = probe.NewProcChecker().StartTime(child.pid)
		if !alive || !known {
			t.Fatal("MCP subprocess missing before retirement")
		}
		return child
	}
	firstMCP := owned(firstRuntime, firstLaunch.ID, secondLaunch.ID)
	secondMCP := owned(secondRuntime, secondLaunch.ID, firstLaunch.ID)
	if _, err := second.API.Pause(context.Background(), api.PauseParams{InstanceID: secondLaunch.ID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owned MCP actually reaped", func() bool { _, err := os.Stat(fmt.Sprintf("/proc/%d", secondMCP.pid)); return os.IsNotExist(err) }, func() string { return procState(secondMCP.pid) })
	start, alive, known := probe.NewProcChecker().StartTime(firstMCP.pid)
	if !known || !alive || start != firstMCP.start {
		t.Fatal("foreign MCP retired with peer")
	}
	if _, err := first.API.SendKeys(api.SendKeysParams{InstanceID: firstLaunch.ID, Text: "ordinary peer continuation"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "foreign conversation remains useful", func() bool {
		pane, err := first.API.ReadPane(api.ReadPaneParams{InstanceID: firstLaunch.ID, NLines: 100})
		return err == nil && strings.Contains(pane.Pane, "Synthetic reply: ordinary peer continuation")
	}, func() string { return fmt.Sprint(readRow(t, first.DBPath, firstLaunch.ID)) })
}
