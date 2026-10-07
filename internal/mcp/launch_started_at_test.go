package mcp_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpVerbObject returns status/get's result for id, or id's row from list.
func mcpVerbObject(t *testing.T, d mcp.Dispatcher, verb, id string) map[string]json.RawMessage {
	t.Helper()
	if verb != "list" {
		return callToolText(t, d, verb, `{"claude_instance_id":"`+id+`"}`)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(callToolText(t, d, "list", `{}`)["spawns"], &rows); err != nil {
		t.Fatalf("parse list spawns: %v", err)
	}
	for _, row := range rows {
		if string(row["claude_instance_id"]) == `"`+id+`"` {
			return row
		}
	}
	t.Fatalf("list has no row %q", id)
	return nil
}

// TestLaunchStartedAtAndTmuxSocketMCP pins the status, get and list texts on
// MCP: launch_started_at only for a pending row (SR-22.2/SR-5.5), and
// tmux_socket, the row's recorded socket, on get only (SR-3.3/SR-16.1,
// AC-LKP-22; SR-5.1 "get also shows tmux_socket"). Every row shape is
// pkg/api's (TestLaunchStartedAtByVerbAndRow, TestTmuxSocketGetByRow,
// TestTmuxSocketStatusAndListOmit).
func TestLaunchStartedAtAndTmuxSocketMCP(t *testing.T) {
	e := newEnv(t)
	custom := filepath.Join(t.TempDir(), "tmux-custom", "default")
	for id, state := range map[string]string{"mls-pending": store.StatePending, "mls-waiting": store.StateWaiting} {
		e.seed(t, id, state, apitest.WithLaunchStartedAt(1790000000123), apitest.WithTmuxSocket(custom))
	}
	const launch = `"2026-09-21T14:13:20.123Z"`
	cases := []struct {
		id, verb, launchStartedAt, tmuxSocket string // "" = key absent
	}{
		{"mls-pending", "get", launch, `"` + custom + `"`},
		{"mls-pending", "status", launch, ""},
		{"mls-pending", "list", launch, ""},
		{"mls-waiting", "get", "", `"` + custom + `"`},
		{"mls-waiting", "status", "", ""},
		{"mls-waiting", "list", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.verb+"/"+tc.id, func(t *testing.T) {
			obj := mcpVerbObject(t, e.d, tc.verb, tc.id)
			for key, want := range map[string]string{"launch_started_at": tc.launchStartedAt, "tmux_socket": tc.tmuxSocket} {
				if got := string(obj[key]); got != want {
					t.Errorf("%s = %s; want %s (empty = key absent)", key, got, want)
				}
			}
		})
	}
}
