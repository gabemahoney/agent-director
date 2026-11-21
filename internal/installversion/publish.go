package installversion

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/layout"
)

func (op *operation) publish(v InstalledVersion, kind string, recovering bool) error {
	if op.anchor == nil {
		return invalid("publication has no permanent anchor")
	}
	v, _, err := readVersion(op.root, v.VersionID, true)
	if err != nil {
		return err
	}
	if err := compatible(v.Facts, *op.anchor, op.floor); err != nil {
		return err
	}
	current, err := readCurrentID(op.root)
	if err != nil {
		return err
	}
	if len(op.history) > 0 {
		last := op.history[len(op.history)-1]
		if last.complete && current == v.VersionID && last.VersionID == current {
			// Re-sync the selection and completion names after a lost successful
			// reply, while leaving the entry and every history record unchanged.
			if err := op.syncDir(op.root, "current.parent-sync"); err != nil {
				return err
			}
			if err := op.syncDir(filepath.Join(op.root, layout.PublicationsDir), "history.completion.parent-sync"); err != nil {
				return err
			}
			op.selected(v, recovering, true)
			return nil
		}
	}
	p, err := op.newIntent(v, kind)
	if err != nil {
		return err
	}
	return op.selectIntent(p, v, recovering)
}

func (op *operation) selected(v InstalledVersion, recovering, durable bool) {
	stamp := v.Version
	op.result.PublishedVersion = &stamp
	if recovering {
		op.result.RecoveredVersion = &stamp
	} else {
		op.result.PublicationState = "published"
		op.result.Durable = durable
	}
}

func (op *operation) selectIntent(p publication, v InstalledVersion, recovering bool) error {
	// Re-sync the intent and its directory before replay: a visible intent
	// could have survived process death just before its original parent sync.
	intent, err := openRegular(filepath.Join(op.root, layout.PublicationsDir, historyName(p.Sequence, "intent")), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(op.syncFile(intent, "history.intent.recovery-sync"), intent.Close()); err != nil {
		return err
	}
	if err := op.syncDir(filepath.Join(op.root, layout.PublicationsDir), "history.intent.recovery-parent-sync"); err != nil {
		return err
	}
	if err := op.replaceLink(filepath.Join(op.root, layout.CurrentVersion), layout.VersionsDir+"/"+v.VersionID, "current", func() {
		if !recovering {
			op.result.PublicationState = "unknown"
		}
	}, func() { op.selected(v, recovering, false) }, func() { op.selected(v, recovering, true) }); err != nil {
		return err
	}
	op.selected(v, recovering, true)
	return op.completeIntent(p)
}

// replaceLink does not unlink the old entry. The optional beforeRename marks
// uncertain publication only when the actual rename is about to be attempted.
func (op *operation) replaceLink(path, target, point string, beforeRename, renamed, synced func()) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-link-")
	if err != nil {
		return err
	}
	name := f.Name()
	if err := errors.Join(f.Close(), os.Remove(name)); err != nil {
		return err
	}
	defer os.Remove(name)
	if err := os.Symlink(target, name); err != nil {
		return err
	}
	if err := op.boundary("before:"+point+".rename", path); err != nil {
		return err
	}
	if beforeRename != nil {
		beforeRename()
	}
	if op.engine.Hooks.Rename != nil {
		err = op.engine.Hooks.Rename(name, path)
	} else {
		err = os.Rename(name, path)
	}
	if err != nil {
		return err
	}
	if renamed != nil {
		renamed()
	}
	if err := op.boundary("after:"+point+".rename", path); err != nil {
		return err
	}
	return op.syncDirThen(filepath.Dir(path), point+".parent-sync", synced)
}
