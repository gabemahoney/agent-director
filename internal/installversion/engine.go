package installversion

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/layout"
)

// Hooks supplies per-engine fault boundaries, never process-global switches
// or environment controls. Point is before:<operation> or after:<operation>;
// path identifies the resource. A crash test may stop its own subprocess in
// Observe. Sync and Rename, when nil, use the real filesystem primitives.
type Hooks struct {
	Observe func(point, path string) error
	Sync    func(*os.File) error
	Rename  func(oldPath, newPath string) error
}

type Engine struct {
	Root  string
	Hooks Hooks
}

type operation struct {
	engine  Engine
	root    string
	result  Result
	anchor  *Contract
	initial *Initialization
	history []publication
	floor   []string
}

func (e Engine) Publish(ctx context.Context, runtimeBinary, adminBinary string, facts Facts) (Result, error) {
	return e.mutate(facts.Version, func(op *operation) error {
		if err := facts.Validate(); err != nil {
			return problem{"version_conflict", "invalid administrator facts"}
		}
		v, err := op.stage(ctx, runtimeBinary, adminBinary, facts)
		if err != nil {
			return err
		}
		if op.anchor == nil {
			if err := op.beginInitialization(v); err != nil {
				return err
			}
		}
		if err := op.ensureReady(v); err != nil {
			return err
		}
		if err := op.publish(v, "publish", false); err != nil {
			return err
		}
		return op.finishInitialization()
	})
}

// Rollback uses the retained pair's previously validated facts and resources.
// It never executes an old binary through the private same-build handshake.
func (e Engine) Rollback(stamp string) (Result, error) {
	return e.mutate(stamp, func(op *operation) error {
		if op.anchor == nil {
			return invalid("no established version layout for rollback")
		}
		v, _, err := readVersion(op.root, layout.VersionID(stamp), true)
		if errors.Is(err, os.ErrNotExist) {
			return invalid("rollback version is not retained and ready")
		}
		if err != nil {
			return err
		}
		op.result.Reused = true
		if err := op.publish(v, "rollback", false); err != nil {
			return err
		}
		return op.finishInitialization()
	})
}

func (e Engine) mutate(stamp string, change func(*operation) error) (result Result, err error) {
	op := &operation{engine: e, result: Result{Version: stamp, VersionID: layout.VersionID(stamp), PublicationState: "not_published"}}
	defer func() {
		result = op.result
		if err != nil {
			err = refusal(err, result)
		}
	}()
	if stamp == "" {
		return result, invalid("empty target version")
	}
	if err = op.prepareRoot(); err != nil {
		return
	}
	lock, lockErr := takeLock(filepath.Join(op.root, layout.InstallLock))
	if lockErr != nil {
		err = lockErr
		return
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err = op.boundary("after:lock", lock.Name()); err != nil {
		return
	}
	// Persist the stable lock inode/name once acquired; it is never removed.
	if err = op.syncFile(lock, "lock.sync"); err != nil {
		return
	}
	if err = op.syncDir(op.root, "lock.parent-sync"); err != nil {
		return
	}
	for _, name := range []string{layout.VersionsDir, layout.PublicationsDir} {
		if err = op.ensureDir(filepath.Join(op.root, name), 0700); err != nil {
			return
		}
	}
	if err = op.recover(); err != nil {
		return
	}
	err = change(op)
	return
}

func refusal(err error, result Result) *Refusal {
	reason := "io_failure"
	var p problem
	if errors.As(err, &p) {
		reason = p.reason
	}
	return &Refusal{Reason: reason, Retryable: reason == "lock_busy" || reason == "io_failure", Result: result, cause: err}
}

// ReadRefusal adapts a lock-free read failure, where no requested mutation has
// begun and the currently published version may be unobservable.
func ReadRefusal(err error) *Refusal {
	return refusal(err, Result{PublicationState: "not_published"})
}

func (op *operation) boundary(point, path string) error {
	if op.engine.Hooks.Observe != nil {
		return op.engine.Hooks.Observe(point, path)
	}
	return nil
}

func (op *operation) syncFile(file *os.File, point string) error {
	return op.syncFileThen(file, point, nil)
}

func (op *operation) syncFileThen(file *os.File, point string, synced func()) error {
	if err := op.boundary("before:"+point, file.Name()); err != nil {
		return err
	}
	var err error
	if op.engine.Hooks.Sync != nil {
		err = op.engine.Hooks.Sync(file)
	} else {
		err = file.Sync()
	}
	if err != nil {
		return err
	}
	if synced != nil {
		synced()
	}
	return op.boundary("after:"+point, file.Name())
}

func (op *operation) syncDir(path, point string) error {
	return op.syncDirThen(path, point, nil)
}

func (op *operation) syncDirThen(path, point string, synced func()) error {
	f, err := openDir(path)
	if err != nil {
		return err
	}
	return errors.Join(op.syncFileThen(f, point, synced), f.Close())
}

func (op *operation) rename(from, to, point string, renamed func()) error {
	if err := op.boundary("before:"+point, to); err != nil {
		return err
	}
	var err error
	if op.engine.Hooks.Rename != nil {
		err = op.engine.Hooks.Rename(from, to)
	} else {
		err = os.Rename(from, to)
	}
	if err != nil {
		return err
	}
	if renamed != nil {
		renamed()
	}
	return op.boundary("after:"+point, to)
}

// prepareRoot creates only missing ancestors. Existing caller-supplied
// ancestors may be symlinked (notably /var on macOS); managed children may not.
func (op *operation) prepareRoot() error {
	if op.engine.Root == "" {
		return invalid("installation root is empty")
	}
	abs, err := filepath.Abs(op.engine.Root)
	if err != nil {
		return err
	}
	missing := []string{}
	parent := abs
	for {
		_, err := os.Stat(parent)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		// A dangling symlink is existing, unprovable layout, not a new directory.
		if _, err := os.Lstat(parent); err == nil {
			return invalid("dangling installation ancestor")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, filepath.Base(parent))
		parent = filepath.Dir(parent)
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		child := filepath.Join(parent, missing[i])
		if err := os.Mkdir(child, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := checkDir(child); err != nil {
			return err
		}
		if err := op.syncDir(child, "root.directory-sync"); err != nil {
			return err
		}
		// The external parent may be /tmp (not owned/private). It is used only
		// to persist the just-created name, never for an installation resource.
		f, err := os.Open(parent)
		if err != nil {
			return err
		}
		if err := errors.Join(op.syncFile(f, "root.parent-sync"), f.Close()); err != nil {
			return err
		}
		parent = child
	}
	op.root = parent
	if err := checkDir(op.root); err != nil {
		return err
	}
	// Reconcile a process death while creating an ancestor name. Syncing the
	// visible chain also covers a prior mkdir whose parent sync never ran.
	for dir := op.root; ; dir = filepath.Dir(dir) {
		f, err := os.Open(dir)
		if err != nil {
			return err
		}
		if err := errors.Join(op.syncFile(f, "root.ancestor-sync"), f.Close()); err != nil {
			return err
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return nil
}

func (op *operation) ensureDir(path string, mode os.FileMode) error {
	err := os.Mkdir(path, mode)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := checkDir(path); err != nil {
		return err
	}
	// Existing names may be from a process that died before its directory
	// sync. Re-establish durability without replacing them.
	if err := op.syncDir(path, "directory.sync"); err != nil {
		return err
	}
	return op.syncDir(filepath.Dir(path), "directory.parent-sync")
}

// writeAtomic keeps evidence complete even when a process dies in a write.
// Exclusive records (anchor, history) are checked under the administrator lock
// and never replaced. Only initialization completion replaces prior evidence.
func (op *operation) writeAtomic(path string, value any, point string, replace bool) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return op.writeBytesAtomic(path, append(data, '\n'), point, replace)
}

func (op *operation) writeBytesAtomic(path string, data []byte, point string, replace bool) error {
	if _, err := os.Lstat(path); err == nil && !replace {
		return invalid("immutable installation evidence already exists")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name) // Only this operation's unpublished temporary file.
	_, err = f.Write(data)
	if err == nil {
		err = op.syncFile(f, point+".file-sync")
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := op.rename(name, path, point+".rename", nil); err != nil {
		return err
	}
	return op.syncDir(filepath.Dir(path), point+".parent-sync")
}
