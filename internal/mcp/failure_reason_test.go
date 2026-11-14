package mcp_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ivj D2/A2: tools/call retains explicit nulls and the Cursor diagnosis across all read builders.
func TestFailureReasonMCP(t *testing.T) {
	e := newEnv(t)
	const evidence = `{"v":"missing","life":0,"token":"1111111111111111","epoch":0,"at":1000,"src":"process","failure_reason":"runtime internal error"}`
	var ids []string
	wants := make(map[string]string)
	for _, tc := range []struct{ id, agent, evidence, want string }{
		{"failed", "cursor", evidence, `"runtime internal error"`},
		{"legacy", "cursor", "", "null"},
		{"claude", "claude", evidence, "null"},
		{"codex", "codex", evidence, "null"},
	} {
		e.seed(t, tc.id, store.StateMissing, apitest.WithAgent(tc.agent),
			apitest.WithLaunchIdentity(store.LaunchIdentity{Token: "1111111111111111"}),
			apitest.WithPermissionEpoch(0), apitest.WithStateEvidence(tc.evidence))
		ids = append(ids, tc.id)
		wants[tc.id] = tc.want
		for _, verb := range []string{"get", "status"} {
			row := callToolText(t, e.d, verb, paramJSON(t, map[string]any{"instance_id": tc.id}))
			if string(row["failure_reason"]) != tc.want {
				t.Fatalf("%s/%s failure_reason=%s; want %s", verb, tc.id, row["failure_reason"], tc.want)
			}
		}
	}
	for _, verb := range []string{"get_many", "list"} {
		params := map[string]any{}
		if verb == "get_many" {
			params["instance_ids"] = ids
		}
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(callToolText(t, e.d, verb, paramJSON(t, params))["spawns"], &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != len(ids) {
			t.Fatalf("%s returned %d rows", verb, len(rows))
		}
		for _, row := range rows {
			var id string
			if err := json.Unmarshal(row["instance_id"], &id); err != nil {
				t.Fatal(err)
			}
			raw, present := row["failure_reason"]
			if verb == "list" {
				if present {
					t.Fatal("list acquired failure_reason")
				}
			} else if !present || string(raw) != wants[id] {
				t.Fatalf("get-many/%s failure_reason=%s; want %s", id, raw, wants[id])
			}
		}
	}
}
