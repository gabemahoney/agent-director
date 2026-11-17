package installversion

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/layout"
)

// beginInitialization records the original pair and exact entry targets before
// anchoring or selecting it. An unfinished final directory is recoverable from
// its complete record; it has not yet been exposed to runtime discovery.
func (op *operation) beginInitialization(v InstalledVersion) error {
	if op.anchor != nil || op.initial != nil {
		return invalid("initialization cannot reset existing evidence")
	}
	_, hash, err := readVersion(op.root, v.VersionID, false)
	if err != nil {
		return err
	}
	// Recovery may have found the final name just after its rename. Make
	// that name durable before durable evidence is allowed to reference it.
	if err := op.syncDir(v.Directory, "bootstrap.version-sync"); err != nil {
		return err
	}
	if err := op.syncDir(filepath.Dir(v.Directory), "bootstrap.version-parent-sync"); err != nil {
		return err
	}
	initial := Initialization{Contract: contractFor(v.Facts), InitialVersionID: v.VersionID, InitialRecordSHA256: hash, RuntimeTarget: layout.RuntimeEntryTarget, AdminTarget: layout.AdminEntryTarget}
	if err := op.writeAtomic(filepath.Join(op.root, layout.InstallInitialization), initial, "bootstrap.intent", false); err != nil {
		return err
	}
	op.initial = &initial
	return op.createAnchor()
}

// Check both fixed entry paths before staging or selecting anything. The
// bootstrap evidence authorizes replacing a leaf, never following a directory
// symlink or overwriting someone else's resource.
func (op *operation) validateEntryPaths() error {
	for _, entry := range []struct{ dir, name, target string }{
		{layout.BinDir, layout.BinaryName, layout.RuntimeEntryTarget},
		{layout.AdminDir, layout.AdminBinaryName, layout.AdminEntryTarget},
	} {
		dir := filepath.Join(op.root, entry.dir)
		if err := checkDir(dir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := canonicalEntry(filepath.Join(dir, entry.name), entry.target); err != nil {
			return err
		}
	}
	return nil
}

func (op *operation) createAnchor() error {
	if op.initial == nil || op.anchor != nil {
		return invalid("cannot recreate the permanent anchor")
	}
	anchor := op.initial.Contract
	if err := op.writeAtomic(filepath.Join(op.root, layout.InstallContract), anchor, "bootstrap.anchor", false); err != nil {
		return err
	}
	op.anchor = &anchor
	return nil
}

func (op *operation) finishInitialization() error {
	if op.initial == nil || op.anchor == nil || op.initial.Contract != *op.anchor || len(op.history) == 0 || !op.history[len(op.history)-1].complete {
		return invalid("permanent entries lack completed publication evidence")
	}
	current, err := readCurrentID(op.root)
	if err != nil {
		return err
	}
	if current != op.history[len(op.history)-1].VersionID {
		return invalid("permanent entries do not select the recorded publication")
	}
	if _, _, err := readVersion(op.root, current, true); err != nil {
		return err
	}
	op.result.InitializationComplete = false
	entries := []struct{ dir, name, target, point string }{
		{layout.BinDir, layout.BinaryName, op.initial.RuntimeTarget, "bootstrap.runtime-entry"},
		{layout.AdminDir, layout.AdminBinaryName, op.initial.AdminTarget, "bootstrap.admin-entry"},
	}
	for _, entry := range entries {
		dir := filepath.Join(op.root, entry.dir)
		if err := op.ensureDir(dir, 0700); err != nil {
			return err
		}
		if entry.dir == layout.AdminDir {
			// The public runtime link must never make the operator executable
			// traversable by another user through its stable admin directory.
			if err := os.Chmod(dir, 0700); err != nil {
				return err
			}
		}
		path := filepath.Join(dir, entry.name)
		canonical, err := canonicalEntry(path, entry.target)
		if err != nil {
			return err
		}
		if !canonical {
			if err := op.replaceLink(path, entry.target, entry.point, nil, nil, nil); err != nil {
				return err
			}
		} else {
			// A prior repair may have died after rename. Re-sync its name,
			// preserving the entry's inode on every ordinary publication.
			if err := op.syncDir(dir, entry.point+".parent-sync"); err != nil {
				return err
			}
		}
	}
	if !op.initial.Complete {
		complete := *op.initial
		complete.Complete = true
		if err := op.writeAtomic(filepath.Join(op.root, layout.InstallInitialization), complete, "bootstrap.completion", true); err != nil {
			return err
		}
		op.initial = &complete
	} else if err := op.syncDir(op.root, "bootstrap.completion.parent-sync"); err != nil {
		return err
	}
	op.result.InitializationComplete = true
	return nil
}

func canonicalEntry(path, target string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !owned(info) {
		return false, invalid("permanent entry is not owned by this user")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		value, err := os.Readlink(path)
		return value == target, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false, invalid("permanent entry cannot be safely replaced")
	}
	return false, nil
}
