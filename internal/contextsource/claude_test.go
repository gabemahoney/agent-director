package contextsource_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gabemahoney/agent-director/internal/contextsource"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestClaudeCapturedUsage(t *testing.T) {
	for _, name := range []string{"claude-parent", "claude-child", "last-ordinary-iteration", "post-compaction", "zero-usage"} {
		t.Run(name, func(t *testing.T) {
			var s contextfix.Sample
			if name == "claude-parent" || name == "claude-child" {
				s = contextfix.Must(t, name)
			} else {
				s = contextfix.ClaudeVariant(t, name)
			}
			path := apitest.SeedContextTranscript(t, s)
			got, ok := contextsource.Claude(s.SessionID, s.ChildID, path)
			var expected struct {
				Used int64  `json:"used_tokens"`
				At   string `json:"as_of"`
			}
			if err := json.Unmarshal(s.Expected, &expected); err != nil {
				t.Fatal(err)
			}
			if !ok || got.Tokens != expected.Used || got.At.Format("2006-01-02T15:04:05.999999999Z07:00") != expected.At || got.UUID != s.ResponseUUID {
				t.Fatalf("usage=%+v valid=%v; want %s uuid=%s", got, ok, s.Expected, s.ResponseUUID)
			}
		})
	}
}

func TestClaudeUnavailableTranscript(t *testing.T) {
	for _, name := range []string{"relative", "missing", "directory", "fifo", "symlink"} {
		t.Run(name, func(t *testing.T) {
			s := contextfix.Must(t, "claude-parent")
			path := apitest.SeedContextTranscript(t, s)
			switch name {
			case "relative":
				path = "relative.jsonl"
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				path = filepath.Dir(path)
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := path
				path = filepath.Join(t.TempDir(), "link.jsonl")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if got, ok := contextsource.Claude(s.SessionID, "", path); ok {
				t.Fatalf("unsafe/unavailable transcript accepted: %+v", got)
			}
		})
	}
}

func TestClaudeTranscriptSizeBoundaries(t *testing.T) {
	for _, boundary := range []string{"line", "file"} {
		for _, offset := range []int{-1, 1} {
			t.Run(fmt.Sprintf("%s%+d", boundary, offset), func(t *testing.T) {
				sample := contextfix.Padded(t, "claude-parent", boundary, offset)
				got, ok := contextsource.Claude(sample.SessionID, "", apitest.SeedContextTranscript(t, sample))
				if offset > 0 {
					if ok {
						t.Fatalf("oversized %s accepted: %+v", boundary, got)
					}
					return
				}
				var expected struct {
					Used int64  `json:"used_tokens"`
					At   string `json:"as_of"`
				}
				if err := json.Unmarshal(sample.Expected, &expected); err != nil {
					t.Fatal(err)
				}
				if !ok || got.Tokens != expected.Used || got.UUID != sample.ResponseUUID || got.At.Format("2006-01-02T15:04:05.999999999Z07:00") != expected.At {
					t.Fatalf("padded capture usage=%+v valid=%v; want %s uuid=%s", got, ok, sample.Expected, sample.ResponseUUID)
				}
			})
		}
	}
}

func TestClaudeUnreliableUsage(t *testing.T) {
	for _, name := range []string{"wrong-session", "wrong-child", "untested-version", "missing-model", "missing-usage", "missing-time", "regressing-time", "missing-category", "negative-usage", "overflow", "fractional-usage", "branch", "missing-uuid", "invalid-ordinary-iteration", "compaction-gap", "malformed-tail", "partial-tail", "unterminated"} {
		t.Run(name, func(t *testing.T) {
			s := contextfix.ClaudeVariant(t, name)
			if got, ok := contextsource.Claude(s.SessionID, s.ChildID, apitest.SeedContextTranscript(t, s)); ok {
				t.Fatalf("unreliable usage accepted: %+v", got)
			}
		})
	}
}
