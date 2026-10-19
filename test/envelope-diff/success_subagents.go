// success_subagents.go holds list-subagents' success cases (SR-15.5): a
// tracked row with a full record, a record of nulls and a record kept off
// the list, and an untracked row. Each pins its subagents list on both
// envelopes, so a field or a time encoding may not vanish from both sides.
package envelope_diff

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const listSubagentsID = "id-list-subagents-1"

// seedListSubagents seeds listSubagentsID with subagent tracking on and three
// stored entries: a full record whose four times take the three encodings
// (whole second, trailing zero dropped, full milliseconds), a confirmed record
// of nulls, and an unconfirmed observation that must stay off the list.
func seedListSubagents(t *testing.T) (string, map[string]any) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, listSubagentsID, store.StateWorking, "", "", "", true); err != nil {
		t.Fatalf("seedListSubagents: %v", err)
	}
	for _, sa := range []struct {
		id   string
		opts []apitest.SubagentOption
	}{
		{"sa-full", []apitest.SubagentOption{
			apitest.WithSubagentState("lost"),
			apitest.WithSubagentCloseReason("agent_gone"),
			apitest.WithSubagentSessionID("sess-1"),
			apitest.WithSubagentType("general-purpose"),
			apitest.WithSubagentDescription("find the bug"),
			apitest.WithSubagentModel("model-1"),
			apitest.WithSubagentParent("agent", listSubagentsID),
			apitest.WithSubagentToolUseID("toolu-1"),
			apitest.WithSubagentRunMode("background"),
			apitest.WithSubagentFirstStartedAt(1790000000000),
			apitest.WithSubagentStartedAt(1790000000120),
			apitest.WithSubagentStoppedAt(1790000000456),
			apitest.WithSubagentLostAt(1790000001007),
		}},
		{"sa-nulls", nil},
		{"sa-observed", []apitest.SubagentOption{apitest.WithSubagentConfirmed(false)}},
	} {
		if err := apitest.SeedSubagent(dbPath, listSubagentsID, sa.id, sa.opts...); err != nil {
			t.Fatalf("seedListSubagents: %s: %v", sa.id, err)
		}
	}
	return filepath.Dir(dbPath), map[string]any{"id": listSubagentsID}
}

// seedListSubagentsUntracked seeds listSubagentsID launched before subagent
// tracking: no records, and the list reads tracked false.
func seedListSubagentsUntracked(t *testing.T) (string, map[string]any) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, listSubagentsID, store.StateWorking, "", "", "", true,
		apitest.WithNoSubagentTracking()); err != nil {
		t.Fatalf("seedListSubagentsUntracked: %v", err)
	}
	return filepath.Dir(dbPath), map[string]any{"id": listSubagentsID}
}

func listSubagentsParams(ctx map[string]any) map[string]any {
	return map[string]any{"instance_id": ctx["id"]}
}

func listSubagentsArgv(ctx map[string]any) []string {
	return []string{"list-subagents", "--instance-id", ctx["id"].(string)}
}

func listSubagentsUnknownContext() map[string]any {
	var reading map[string]any
	if err := json.Unmarshal([]byte(contextfix.UnknownJSON), &reading); err != nil {
		panic(err)
	}
	return reading
}

// listSubagentsSuccessCases are appended to successCases. Order on the list is
// first_started_at ascending with nulls last, so sa-full precedes sa-nulls.
var listSubagentsSuccessCases = []successCase{

	// ── list-subagents ────────────────────────────────────────────────────
	{
		verb:    "list-subagents",
		seed:    seedListSubagents,
		params:  listSubagentsParams,
		cliArgv: listSubagentsArgv,
		want: map[string]any{
			"tracked": true,
			"subagents": []any{
				map[string]any{
					"subagent_id":      "sa-full",
					"context":          listSubagentsUnknownContext(),
					"instance_id":      listSubagentsID,
					"state":            "lost",
					"session_id":       "sess-1",
					"subagent_type":    "general-purpose",
					"description":      "find the bug",
					"model":            "model-1",
					"parent_type":      "agent",
					"parent_id":        listSubagentsID,
					"tool_use_id":      "toolu-1",
					"run_mode":         "background",
					"close_reason":     "agent_gone",
					"first_started_at": "2026-09-21T14:13:20Z",
					"started_at":       "2026-09-21T14:13:20.12Z",
					"stopped_at":       "2026-09-21T14:13:20.456Z",
					"lost_at":          "2026-09-21T14:13:21.007Z",
				},
				map[string]any{
					"subagent_id":      "sa-nulls",
					"context":          listSubagentsUnknownContext(),
					"instance_id":      listSubagentsID,
					"state":            "running",
					"session_id":       nil,
					"subagent_type":    nil,
					"description":      nil,
					"model":            nil,
					"parent_type":      nil,
					"parent_id":        nil,
					"tool_use_id":      nil,
					"run_mode":         nil,
					"close_reason":     nil,
					"first_started_at": nil,
					"started_at":       nil,
					"stopped_at":       nil,
					"lost_at":          nil,
				},
			},
		},
	},

	// ── list-subagents / untracked ────────────────────────────────────────
	// A row launched before subagent tracking: tracked false and an empty
	// (not null) list on both sides.
	{
		verb:    "list-subagents",
		name:    "untracked",
		seed:    seedListSubagentsUntracked,
		params:  listSubagentsParams,
		cliArgv: listSubagentsArgv,
		want:    map[string]any{"tracked": false, "subagents": []any{}},
	},
}
