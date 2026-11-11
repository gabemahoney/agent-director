package adminapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

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

// JournalRefusal is every error MigrateJournal and CheckJournalMigration
// return: a bounded reason, the processes to stop, the reason's own action,
// and the attempt's commit state, which the administrator writes beside the
// refusal rather than inside it.
type JournalRefusal struct {
	Reason    string           `json:"reason"`
	Processes []JournalProcess `json:"processes"`
	Action    string           `json:"action"`
	Commit    JournalCommit    `json:"-"`
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

// JournalCommit is what a refused migration committed (SRD SR-2.16 "Commit
// state"): Cutover is not_committed, committed or unknown, and StepsCommitted,
// set only when Cutover is not_committed, says whether an earlier schema step
// of the attempt committed.
type JournalCommit struct {
	Cutover        string `json:"cutover"`
	StepsCommitted *bool  `json:"steps_committed,omitempty"`
}

// journalCommitOf is the commit state a store attempt reports.
func journalCommitOf(commit store.InputMigrationCommit) JournalCommit {
	cutover := commit.Cutover()
	if cutover != store.CutoverNotCommitted {
		return JournalCommit{Cutover: cutover}
	}
	steps := commit.StepsCommitted
	return JournalCommit{Cutover: cutover, StepsCommitted: &steps}
}

// JournalCommitBeforeAttempt is the commit state of a migrate-journal error
// made before any attempt began: not_committed when no other administrator
// holds the store's migration lock, which it checks without creating the lock
// file; unknown otherwise: when one does, when the lock cannot be checked, or
// when path is empty, so there is no lock to check.
func JournalCommitBeforeAttempt(path string) JournalCommit {
	if path == "" {
		return JournalCommit{Cutover: store.CutoverUnknown}
	}
	return journalCommitOf(store.InputMigrationCommit{Lock: store.InputMigrationLockHolder(path)})
}

// withJournalCommit sets a refusal's commit state; any other err, nil
// included, is returned as it is.
func withJournalCommit(err error, commit JournalCommit) error {
	var refusal *JournalRefusal
	if errors.As(err, &refusal) {
		refusal.Commit = commit
	}
	return err
}

// journalNextStep is SRD SR-2.16's next step for a cutover that is not
// verified, and the one way on that works in every state a refusal can
// leave: before or past the cutover, restored to the previous release or
// not, after the user's own run or the installer's. The installer's re-run
// writes the authorization for the store's current version, which
// migrate-journal never does, and migrates a store before the cutover; past
// it, the installer takes its ordinary route, which never verifies, so
// migrate-journal's own run follows to verify. Each new attempt backs up to a
// directory that does not exist yet.
const journalNextStep = "re-run this release's installer with a new --journal-backup, which finishes any pending schema step, then run migrate-journal with a new --backup-dir until it exits 0 with authority \"sqlite\""

// journalRetry ends the action of every reason whose fix is followed by a new
// attempt.
const journalRetry = "then " + journalNextStep

// journalActions is the one reason table: every reason migrate-journal
// emits, the store's causes (store.InputMigrationCauses) included, with its
// own action. A reason it lacks gets no action, never generic advice. Each
// action names the administrator flag with install.sh's spelling beside it,
// because install.sh relays the refusal unchanged and a user follows it from
// either tool. No action carries a path, file contents or OS text.
var journalActions = map[string]string{
	// The argument checks (journalArguments), before any attempt.
	"maintenance_boundary_required": "establish the maintenance window (stop old mutators and prevent new old-binary invocations through committed verification), then pass --maintenance-window (install.sh: --journal-maintenance-window)",
	"legacy_roots_not_established":  "declare every additional legacy input root with --legacy-root, then pass --roots-complete (install.sh: --journal-legacy-root, --journal-roots-complete)",
	"invoking_home_unavailable":     "run it with HOME set to the invoking user's absolute home directory",
	"legacy_root_not_absolute":      "give every --legacy-root (install.sh: --journal-legacy-root) as an absolute path",
	"legacy_root_not_clean":         "give every --legacy-root (install.sh: --journal-legacy-root) in clean form, with no trailing /, no repeated / and no . or .. element",
	"absolute_backup_required":      "pass --backup-dir (install.sh: --journal-backup) a new absolute directory outside every legacy input root",
	"backup_inside_legacy_root":     "choose a --backup-dir (install.sh: --journal-backup) outside every legacy input root",
	"backup_exists":                 "choose a --backup-dir (install.sh: --journal-backup) that does not exist yet; keep the existing one",
	"backup_unreadable":             "choose a --backup-dir (install.sh: --journal-backup) whose parent directories exist and can be searched",

	// The quiescence verifier, before the lock, inside the cutover step and
	// after its commit.
	"legacy_root_unreadable":           "make each legacy root and its parent directories readable and searchable by the user running the migration, keep the maintenance window, " + journalRetry,
	"legacy_root_symlink":              "make the store path, HOME and every --legacy-root (install.sh: --journal-legacy-root) real paths with no symbolic link in them, keep the maintenance window, " + journalRetry,
	"legacy_root_access_unestablished": "make the store and every legacy root owned by the user running the migration and private to it (chmod 0600 the store, chmod 0700 each root, and remove any access-control entry, for example chmod -N <path>), keep the maintenance window, " + journalRetry,
	"process_inventory_unreadable":     "run it as the agents' user with every mounted file system reachable or unmounted (on Linux, with the proc file system mounted and readable), keep the maintenance window, " + journalRetry + "; report it if it persists",
	"inventory_deadline":               "make every mounted file system reachable or unmount it, or answer any privacy consent prompt, keep the maintenance window, " + journalRetry,
	"old_or_unverified_mutators":       "stop each listed agent_director_mutator (an agent-director program or a process holding the store or a file under a legacy root); each may run again once the cutover is verified; keep the maintenance window, " + journalRetry,
	"process_inventory_unsupported":    "this host cannot verify that old mutators are stopped; run the migration on a supported install host (Linux x86_64, Linux aarch64 or Darwin arm64); nothing was changed",

	// The store's causes.
	"migration_lock_held":                    "wait for the other administrator's migrate-journal to finish, " + journalRetry,
	"migration_lock_unestablished":           "only after confirming no administrator is running, remove <store>.input-migration.lock, " + journalRetry,
	"authorization_missing":                  "the installer writes the migration authorization, which migrate-journal never does: " + journalNextStep + "; the store was not changed",
	"store_version_unreadable":               "stop every process that holds the store open, make it a readable SQLite database and leave room in the temp directory (TMPDIR) for a private copy of it, " + journalRetry + "; the store was not changed",
	"store_newer_than_release":               "a newer release made this store: install that release or a later one instead of this one; the store was not changed",
	"backup_failed":                          "make the store and everything under the legacy roots readable regular files and directories, and choose a new backup directory in a writable directory with room for their copy, " + journalRetry + "; the store was not changed",
	"store_open_failed":                      "point --store-path (install.sh: the config's [store] db_path) at the existing store, in a directory the user running the migration can write, and make the store a readable and writable SQLite database owned by that user; if it already is one, its metadata is damaged: inspect it or restore it from its pre-upgrade backup; " + journalRetry,
	"store_busy":                             "stop every process that holds the store open, keep the maintenance window, " + journalRetry,
	"schema_step_failed":                     "fix what stopped the schema step, such as disk space, write access or a schema changed by hand, keep the maintenance window, " + journalRetry,
	"legacy_writer_active":                   "stop the old agent-director process that holds a legacy launch lock, keep the maintenance window, " + journalRetry,
	"legacy_evidence_unestablished":          "make the launch evidence under each legacy root readable and writable by the user running the migration and remove what is not this store's launch evidence; if the store itself records one launch token twice, inspect it or restore it from its pre-upgrade backup; keep the maintenance window, " + journalRetry,
	"native_input_state_inconsistent":        "the store's native-input state was changed by hand: inspect the store or restore it from its pre-upgrade backup, keep the maintenance window, " + journalRetry,
	"attempt_deadline":                       "make the attempt finish within its time limit (a less busy disk for the store and the backup), keep the maintenance window, " + journalRetry,
	"interrupted":                            "the attempt was interrupted before its cutover commit; keep the maintenance window, " + journalRetry,
	"migration_incomplete_or_commit_unknown": "keep the maintenance window; " + journalNextStep + "; details.cutover says whether the cutover committed",
	// Reached only once the open left the store current, which a restore never
	// follows, so the direct run needs no authorization and verifies.
	"committed_verification_incomplete": "the cutover committed; keep the maintenance window, stop any listed process, then run migrate-journal again with a new --backup-dir until it exits 0 with authority \"sqlite\"",
}

// journalRefusal is the refusal for reason with its own action. Its commit
// state is set where the refusal is returned.
func journalRefusal(reason string, processes ...JournalProcess) *JournalRefusal {
	// Callers read processes as an array, as install.sh's own envelope writes it.
	if processes == nil {
		processes = []JournalProcess{}
	}
	return &JournalRefusal{Reason: reason, Processes: processes, Action: journalActions[reason]}
}

// journalUnsupportedHost is the refusal of a build with no quiescence
// verifier for its host: nothing is verified, so nothing changes.
func journalUnsupportedHost() error {
	return journalRefusal("process_inventory_unsupported")
}

var (
	journalVerifyMu sync.Mutex
	journalVerify   = verifyJournalQuiescence
)

// SetJournalVerifierForTest replaces the quiescence verifier MigrateJournal
// hands the store, for every later call in this process until restore runs,
// so a test can fail any of an attempt's three verifications. Normal
// administrator runs never call it. It is the one verifier seam: a test that
// fails a verification uses it, never a second seam (docs/architecture.md
// "migrate-journal fault seams and child runner").
func SetJournalVerifierForTest(verify func(ctx context.Context, dbPath string, roots []string) error) (restore func()) {
	journalVerifyMu.Lock()
	previous := journalVerify
	journalVerify = verify
	journalVerifyMu.Unlock()
	return func() {
		journalVerifyMu.Lock()
		journalVerify = previous
		journalVerifyMu.Unlock()
	}
}

func journalVerifier() func(context.Context, string, []string) error {
	journalVerifyMu.Lock()
	defer journalVerifyMu.Unlock()
	return journalVerify
}

// CheckJournalMigration makes only migrate-journal's argument checks, the
// ones MigrateJournal makes first. It opens no store, takes no lock and
// writes nothing, so install.sh can refuse bad journal flags before its
// binary swap. Its refusals are not_committed, with no step committed.
func CheckJournalMigration(options JournalMigrationOptions) error {
	_, err := journalArguments(options)
	return withJournalCommit(err, JournalCommitCheckOnly())
}

// JournalCommitCheckOnly is the commit state of every --check-only error:
// not_committed, with no step committed, because it begins no attempt.
func JournalCommitCheckOnly() JournalCommit {
	steps := false
	return JournalCommit{Cutover: store.CutoverNotCommitted, StepsCommitted: &steps}
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
	if inside, err := backupInsideRoot(filepath.Clean(options.BackupDir), unique); err != nil {
		return nil, err
	} else if inside {
		return nil, journalRefusal("backup_inside_legacy_root")
	}
	// A directory made after this check fails the backup's exclusive mkdir.
	if _, err := os.Lstat(options.BackupDir); err == nil {
		return nil, journalRefusal("backup_exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, journalRefusal("backup_unreadable")
	}
	// That mkdir makes only the last element, so a missing parent would fail
	// it after the installer's swap (MI decision Q1, Task t2.7tz.ab.1h).
	if _, err := os.Lstat(filepath.Dir(filepath.Clean(options.BackupDir))); errors.Is(err, fs.ErrNotExist) {
		return nil, journalRefusal("backup_unreadable")
	}
	return unique, nil
}

// journalContainmentHost is the host-selection seam for containment, and it
// is must-use: the one way to choose how backupInsideRoot judges whether the
// backup lies inside a legacy root, and the journalFS it looks up with.
// Darwin compares file identity (SRD SR-2.15 "Limits"), so a case variant
// cannot escape a root; Linux stays lexical. In-package tests set it to
// select the Darwin rule on Linux, where runtime.GOOS is never darwin; such a
// test is serial and restores it in t.Cleanup. It is a host choice, not
// fault injection. Never add a second host switch for containment.
var journalContainmentHost = struct {
	byIdentity bool
	fs         journalFS
}{runtime.GOOS == "darwin", osJournalFS{}}

// backupInsideRoot reports whether the clean absolute backup is a root or
// lies under one, by the rule journalContainmentHost selects. Only the
// identity rule looks anything up, so only it can fail, with a refusal.
func backupInsideRoot(backup string, roots []string) (bool, error) {
	if journalContainmentHost.byIdentity {
		return backupInsideRootByIdentity(journalContainmentHost.fs, backup, roots)
	}
	return backupInsideRootLexical(backup, roots), nil
}

// backupInsideRootLexical reports whether the clean absolute backup is a
// root or lies under one, compared lexically.
func backupInsideRootLexical(backup string, roots []string) bool {
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
//
// Every error is a *JournalRefusal with its cause's own reason and the
// attempt's commit state (journalMigrationRefusal); an argument refusal's
// comes from the lock check (JournalCommitBeforeAttempt). The end of ctx
// before the cutover commit begins interrupts the attempt; once it has begun,
// the attempt finishes. The result carries the store's authority whenever the
// attempt read it, on the error path too.
func MigrateJournal(ctx context.Context, path string, options JournalMigrationOptions) (store.InputMigrationResult, error) {
	var result store.InputMigrationResult
	roots, err := journalArguments(options)
	if err != nil {
		return result, withJournalCommit(err, JournalCommitBeforeAttempt(path))
	}
	result, err = store.MigrateInputJournal(ctx, path, roots, options.BackupDir, journalVerifier())
	if err != nil {
		return result, journalMigrationRefusal(err)
	}
	return result, nil
}

// journalMigrationRefusal names a failed attempt's reason, in this order: an
// interrupt before the cutover commit began, whatever failure it caused; a
// failed verification after the cutover committed, keeping the verifier's
// processes; an unconfirmed or committed cutover left incomplete; the
// verifier's own refusal (process_inventory_unreadable for a verifier error
// that is not one); otherwise the store's cause. File contents and raw OS
// diagnostic strings are never copied to output.
func journalMigrationRefusal(err error) error {
	var failure *store.InputMigrationError
	if !errors.As(err, &failure) {
		// MigrateInputJournal returns no other error; one would be uncertain.
		return withJournalCommit(journalRefusal("migration_incomplete_or_commit_unknown"), JournalCommit{Cutover: store.CutoverUnknown})
	}
	var refusal *JournalRefusal
	verifierRefused := errors.As(failure.Err, &refusal)
	switch {
	case failure.Commit.Interrupted:
		refusal = journalRefusal("interrupted")
	case failure.Cause == store.CauseCommittedVerificationIncomplete:
		var processes []JournalProcess
		if verifierRefused {
			processes = refusal.Processes
		}
		refusal = journalRefusal(store.CauseCommittedVerificationIncomplete, processes...)
	case failure.Commit.Unconfirmed, failure.Commit.CutoverCommitted:
		refusal = journalRefusal(store.CauseMigrationIncomplete)
	case failure.Cause == "" && verifierRefused:
		// The verifier's own refusal, as it made it.
	case failure.Cause == "":
		refusal = journalRefusal("process_inventory_unreadable")
	default:
		refusal = journalRefusal(failure.Cause)
	}
	return withJournalCommit(refusal, journalCommitOf(failure.Commit))
}
