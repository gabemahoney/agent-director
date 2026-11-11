package installsh_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// b.zia: a requested journal backup must never disappear without notice on
// a non-crossing install. Reuse the move fixture without changing its setup.
func TestUnusedJournalFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping installer regression in short mode")
	}
	for _, name := range []string{"fresh", "maintenance", "roots-complete", "backup", "legacy-root", "empty-backup"} {
		t.Run(name, func(t *testing.T) {
			h := newMoveHome(t)
			store := "there was no store before this install"
			if name != "fresh" {
				initial := h.Install()
				moveWant{}.Check(t, h, initial)
				if strings.Contains(initial.Stderr, "journal flags were not used") {
					t.Fatal("install without journal flags warned about unused flags")
				}
				version := h.migStoreVersion(filepath.Join(h.New, "state.db"))
				v, err := strconv.Atoi(version)
				if err != nil || v < 16 {
					t.Fatalf("fresh install's schema = %q, want v16 or later", version)
				}
				store = "state.db was at v" + version + " before this install"
			}
			backup := filepath.Join(h.Root, "unused-backup")
			flags := map[string][]string{
				"fresh":          {"--journal-maintenance-window", "--journal-roots-complete", "--journal-backup", backup, "--journal-legacy-root", "relative-unused-root"},
				"maintenance":    {"--journal-maintenance-window"},
				"roots-complete": {"--journal-roots-complete"},
				"backup":         {"--journal-backup", backup},
				"legacy-root":    {"--journal-legacy-root", "relative-unused-root"},
				"empty-backup":   {"--journal-backup", ""},
			}[name]
			res := h.Install(flags...)
			warning := fmt.Sprintf("install.sh: warning: %s; journal flags were not used and no journal backup was made.", store)
			moveWant{Stderr: []string{warning}}.Check(t, h, res)
			if !strings.HasSuffix(res.Stderr, warning+"\n") {
				t.Errorf("unused-flags warning must follow the completed install's other diagnostics:\n%s", res.Stderr)
			}
			if _, err := os.Stat(backup); !os.IsNotExist(err) {
				t.Errorf("unused backup directory exists or cannot be checked: %v", err)
			}
		})
	}
	t.Run("MCP-failed-install", func(t *testing.T) {
		h := newMoveHome(t)
		ownedClaude(t, h, "", "none-found-many-others")
		h.Env["CLAUDE_STANDIN_ADD_FAIL"] = "1"
		res := h.Install("--register-mcp", "--journal-maintenance-window")
		moveWant{Code: 6}.Check(t, h, res)
		warning := "install.sh: warning: there was no store before this install; journal flags were not used and no journal backup was made."
		if !strings.HasSuffix(res.Stderr, warning+"\n"+ownedFailLast+"\n") {
			t.Errorf("want the unused-flags warning before the final MCP exit-6 line:\n%s", res.Stderr)
		}
	})
	t.Run("refused-install", func(t *testing.T) {
		h := newMoveHome(t)
		h.Write(filepath.Join(h.New, "config.toml"), "[defaults]\nexpire_retention_days = -1\n", 0o600)
		res := h.Install("--journal-maintenance-window")
		moveWant{Code: 5, Err: "ErrConfigMalformed", NoStderr: []string{"journal flags were not used"}}.Check(t, h, res)
	})
}
