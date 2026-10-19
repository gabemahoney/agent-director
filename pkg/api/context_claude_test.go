package api_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestClaudeContextOnDemandHistoryAndCapacity(t *testing.T) {
	t.Parallel()
	sample := contextfix.Must(t, "claude-parent")
	s, id, path := seedContext(t, sample)
	for _, stage := range []string{"captured", "compaction-gap", "new-response", "new-capacity", "malformed-tail", "partial-tail"} {
		current := sample
		want := sample.Expected
		switch stage {
		case "compaction-gap", "malformed-tail", "partial-tail":
			current = contextfix.ClaudeVariant(t, stage)
			want = json.RawMessage(contextfix.UnknownJSON)
		case "new-response", "new-capacity":
			current = contextfix.ClaudeVariant(t, "new-response")
			if stage == "new-response" {
				want = json.RawMessage(contextfix.UnknownJSON)
			} else {
				apitest.SeedClaudeCapacity(t, s.Path(), id, current)
			}
		}
		if err := os.WriteFile(path, current.Transcript, 0600); err != nil {
			t.Fatal(err)
		}
		for _, view := range []interface {
			api.StatusStore
			api.GetStore
		}{s, s.WithCursorMissingReads(nil)} {
			status, err := api.Status(view, id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, status.Context, want)
			row, err := api.Get(view, api.RelayView{}, id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, row.Context, want)
		}
	}
}

func TestClaudeVisibleChildUsesOwnTranscriptAndCapacity(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"current", "prior launch", "wrong session", "missing own transcript", "parent transcript substituted"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parent := contextfix.Must(t, "claude-parent")
			child := contextfix.Must(t, "claude-child")
			s, id, _ := seedContext(t, parent)
			var opts []apitest.SubagentOption
			if name == "prior launch" {
				opts = append(opts, apitest.WithSubagentLaunchToken("b2b2b2b2b2b2b2b2"))
			}
			if name == "wrong session" {
				opts = append(opts, apitest.WithSubagentSessionID("other-session"))
			}
			path := apitest.SeedContextChild(t, s.Path(), id, child, opts...)
			if name == "missing own transcript" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if name == "parent transcript substituted" {
				if err := os.WriteFile(path, parent.Transcript, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			result, err := api.ListSubagents(s, id)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Tracked || len(result.Subagents) != 1 {
				t.Fatalf("existing visibility changed: %+v", result)
			}
			want := json.RawMessage(contextfix.UnknownJSON)
			if name == "current" {
				want = child.Expected
			}
			assertContext(t, result.Subagents[0].Context, want)
			status, err := api.Status(s, id)
			if err != nil {
				t.Fatal(err)
			}
			assertContext(t, status.Context, parent.Expected)
			after, err := s.GetSpawn(id)
			if err != nil || after.Snapshot != before.Snapshot || after.State != store.StateWaiting {
				t.Fatalf("context read changed lifecycle: %+v %v", after, err)
			}
		})
	}
}
