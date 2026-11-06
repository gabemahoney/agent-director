package adminapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// defaultJournalAction is the action of every reason journalActions lacks.
const defaultJournalAction = "keep the maintenance boundary, stop incompatible mutators, and restart administrator verification"

// journalActions gives a reason its own action. Each names the administrator
// flag with install.sh's spelling beside it, because install.sh relays the
// refusal unchanged and a user follows it from either tool.
var journalActions = map[string]string{
	"maintenance_boundary_required": "establish the maintenance window (stop old mutators and prevent new old-binary invocations through committed verification), then pass --maintenance-window (install.sh: --journal-maintenance-window)",
	"legacy_roots_not_established":  "declare every additional legacy input root with --legacy-root, then pass --roots-complete (install.sh: --journal-legacy-root, --journal-roots-complete)",
	"invoking_home_unavailable":     "run it with HOME set to the invoking user's absolute home directory",
	"legacy_root_not_absolute":      "give every --legacy-root (install.sh: --journal-legacy-root) as an absolute path",
	"legacy_root_not_clean":         "give every --legacy-root (install.sh: --journal-legacy-root) in clean form, with no trailing /, no repeated / and no . or .. element",
	"absolute_backup_required":      "pass --backup-dir (install.sh: --journal-backup) a new absolute directory outside every legacy input root",
	"backup_inside_legacy_root":     "choose a --backup-dir (install.sh: --journal-backup) outside every legacy input root",
	"backup_exists":                 "choose a --backup-dir (install.sh: --journal-backup) that does not exist yet; keep the existing one",
	"backup_unreadable":             "choose a --backup-dir (install.sh: --journal-backup) whose parent directories exist and can be searched",
}

func journalRefusal(reason string, processes ...JournalProcess) error {
	action, ok := journalActions[reason]
	if !ok {
		action = defaultJournalAction
	}
	// Callers read processes as an array, as install.sh's own envelope writes it.
	if processes == nil {
		processes = []JournalProcess{}
	}
	return &JournalRefusal{Reason: reason, Processes: processes, Action: action}
}

// CheckJournalMigration makes only migrate-journal's argument checks, the
// ones MigrateJournal makes first. It opens no store, takes no lock and
// writes nothing, so install.sh can refuse bad journal flags before its
// binary swap.
func CheckJournalMigration(options JournalMigrationOptions) error {
	_, err := journalArguments(options)
	return err
}

// journalArguments is the one implementation of the argument checks. It
// returns the complete legacy roots, invoking HOME's included, deduplicated
// and sorted.
func journalArguments(options JournalMigrationOptions) ([]string, error) {
	if !options.MaintenanceWindow {
		return nil, journalRefusal("maintenance_boundary_required")
	}
	if !options.RootsComplete {
		return nil, journalRefusal("legacy_roots_not_established")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil, journalRefusal("invoking_home_unavailable")
	}
	roots := append([]string{filepath.Join(home, ".config", "agent-director", "input"), filepath.Join(home, ".agent-director", "input")}, options.LegacyRoots...)
	seen := make(map[string]bool)
	var unique []string
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, journalRefusal("legacy_root_not_absolute")
		}
		if filepath.Clean(root) != root {
			return nil, journalRefusal("legacy_root_not_clean")
		}
		if !seen[root] {
			unique = append(unique, root)
			seen[root] = true
		}
	}
	sort.Strings(unique)
	if !filepath.IsAbs(options.BackupDir) {
		return nil, journalRefusal("absolute_backup_required")
	}
	if backupInsideRoot(filepath.Clean(options.BackupDir), unique) {
		return nil, journalRefusal("backup_inside_legacy_root")
	}
	// A directory made after this check fails the backup's exclusive mkdir.
	if _, err := os.Lstat(options.BackupDir); err == nil {
		return nil, journalRefusal("backup_exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, journalRefusal("backup_unreadable")
	}
	return unique, nil
}

// backupInsideRoot reports whether the clean absolute backup is a root or
// lies under one, compared lexically.
func backupInsideRoot(backup string, roots []string) bool {
	for _, root := range roots {
		if backup == root || strings.HasPrefix(backup, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// MigrateJournal never opens the public Client. Its ordinary open could consume
// an old sentinel before this operator-only exclusion and backup procedure.
// The argument checks (journalArguments) come first, before any verification
// or lock.
func MigrateJournal(ctx context.Context, path string, options JournalMigrationOptions) (store.InputMigrationResult, error) {
	var result store.InputMigrationResult
	roots, err := journalArguments(options)
	if err != nil {
		return result, err
	}
	result, err = store.MigrateInputJournal(ctx, path, roots, options.BackupDir, verifyJournalQuiescence)
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
