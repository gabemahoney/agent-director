package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

type inputMigrationWindow struct {
	ctx      context.Context
	path     string
	roots    []string
	backup   string
	verify   func(context.Context, string, []string) error
	journals []*inputjournal.LegacyJournal
	records  []inputjournal.ImportRecord
}

// MigrateInputJournal is the store portion of the explicit administrator
// procedure. The admin layer owns the maintained admission boundary and real
// process inventory; verify must refuse unreadable evidence, never kill it.
// This entry is internal to this module, absent from the agent-facing API.
// Roots are complete, canonical legacy input roots, including invoking HOME.
func MigrateInputJournal(ctx context.Context, path string, roots []string, backup string, verify func(context.Context, string, []string) error) (result InputMigrationResult, resultErr error) {
	if verify == nil || len(roots) == 0 || !filepath.IsAbs(backup) {
		return result, fmt.Errorf("maintenance boundary and complete roots are required")
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return result, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := verify(ctx, resolved, roots); err != nil {
		return result, err
	}
	lock, err := os.OpenFile(resolved+".input-migration.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if err := acquireInputMigrationLock(lock); err != nil {
		return result, err
	}
	defer func() {
		// Registered before the journal/database closers: critical work and
		// handles finish first. Unlink while still locked, then release last.
		// A replacement path is never ours to remove.
		if err := checkInputMigrationLock(lock); err != nil {
			resultErr = errors.Join(resultErr, err)
			return
		}
		if err := os.Remove(lock.Name()); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove journal migration lock: %w", err))
			return
		}
		resultErr = errors.Join(resultErr, syncInputDir(filepath.Dir(lock.Name())))
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	w := &inputMigrationWindow{ctx: ctx, path: resolved, roots: append([]string(nil), roots...), backup: backup, verify: verify}
	defer func() {
		for _, j := range w.journals {
			resultErr = errors.Join(resultErr, j.Close())
		}
	}()
	// Back up while all old writers are stopped and before any schema hop.
	// The directory is exclusive: a retry must never overwrite the older copy.
	if err := backupInputStore(ctx, resolved, roots, backup); err != nil {
		return result, err
	}
	s, err := openDBWithInputWindow(resolved, DefaultBusyTimeoutMs, w)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, s.Close()) }()
	if err := inputAuthority(ctx, s.db, s.storeID); err != nil {
		return result, err
	}
	result = InputMigrationResult{StoreID: s.storeID, Authority: "sqlite", Backup: backup}
	// The second inventory is still under the operator-maintained boundary.
	// Failure here is an uncertainty AFTER cutover, never file authority.
	if err := verify(ctx, resolved, roots); err != nil {
		return result, fmt.Errorf("SQLite cutover committed; keep the maintenance boundary and restart administrator verification: %w", err)
	}
	for _, j := range w.journals {
		if err := j.Retire(ctx); err != nil {
			result.RetirementPending = true
		}
	}
	// A retry after an uncertain commit never imports or deletes from a new
	// filesystem read. Report remaining operational files for inspection.
	if len(w.journals) == 0 {
		for _, root := range roots {
			err := filepath.WalkDir(filepath.Join(root, s.storeID), func(path string, e os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
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

// acquireInputMigrationLock admits an already-open descriptor only while its
// inode still owns the pathname. The caller must close it even on failure.
func acquireInputMigrationLock(lock *os.File) error {
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another administrator owns the journal migration")
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
		return err
	}
	named, err := os.Lstat(lock.Name())
	if err != nil {
		return fmt.Errorf("journal migration lock changed; restart administrator verification: %w", err)
	}
	if !held.Mode().IsRegular() || !named.Mode().IsRegular() || held.Size() != 0 || named.Size() != 0 {
		return fmt.Errorf("journal migration lock is not an empty regular file; restart administrator verification")
	}
	if !os.SameFile(held, named) {
		return fmt.Errorf("journal migration lock changed; restart administrator verification")
	}
	return nil
}

// migrateInputJournal runs inside one schema-step transaction. It is prepared
// separately from registration so storage and migration cannot activate before
// every runtime reader/writer has moved to the same SQLite authority.
func migrateInputJournal(db *sql.DB, w *inputMigrationWindow, target int) error {
	if w == nil {
		return buildMigrationRefusal(target - 1)
	}
	ctx := w.ctx
	if err := w.verify(ctx, w.path, w.roots); err != nil {
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: administrator cutover requires committed-marker verification before restart", inputjournal.ErrUnconfirmed)
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
		return fmt.Errorf("%w: partial native-input schema requires administrator inspection; do not reset the version", ErrSchemaMismatch)
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM store_meta WHERE key=?`, inputAuthorityKey).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: native authority already recorded", ErrSchemaMismatch)
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
				return fmt.Errorf("%w: duplicate launch identity", ErrSchemaMismatch)
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
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("legacy input root is unreadable; restart administrator verification")
		}
		for _, e := range entries {
			if err := w.ctx.Err(); err != nil {
				return err
			}
			if strings.HasSuffix(e.Name(), ".lock") && e.Type().IsRegular() {
				continue
			}
			if !e.IsDir() || decodeLaunchToken(e.Name()) == "" {
				return fmt.Errorf("legacy root contains unestablished launch evidence")
			}
			j, err := inputjournal.OpenLegacyJournal(filepath.Join(dir, e.Name()))
			if err != nil {
				return fmt.Errorf("legacy writer or unreadable lock; stop old mutators and restart administrator verification: %w", err)
			}
			w.journals = append(w.journals, j)
			var owner *inputjournal.Identity
			if i, ok := owners[e.Name()]; ok {
				copy := i
				owner = &copy
			}
			r, err := j.Read(w.ctx, storeID, owner)
			if err != nil {
				return fmt.Errorf("legacy journal unreadable; restart administrator verification: %w", err)
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
	for _, root := range roots {
		if dest == root || strings.HasPrefix(dest, root+string(filepath.Separator)) {
			return fmt.Errorf("backup must be outside every legacy input root")
		}
	}
	if err := makeInputBackupDir(dest); err != nil {
		return fmt.Errorf("create exclusive stopped-writer backup: %w", err)
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
		if _, err := os.Lstat(root); os.IsNotExist(err) {
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
