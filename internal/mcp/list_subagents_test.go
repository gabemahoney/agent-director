package mcp_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// subagentKeys are the keys of every list_subagents record, sorted; all always present.
var subagentKeys = []string{
	"close_reason", "context", "description", "first_started_at", "instance_id", "lost_at", "model",
	"parent_id", "parent_type", "run_mode", "session_id", "started_at", "state",
	"stopped_at", "subagent_id", "subagent_type", "tool_use_id",
}

// listSubagentsRecords calls list_subagents for id and returns its tracked text
// and each record's raw fields, in order.
func listSubagentsRecords(t *testing.T, d mcp.Dispatcher, id string) (string, []map[string]json.RawMessage) {
	t.Helper()
	obj := callToolText(t, d, "list_subagents", paramJSON(t, map[string]any{"instance_id": id}))
	var recs []map[string]json.RawMessage
	if err := json.Unmarshal(obj["subagents"], &recs); err != nil {
		t.Fatalf("parse subagents %s: %v", obj["subagents"], err)
	}
	return string(obj["tracked"]), recs
}

// TestListSubagentsMCPResultShape: the tool's text carries tracked and every
// record with all sixteen keys, nulls explicit, times as pkg/api writes them
// (trailing zeros dropped, no fraction on a whole second), records ordered by
// first_started_at with unknown last.
func TestListSubagentsMCPResultShape(t *testing.T) {
	e := newEnv(t)
	e.seed(t, "mls-full", store.StateWaiting)
	for _, s := range []struct {
		id   string
		opts []apitest.SubagentOption
	}{
		{"sub-unknown", nil},
		{"sub-full", []apitest.SubagentOption{
			apitest.WithSubagentState("lost"), apitest.WithSubagentCloseReason("agent_gone"),
			apitest.WithSubagentSessionID("sess-1"), apitest.WithSubagentType("Explore"),
			apitest.WithSubagentDescription("scan the repo"), apitest.WithSubagentModel("opus"),
			apitest.WithSubagentParent("agent", "mls-full"), apitest.WithSubagentToolUseID("toolu_1"),
			apitest.WithSubagentRunMode("background"),
			apitest.WithSubagentFirstStartedAt(1790000000100), apitest.WithSubagentStartedAt(1790000000000),
			apitest.WithSubagentStoppedAt(1790000000450), apitest.WithSubagentLostAt(1790000000456),
		}},
	} {
		if err := apitest.SeedSubagent(e.storePath, "mls-full", s.id, s.opts...); err != nil {
			t.Fatalf("SeedSubagent(%s): %v", s.id, err)
		}
	}

	tracked, recs := listSubagentsRecords(t, e.d, "mls-full")
	if tracked != "true" {
		t.Errorf("tracked = %s; want true", tracked)
	}
	want := []map[string]string{
		{
			"subagent_id": `"sub-full"`, "instance_id": `"mls-full"`, "session_id": `"sess-1"`,
			"subagent_type": `"Explore"`, "description": `"scan the repo"`, "model": `"opus"`,
			"parent_type": `"agent"`, "parent_id": `"mls-full"`, "tool_use_id": `"toolu_1"`, "run_mode": `"background"`,
			"first_started_at": `"2026-09-21T14:13:20.1Z"`, "started_at": `"2026-09-21T14:13:20Z"`,
			"stopped_at": `"2026-09-21T14:13:20.45Z"`, "lost_at": `"2026-09-21T14:13:20.456Z"`,
			"state": `"lost"`, "close_reason": `"agent_gone"`,
		},
		{
			"subagent_id": `"sub-unknown"`, "instance_id": `"mls-full"`, "session_id": "null",
			"subagent_type": "null", "description": "null", "model": "null",
			"parent_type": "null", "parent_id": "null", "tool_use_id": "null", "run_mode": "null",
			"first_started_at": "null", "started_at": "null", "stopped_at": "null", "lost_at": "null",
			"state": `"running"`, "close_reason": "null",
		},
	}
	if len(recs) != len(want) {
		t.Fatalf("subagents = %d records; want %d", len(recs), len(want))
	}
	for i, rec := range recs {
		want[i]["context"] = string(contextfix.Must(t, "claude-missing-capacity").Expected)
		keys := make([]string, 0, len(rec))
		got := map[string]string{}
		for k, v := range rec {
			keys = append(keys, k)
			got[k] = string(v)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, subagentKeys) {
			t.Errorf("record %d keys = %v; want %v", i, keys, subagentKeys)
		}
		for k, w := range want[i] {
			if got[k] != w {
				t.Errorf("record %d %s = %s; want %s", i, k, got[k], w)
			}
		}
	}
}

// TestListSubagentsMCPTracked: a tracked row without records reads
// {"tracked":true,"subagents":[]}, a row launched before tracking
// or a Codex row {"tracked":false,"subagents":[]}; the list is never null.
func TestListSubagentsMCPTracked(t *testing.T) {
	e := newEnv(t)
	e.seed(t, "mls-tracked", store.StateWaiting)
	e.seed(t, "mls-untracked", store.StateWaiting, apitest.WithNoSubagentTracking())
	e.seed(t, "mls-codex", store.StateWaiting, apitest.WithAgent("codex"), apitest.WithNoSubagentTracking())
	for id, want := range map[string]string{"mls-tracked": "true", "mls-untracked": "false", "mls-codex": "false"} {
		t.Run(id, func(t *testing.T) {
			obj := callToolText(t, e.d, "list_subagents", paramJSON(t, map[string]any{"instance_id": id}))
			if got := string(obj["tracked"]); got != want {
				t.Errorf("tracked = %s; want %s", got, want)
			}
			if got := string(obj["subagents"]); got != "[]" {
				t.Errorf("subagents = %s; want []", got)
			}
		})
	}
}

// TestListSubagentsMCPRefusals: a missing or empty instance_id is refused as
// get refuses it (the empty id names no row), an unknown id is ErrSpawnNotFound.
func TestListSubagentsMCPRefusals(t *testing.T) {
	e := newEnv(t)
	for name, args := range map[string]string{
		"missing":    `{}`,
		"empty":      `{"instance_id":""}`,
		"unknown id": `{"instance_id":"mls-no-such-row"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := toolErrorData(t, callTool(t, e.d, "list_subagents", args))
			want := toolErrorData(t, callTool(t, e.d, "get", args))
			if got.ErrName != "ErrSpawnNotFound" {
				t.Errorf("err_name = %s: %q; want ErrSpawnNotFound", got.ErrName, got.ErrDescription)
			}
			if got.ErrName != want.ErrName || got.ErrDescription != want.ErrDescription {
				t.Errorf("list_subagents = %s: %q; get = %s: %q; want the same refusal",
					got.ErrName, got.ErrDescription, want.ErrName, want.ErrDescription)
			}
		})
	}
}
