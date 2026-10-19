package main_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestContextCLI(t *testing.T) {
	for _, name := range []string{"writer", "claude-parent", "claude-missing-capacity", "cursor-2026.10.01-e373342", "cursor-2026.10.12-d47ca18"} {
		t.Run(name, func(t *testing.T) {
			sample := contextfix.Must(t, name)
			home := t.TempDir()
			id, _ := apitest.SeedContextSpawn(t, stateDB(home), "", true, sample)
			for _, verb := range []string{"status", "get"} {
				stdout, stderr, code := runCLIWithHome(t, home, verb, "--instance-id", id)
				if code != 0 || stderr != "" {
					t.Fatalf("%s: %d %s", verb, code, stderr)
				}
				var obj map[string]json.RawMessage
				if err := json.Unmarshal([]byte(stdout), &obj); err != nil {
					t.Fatal(err)
				}
				var got, want bytes.Buffer
				if err := json.Compact(&got, obj["context"]); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&want, sample.Expected); err != nil {
					t.Fatal(err)
				}
				if got.String() != want.String() {
					t.Errorf("%s context = %s; want %s", verb, &got, &want)
				}
			}
			if name == "claude-parent" {
				child := contextfix.Must(t, "claude-child")
				apitest.SeedContextChild(t, stateDB(home), id, child)
				stdout, stderr, code := runCLIWithHome(t, home, "list-subagents", "--instance-id", id)
				if code != 0 || stderr != "" {
					t.Fatalf("list-subagents: %d %s", code, stderr)
				}
				var result struct {
					Tracked   bool
					Subagents []struct{ Context json.RawMessage }
				}
				if err := json.Unmarshal([]byte(stdout), &result); err != nil {
					t.Fatal(err)
				}
				if !result.Tracked || len(result.Subagents) != 1 {
					t.Fatalf("child visibility: %s", stdout)
				}
				var got, want bytes.Buffer
				if err := json.Compact(&got, result.Subagents[0].Context); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&want, child.Expected); err != nil {
					t.Fatal(err)
				}
				if got.String() != want.String() {
					t.Errorf("child context=%s; want %s", &got, &want)
				}
			}
		})
	}
}
