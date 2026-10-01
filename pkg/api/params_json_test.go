package api_test

import (
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestParamsStructsDecodeSnakeCaseJSON pins the MCP-side wire contract:
// the verb params structs the MCP dispatcher unmarshals into must accept
// snake_case JSON keys. Without explicit json tags, Go's encoding/json
// uses case-insensitive struct-field-name matching, which means
// `{"claude_instance_id":"x"}` silently FAILS to populate the
// `ClaudeInstanceID` field — the field stays empty and the verb sees
// "missing id" instead of "unknown id". This test catches that
// regression for every params struct the dispatcher touches via the
// unmarshalSnake path.
func TestParamsStructsDecodeSnakeCaseJSON(t *testing.T) {
	t.Run("SendKeysParams", func(t *testing.T) {
		var p api.SendKeysParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-1","text":"hello","allow_pending":true}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-1" || p.Text != "hello" || !p.AllowPending {
			t.Errorf("decoded = %+v; want {ClaudeInstanceID:id-1, Text:hello, AllowPending:true}", p)
		}
	})

	t.Run("ReadPaneParams", func(t *testing.T) {
		var p api.ReadPaneParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-2","n_lines":42,"ansi":true,"allow_pending":true}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-2" || p.NLines != 42 || !p.ANSI || !p.AllowPending {
			t.Errorf("decoded = %+v; want {ClaudeInstanceID:id-2, NLines:42, ANSI:true, AllowPending:true}", p)
		}
	})

	t.Run("KillParams", func(t *testing.T) {
		var p api.KillParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-3"}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-3" {
			t.Errorf("decoded = %+v; want id-3", p)
		}
	})

	t.Run("PauseParams", func(t *testing.T) {
		var p api.PauseParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-4"}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-4" {
			t.Errorf("decoded = %+v; want id-4", p)
		}
	})

	t.Run("ResumeParams", func(t *testing.T) {
		var p api.ResumeParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-5"}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-5" {
			t.Errorf("decoded = %+v; want id-5", p)
		}
	})

	t.Run("DecideParams", func(t *testing.T) {
		var p api.DecideParams
		if err := json.Unmarshal([]byte(`{"claude_instance_id":"id-6","decision":"allow","reason":"ok"}`), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if p.ClaudeInstanceID != "id-6" || p.Decision != "allow" || p.Reason != "ok" {
			t.Errorf("decoded = %+v; want {ClaudeInstanceID:id-6, Decision:allow, Reason:ok}", p)
		}
	})
}

// TestKillParamsJSONNeverSetsIncludeFinished pins SR-6.8: no JSON key, in any
// spelling, sets the operator-only opt-in, and encoding never carries it.
func TestKillParamsJSONNeverSetsIncludeFinished(t *testing.T) {
	for _, key := range []string{"include-finished", "include_finished", "IncludeFinished", "includefinished"} {
		for _, withID := range []bool{false, true} {
			body, wantID := `{"`+key+`":true}`, ""
			if withID {
				body, wantID = `{"claude_instance_id":"id-k","`+key+`":true}`, "id-k"
			}
			t.Run(body, func(t *testing.T) {
				var p api.KillParams
				if err := json.Unmarshal([]byte(body), &p); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if p.IncludeFinished || p.ClaudeInstanceID != wantID {
					t.Errorf("SR-6.8: decoded %s = %+v; want IncludeFinished false and ClaudeInstanceID %q", body, p, wantID)
				}
			})
		}
	}

	on, err := json.Marshal(api.KillParams{ClaudeInstanceID: "id-k", IncludeFinished: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	off, _ := json.Marshal(api.KillParams{ClaudeInstanceID: "id-k"})
	if string(on) != string(off) {
		t.Errorf("SR-6.8: KillParams with the opt-in encodes as %s; want %s, as without it", on, off)
	}
	var back api.KillParams
	if err := json.Unmarshal(on, &back); err != nil || back.IncludeFinished {
		t.Errorf("SR-6.8: re-decoding %s = %+v (err %v); want IncludeFinished false", on, back, err)
	}
}
