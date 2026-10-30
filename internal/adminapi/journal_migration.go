package adminapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/gabemahoney/agent-director/internal/store"
)

type JournalMigrationOptions struct {
	MaintenanceWindow bool
	RootsComplete     bool
	LegacyRoots       []string
	BackupDir         string
}

// JournalProcess exposes only safe OS identity and the operator action. It
// never contains a process environment, arguments, command line or prompt.
type JournalProcess struct {
	PID       int    `json:"pid"`
	Starttime string `json:"starttime,omitempty"`
	Role      string `json:"role"`
	Action    string `json:"action"`
}

type JournalRefusal struct {
	Reason    string           `json:"reason"`
	Processes []JournalProcess `json:"processes"`
	Action    string           `json:"action"`
}

func (r *JournalRefusal) Error() string {
	return "journal migration refused: " + r.Reason + "; " + r.Action
}

// Is identifies the existing administrator-only refusal category. Details remain
// on the original error; the catalog needs no duplicate public API sentinel.
func (r *JournalRefusal) Is(target error) bool {
	_, ok := target.(*JournalRefusal)
	return ok
}

func journalRefusal(reason string, processes ...JournalProcess) error {
	return &JournalRefusal{Reason: reason, Processes: processes, Action: "keep the maintenance boundary, stop incompatible mutators, and restart administrator verification"}
}

// MigrateJournal never opens the public Client. Its ordinary open could consume
// an old sentinel before this operator-only exclusion and backup procedure.
func MigrateJournal(ctx context.Context, path string, options JournalMigrationOptions) (store.InputMigrationResult, error) {
	var result store.InputMigrationResult
	if !options.MaintenanceWindow {
		return result, journalRefusal("maintenance_boundary_required")
	}
	if !options.RootsComplete {
		return result, journalRefusal("legacy_roots_not_established")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return result, journalRefusal("invoking_home_unavailable")
	}
	roots := append([]string{filepath.Join(home, ".config", "agent-director", "input"), filepath.Join(home, ".agent-director", "input")}, options.LegacyRoots...)
	seen := make(map[string]bool)
	var unique []string
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return result, journalRefusal("legacy_root_not_absolute")
		}
		if !seen[root] {
			unique = append(unique, root)
			seen[root] = true
		}
	}
	sort.Strings(unique)
	if !filepath.IsAbs(options.BackupDir) {
		return result, journalRefusal("absolute_backup_required")
	}
	result, err = store.MigrateInputJournal(ctx, path, unique, options.BackupDir, verifyJournalQuiescence)
	if err != nil {
		var refusal *JournalRefusal
		if errors.As(err, &refusal) && result.Authority != "sqlite" {
			return result, refusal
		}
		// Operator-only failures retain a bounded, actionable category. File
		// contents and raw OS diagnostic strings are never copied to output.
		if result.Authority == "sqlite" {
			return result, journalRefusal("committed_verification_incomplete")
		}
		return result, journalRefusal("migration_incomplete_or_commit_unknown")
	}
	return result, nil
}
