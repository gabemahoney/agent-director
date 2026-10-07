package api_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestParamsStructsDecodeSnakeCaseJSON pins the MCP wire contract: each params
// struct the dispatcher decodes takes snake_case keys (an untagged
// ClaudeInstanceID would leave {"claude_instance_id":"x"} unset and turn
// "unknown id" into "missing id").
func TestParamsStructsDecodeSnakeCaseJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		into any // a pointer to the zero params struct
		want any
	}{
		{`{"claude_instance_id":"id-1","text":"hello","allow_pending":true}`, &api.SendKeysParams{},
			&api.SendKeysParams{ClaudeInstanceID: "id-1", Text: "hello", AllowPending: true}},
		{`{"claude_instance_id":"id-2","n_lines":42,"ansi":true,"allow_pending":true}`, &api.ReadPaneParams{},
			&api.ReadPaneParams{ClaudeInstanceID: "id-2", NLines: 42, ANSI: true, AllowPending: true}},
		{`{"claude_instance_id":"id-3"}`, &api.KillParams{}, &api.KillParams{ClaudeInstanceID: "id-3"}},
		{`{"claude_instance_id":"id-4"}`, &api.PauseParams{}, &api.PauseParams{ClaudeInstanceID: "id-4"}},
		{`{"claude_instance_id":"id-5"}`, &api.ResumeParams{}, &api.ResumeParams{ClaudeInstanceID: "id-5"}},
		{`{"claude_instance_id":"id-6","decision":"allow","reason":"ok"}`, &api.DecideParams{},
			&api.DecideParams{ClaudeInstanceID: "id-6", Decision: "allow", Reason: "ok"}},
	} {
		if err := json.Unmarshal([]byte(tc.in), tc.into); err != nil || !reflect.DeepEqual(tc.into, tc.want) {
			t.Errorf("%T from %s = %+v, %v; want %+v", tc.into, tc.in, tc.into, err, tc.want)
		}
	}
}
