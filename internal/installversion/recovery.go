package installversion

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/layout"
)

func (op *operation) recover() error {
	if err := op.validateEntryPaths(); err != nil {
		return err
	}
	anchor, err := ReadContract(op.root)
	if err == nil {
		op.anchor = &anchor
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	initial, err := ReadInitialization(op.root)
	if err == nil {
		op.initial = &initial
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := readCurrentID(op.root)
	if err != nil {
		return err
	}
	hasHistory, err := op.hasHistory()
	if err != nil {
		return err
	}
	if op.initial == nil {
		if op.anchor != nil || current != "" || hasHistory {
			return invalid("layout lacks original initialization evidence")
		}
		// The only pre-evidence durable artifact can be the first fully
		// validated final directory. Recover its identity, never the new
		// caller's claims. Multiple finals here cannot arise in the protocol.
		versions, err := listVersions(op.root, false)
		if err != nil {
			return err
		}
		if len(versions) > 1 {
			return invalid("ambiguous initial retained version")
		}
		if len(versions) == 0 {
			// Canonical entries themselves are layout evidence. Their missing
			// records must not be treated as permission to initialize afresh.
			for _, entry := range []struct{ dir, name, target string }{
				{layout.BinDir, layout.BinaryName, layout.RuntimeEntryTarget},
				{layout.AdminDir, layout.AdminBinaryName, layout.AdminEntryTarget},
			} {
				path := filepath.Join(op.root, entry.dir, entry.name)
				canonical, err := canonicalEntry(path, entry.target)
				if err != nil {
					return err
				}
				if canonical {
					return invalid("permanent entry has lost its initialization evidence")
				}
			}
			return nil
		}
		if err := op.beginInitialization(versions[0]); err != nil {
			return err
		}
	}
	if op.anchor == nil {
		if op.initial.Complete || current != "" || hasHistory {
			return invalid("established layout lost its permanent anchor")
		}
		v, hash, err := readVersion(op.root, op.initial.InitialVersionID, false)
		if err != nil {
			return err
		}
		if hash != op.initial.InitialRecordSHA256 || contractFor(v.Facts) != op.initial.Contract {
			return invalid("initialization evidence disagrees with the original version")
		}
		if err := op.createAnchor(); err != nil {
			return err
		}
	}
	if op.initial.Contract != *op.anchor {
		return invalid("initialization evidence disagrees with the permanent anchor")
	}
	v, hash, err := readVersion(op.root, op.initial.InitialVersionID, false)
	if err != nil {
		return err
	}
	if hash != op.initial.InitialRecordSHA256 || contractFor(v.Facts) != *op.anchor {
		return invalid("initial retained version no longer matches bootstrap evidence")
	}
	// Re-sync evidence which may have been visible at the preceding crash
	// before its parent sync. No record's content is replaced by this step.
	if err := op.syncDir(op.root, "bootstrap.evidence-parent-sync"); err != nil {
		return err
	}
	if !hasHistory {
		if current != "" || op.initial.Complete {
			return invalid("initial layout has selection without publication history")
		}
		if err := op.ensureReady(v); err != nil {
			return err
		}
		if err := op.publish(v, "bootstrap", true); err != nil {
			return err
		}
	} else {
		if err := op.readHistory(); err != nil {
			return err
		}
		if op.history[0].VersionID != op.initial.InitialVersionID {
			return invalid("publication history does not begin with the original version")
		}
		last := op.history[len(op.history)-1]
		if last.complete {
			if current != last.VersionID {
				return invalid("current disagrees with completed publication history")
			}
			stamp := last.version
			op.result.PublishedVersion = &stamp
		} else {
			prior := ""
			if last.PreviousVersionID != nil {
				prior = *last.PreviousVersionID
			}
			if current != prior && current != last.VersionID {
				return invalid("current cannot be explained by the unfinished intent")
			}
			// readHistory already validated both records. Preserve the observed
			// selection before any replay I/O, without claiming a new selection,
			// completed recovery or durability from this observation alone.
			if current != "" {
				stamp := last.version
				if current == prior {
					stamp = op.history[len(op.history)-2].version
				}
				op.result.PublishedVersion = &stamp
			}
			selected, _, err := readVersion(op.root, last.VersionID, true)
			if err != nil {
				return err
			}
			if err := op.selectIntent(last, selected, true); err != nil {
				return err
			}
		}
	}
	if err := op.finishInitialization(); err != nil {
		return err
	}
	// Complete any earlier validated final staging before allocating a later
	// sequence. It remains unpublished, and therefore does not grow the floor.
	// This preserves first-complete staging order after a death before ready.
	versions, err := listVersions(op.root, false)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if _, err := os.Lstat(filepath.Join(version.Directory, layout.VersionReady)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := op.ensureReady(version); err != nil {
			return err
		}
	}
	return nil
}

func (op *operation) hasHistory() (bool, error) {
	entries, err := os.ReadDir(filepath.Join(op.root, layout.PublicationsDir))
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".tmp-") {
			return true, nil
		}
	}
	return false, nil
}
