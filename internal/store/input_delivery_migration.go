package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gabemahoney/agent-director/internal/inputjournal"
	"golang.org/x/sys/unix"
)

// InputMigrationResult is operator-only. Runtime opens never construct the
// maintenance window and cannot use a sentinel alone to switch input authority.
type InputMigrationResult struct {
	StoreID           string `json:"store_id"`
	Authority         string `json:"authority"`
	Backup            string `json:"backup"`
	RetirementPending bool   `json:"retirement_pending"`
}

// The causes an InputMigrationError names, each a bounded reason code the
// administrator reports with its own action. A failing site names its cause;
// a failure no site names takes its stage's (inputMigrationStage.cause).
const (
	CauseMigrationLockUnestablished      = "migration_lock_unestablished"
	CauseMigrationLockHeld               = "migration_lock_held"
	CauseAuthorizationMissing            = "authorization_missing"
	CauseStoreVersionUnreadable          = "store_version_unreadable"
	CauseStoreNewerThanRelease           = "store_newer_than_release"
	CauseBackupExists                    = "backup_exists"
	CauseBackupFailed                    = "backup_failed"
	CauseStoreOpenFailed                 = "store_open_failed"
	CauseStoreBusy                       = "store_busy"
	CauseSchemaStepFailed                = "schema_step_failed"
	CauseLegacyRootUnreadable            = "legacy_root_unreadable"
	CauseLegacyWriterActive              = "legacy_writer_active"
	CauseLegacyEvidenceUnestablished     = "legacy_evidence_unestablished"
	CauseNativeInputStateInconsistent    = "native_input_state_inconsistent"
	CauseAttemptDeadline                 = "attempt_deadline"
	CauseMigrationIncomplete             = "migration_incomplete_or_commit_unknown"
	CauseCommittedVerificationIncomplete = "committed_verification_incomplete"
)

// InputMigrationCauses lists every cause MigrateInputJournal can name, so the
// administrator can check that each has an action. A new cause is a constant
// above, listed here, with its entry in adminapi's reason table.
var InputMigrationCauses = []string{
	CauseMigrationLockUnestablished, CauseMigrationLockHeld, CauseAuthorizationMissing,
	CauseStoreVersionUnreadable, CauseStoreNewerThanRelease, CauseBackupExists, CauseBackupFailed,
	CauseStoreOpenFailed, CauseStoreBusy, CauseSchemaStepFailed, CauseLegacyRootUnreadable,
	CauseLegacyWriterActive, CauseLegacyEvidenceUnestablished, CauseNativeInputStateInconsistent,
	CauseAttemptDeadline, CauseMigrationIncomplete, CauseCommittedVerificationIncomplete,
}

// The values of InputMigrationCommit.Cutover: SRD SR-2.16 "Commit state"'s
// details.cutover.
const (
	CutoverNotCommitted = "not_committed"
	CutoverCommitted    = "committed"
	CutoverUnknown      = "unknown"
)

// InputMigrationLockState is what an attempt found of the migration lock
// (<store>.input-migration.lock) another administrator may hold.
type InputMigrationLockState string

const (
	// InputMigrationLockFree: no other administrator holds it. An attempt
	// that holds it finds it free.
	InputMigrationLockFree InputMigrationLockState = "free"
	// InputMigrationLockHeldByOther: another administrator holds it.
	InputMigrationLockHeldByOther InputMigrationLockState = "held_by_other"
	// InputMigrationLockUnreadable: whether anyone holds it is unknown.
	InputMigrationLockUnreadable InputMigrationLockState = "unreadable"
)

// InputMigrationCommit reports what one MigrateInputJournal attempt committed.
type InputMigrationCommit struct {
	// StepsCommitted: an ordinary schema step committed in this attempt
	// before its cutover commit began, raising the store's version.
	StepsCommitted bool
	// Unconfirmed: a schema step's or the cutover's COMMIT returned an
	// error, so it may or may not have landed.
	Unconfirmed bool
	// CutoverBegan: the cutover's COMMIT was issued.
	CutoverBegan bool
	// CutoverCommitted: the cutover's COMMIT succeeded.
	CutoverCommitted bool
	// Interrupted: the caller's context ended before the cutover commit
	// began.
	Interrupted bool
	// Lock is the migration lock as the attempt found it.
	Lock InputMigrationLockState
}

// Cutover is this attempt's commit state: committed once its cutover
// committed; unknown when the cutover commit began without committing, a
// commit is unconfirmed, or the lock is not known to be free; otherwise
// not_committed.
func (c InputMigrationCommit) Cutover() string {
	switch {
	case c.CutoverCommitted:
		return CutoverCommitted
	case c.CutoverBegan, c.Unconfirmed, c.Lock != InputMigrationLockFree:
		return CutoverUnknown
	}
	return CutoverNotCommitted
}

// InputMigrationError is every error MigrateInputJournal returns. Its text is
// Err's unchanged; the cause and commit report travel beside it.
type InputMigrationError struct {
	// Cause is one of InputMigrationCauses, or empty for a verifier error
	// before the post-commit verification, which names its own reason.
	Cause  string
	Commit InputMigrationCommit
	Err    error
}

func (e *InputMigrationError) Error() string { return e.Err.Error() }

func (e *InputMigrationError) Unwrap() error { return e.Err }

// inputMigrationCause carries the cause the failing site named beside err's
// unchanged text. An empty cause marks a verifier error, which names its own.
type inputMigrationCause struct {
	cause string
	err   error
}

func (e *inputMigrationCause) Error() string { return e.err.Error() }

func (e *inputMigrationCause) Unwrap() error { return e.err }

// withInputMigrationCause names err's cause; a nil err stays nil. Ordinary
// opens never read the cause, so a shared site may name one.
func withInputMigrationCause(cause string, err error) error {
	if err == nil {
		return nil
	}
	return &inputMigrationCause{cause: cause, err: err}
}

func inputMigrationCauseOf(err error) (cause string, named bool) {
	var c *inputMigrationCause
	if errors.As(err, &c) {
		return c.cause, true
	}
	return "", false
}

// inputMigrationStage is how far an attempt got. The schema chain hands back
// to the open's stage when it finishes.
type inputMigrationStage int

const (
	inputStagePrepare   inputMigrationStage = iota // the path and the first verification
	inputStageLock                                 // taking the migration lock
	inputStageAuthorize                            // the version and authorization, before the backup
	inputStageBackup
	inputStageOpen  // the store's open, but for its schema chain
	inputStageChain // the schema steps, the cutover's included
	inputStageVerify
)

// cause is the cause of a failure no site named.
func (s inputMigrationStage) cause(cutoverBegan bool) string {
	switch s {
	case inputStageLock:
		return CauseMigrationLockUnestablished
	case inputStageAuthorize:
		return CauseStoreVersionUnreadable
	case inputStageBackup:
		return CauseBackupFailed
	case inputStageChain:
		if cutoverBegan {
			return CauseMigrationIncomplete
		}
		return CauseSchemaStepFailed
	case inputStageVerify:
		// The open read committed authority; what failed after it is
		// verification and cleanup.
		return CauseCommittedVerificationIncomplete
	}
	return CauseStoreOpenFailed
}

type inputMigrationWindow struct {
	// ctx is the attempt's work context: every step runs under it.
	ctx context.Context
	// caller is the caller's context. Its end interrupts the attempt until
	// the cutover commit begins; nil interrupts nothing.
	caller   context.Context
	cancel   context.CancelFunc // ends ctx
	path     string
	roots    []string
	backup   string
	verify   func(context.Context, string, []string) error
	faults   InputMigrationFaults
	stage    inputMigrationStage
	journals []*inputjournal.LegacyJournal
	records  []inputjournal.ImportRecord

	// mu orders the caller's interrupt against the cutover gate. commit is
	// written only under it.
	mu     sync.Mutex
	commit InputMigrationCommit
}

func (w *inputMigrationWindow) note(update func(*InputMigrationCommit)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	update(&w.commit)
}

func (w *inputMigrationWindow) report() InputMigrationCommit {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.commit
}

func (w *inputMigrationWindow) setStage(stage inputMigrationStage) {
	if w != nil {
		w.stage = stage
	}
}

// interrupt is the caller context's AfterFunc: until the cutover commit
// begins, the caller's end interrupts the attempt and ends its work context.
func (w *inputMigrationWindow) interrupt() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interruptLocked()
}

func (w *inputMigrationWindow) interruptLocked() {
	if w.commit.CutoverBegan || w.commit.Interrupted {
		return
	}
	w.commit.Interrupted = true
	w.cancel()
}

// stopped is the attempt's checkpoint before each change until its cutover
// commit begins: an error once the caller's context has ended, which
// interrupts the attempt at once rather than when the AfterFunc runs, or the
// work context has. Every such checkpoint uses it, the cutover gate included,
// so no change starts after an interrupt has arrived.
func (w *inputMigrationWindow) stopped() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stoppedLocked()
}

func (w *inputMigrationWindow) stoppedLocked() error {
	if w.caller != nil && w.caller.Err() != nil {
		w.interruptLocked()
	}
	return w.ctx.Err()
}

// verifyBoundary runs the verifier on the work context. Its refusal names its
// own reason, so it carries an empty cause, never a stage's.
func (w *inputMigrationWindow) verifyBoundary() error {
	if err := w.verify(w.ctx, w.path, w.roots); err != nil {
		return withInputMigrationCause("", err)
	}
	return nil
}

// applyStep runs an ordinary schema step. Until the cutover commit begins in
// this attempt, the attempt's end (stopped) stops the chain before the step,
// and the step's commit is recorded (StepsCommitted, or Unconfirmed when its
// COMMIT returned an error). Once it began, the step runs unchecked, so the
// chain finishes.
func (w *inputMigrationWindow) applyStep(db *sql.DB, step migrationStep) error {
	if w == nil || w.report().CutoverBegan {
		return step.apply(db)
	}
	if err := w.stopped(); err != nil {
		return err
	}
	err := step.apply(db)
	if err == nil && w.faults.AfterStepCommit != nil {
		if fault := w.faults.AfterStepCommit(step.from); fault != nil {
			err = stepCommitError{fault}
		}
	}
	if errors.Is(err, errCommitUnconfirmed) {
		w.note(func(c *InputMigrationCommit) { c.Unconfirmed = true })
		return withInputMigrationCause(CauseMigrationIncomplete, err)
	}
	if err != nil {
		return err
	}
	w.note(func(c *InputMigrationCommit) { c.StepsCommitted = true })
	return nil
}

// beginCutoverCommit is the cutover gate, just before its COMMIT. It stops
// the attempt as stopped does; otherwise, under the same lock, it records that
// the commit began, after which the caller's end no longer cancels the work
// context, the transaction's context. A commit that must finish once begun is
// admitted only through this gate.
func (w *inputMigrationWindow) beginCutoverCommit() error {
	if w.faults.BeforeCutoverCommit != nil {
		w.faults.BeforeCutoverCommit()
	}
	w.mu.Lock()
	err := w.stoppedLocked()
	if err == nil {
		w.commit.CutoverBegan = true
	}
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if w.faults.AfterCutoverCommitBegan != nil {
		w.faults.AfterCutoverCommitBegan()
	}
	return nil
}

// commitCutover issues the cutover's COMMIT, through the fault seam when set.
func (w *inputMigrationWindow) commitCutover(tx *sql.Tx) error {
	var err error
	if w.faults.CutoverCommit != nil {
		err = w.faults.CutoverCommit(tx.Commit)
	} else {
		err = tx.Commit()
	}
	w.note(func(c *InputMigrationCommit) {
		c.Unconfirmed = c.Unconfirmed || err != nil
		c.CutoverCommitted = err == nil
	})
	return err
}

// failure wraps a failed attempt's error with its cause and commit report.
// main is the attempt's own error and closeErr its closers'; either may be
// nil, not both.
func (w *inputMigrationWindow) failure(path string, base context.Context, main, closeErr error) error {
	if w.caller.Err() != nil {
		// A cancellation the AfterFunc has not handled yet still interrupts
		// an attempt whose cutover commit had not begun.
		w.interrupt()
	}
	commit := w.report()
	if commit.Lock == "" {
		// The attempt failed before it opened the lock.
		commit.Lock = InputMigrationLockHolder(path)
	}
	err := main
	if closeErr != nil {
		err = errors.Join(main, closeErr)
	}
	return &InputMigrationError{Cause: w.failureCause(base, main, closeErr, commit), Commit: commit, Err: err}
}

// failureCause names the cause: a verifier error before the post-commit
// verification names its own; a failure the attempt's deadline caused before
// the cutover commit began is attempt_deadline; then the failing site's cause,
// store_busy for a busy store, and the stage's cause.
func (w *inputMigrationWindow) failureCause(base context.Context, main, closeErr error, commit InputMigrationCommit) string {
	err := main
	if err == nil {
		err = closeErr // the attempt finished; only a closer failed
	}
	cause, named := inputMigrationCauseOf(err)
	switch {
	case named && cause == "":
		return ""
	case w.stage < inputStageVerify && !commit.CutoverBegan && errors.Is(err, context.DeadlineExceeded) && errors.Is(base.Err(), context.DeadlineExceeded):
		return CauseAttemptDeadline
	case named:
		return cause
	case isBusy(err):
		return CauseStoreBusy
	}
	return w.stage.cause(commit.CutoverBegan)
}

// MigrateInputJournal is the store portion of the explicit administrator
// procedure. The admin layer owns the maintained admission boundary and real
// process inventory; verify must refuse unreadable evidence, never kill it.
// This entry is internal to this module, absent from the agent-facing API.
// Roots are complete, canonical legacy input roots, including invoking HOME.
// The caller makes the argument checks (adminapi's journalArguments),
// including a new backup outside every root; the guard below is only this
// entry's own precondition.
//
// Every error is an *InputMigrationError naming its cause and what the
// attempt committed (SRD SR-2.16 "Commit state"), with the text the failure
// always had. The attempt runs under its own work context, 30 s from its
// start. Until its cutover commit begins, the end of ctx interrupts it
// (InputMigrationCommit.Interrupted); once that commit has begun, the end of
// ctx changes nothing and the attempt finishes. A store that does not exist
// is refused before the lock. Authorization and the store's version are checked before
// the backup is taken (SR-2.13), reading the store only through a private
// snapshot. The result carries the store's authority whenever the store
// opened, on the error path too.
func MigrateInputJournal(ctx context.Context, path string, roots []string, backup string, verify func(context.Context, string, []string) error) (result InputMigrationResult, resultErr error) {
	faults := currentInputMigrationFaults()
	timeout := 30 * time.Second
	if faults.AttemptTimeout != 0 {
		timeout = faults.AttemptTimeout
	}
	busyMs := DefaultBusyTimeoutMs
	if faults.BusyTimeoutMs > 0 {
		busyMs = faults.BusyTimeoutMs
	}
	base, cancelBase := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	work, cancelWork := context.WithCancel(base)
	w := &inputMigrationWindow{ctx: work, caller: ctx, cancel: cancelWork, roots: append([]string(nil), roots...), backup: backup, verify: verify, faults: faults}
	// Closers report here, apart from the attempt's own error, so a closer's
	// failure never decides the cause of an earlier one.
	var closeErr error
	// Registered first, so it runs last, after every closer.
	defer func() {
		if resultErr != nil || closeErr != nil {
			resultErr = w.failure(path, base, resultErr, closeErr)
		}
	}()
	stopInterrupt := context.AfterFunc(ctx, w.interrupt)
	defer stopInterrupt()
	defer cancelWork()
	defer cancelBase()
	if verify == nil || len(roots) == 0 || !filepath.IsAbs(backup) {
		return result, fmt.Errorf("maintenance boundary and complete roots are required")
	}
	resolved, err := resolveInputMigrationPath(path)
	if err != nil {
		return result, err
	}
	w.path = resolved
	if err := w.verifyBoundary(); err != nil {
		return result, err
	}
	// The migration needs the store it migrates: a mistyped path, one through
	// a regular file included, would otherwise pass the checks and fail at the
	// backup.
	if _, err := os.Lstat(resolved); inputPathAbsent(err) {
		return result, withInputMigrationCause(CauseStoreOpenFailed, fmt.Errorf("the store does not exist; migration requires an existing store"))
	}
	w.setStage(inputStageLock)
	lockPath := inputMigrationLockPath(resolved)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		// The report reads the lock's holder without creating the file. A
		// lock file that exists but can't be opened is the lock's problem
		// (the stage's cause); with none there, the store's directory can't
		// take it (missing, read-only or not writable), which is the store's.
		if _, lerr := os.Lstat(lockPath); lerr != nil {
			err = withInputMigrationCause(CauseStoreOpenFailed, err)
		}
		return result, err
	}
	defer func() {
		closeErr = errors.Join(closeErr, withInputMigrationCause(CauseMigrationLockUnestablished, lock.Close()))
	}()
	if err := acquireInputMigrationLock(lock); err != nil {
		state := InputMigrationLockUnreadable
		if cause, _ := inputMigrationCauseOf(err); cause == CauseMigrationLockHeld {
			state = InputMigrationLockHeldByOther
		}
		w.note(func(c *InputMigrationCommit) { c.Lock = state })
		return result, err
	}
	w.note(func(c *InputMigrationCommit) { c.Lock = InputMigrationLockFree })
	defer func() {
		// Registered before the journal/database closers: critical work and
		// handles finish first. Unlink while still locked, then release last.
		// A replacement path is never ours to remove.
		if err := checkInputMigrationLock(lock); err != nil {
			closeErr = errors.Join(closeErr, err)
			return
		}
		if err := os.Remove(lock.Name()); err != nil {
			closeErr = errors.Join(closeErr, withInputMigrationCause(CauseMigrationLockUnestablished, fmt.Errorf("remove journal migration lock: %w", err)))
			return
		}
		closeErr = errors.Join(closeErr, withInputMigrationCause(CauseMigrationLockUnestablished, syncInputDir(filepath.Dir(lock.Name()))))
	}()
	w.setStage(inputStageAuthorize)
	if err := w.stopped(); err != nil {
		return result, err
	}
	defer func() {
		for _, j := range w.journals {
			closeErr = errors.Join(closeErr, j.Close())
		}
	}()
	// Validate first: a refused authorization or a newer store takes no
	// backup (SR-2.13).
	if err := authorizeInputMigration(work, resolved, busyMs); err != nil {
		return result, err
	}
	w.setStage(inputStageBackup)
	if err := w.stopped(); err != nil {
		return result, err
	}
	// Back up while all old writers are stopped and before any schema hop.
	// The directory is exclusive: a retry must never overwrite the older copy.
	if err := backupInputStore(work, resolved, roots, backup); err != nil {
		return result, err
	}
	w.setStage(inputStageOpen)
	s, err := openDBWithInputWindow(resolved, busyMs, w)
	if err != nil {
		return result, err
	}
	defer func() { closeErr = errors.Join(closeErr, s.Close()) }()
	w.setStage(inputStageVerify)
	// The open read committed authority, so the result reports it even when
	// a later check fails.
	result = InputMigrationResult{StoreID: s.storeID, Authority: "sqlite", Backup: backup}
	if err := inputAuthority(work, s.db, s.storeID); err != nil {
		return result, err
	}
	// The second inventory is still under the operator-maintained boundary.
	// Failure here is an uncertainty AFTER cutover, never file authority.
	if err := verify(work, resolved, roots); err != nil {
		return result, withInputMigrationCause(CauseCommittedVerificationIncomplete, fmt.Errorf("SQLite cutover committed; keep the maintenance boundary and restart administrator verification: %w", err))
	}
	for _, j := range w.journals {
		if err := j.Retire(work); err != nil {
			result.RetirementPending = true
		}
	}
	// A retry after an uncertain commit never imports or deletes from a new
	// filesystem read. Report remaining operational files for inspection.
	if len(w.journals) == 0 {
		for _, root := range roots {
			err := filepath.WalkDir(filepath.Join(root, s.storeID), func(path string, e os.DirEntry, err error) error {
				if inputPathAbsent(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if err := work.Err(); err != nil {
					return err
				}
				name := e.Name()
				if name == "launch.json" || name == "attempt.json" || strings.HasPrefix(name, "request-") && strings.HasSuffix(name, ".json") {
					result.RetirementPending = true
				}
				return nil
			})
			if err != nil {
				result.RetirementPending = true
			}
		}
	}
	return result, nil
}

// resolveInputMigrationPath resolves the store path as MigrateInputJournal
// opens it.
func resolveInputMigrationPath(path string) (string, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func inputMigrationLockPath(resolved string) string {
	return resolved + ".input-migration.lock"
}

// inputPathAbsent reports whether err says a path does not exist, one
// through a regular file included, since nothing can exist under a file. The
// attempt and the verifier treat both alike, so a mistyped path lands on the
// reason whose action points at the right one.
func inputPathAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR)
}

// InputMigrationLockHolder reports whether another administrator holds the
// migration lock of the store at path, without ever creating, changing or
// removing the lock file. A lock file that does not exist is free. It takes
// the lock for a moment to test it, so a contender starting at that moment is
// refused as if another administrator held it.
func InputMigrationLockHolder(path string) InputMigrationLockState {
	resolved, err := resolveInputMigrationPath(path)
	if err != nil {
		return InputMigrationLockUnreadable
	}
	lock, err := os.OpenFile(inputMigrationLockPath(resolved), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if inputPathAbsent(err) {
		return InputMigrationLockFree
	}
	if err != nil {
		return InputMigrationLockUnreadable
	}
	defer lock.Close()
	if info, err := lock.Stat(); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return InputMigrationLockUnreadable
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); errors.Is(err, unix.EWOULDBLOCK) {
		return InputMigrationLockHeldByOther
	} else if err != nil {
		return InputMigrationLockUnreadable
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		return InputMigrationLockUnreadable
	}
	return InputMigrationLockFree
}

// acquireInputMigrationLock admits an already-open descriptor only while its
// inode still owns the pathname. The caller must close it even on failure.
func acquireInputMigrationLock(lock *os.File) error {
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		cause := CauseMigrationLockUnestablished
		if errors.Is(err, unix.EWOULDBLOCK) {
			cause = CauseMigrationLockHeld
		}
		return withInputMigrationCause(cause, fmt.Errorf("another administrator owns the journal migration"))
	}
	// An opener can retain the previous owner's descriptor across unlink and
	// only acquire it after that owner closes. Never admit that obsolete inode.
	return checkInputMigrationLock(lock)
}

// checkInputMigrationLock is used only with the descriptor exclusively locked.
// Both admission and removal must still name that empty regular file. Its
// contents never carry authority, and unexpected content is not ours to erase.
// Cooperative callers unlink only after all critical work; an unlinked or
// replaced opener refuses instead of running alongside a new pathname owner.
func checkInputMigrationLock(lock *os.File) error {
	held, err := lock.Stat()
	if err != nil {
		return withInputMigrationCause(CauseMigrationLockUnestablished, err)
	}
	named, err := os.Lstat(lock.Name())
	if err != nil {
		return withInputMigrationCause(CauseMigrationLockUnestablished, fmt.Errorf("journal migration lock changed; restart administrator verification: %w", err))
	}
	if !held.Mode().IsRegular() || !named.Mode().IsRegular() || held.Size() != 0 || named.Size() != 0 {
		return withInputMigrationCause(CauseMigrationLockUnestablished, fmt.Errorf("journal migration lock is not an empty regular file; restart administrator verification"))
	}
	if !os.SameFile(held, named) {
		return withInputMigrationCause(CauseMigrationLockUnestablished, fmt.Errorf("journal migration lock changed; restart administrator verification"))
	}
	return nil
}

// authorizeInputMigration makes, before the backup, the checks the store's
// open would refuse on: an authorization that is missing or does not match,
// and a store newer than this release (SR-2.13's validate-first). It reads the
// version from a private snapshot, never through SQLite on the store, so a
// refusal leaves the store and its sidecars as they were; it runs before this
// process opens the store, as ReadSnapshot requires. MigrateInputJournal has
// refused a store that does not exist; one that vanished since cannot be read.
func authorizeInputMigration(ctx context.Context, resolved string, busyMs int) error {
	version, err := snapshotUserVersion(ctx, resolved, busyMs)
	if err != nil {
		return withInputMigrationCause(CauseStoreVersionUnreadable, err)
	}
	switch {
	case version > schemaVersion:
		return schemaNewerThanRelease(version)
	case version > 0 && version < schemaVersion:
		if err := authorizeMigration(resolved, version); err != nil {
			return withInputMigrationCause(CauseAuthorizationMissing, err)
		}
	}
	return nil
}

// snapshotUserVersion reads the store's user_version from a read-only open
// of a private copy (ReadSnapshot), then removes the copy.
func snapshotUserVersion(ctx context.Context, resolved string, busyMs int) (version int, resultErr error) {
	directory, err := os.MkdirTemp("", "agent-director-version-")
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(directory)) }()
	snapshot, err := ReadSnapshot(ctx, resolved, directory, busyMs)
	if err != nil {
		return 0, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: snapshot, RawQuery: "mode=ro"}).String())
	if err != nil {
		return 0, err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

// migrateInputJournal runs inside one schema-step transaction. It is prepared
// separately from registration so storage and migration cannot activate before
// every runtime reader/writer has moved to the same SQLite authority. Its
// transaction runs under the window's work context; the cutover gate
// (beginCutoverCommit) decides, just before COMMIT, whether the commit begins.
// A COMMIT error is unconfirmed (inputjournal.ErrUnconfirmed): the cutover may
// or may not have landed.
func migrateInputJournal(db *sql.DB, w *inputMigrationWindow, target int) error {
	if w == nil {
		return buildMigrationRefusal(target - 1)
	}
	ctx := w.ctx
	if err := w.verifyBoundary(); err != nil {
		return err
	}
	storeID, err := readStoreID(db)
	if err != nil {
		return err
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version >= target {
		return inputAuthority(ctx, db, storeID)
	}
	if err := w.readLegacy(db, storeID); err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := inputDurability(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Reject partial/incompatible or already-authoritative input state before
	// DDL. This step never repairs a hand-reset schema or imports over new data.
	if err := validateInputSchemaAbsent(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, inputDeliveryDDL); err != nil {
		return err
	}
	if err := importInputRecords(ctx, tx, storeID, w.records); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO store_meta(key,value) VALUES(?,?)`, inputAuthorityKey, inputAuthorityVersion+storeID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", target)); err != nil {
		return err
	}
	if err := w.beginCutoverCommit(); err != nil {
		return err
	}
	if err := w.commitCutover(tx); err != nil {
		return withInputMigrationCause(CauseMigrationIncomplete, fmt.Errorf("%w: administrator cutover requires committed-marker verification before restart", inputjournal.ErrUnconfirmed))
	}
	// No dispatch or retirement here. The outer administrator call verifies
	// the committed marker on its reopened handle before retiring any file.
	return nil
}

func validateInputSchemaAbsent(ctx context.Context, tx *sql.Tx) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE lower(name) IN ('input_launches','input_attempts','input_imports')`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return withInputMigrationCause(CauseNativeInputStateInconsistent, fmt.Errorf("%w: partial native-input schema requires administrator inspection; do not reset the version", ErrSchemaMismatch))
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM store_meta WHERE key=?`, inputAuthorityKey).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return withInputMigrationCause(CauseNativeInputStateInconsistent, fmt.Errorf("%w: native authority already recorded", ErrSchemaMismatch))
	}
	return nil
}

func (w *inputMigrationWindow) readLegacy(db *sql.DB, storeID string) error {
	// Read only identities; no lifecycle state is interpreted as process exit.
	rows, err := db.QueryContext(w.ctx, `SELECT instance_id, COALESCE(life_number,0), COALESCE(launch_token,''), COALESCE(tmux_socket,''), COALESCE(pane_id,''), COALESCE(pane_pid,0), COALESCE(pane_starttime,''),agent,cwd FROM spawns`)
	if err != nil {
		return err
	}
	owners := make(map[string]inputjournal.Identity)
	for rows.Next() {
		i := inputjournal.Identity{StoreID: storeID}
		var agent sql.NullString
		if err := rows.Scan(&i.InstanceID, &i.LifeNumber, &i.Token, &i.Socket, &i.PaneID, &i.PID, &i.Starttime, &agent, &i.CWD); err != nil {
			rows.Close()
			return err
		}
		// Historical NULL agents remain NULL in spawns. The import identity
		// keeps an absent agent empty, never the runtime's default provider;
		// any journal claiming a native provider still conflicts with it.
		i.Agent = agent.String
		if i.Token != "" {
			if _, ok := owners[i.Token]; ok {
				rows.Close()
				return withInputMigrationCause(CauseLegacyEvidenceUnestablished, fmt.Errorf("%w: duplicate launch identity", ErrSchemaMismatch))
			}
			owners[i.Token] = i
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, root := range w.roots {
		dir := filepath.Join(root, storeID)
		entries, err := os.ReadDir(dir)
		if inputPathAbsent(err) {
			continue
		}
		if err != nil {
			return withInputMigrationCause(CauseLegacyRootUnreadable, fmt.Errorf("legacy input root is unreadable; restart administrator verification"))
		}
		for _, e := range entries {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			if strings.HasSuffix(e.Name(), ".lock") && e.Type().IsRegular() {
				continue
			}
			if !e.IsDir() || decodeLaunchToken(e.Name()) == "" {
				return withInputMigrationCause(CauseLegacyEvidenceUnestablished, fmt.Errorf("legacy root contains unestablished launch evidence"))
			}
			j, err := inputjournal.OpenLegacyJournal(filepath.Join(dir, e.Name()))
			if err != nil {
				cause := CauseLegacyEvidenceUnestablished
				if errors.Is(err, unix.EWOULDBLOCK) {
					cause = CauseLegacyWriterActive // inputjournal.LockWriter: a writer holds it
				}
				return withInputMigrationCause(cause, fmt.Errorf("legacy writer or unreadable lock; stop old mutators and restart administrator verification: %w", err))
			}
			w.journals = append(w.journals, j)
			var owner *inputjournal.Identity
			if i, ok := owners[e.Name()]; ok {
				copy := i
				owner = &copy
			}
			r, err := j.Read(w.ctx, storeID, owner)
			if err != nil {
				return withInputMigrationCause(CauseLegacyEvidenceUnestablished, fmt.Errorf("legacy journal unreadable; restart administrator verification: %w", err))
			}
			if seen[e.Name()] {
				// Multiple roots claiming the same token are conflicting owners,
				// not a priority rule. Retain both snapshots in one blocker.
				for k := range w.records {
					if w.records[k].Launch.Identity.Token != e.Name() {
						continue
					}
					prior := &w.records[k]
					prior.Launch.Blocker, prior.Launch.CurrentRequestID, prior.Attempts = "conflicting_roots", "", nil
					prior.Evidence, err = json.Marshal([]inputjournal.ImportRecord{*prior, r})
					if err != nil {
						return err
					}
					prior.SourceSHA256 = inputjournal.Hash(prior.Evidence)
				}
				continue
			}
			seen[e.Name()] = true
			w.records = append(w.records, r)
		}
	}
	sort.Slice(w.records, func(i, j int) bool { return w.records[i].Source < w.records[j].Source })
	return nil
}

func backupInputStore(ctx context.Context, dbPath string, roots []string, dest string) error {
	if err := makeInputBackupDir(dest); err != nil {
		err = fmt.Errorf("create exclusive stopped-writer backup: %w", err)
		if errors.Is(err, fs.ErrExist) {
			// Made after the argument checks found it absent.
			return withInputMigrationCause(CauseBackupExists, err)
		}
		return err
	}
	// A failed backup stays visibly incomplete; never erase evidence or reuse
	// it on a later invocation. COMPLETE is written and synced last.
	manifest := struct {
		Database string
		Roots    []string
		Recovery string
	}{dbPath, roots, "Restore only with the matching prior binary under a maintained maintenance boundary. Later writes are lost. Reconcile uncertain attempts before restarting any older runtime. Never reset user_version or synthesize replayable journals."}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeBackupMetadata(filepath.Join(dest, "manifest.json"), data); err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := backupInputFile(ctx, dbPath+suffix, filepath.Join(dest, "database"+suffix)); err != nil && !(suffix != "" && errors.Is(err, os.ErrNotExist)) {
			return err
		}
	}
	for k, root := range roots {
		if _, err := os.Lstat(root); inputPathAbsent(err) {
			continue
		} else if err != nil {
			return err
		}
		// Include all evidence under each declared root; no provider transcript
		// outside these roots is moved or rewritten by migration or backup.
		err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out := filepath.Join(dest, fmt.Sprintf("legacy-%d", k), rel)
			if e.IsDir() {
				return makeInputBackupDir(out)
			}
			if strings.HasSuffix(e.Name(), ".lock") {
				return nil
			}
			return backupInputFile(ctx, path, out)
		})
		if err != nil {
			return err
		}
	}
	return writeBackupMetadata(filepath.Join(dest, "COMPLETE"), []byte("stopped-writer pre-upgrade backup\n"))
}

func makeInputBackupDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	// File fsync does not preserve the directory entries above it. Persist
	// each new directory and its parent (including empty evidence directories)
	// before COMPLETE can attest to the entire stopped-writer backup.
	if err := syncInputDir(path); err != nil {
		return err
	}
	return syncInputDir(filepath.Dir(path))
}

func backupInputFile(ctx context.Context, source, dest string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup requires regular evidence files")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return syncInputDir(filepath.Dir(dest))
}

func writeBackupMetadata(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncInputDir(filepath.Dir(path))
}

func syncInputDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
