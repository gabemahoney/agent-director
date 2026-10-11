package api_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestParamsStructsDecodeSnakeCaseJSON pins the MCP wire contract: each params
// struct the dispatcher decodes takes snake_case keys (an untagged
// InstanceID would leave {"instance_id":"x"} unset and turn
// "unknown id" into "missing id").
func TestParamsStructsDecodeSnakeCaseJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		into any // a pointer to the zero params struct
		want any
	}{
		{`{"instance_id":"id-1","text":"hello","allow_pending":true}`, &api.SendKeysParams{},
			&api.SendKeysParams{InstanceID: "id-1", Text: "hello", AllowPending: true}},
		{`{"instance_id":"id-2","n_lines":42,"ansi":true,"allow_pending":true}`, &api.ReadPaneParams{},
			&api.ReadPaneParams{InstanceID: "id-2", NLines: 42, ANSI: true, AllowPending: true}},
		{`{"instance_id":"id-3"}`, &api.KillParams{}, &api.KillParams{InstanceID: "id-3"}},
		{`{"instance_id":"id-4"}`, &api.PauseParams{}, &api.PauseParams{InstanceID: "id-4"}},
		{`{"instance_id":"id-5"}`, &api.ResumeParams{}, &api.ResumeParams{InstanceID: "id-5"}},
		{`{"instance_id":"id-6","decision":"allow","reason":"ok"}`, &api.DecideParams{},
			&api.DecideParams{InstanceID: "id-6", Decision: "allow", Reason: "ok"}},
	} {
		if err := json.Unmarshal([]byte(tc.in), tc.into); err != nil || !reflect.DeepEqual(tc.into, tc.want) {
			t.Errorf("%T from %s = %+v, %v; want %+v", tc.into, tc.in, tc.into, err, tc.want)
		}
	}
}
